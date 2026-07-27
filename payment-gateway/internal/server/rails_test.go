package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"payment-gateway/internal/config"
	"payment-gateway/internal/database"
	"payment-gateway/internal/workers"
)

func TestNormalizePaymentRailPixBRL(t *testing.T) {
	currency, method, amount := normalizePaymentRail("", "", 0, 150, 0)
	if currency != "BRL" || method != "pix" || amount != 150 {
		t.Fatalf("unexpected rail: %s %s %.2f", currency, method, amount)
	}
}

func TestNormalizePaymentRailEfiCardBRL(t *testing.T) {
	currency, method, amount := normalizePaymentRail("BRL", "visa", 125, 0, 0)
	if currency != "BRL" || method != "credit_card" || amount != 125 {
		t.Fatalf("unexpected rail: %s %s %.2f", currency, method, amount)
	}
}

func TestNormalizePaymentRailRejectsUnsupported(t *testing.T) {
	currency, method, amount := normalizePaymentRail("USD", "pix", 10, 0, 0)
	if currency != "" || method != "" || amount != 0 {
		t.Fatalf("expected unsupported rail to be rejected")
	}
}

func TestCustomerAccessTokenPrefersHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/buy/id?accessToken=query-token", nil)
	req.Header.Set("X-Customer-Access-Token", "header-token")

	if got := customerAccessToken(req); got != "header-token" {
		t.Fatalf("expected header token, got %q", got)
	}
}

func TestPixBuyWebhookRequiresProviderID(t *testing.T) {
	secret := "pix-secret"
	body := []byte(`{"buyId":"00000000-0000-4000-8000-000000000001","status":"concluido"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/pix/webhook/buy", strings.NewReader(string(body)))
	req.Header.Set("x-efi-signature", rawHMAC(secret, body))
	rec := httptest.NewRecorder()

	s := &Server{cfg: &config.Config{PixWebhookSecret: secret}, workers: &workers.WorkerManager{}}
	s.handlePixWebhookBuy(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected missing providerId to be rejected with 400, got %d", rec.Code)
	}
}

func TestEmailTestRequiresInternalHMAC(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/internal/email/test", strings.NewReader(`{"to":"ops@example.com"}`))
	rec := httptest.NewRecorder()

	s := &Server{cfg: &config.Config{SignerHmacSecret: "internal-secret"}}
	s.handleEmailTest(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected unsigned email test to be rejected, got %d", rec.Code)
	}
}

func TestMCPInitializeRouteUsesAPIKeyAuth(t *testing.T) {
	cfg := &config.Config{
		ChainFXLiveSecretKeys: "sk_live_test_mcp",
		ChainFXRequireAPIKey:  true,
	}
	wm := workers.NewWorkerManager(nil, cfg, nil, nil)
	s := New(cfg, nil, wm, nil)

	req := httptest.NewRequest(http.MethodPost, "/mcp/initialize", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer sk_live_test_mcp")
	rec := httptest.NewRecorder()

	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected MCP initialize route to return 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"chainfx-mcp"`) {
		t.Fatalf("expected MCP server info in response, got %s", rec.Body.String())
	}
}

