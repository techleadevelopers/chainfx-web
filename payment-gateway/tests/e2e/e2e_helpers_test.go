package e2e_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

type e2eClient struct {
	t       *testing.T
	baseURL string
	apiKey  string
	http    *http.Client
}

type e2eResponse struct {
	Status int
	Body   map[string]any
	Raw    string
}

const (
	testAgentWallet = "0x0000000000000000000000000000000000001001"
	testPayerWallet = "0x0000000000000000000000000000000000001001"
)

func newE2EClient(t *testing.T) *e2eClient {
	t.Helper()
	if os.Getenv("RUN_E2E_TESTS") != "true" {
		t.Skip("set RUN_E2E_TESTS=true to run HTTP E2E tests")
	}
	baseURL := strings.TrimRight(getenv("E2E_BASE_URL", "http://localhost:8080"), "/")
	apiKey := strings.TrimSpace(os.Getenv("E2E_API_KEY"))
	if apiKey == "" {
		t.Skip("set E2E_API_KEY to run authenticated E2E tests")
	}
	return &e2eClient{
		t:       t,
		baseURL: baseURL,
		apiKey:  apiKey,
		http:    &http.Client{Timeout: 20 * time.Second},
	}
}

func (c *e2eClient) get(path string) e2eResponse {
	c.t.Helper()
	return c.do(http.MethodGet, path, nil, "")
}

func (c *e2eClient) post(path string, body any, idempotencyKey string) e2eResponse {
	c.t.Helper()
	return c.do(http.MethodPost, path, body, idempotencyKey)
}

func (c *e2eClient) do(method, path string, body any, idempotencyKey string) e2eResponse {
	c.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			c.t.Fatalf("marshal request body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.baseURL+path, reader)
	if err != nil {
		c.t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
		req.Header.Set("X-Idempotency-Key", idempotencyKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s failed: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		c.t.Fatalf("read response body: %v", err)
	}
	out := e2eResponse{Status: resp.StatusCode, Raw: string(raw)}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out.Body)
	}
	return out
}

func requireStatus(t *testing.T, resp e2eResponse, allowed ...int) {
	t.Helper()
	for _, status := range allowed {
		if resp.Status == status {
			return
		}
	}
	t.Fatalf("unexpected status %d, want %v, body=%s", resp.Status, allowed, resp.Raw)
}

func requireStringField(t *testing.T, payload map[string]any, keys ...string) string {
	t.Helper()
	for _, key := range keys {
		if value, ok := payload[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	t.Fatalf("missing string field %v in %#v", keys, payload)
	return ""
}

func getStringField(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := payload[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func idempotencyKey(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

func envWallet(name, fallback string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	return value
}

func getenv(name, fallback string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	return value
}

func firstCapabilityID(t *testing.T, resp e2eResponse) string {
	t.Helper()
	capabilities, ok := resp.Body["capabilities"].([]any)
	if !ok || len(capabilities) == 0 {
		t.Skip("marketplace has no capabilities seeded")
	}
	for _, item := range capabilities {
		capability, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if id := getStringField(capability, "id", "capabilityId", "slug"); id != "" {
			return id
		}
	}
	t.Skip("marketplace capabilities response has no capability id")
	return ""
}
