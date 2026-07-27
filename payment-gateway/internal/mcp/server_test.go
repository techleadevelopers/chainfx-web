package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTierLimitSupportsMCPAvailabilityLoad(t *testing.T) {
	if got := tierLimit("sk_test_cfx_probe", "mcp_tool_read"); got != 900 {
		t.Fatalf("expected test MCP read limit 900/min, got %d", got)
	}
	if got := tierLimit("sk_live_cfx_probe", "mcp_tool_read"); got != 1800 {
		t.Fatalf("expected live MCP read limit 1800/min, got %d", got)
	}
	if got := tierLimit("", "mcp_tool_read"); got != 300 {
		t.Fatalf("expected anonymous MCP read limit 300/min, got %d", got)
	}
	if got := tierLimit("", "mcp_ai_expensive"); got != 120 {
		t.Fatalf("expected anonymous MCP AI fallback limit 120/min, got %d", got)
	}
	if got := tierLimit("sk_live_cfx_probe", "mcp_financial"); got != 300 {
		t.Fatalf("expected live MCP financial limit 300/min, got %d", got)
	}
	if got := tierLimit("sk_live_cfx_probe", "mcp_abuse"); got != 30 {
		t.Fatalf("expected live MCP abuse limit 30/min, got %d", got)
	}
}