func TestAuthorizeChainFXAllowsAnonymousMCPAIOnly(t *testing.T) {
	s := &Server{cfg: &config.Config{Environment: "production", ChainFXRequireAPIKey: true}}
	req := httptest.NewRequest(http.MethodPost, "/mcp/tools/call", strings.NewReader(`{"name":"market_analysis","arguments":{}}`))
	rec := httptest.NewRecorder()
	auth, ok := s.authorizeChainFX(rec, req)
	if !ok {
		t.Fatalf("expected anonymous MCP AI probe to pass, got status %d body %s", rec.Code, rec.Body.String())
	}
	if auth.Mode != "anonymous-mcp-ai" {
		t.Fatalf("expected anonymous-mcp-ai mode, got %q", auth.Mode)
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"market_analysis"`) {
		t.Fatalf("expected authorize to restore request body, got %s", string(body))
	}

	req = httptest.NewRequest(http.MethodPost, "/mcp/tools/call", strings.NewReader(`{"name":"purchaseCapability","arguments":{}}`))
	rec = httptest.NewRecorder()
	if _, ok := s.authorizeChainFX(rec, req); ok {
		t.Fatal("expected anonymous MCP financial/write tool to remain unauthorized")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body %s", rec.Code, rec.Body.String())
	}
}

func TestCORSPreflightAllowsTraceHeaders(t *testing.T) {
	cfg := &config.Config{AllowedOrigins: "https://chatgpt.com"}
	req := httptest.NewRequest(http.MethodOptions, "/readyz", nil)
	req.Header.Set("Origin", "https://chatgpt.com")
	req.Header.Set("Access-Control-Request-Method", "GET")
	req.Header.Set("Access-Control-Request-Headers", "X-Request-Id, X-Correlation-Id, X-Trace-Id, PAYMENT")
	rec := httptest.NewRecorder()

	cors(cfg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("preflight should not reach next handler")
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 preflight, got %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://chatgpt.com" {
		t.Fatalf("expected allowed origin to be reflected, got %q", got)
	}
	allowHeaders := rec.Header().Get("Access-Control-Allow-Headers")
	for _, header := range []string{"X-Request-Id", "X-Correlation-Id", "X-Trace-Id", "PAYMENT"} {
		if !strings.Contains(allowHeaders, header) {
			t.Fatalf("expected %s in Access-Control-Allow-Headers, got %q", header, allowHeaders)
		}
	}
	exposeHeaders := rec.Header().Get("Access-Control-Expose-Headers")
	for _, header := range []string{"X-Payment-Required", "PAYMENT-RESPONSE"} {
		if !strings.Contains(exposeHeaders, header) {
			t.Fatalf("expected %s in Access-Control-Expose-Headers, got %q", header, exposeHeaders)
		}
	}
}

func TestWebAvailabilityAliasesBypassAuthAndDB(t *testing.T) {
	cfg := &config.Config{ChainFXRequireAPIKey: true}
	wm := workers.NewWorkerManager(nil, cfg, nil, nil)
	s := New(cfg, nil, wm, nil)

	for _, path := range []string{"/admin", "/app/agent/", "/app/developer/"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()

		s.Handler().ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s: expected 200 availability response, got %d body=%s", path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"ok":true`) {
			t.Fatalf("%s: expected ok response, got %s", path, rec.Body.String())
		}
	}
}

func TestSmartRateLimitSkipsCriticalWebhooks(t *testing.T) {
	s := &Server{cfg: &config.Config{}}
	for _, path := range []string{
		"/healthz",
		"/readyz",
		"/api/mobile/health",
		"/internal/sweep",
		"/api/pix/webhook",
		"/api/pix/webhook/buy",
		"/api/efi/charges/webhook/buy",
		"/api/rates",
		"/mcp/initialize",
		"/mcp/tools/list",
		"/mcp/resources/list",
	} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		if !s.shouldSkipSmartRateLimit(req) {
			t.Fatalf("expected %s to bypass global smart rate limit", path)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/api/order", nil)
	if s.shouldSkipSmartRateLimit(req) {
		t.Fatal("expected /api/order to stay protected by global smart rate limit")
	}
}

func TestProductionRejectsAPIKeyInQueryString(t *testing.T) {
	s := &Server{cfg: &config.Config{Environment: "production"}}
	req := httptest.NewRequest(http.MethodGet, "/developers/dashboard?apiKey=sk_live_secret", nil)
	rec := httptest.NewRecorder()

	withRequestID(s.withPublicSurfaceGuards(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be reached")
	}))).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "SECRET_IN_QUERY_NOT_ALLOWED") {
		t.Fatalf("expected secret-in-query error, got %s", rec.Body.String())
	}
}

func TestSensitiveProbePathsAreBlockedBeforeFallback(t *testing.T) {
	s := &Server{cfg: &config.Config{Environment: "production"}}
	for _, path := range []string{
		"/.env",
		"/.git/config",
		"/secrets/efi-production.p12",
		"/debug/pprof/",
		"/actuator/env",
		"/phpinfo.php",
		"/config.json",
		"/docs/..%2f..%2f.env",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		withRequestID(s.withPublicSurfaceGuards(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatalf("handler should not be reached for %s", path)
		}))).ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected %s to return 404, got %d", path, rec.Code)
		}
	}
}

func TestProductionSecurityHeaders(t *testing.T) {
	s := &Server{cfg: &config.Config{Environment: "production"}}
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()

	securityHeaders(s.cfg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})).ServeHTTP(rec, req)

	if got := rec.Header().Get("Strict-Transport-Security"); got != "max-age=31536000; includeSubDomains" {
		t.Fatalf("expected HSTS header, got %q", got)
	}
	if got := rec.Header().Get("Content-Security-Policy"); !strings.Contains(got, "frame-ancestors 'none'") {
		t.Fatalf("expected API CSP header, got %q", got)
	}
}

