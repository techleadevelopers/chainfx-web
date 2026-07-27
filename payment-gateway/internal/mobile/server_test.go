package mobile

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"payment-gateway/internal/config"
)

func TestMobileWrapHandlesCORSPreflight(t *testing.T) {
	t.Setenv("ALLOWED_ORIGINS", "https://chatgpt.com")
	s := New(&config.Config{}, nil, nil, nil)
	handler := s.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("mobile preflight should not delegate to existing handler")
	}))
	req := httptest.NewRequest(http.MethodOptions, "/api/mobile/assets", nil)
	req.Header.Set("Origin", "https://chatgpt.com")
	req.Header.Set("Access-Control-Request-Headers", "Authorization, X-Request-Id, Idempotency-Key")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://chatgpt.com" {
		t.Fatalf("expected reflected origin, got %q", got)
	}
	allowHeaders := rec.Header().Get("Access-Control-Allow-Headers")
	for _, header := range []string{"Authorization", "X-Request-Id", "Idempotency-Key", "X-Idempotency-Key"} {
		if !strings.Contains(allowHeaders, header) {
			t.Fatalf("expected %s in allow headers, got %q", header, allowHeaders)
		}
	}
	exposeHeaders := rec.Header().Get("Access-Control-Expose-Headers")
	for _, header := range []string{"X-Request-Id", "Idempotency-Key", "Idempotency-Key-Source", "Idempotent-Replayed"} {
		if !strings.Contains(exposeHeaders, header) {
			t.Fatalf("expected %s in expose headers, got %q", header, exposeHeaders)
		}
	}
}

func TestMobileCORSAllowsProductionAdminOrigin(t *testing.T) {
	t.Setenv("ALLOWED_ORIGINS", "https://chatgpt.com")
	s := New(&config.Config{}, nil, nil, nil)
	handler := s.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("mobile preflight should not delegate to existing handler")
	}))
	req := httptest.NewRequest(http.MethodOptions, "/api/mobile/assets", nil)
	req.Header.Set("Origin", "https://www.chainfx.store")
	req.Header.Set("Access-Control-Request-Headers", "Authorization, X-Request-Id, Idempotency-Key")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://www.chainfx.store" {
		t.Fatalf("expected production admin origin, got %q", got)
	}
}

func TestMobileCORSAllowsLocalhostDevOriginInProduction(t *testing.T) {
	t.Setenv("ALLOWED_ORIGINS", "*")
	s := New(&config.Config{Environment: "production"}, nil, nil, nil)
	handler := s.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("mobile preflight should not delegate to existing handler")
	}))
	req := httptest.NewRequest(http.MethodOptions, "/api/mobile/order/buy/quote", nil)
	req.Header.Set("Origin", "http://localhost:8081")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "Authorization, Content-Type")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:8081" {
		t.Fatalf("expected localhost origin, got %q", got)
	}
}

func TestMobileCORSAllowsPrivateLANDevOriginInProduction(t *testing.T) {
	t.Setenv("ALLOWED_ORIGINS", "*")
	s := New(&config.Config{Environment: "production"}, nil, nil, nil)
	handler := s.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("mobile preflight should not delegate to existing handler")
	}))
	req := httptest.NewRequest(http.MethodOptions, "/api/mobile/order/buy/quote", nil)
	req.Header.Set("Origin", "http://192.168.250.53:8081")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "Authorization, Content-Type")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://192.168.250.53:8081" {
		t.Fatalf("expected private LAN origin, got %q", got)
	}
}

func TestMobileCORSRejectsUnknownOriginWhenRestricted(t *testing.T) {
	t.Setenv("ALLOWED_ORIGINS", "https://chatgpt.com")
	s := New(&config.Config{}, nil, nil, nil)
	handler := s.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("mobile preflight should not delegate to existing handler")
	}))
	req := httptest.NewRequest(http.MethodOptions, "/api/mobile/assets", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("expected no CORS origin for unknown host, got %q", got)
	}
}

func TestMobileCORSRejectsWildcardUnknownOriginInProduction(t *testing.T) {
	t.Setenv("ALLOWED_ORIGINS", "*")
	s := New(&config.Config{Environment: "production"}, nil, nil, nil)
	handler := s.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("mobile preflight should not delegate to existing handler")
	}))
	req := httptest.NewRequest(http.MethodOptions, "/api/mobile/assets", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("expected no CORS origin for unknown host in production, got %q", got)
	}
}