func TestMCPStaticDiscoveryUsesCachedJSON(t *testing.T) {
	s := New(nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp/tools/list", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()

	s.handleToolsList(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"tools"`) || !strings.Contains(rec.Body.String(), `"get_rates"`) {
		t.Fatalf("expected tools list response, got %s", rec.Body.String())
	}
}

func TestMCPResourceFallbacksDoNotFailWhenCatalogIsMissing(t *testing.T) {
	s := New(nil, nil, nil, nil, nil)
	cases := []string{
		"chainfx://agent/assets",
		"chainfx://capability-contracts/:id",
		"chainfx://capability-contracts/{id}",
	}
	for _, uri := range cases {
		t.Run(uri, func(t *testing.T) {
			got, err := s.readResource(context.Background(), uri)
			if err != nil {
				t.Fatalf("expected fallback resource, got error: %v", err)
			}
			if got == nil {
				t.Fatalf("expected fallback resource body")
			}
		})
	}
}

func TestMCPResourceReadKeepsPublicResourcesAnonymous(t *testing.T) {
	s := New(nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp/resources/read", strings.NewReader(`{"uri":"chainfx://marketplace/capabilities"}`))
	rec := httptest.NewRecorder()
	authorizeCalled := false

	s.handleResourcesReadWithAuthorize(func(w http.ResponseWriter, r *http.Request) bool {
		authorizeCalled = true
		w.WriteHeader(http.StatusUnauthorized)
		return false
	})(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected public resource read to return 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if authorizeCalled {
		t.Fatal("public resource should not call authorizer")
	}
	if !strings.Contains(rec.Body.String(), `"chainfx://marketplace/capabilities"`) {
		t.Fatalf("expected resource content, got %s", rec.Body.String())
	}
}

func TestMCPPrivateResourceReadWithoutKeyReturnsSafeAuthRequiredContent(t *testing.T) {
	s := New(nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp/resources/read", strings.NewReader(`{"uri":"chainfx://agent/policy/:wallet"}`))
	rec := httptest.NewRecorder()
	authorizeCalled := false

	s.handleResourcesReadWithAuthorize(func(w http.ResponseWriter, r *http.Request) bool {
		authorizeCalled = true
		w.WriteHeader(http.StatusUnauthorized)
		return false
	})(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected safe MCP content response, got %d body=%s", rec.Code, rec.Body.String())
	}
	if authorizeCalled {
		t.Fatal("anonymous private resource should not call authorizer after it writes a transport error")
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	contents := body["contents"].([]any)
	entry := contents[0].(map[string]any)
	content := entry["json"].(map[string]any)
	if content["authRequired"] != true {
		t.Fatalf("expected authRequired content, got %#v", content)
	}
}

func TestMCPPrivateReadToolWithoutKeyReturnsSafeAuthRequiredContent(t *testing.T) {
	s := New(nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp/tools/call", strings.NewReader(`{"name":"listAgentGrants","arguments":{"agentWallet":"0x0000000000000000000000000000000000000000"}}`))
	rec := httptest.NewRecorder()
	authorizeCalled := false

	s.handleToolsCallWithAuthorize(func(w http.ResponseWriter, r *http.Request) bool {
		authorizeCalled = true
		w.WriteHeader(http.StatusUnauthorized)
		return false
	})(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected safe MCP tool content response, got %d body=%s", rec.Code, rec.Body.String())
	}
	if authorizeCalled {
		t.Fatal("anonymous read-only private tool should not call authorizer after it writes a transport error")
	}
	if !strings.Contains(rec.Body.String(), `"authRequired":true`) {
		t.Fatalf("expected authRequired tool content, got %s", rec.Body.String())
	}
}

func TestMCPFinancialToolWithoutKeyStillRequiresTransportAuth(t *testing.T) {
	s := New(nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp/tools/call", strings.NewReader(`{"name":"purchaseCapability","arguments":{}}`))
	rec := httptest.NewRecorder()

	s.handleToolsCallWithAuthorize(func(w http.ResponseWriter, r *http.Request) bool {
		w.WriteHeader(http.StatusUnauthorized)
		return false
	})(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected financial tool to require auth, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestMCPAIToolWithoutKeyReturnsFallbackAndSkipsTransportAuth(t *testing.T) {
	s := New(nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp/tools/call", strings.NewReader(`{"name":"market_analysis","arguments":{}}`))
	rec := httptest.NewRecorder()
	authorizeCalled := false

	s.handleToolsCallWithAuthorize(func(w http.ResponseWriter, r *http.Request) bool {
		authorizeCalled = true
		w.WriteHeader(http.StatusUnauthorized)
		return false
	})(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected anonymous AI fallback to return 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if authorizeCalled {
		t.Fatal("anonymous AI fallback should not call transport authorizer")
	}
	if !strings.Contains(rec.Body.String(), `"source":"fallback"`) {
		t.Fatalf("expected fallback AI content, got %s", rec.Body.String())
	}
}

func TestMCPAIToolWithKeyStillRequiresTransportAuth(t *testing.T) {
	s := New(nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mcp/tools/call", strings.NewReader(`{"name":"market_analysis","arguments":{}}`))
	req.Header.Set("Authorization", "Bearer sk_test_invalid")
	rec := httptest.NewRecorder()

	s.handleToolsCallWithAuthorize(func(w http.ResponseWriter, r *http.Request) bool {
		w.WriteHeader(http.StatusUnauthorized)
		return false
	})(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected keyed AI call to require auth, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestMCPAIToolsReturnFallbackWhenProviderUnavailable(t *testing.T) {
	s := New(nil, nil, nil, nil, nil)
	got, err := s.callTool(context.Background(), "market_analysis", map[string]any{})
	if err != nil {
		t.Fatalf("expected AI fallback, got error: %v", err)
	}
	body, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("expected map fallback, got %T", got)
	}
	if body["source"] != "fallback" {
		t.Fatalf("expected fallback source, got %#v", body)
	}
}

func TestCachedValueCoalescesConcurrentBuilds(t *testing.T) {
	s := New(nil, nil, nil, nil, nil)
	var builds int32
	const callers = 16
	start := make(chan struct{})
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			<-start
			value, err := s.cachedValue("catalog:test", time.Minute, func() (any, error) {
				atomic.AddInt32(&builds, 1)
				time.Sleep(20 * time.Millisecond)
				return "ready", nil
			})
			if err != nil {
				errs <- err
				return
			}
			if value != "ready" {
				errs <- context.Canceled
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("unexpected cachedValue error: %v", err)
		}
	}
	if got := atomic.LoadInt32(&builds); got != 1 {
		t.Fatalf("expected one coalesced build, got %d", got)
	}
}

func TestDryRunCapabilityFallsBackWithoutLiveCatalog(t *testing.T) {
	s := New(nil, nil, nil, nil, nil)
	got, err := s.toolDryRunCapability(context.Background(), map[string]any{
		"input": map[string]any{"prompt": "ping"},
	})
	if err != nil {
		t.Fatalf("expected dry run fallback, got error: %v", err)
	}
	resp, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("expected map response, got %T", got)
	}
	if resp["dry_run"] != true {
		t.Fatalf("expected dry_run=true, got %#v", resp["dry_run"])
	}
	if resp["capability"] == nil || resp["route"] == nil {
		t.Fatalf("expected fallback capability and route, got %#v", resp)
	}
}