func TestHTMLRoutesDoNotReceiveStrictAPICSP(t *testing.T) {
	s := &Server{cfg: &config.Config{Environment: "production"}}
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.Header.Set("Accept", "text/html")
	rec := httptest.NewRecorder()

	securityHeaders(s.cfg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Security-Policy"); got != "" {
		t.Fatalf("expected no strict API CSP on HTML route, got %q", got)
	}
}

func TestDevelopmentAllowsAPIKeyInQueryStringForCompatibility(t *testing.T) {
	s := &Server{cfg: &config.Config{Environment: "development"}}
	req := httptest.NewRequest(http.MethodGet, "/developers/dashboard?apiKey=sk_test_local", nil)
	rec := httptest.NewRecorder()

	withRequestID(s.withPublicSurfaceGuards(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))).ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected compatibility path to pass, got %d", rec.Code)
	}
}

func TestManualWebhookTargetRejectsSSRFAndPlainHTTP(t *testing.T) {
	for _, target := range []string{
		"http://example.com/hook",
		"https://127.0.0.1/hook",
		"https://169.254.169.254/latest/meta-data",
		"https://localhost/hook",
	} {
		if err := validateManualWebhookTarget(target); err == nil {
			t.Fatalf("expected %s to be rejected", target)
		}
	}
}

func TestInternalSurfaceRequiresHMACAndAllowedRemoteIP(t *testing.T) {
	s := &Server{cfg: &config.Config{InternalAllowedCIDRs: "10.10.0.0/16"}}
	req := httptest.NewRequest(http.MethodPost, "/internal/sweep", strings.NewReader(`{}`))
	req.RemoteAddr = "10.11.0.5:1234"
	req.Header.Set("x-internal-hmac", "sig")
	rec := httptest.NewRecorder()

	withRequestID(s.withPublicSurfaceGuards(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be reached")
	}))).ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected forbidden for remote IP outside CIDR, got %d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/internal/sweep", strings.NewReader(`{}`))
	req.RemoteAddr = "10.10.8.5:1234"
	req.Header.Set("x-internal-hmac", "sig")
	rec = httptest.NewRecorder()
	withRequestID(s.withPublicSurfaceGuards(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected allowed internal request to pass to handler, got %d", rec.Code)
	}
}

func TestWriteErrorUsesStandardSanitizedResponse(t *testing.T) {
	rec := httptest.NewRecorder()
	writeError(rec, errors.New("provider leaked secret token"))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "provider leaked secret token") {
		t.Fatalf("raw error leaked to client: %s", rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["code"] != "INTERNAL_ERROR" || body["message"] == "" {
		t.Fatalf("expected standard error body, got %#v", body)
	}
}

func TestSmartRateLimitRouteClasses(t *testing.T) {
	cases := []struct {
		method string
		path   string
		want   string
	}{
		{http.MethodGet, "/openapi.json", "discovery"},
		{http.MethodPost, "/mcp/initialize", "discovery"},
		{http.MethodPost, "/mcp/tools/list", "discovery"},
		{http.MethodPost, "/mcp/resources/list", "discovery"},
		{http.MethodPost, "/mcp/tools/call", "mcp_tool"},
		{http.MethodPost, "/mcp/resources/read", "mcp_resource_read"},
		{http.MethodGet, "/mcp/capabilities.json", "public_discovery"},
		{http.MethodGet, "/marketplace/capabilities", "public_discovery"},
		{http.MethodPost, "/x402/capabilities/document_ocr/execute", "x402_challenge"},
		{http.MethodPost, "/marketplace/purchase/mp_1/execute", "execution"},
		{http.MethodPost, "/api/order", "write"},
		{http.MethodPost, "/api/quote", "read"},
		{http.MethodGet, "/api/price", "read"},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		if got := smartRateLimitRouteClass(req); got != tc.want {
			t.Fatalf("%s %s: expected class %s, got %s", tc.method, tc.path, tc.want, got)
		}
	}
	if got := smartRateLimitMax("live", "mcp_tool", 100); got != 2400 {
		t.Fatalf("expected live MCP tool limit 2400, got %d", got)
	}
	if got := smartRateLimitMax("test", "mcp_tool", 100); got != 1200 {
		t.Fatalf("expected test MCP tool limit 1200, got %d", got)
	}
	if got := smartRateLimitMax("anonymous", "read", 100); got != 600 {
		t.Fatalf("expected anonymous read limit 600 for availability probes, got %d", got)
	}
	if got := smartRateLimitMax("anonymous", "x402_challenge", 100); got != 120 {
		t.Fatalf("expected anonymous x402 challenge limit 120, got %d", got)
	}
}

func TestPenaltyBoxBlocksAfterRepeatedRateLimitViolations(t *testing.T) {
	cfg := &config.Config{RateLimitMax: 1}
	s := &Server{
		cfg:           cfg,
		globalLimiter: newRateLimiter(60000, 20),
		penaltyBox:    newPenaltyBox(true, 2, time.Minute, 15*time.Minute, time.Hour, 24*time.Hour),
	}
	hits := 0
	handler := s.withSmartRateLimit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusNoContent)
	}))

	limit := smartRateLimitMax("anonymous", "write", cfg.RateLimitMax)
	for i := 0; i < limit+3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/order", nil)
		req.RemoteAddr = "203.0.113.10:1234"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if i < limit && rec.Code != http.StatusNoContent {
			t.Fatalf("request %d should pass before limit, got %d body=%s", i, rec.Code, rec.Body.String())
		}
		if i >= limit && rec.Code != http.StatusTooManyRequests {
			t.Fatalf("request %d should be limited/blocked, got %d body=%s", i, rec.Code, rec.Body.String())
		}
	}
	if hits != limit {
		t.Fatalf("expected only requests before the limit to reach handler, got %d hits", hits)
	}
}

