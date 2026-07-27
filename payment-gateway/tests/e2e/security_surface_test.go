package e2e_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestAPIKeyInQueryIsRejectedWhenProductionGuardEnabled(t *testing.T) {
	c := newE2EClient(t)
	req, err := http.NewRequest(http.MethodGet, c.baseURL+"/developers/dashboard?apiKey="+c.apiKey, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatalf("query secret request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected query api key to be rejected, got %d", resp.StatusCode)
	}
}

func TestInternalRouteRequiresHMAC(t *testing.T) {
	c := newE2EClient(t)
	resp := c.post("/internal/email/test", map[string]any{"to": "nobody@example.com"}, "")
	requireStatus(t, resp, 401, 403)
}

func TestMCPDiscoveryIsPublicButProtectedToolsRequireAuth(t *testing.T) {
	c := newE2EClient(t)
	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/mcp/initialize", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatalf("MCP unauthenticated request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected MCP discovery without auth to pass, got %d", resp.StatusCode)
	}

	protected, err := http.NewRequest(http.MethodPost, c.baseURL+"/mcp/tools/call", strings.NewReader(`{"name":"purchaseCapability","arguments":{}}`))
	if err != nil {
		t.Fatalf("new protected request: %v", err)
	}
	protected.Header.Set("Content-Type", "application/json")
	protectedResp, err := c.http.Do(protected)
	if err != nil {
		t.Fatalf("MCP protected unauthenticated request failed: %v", err)
	}
	defer protectedResp.Body.Close()
	if protectedResp.StatusCode != http.StatusUnauthorized && protectedResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected protected MCP tool without auth to fail, got %d", protectedResp.StatusCode)
	}
}