func TestPenaltyBoxBannedRequestsDoNotEscalateOffense(t *testing.T) {
	box := newPenaltyBox(true, 1, time.Minute, 15*time.Minute, time.Hour, 24*time.Hour)
	now := time.Now()
	banned, _ := box.recordViolation("ip:203.0.113.10|route:write", now)
	if !banned {
		t.Fatal("expected first violation to ban with threshold 1")
	}
	for i := 0; i < 5; i++ {
		if blocked, _ := box.banned("ip:203.0.113.10|route:write", now.Add(time.Duration(i+1)*time.Second)); !blocked {
			t.Fatalf("expected request %d to stay blocked", i)
		}
	}
	entry := box.entries["ip:203.0.113.10|route:write"]
	if entry == nil || entry.offenses != 1 {
		t.Fatalf("blocked requests should not escalate offense count, got %#v", entry)
	}
}

func TestPenaltyBoxIsScopedByRouteClass(t *testing.T) {
	cfg := &config.Config{RateLimitMax: 1}
	s := &Server{
		cfg:           cfg,
		globalLimiter: newRateLimiter(60000, 20),
		penaltyBox:    newPenaltyBox(true, 1, time.Minute, 15*time.Minute, time.Hour, 24*time.Hour),
	}
	hits := 0
	handler := s.withSmartRateLimit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusNoContent)
	}))

	limit := smartRateLimitMax("anonymous", "write", cfg.RateLimitMax)
	for i := 0; i < limit+1; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/order", nil)
		req.RemoteAddr = "203.0.113.11:1234"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}

	readReq := httptest.NewRequest(http.MethodGet, "/api/mobile/user/profile", nil)
	readReq.RemoteAddr = "203.0.113.11:1234"
	readRec := httptest.NewRecorder()
	handler.ServeHTTP(readRec, readReq)
	if readRec.Code != http.StatusNoContent {
		t.Fatalf("write route penalty should not block read route class, got %d body=%s", readRec.Code, readRec.Body.String())
	}
	if hits != limit+1 {
		t.Fatalf("expected write passes plus read pass to hit handler, got %d", hits)
	}
}

func TestClientIPTrustsForwardedHeadersOnlyFromTrustedProxy(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/order", nil)
	req.RemoteAddr = "203.0.113.10:1234"
	req.Header.Set("X-Forwarded-For", "198.51.100.99")
	if got := clientIP(req); got != "203.0.113.10" {
		t.Fatalf("expected untrusted public remote to ignore X-Forwarded-For, got %s", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/order", nil)
	req.RemoteAddr = "10.0.0.5:1234"
	req.Header.Set("X-Forwarded-For", "198.51.100.99")
	if got := clientIP(req); got != "198.51.100.99" {
		t.Fatalf("expected trusted private proxy to use X-Forwarded-For, got %s", got)
	}
}

func TestNormalizeSellNetworkAliases(t *testing.T) {
	cases := map[string]string{
		"":        "BSC",
		"BEP20":   "BSC",
		"binance": "BSC",
		"POL":     "POLYGON",
		"matic":   "POLYGON",
		"polygon": "POLYGON",
	}
	for input, want := range cases {
		if got := normalizeSellNetwork(input); got != want {
			t.Fatalf("normalizeSellNetwork(%q): expected %s, got %s", input, want, got)
		}
	}
}

func TestSellNetworkEnabledRequiresPolygonConfig(t *testing.T) {
	s := &Server{cfg: &config.Config{}}
	if !s.sellNetworkEnabled("BSC") {
		t.Fatal("expected BSC sell network to remain enabled")
	}
	if s.sellNetworkEnabled("POLYGON") {
		t.Fatal("expected Polygon sell network to require Polygon RPC and USDT contract config")
	}

	s.cfg.PolygonRpcUrls = "https://polygon-rpc.example"
	s.cfg.PolygonUsdtContract = "0xc2132d05d31c914a87c6611c10748aeb04b58e8f"
	if !s.sellNetworkEnabled("POL") {
		t.Fatal("expected Polygon aliases to be enabled when Polygon config exists")
	}
}

func TestAgentDiscoveryAdvertisesSixPercentFee(t *testing.T) {
	s := &Server{cfg: &config.Config{TreasuryHot: "0x000000000000000000000000000000000000dEaD"}}
	req := httptest.NewRequest(http.MethodGet, "/.well-known/ai-services.json", nil)
	rec := httptest.NewRecorder()

	s.handleAIServicesWellKnown(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected discovery to return 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"gatewayFeeBps":600`) {
		t.Fatalf("expected 600 bps ChainFX fee, got %s", body)
	}
	if !strings.Contains(body, "ChainFX 0.60") {
		t.Fatalf("expected 10 USDT fee example with ChainFX 0.60, got %s", body)
	}
}

func TestUSDTAmountToWeiUsesBSCUSDTDecimals(t *testing.T) {
	got := usdtAmountToWei(10)
	want := "10000000000000000000"
	if got.String() != want {
		t.Fatalf("expected %s wei, got %s", want, got.String())
	}
}

func TestDecimalStringToBaseUnitsAvoidsFloatRounding(t *testing.T) {
	got, err := decimalStringToBaseUnits("300.000001", 6)
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != "300000001" {
		t.Fatalf("expected 300000001 base units, got %s", got.String())
	}
	got, err = decimalStringToBaseUnits("300.000000", 18)
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != "300000000000000000000" {
		t.Fatalf("expected 300e18 base units, got %s", got.String())
	}
}

func TestAgentTradeQuoteCalculatesHighFeeForReceiveAmount(t *testing.T) {
	amounts, err := calculateAgentTradeAmounts(500, "receive", agentGatewayFeeBps)
	if err != nil {
		t.Fatal(err)
	}
	if amounts.ReceiveAmount != 500 {
		t.Fatalf("expected receive amount 500, got %.6f", amounts.ReceiveAmount)
	}
	if amounts.PayAmount != 531.914894 {
		t.Fatalf("expected pay amount 531.914894 with 6%% fee, got %.6f", amounts.PayAmount)
	}
	if amounts.ChainFXFeeAmount != 31.914894 {
		t.Fatalf("expected ChainFX fee 31.914894, got %.6f", amounts.ChainFXFeeAmount)
	}
}

func TestAgentDiscoveryAdvertisesLiquidityRail(t *testing.T) {
	s := &Server{cfg: &config.Config{TreasuryHot: "0x000000000000000000000000000000000000dEaD"}}
	req := httptest.NewRequest(http.MethodGet, "/.well-known/ai-services.json", nil)
	rec := httptest.NewRecorder()

	s.handleAIServicesWellKnown(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "/agent/v1/trade/quote") {
		t.Fatalf("expected agent trade quote discovery, got %s", body)
	}
	if !strings.Contains(body, "/agent/v1/assets") || !strings.Contains(body, "enabled BSC/Polygon stablecoin pairs") {
		t.Fatalf("expected supported liquidity rail, got %s", body)
	}
}

func TestAgentAssetsFallbackListsStablecoinsWithSixPercentFee(t *testing.T) {
	s := &Server{cfg: &config.Config{BscUsdtContract: "0x55d398326f99059fF775485246999027B3197955"}}
	assets := s.fallbackAgentTradeAssets()
	if len(assets) != 3 {
		t.Fatalf("expected 3 seeded stablecoins, got %d", len(assets))
	}
	seen := map[string]bool{}
	for _, asset := range assets {
		seen[asset.Symbol] = true
		if asset.FeeBps != agentGatewayFeeBps {
			t.Fatalf("expected %s fee %d, got %d", asset.Symbol, agentGatewayFeeBps, asset.FeeBps)
		}
		if asset.Symbol == "BUSD" && (asset.Enabled || asset.Status != "legacy") {
			t.Fatalf("expected BUSD to be legacy disabled, got enabled=%v status=%s", asset.Enabled, asset.Status)
		}
	}
	for _, symbol := range []string{"USDT", "USDC", "BUSD"} {
		if !seen[symbol] {
			t.Fatalf("expected %s in fallback assets", symbol)
		}
	}
}

func TestAgentCapabilitiesExposeMachineReadableLifecycle(t *testing.T) {
	s := &Server{cfg: &config.Config{BscUsdtContract: "0x55d398326f99059fF775485246999027B3197955"}}
	req := httptest.NewRequest(http.MethodGet, "/agent/v1/capabilities", nil)
	rec := httptest.NewRecorder()

	s.handleAgentCapabilities(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected capabilities 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, expected := range []string{
		"stablecoin_exchange",
		"api_access_purchase",
		"discover_capabilities",
		"create_trade_intent",
		"wallet_signature_headers",
		"/agent/v1/trade/quote",
		"/agent/v1/assets",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("expected capabilities to contain %q, got %s", expected, body)
		}
	}
}

func TestAgentCapabilitiesAdvertiseMarketplacePurchase(t *testing.T) {
	s := &Server{cfg: &config.Config{BscUsdtContract: "0x55d398326f99059fF775485246999027B3197955"}}
	req := httptest.NewRequest(http.MethodGet, "/agent/v1/capabilities", nil)
	rec := httptest.NewRecorder()

	s.handleAgentCapabilities(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected capabilities 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, expected := range []string{
		"marketplace_api_purchase",
		"discover_marketplace",
		"list_products",
		"create_purchase",
		"verify_receipt",
		"receive_access_grant",
		"wallet_signature_auth",
		"planned",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("expected capabilities to contain %q, got %s", expected, body)
		}
	}
}

func TestAgentCapabilitiesAdvertiseCapabilityExchange(t *testing.T) {
	s := &Server{cfg: &config.Config{BscUsdtContract: "0x55d398326f99059fF775485246999027B3197955"}}
	req := httptest.NewRequest(http.MethodGet, "/agent/v1/capabilities", nil)
	rec := httptest.NewRecorder()

	s.handleAgentCapabilities(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected capabilities 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, expected := range []string{
		"capability_exchange",
		"/marketplace/capabilities",
		"semantic_memory",
		"llm_chat",
		"document_ocr",
		"payments_fx",
		"capability_discovery",
		"agent_connect",
		"/agent/connect",
		"/agent/v1/capabilities/{capability}/execute",
		"capabilityRouter",
		"mock_dev",
		"mock_provider_execution",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("expected capabilities to contain %q, got %s", expected, body)
		}
	}
}

func TestAgentTradeQuoteResponseExplainsGrossPaymentFee(t *testing.T) {
	expires := time.Now().UTC().Add(time.Minute)
	resp := agentTradeQuoteResponse(&database.AgentTradeIntent{
		ID:                   "00000000-0000-4000-8000-000000000001",
		PayAsset:             "USDC",
		ReceiveAsset:         "USDT",
		PayAmount:            531.914894,
		ReceiveAmount:        500,
		ChainFXFeeAmount:     31.914894,
		FeeBps:               agentGatewayFeeBps,
		Network:              "BSC",
		PaymentAddress:       "0x000000000000000000000000000000000000dead",
		DestinationWallet:    "0x000000000000000000000000000000000000beef",
		PayTokenContract:     "0x8ac76a51cc950d9822d68b83fe1ad97b32cd580d",
		ReceiveTokenContract: "0x55d398326f99059ff775485246999027b3197955",
		Nonce:                "tr_test",
		RequestHash:          "hash",
		ExpiresAt:            expires,
	}, "https://example.com")
	if resp["feeCalculation"] != "deducted_from_gross_payment" {
		t.Fatalf("expected gross payment fee calculation, got %#v", resp["feeCalculation"])
	}
	if resp["overpaymentPolicy"] == "" {
		t.Fatal("expected overpayment policy in quote response")
	}
}

func rawHMAC(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
