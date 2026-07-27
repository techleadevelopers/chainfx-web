package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	pathpkg "path"
	"strconv"
	"strings"
	"sync"
	"time"

	"payment-gateway/internal/config"
	"payment-gateway/internal/database"
	"payment-gateway/internal/metrics"
)

func (s *Server) withPublicSurfaceGuards(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.rejectSensitivePathProbe(w, r) {
			return
		}
		if s.rejectQueryAPIKeyInProduction(w, r) {
			return
		}
		if s.rejectUnsafeInternalRequest(w, r) {
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) rejectSensitivePathProbe(w http.ResponseWriter, r *http.Request) bool {
	if !isSensitiveProbePath(r.URL.Path) {
		return false
	}
	http.NotFound(w, r)
	return true
}

func isSensitiveProbePath(rawPath string) bool {
	normalized := "/" + strings.TrimLeft(pathpkg.Clean("/"+strings.TrimSpace(rawPath)), "/")
	lower := strings.ToLower(normalized)
	blockedPrefixes := []string{
		"/.env",
		"/.git",
		"/secrets",
		"/debug/pprof",
		"/actuator",
		"/phpinfo.php",
		"/config.json",
	}
	for _, prefix := range blockedPrefixes {
		if lower == prefix || strings.HasPrefix(lower, prefix+"/") {
			return true
		}
	}
	return false
}

func (s *Server) rejectQueryAPIKeyInProduction(w http.ResponseWriter, r *http.Request) bool {
	if s == nil || s.cfg == nil || !s.cfg.IsProduction() {
		return false
	}
	if strings.TrimSpace(r.URL.Query().Get("apiKey")) == "" {
		return false
	}
	writeAPIError(w, r, http.StatusBadRequest, "SECRET_IN_QUERY_NOT_ALLOWED", "Send API keys with Authorization: Bearer or X-Api-Key headers.")
	return true
}

func (s *Server) rejectUnsafeInternalRequest(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/internal/") {
		return false
	}
	if strings.TrimSpace(r.Header.Get("x-internal-hmac")) == "" {
		writeAPIError(w, r, http.StatusUnauthorized, "INTERNAL_SIGNATURE_REQUIRED", "Internal signature is required.")
		return true
	}
	if s == nil || s.cfg == nil || strings.TrimSpace(s.cfg.InternalAllowedCIDRs) == "" {
		writeAPIError(w, r, http.StatusForbidden, "INTERNAL_NETWORK_NOT_ALLOWED", "Internal endpoint is not available from this network.")
		return true
	}
	if !ipAllowedByCIDRList(remoteIP(r), s.cfg.InternalAllowedCIDRs) {
		writeAPIError(w, r, http.StatusForbidden, "INTERNAL_NETWORK_NOT_ALLOWED", "Internal endpoint is not available from this network.")
		return true
	}
	return false
}

func (s *Server) withSmartRateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limiterStart := time.Now()
		if s.shouldSkipSmartRateLimit(r) {
			addServerTiming(r.Context(), "limiter", time.Since(limiterStart))
			next.ServeHTTP(w, r)
			return
		}
		routeClass := smartRateLimitRouteClass(r)
		w.Header().Set("X-Route-Class", routeClass)
		penaltyKey := penaltyKeyForRequest(r, routeClass)
		if banned, bannedUntil := s.penaltyBox.banned(penaltyKey, time.Now()); banned {
			addServerTiming(r.Context(), "limiter", time.Since(limiterStart))
			retryAfter := int(time.Until(bannedUntil).Seconds())
			if retryAfter < 1 {
				retryAfter = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			writeJSON(w, http.StatusTooManyRequests, map[string]any{
				"error":      "temporarily blocked after repeated rate limit violations",
				"routeClass": routeClass,
				"retryAfter": retryAfter,
			})
			return
		}
		key, tier := s.smartRateLimitKey(r)
		limit := smartRateLimitMax(tier, routeClass, s.cfg.RateLimitMax)
		allowed, resetAt, remaining := s.globalLimiter.AllowN(key+":"+routeClass, limit)
		w.Header().Set("X-RateLimit-Limit", strconv.Itoa(limit))
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetAt.Unix(), 10))
		if !allowed {
			addServerTiming(r.Context(), "limiter", time.Since(limiterStart))
			retryAfter := int(time.Until(resetAt).Seconds())
			if retryAfter < 1 {
				retryAfter = 1
			}
			if banned, bannedUntil := s.penaltyBox.recordViolation(penaltyKey, time.Now()); banned {
				if penaltyRetryAfter := int(time.Until(bannedUntil).Seconds()); penaltyRetryAfter > retryAfter {
					retryAfter = penaltyRetryAfter
				}
			}
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			writeJSON(w, http.StatusTooManyRequests, map[string]any{
				"error":      "rate limit exceeded",
				"tier":       tier,
				"routeClass": routeClass,
				"retryAfter": retryAfter,
			})
			return
		}
		addServerTiming(r.Context(), "limiter", time.Since(limiterStart))
		next.ServeHTTP(w, r)
	})
}

func (s *Server) shouldSkipSmartRateLimit(r *http.Request) bool {
	if r.Method == http.MethodOptions {
		return true
	}
	if strings.HasPrefix(r.URL.Path, "/internal/") {
		return true
	}
	switch r.URL.Path {
	case "/healthz", "/readyz", "/api/mobile/health",
		"/api/rates", "/rates", "/api/price", "/price",
		"/mcp/initialize", "/mcp/tools/list", "/mcp/resources/list", "/mcp/prompts/list",
		"/api/pix/webhook", "/api/pix/webhook/buy", "/api/efi/pix/send/webhook", "/api/efi/charges/webhook/buy":
		return true
	default:
		return false
	}
}

func (s *Server) smartRateLimitKey(r *http.Request) (string, string) {
	apiKey := chainFXAPIKey(r)
	if apiKey != "" {
		auth := s.chainFXAuthContext(r)
		tier := "api"
		switch auth.Mode {
		case "live", "live-dev":
			tier = "live"
		case "test", "development":
			tier = "test"
		}
		if !auth.Valid {
			tier = "invalid_key"
		}
		return "key:" + shortSecretHash(apiKey), tier
	}
	return "ip:" + clientIP(r), "anonymous"
}

func shortSecretHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:16]
}

func smartRateLimitRouteClass(r *http.Request) string {
	path := r.URL.Path
	method := r.Method
	if method == http.MethodGet && (path == "/mcp/capabilities.json" || path == "/agent/v1/capabilities" || path == "/agent/v1/reputation" || path == "/agent/v1/sla" || path == "/agent/v1/registries" || strings.HasPrefix(path, "/agent/v1/registry-records/") || path == "/agent/v1/eips" || path == "/agent/v1/assets" || strings.HasPrefix(path, "/agent/v1/pricing/") || path == "/marketplace/capabilities" || path == "/marketplace/products" || strings.HasPrefix(path, "/.well-known/")) {
		return "public_discovery"
	}
	if method == http.MethodPost && path == "/agent/v1/eips/prepare" {
		return "read"
	}
	if path == "/api/rates" || path == "/rates" || path == "/api/price" || path == "/price" || path == "/api/quote" || path == "/quote" || path == "/api/buy/pairs" {
		return "read"
	}
	if method == http.MethodGet && (path == "/openapi.json" || path == "/llms.txt" || path == "/robots.txt" || path == "/sitemap.xml" || strings.HasPrefix(path, "/.well-known/")) {
		return "discovery"
	}
	if method == http.MethodPost && (path == "/mcp/initialize" || path == "/mcp/tools/list" || path == "/mcp/resources/list" || path == "/mcp/prompts/list") {
		return "discovery"
	}
	if method == http.MethodPost && path == "/mcp/resources/read" {
		return "mcp_resource_read"
	}
	if method == http.MethodPost && path == "/mcp/tools/call" {
		return "mcp_tool"
	}
	if strings.HasPrefix(path, "/mcp/") {
		return "mcp"
	}
	if method == http.MethodPost && strings.HasPrefix(path, "/x402/capabilities/") && strings.HasSuffix(path, "/execute") {
		return "x402_challenge"
	}
	if strings.Contains(path, "/execute") || strings.Contains(path, "/usage") || strings.Contains(path, "/purchase") || strings.HasSuffix(path, "/trade/execute") {
		return "execution"
	}
	if method == http.MethodPost {
		return "write"
	}
	return "read"
}

func smartRateLimitMax(tier, routeClass string, configuredMax int) int {
	if configuredMax <= 0 {
		configuredMax = 100
	}
	limits := map[string]map[string]int{
		"anonymous": {
			"public_discovery": 600, "discovery": 600, "read": 600, "write": 20,
			"mcp_resource_read": 300, "mcp_tool": 600, "mcp": 60, "x402_challenge": 120, "execution": 10,
		},
		"invalid_key": {
			"public_discovery": 120, "discovery": 60, "read": 30, "write": 10,
			"mcp_resource_read": 30, "mcp_tool": 30, "mcp": 5, "x402_challenge": 20, "execution": 5,
		},
		"test": {
			"public_discovery": 1800, "discovery": 1200, "read": 800, "write": 240,
			"mcp_resource_read": 900, "mcp_tool": 1200, "mcp": 600, "x402_challenge": 240, "execution": 180,
		},
		"live": {
			"public_discovery": 3600, "discovery": 3000, "read": 1200, "write": 400,
			"mcp_resource_read": 1800, "mcp_tool": 2400, "mcp": 2000, "x402_challenge": 600, "execution": 300,
		},
		"api": {
			"public_discovery": 1800, "discovery": 1200, "read": 800, "write": 200,
			"mcp_resource_read": 900, "mcp_tool": 1200, "mcp": 600, "x402_challenge": 240, "execution": 120,
		},
	}
	if byClass, ok := limits[tier]; ok {
		if limit, ok := byClass[routeClass]; ok {
			return limit
		}
	}
	return configuredMax
}

func cors(cfg *config.Config, next http.Handler) http.Handler {
	allowedOrigins := ""
	if cfg != nil {
		allowedOrigins = cfg.AllowedOrigins
	}
	allowed := strings.Split(allowedOrigins, ",")
	allowed = append(allowed, "http://localhost:5173", "http://127.0.0.1:5173", "https://swapped-cryptocurrensy.vercel.app", "https://www.chainfx.store", "https://chainfx.store", "https://chatgpt.com", "https://chat.openai.com", "https://codex.openai.com")
	allowedHeaders := "Content-Type, Authorization, PAYMENT, Payment, PAYMENT-SIGNATURE, X-Payment, X-Api-Key, X-Admin-Console-Key, X-Request-Id, X-Correlation-Id, X-Trace-Id, X-Agent-ID, X-Agent-Id, X-Client-Agent, X-Agent-Signature, X-Agent-Card-Signature, MCP-Agent-ID, MCP-Agent-Signature, x-internal-hmac, x-idempotency-key, x-efi-signature, x-chainfx-signature"
	allowedMethods := "GET, POST, PATCH, DELETE, OPTIONS"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		w.Header().Add("Vary", "Origin")
		w.Header().Set("Access-Control-Expose-Headers", "X-Request-Id, X-RateLimit-Limit, X-RateLimit-Remaining, X-RateLimit-Reset, Retry-After, Server-Timing, X-Route-Class, X-MCP-Tool-Class, X-Payment-Required, PAYMENT-RESPONSE")
		for _, item := range allowed {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			if item == "*" {
				w.Header().Set("Access-Control-Allow-Origin", "*")
				w.Header().Set("Access-Control-Allow-Headers", allowedHeaders)
				w.Header().Set("Access-Control-Allow-Methods", allowedMethods)
				break
			}
			if origin != "" && item == origin {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Headers", allowedHeaders)
				w.Header().Set("Access-Control-Allow-Methods", allowedMethods)
				break
			}
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func securityHeaders(cfg *config.Config, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if cfg != nil && cfg.IsProduction() {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		if shouldApplyAPICSP(r) {
			w.Header().Set("Content-Security-Policy", "default-src 'self'; base-uri 'self'; frame-ancestors 'none'; object-src 'none'; form-action 'self'")
		}
		next.ServeHTTP(w, r)
	})
}

func shouldApplyAPICSP(r *http.Request) bool {
	if r == nil {
		return true
	}
	path := r.URL.Path
	if path == "/" || path == "/admin" || path == "/developers" || strings.HasPrefix(path, "/app/") {
		return false
	}
	if strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/agent/") || strings.HasPrefix(path, "/mcp/") ||
		strings.HasPrefix(path, "/.well-known/") || strings.HasPrefix(path, "/v1/") || strings.HasPrefix(path, "/x402/") ||
		path == "/healthz" || path == "/readyz" || path == "/rates" || path == "/openapi.json" || path == "/metrics" {
		return true
	}
	accept := strings.ToLower(r.Header.Get("Accept"))
	return !strings.Contains(accept, "text/html")
}

type serverTimingWriter struct {
	http.ResponseWriter
	start   time.Time
	timings *requestTimings
	wrote   bool
	status  int
}

func (w *serverTimingWriter) WriteHeader(status int) {
	if !w.wrote {
		total := time.Since(w.start)
		w.Header().Set("Server-Timing", serverTimingHeader(w.timings, total))
		w.wrote = true
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *serverTimingWriter) Write(body []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func (w *serverTimingWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *serverTimingWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (s *Server) withServerTiming(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		timings := &requestTimings{stages: make(map[string]time.Duration)}
		ctx := context.WithValue(r.Context(), serverTimingContextKey{}, timings)
		start := time.Now()
		rec := &serverTimingWriter{ResponseWriter: w, start: start, timings: timings}
		next.ServeHTTP(rec, r.WithContext(ctx))
		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		route := metrics.RoutePattern(r.Method, r.URL.Path, r.Pattern)
		metrics.ObserveHTTPRequest(r.Method, route, status, time.Since(start))
		for stage, duration := range timings.snapshot() {
			metrics.ObserveHTTPStage(r.Method, route, stage, duration)
		}
	})
}

type serverTimingContextKey struct{}

type requestTimings struct {
	mu     sync.Mutex
	stages map[string]time.Duration
}

func addServerTiming(ctx context.Context, name string, duration time.Duration) {
	timings, ok := ctx.Value(serverTimingContextKey{}).(*requestTimings)
	if !ok || timings == nil || name == "" {
		return
	}
	timings.mu.Lock()
	timings.stages[name] += duration
	timings.mu.Unlock()
}

func (t *requestTimings) snapshot() map[string]time.Duration {
	out := map[string]time.Duration{}
	if t == nil {
		return out
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, v := range t.stages {
		out[k] = v
	}
	return out
}

func serverTimingHeader(timings *requestTimings, total time.Duration) string {
	stages := map[string]time.Duration{}
	if timings != nil {
		timings.mu.Lock()
		for k, v := range timings.stages {
			stages[k] = v
		}
		timings.mu.Unlock()
	}
	parts := make([]string, 0, 16)
	for _, name := range []string{
		"limiter", "auth", "json_decode", "terminal_auth", "amount_parse",
		"token_validation", "price_lookup", "risk_validation",
		"authorization_lookup", "db_transaction", "ledger_capture", "ledger_reverse",
		"queue", "response_write",
	} {
		if duration, ok := stages[name]; ok {
			parts = append(parts, fmt.Sprintf("%s;dur=%.2f", name, duration.Seconds()*1000))
		}
	}
	totalMS := total.Seconds() * 1000
	if _, ok := stages["handler"]; !ok {
		parts = append(parts, fmt.Sprintf("handler;dur=%.2f", totalMS))
	}
	parts = append(parts, fmt.Sprintf("server_total;dur=%.2f", totalMS))
	return strings.Join(parts, ", ")
}

type statusCaptureWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusCaptureWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusCaptureWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(body)
}

func (w *statusCaptureWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *statusCaptureWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (s *Server) withDeveloperRequestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusCaptureWriter{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		duration := time.Since(start)
		if shouldEmitHTTPLog(status, duration) {
			slog.Warn("http_request_slow_or_error", "request_id", requestID(r), "method", r.Method, "path", r.URL.Path, "status", status, "duration_ms", duration.Milliseconds())
		}
		if s == nil || s.db == nil || s.shouldSkipDeveloperRequestLog(r) || status == http.StatusTooManyRequests {
			return
		}
		apiKey := chainFXAPIKey(r)
		auth := s.chainFXAuthContext(r)
		if s.cfg != nil && s.cfg.IsProduction() {
			apiKey = chainFXAPIKeyFromHeader(r)
			auth = s.chainFXAuthForKey(apiKey)
			if !auth.Valid && apiKey != "" {
				if validated, err := s.db.ValidateDeveloperAPIKey(r.Context(), apiKey); err == nil && validated != nil {
					auth = chainFXAuth{
						Valid:         true,
						Sandbox:       validated.Environment != "production",
						Mode:          validated.Environment,
						ProjectID:     validated.ProjectID,
						APIKeyID:      validated.KeyID,
						APIKeyLogHash: validated.LogHash,
						Scopes:        validated.Scopes,
					}
				}
			}
		}
		authMode := auth.Mode
		if authMode == "" {
			authMode = "anonymous"
		}
		scope := "none"
		if apiKey != "" {
			scope = "api_key"
		}
		apiKeyHash := shortSecretHash(apiKey)
		if auth.APIKeyLogHash != "" {
			apiKeyHash = auth.APIKeyLogHash
		}
		s.enqueueAPIRequestLog(database.APIRequestLogInput{
			RequestID:    requestID(r),
			Method:       r.Method,
			Path:         r.URL.Path,
			RouteClass:   smartRateLimitRouteClass(r),
			StatusCode:   status,
			DurationMS:   duration.Milliseconds(),
			APIKeyHash:   apiKeyHash,
			APIKeyScope:  scope,
			AuthMode:     authMode,
			ClientIP:     clientIP(r),
			UserAgent:    r.UserAgent(),
			AgentID:      requestAgentID(r),
			AgentSigHash: requestAgentSignatureHash(r),
		})
	})
}

func requestAgentID(r *http.Request) string {
	if r == nil {
		return ""
	}
	for _, header := range []string{"X-Agent-ID", "X-Agent-Id", "X-Client-Agent", "MCP-Agent-ID"} {
		if value := strings.TrimSpace(r.Header.Get(header)); value != "" {
			if len(value) > 160 {
				value = value[:160]
			}
			return value
		}
	}
	return ""
}

func requestAgentSignatureHash(r *http.Request) string {
	if r == nil {
		return ""
	}
	for _, header := range []string{"X-Agent-Signature", "X-Agent-Card-Signature", "MCP-Agent-Signature"} {
		if value := strings.TrimSpace(r.Header.Get(header)); value != "" {
			return shortSecretHash(value)
		}
	}
	return ""
}

func shouldEmitHTTPLog(status int, duration time.Duration) bool {
	if status >= http.StatusInternalServerError {
		return true
	}
	if status >= http.StatusBadRequest && status != http.StatusTooManyRequests {
		return true
	}
	return duration >= 1500*time.Millisecond
}

func (s *Server) shouldSkipDeveloperRequestLog(r *http.Request) bool {
	if r.Method == http.MethodOptions {
		return true
	}
	switch r.URL.Path {
	case "/healthz", "/readyz", "/api/mobile/health",
		"/api/rates", "/rates", "/api/price", "/price", "/api/quote", "/quote", "/api/buy/pairs",
		"/mcp/initialize", "/mcp/tools/list", "/mcp/resources/list", "/mcp/prompts/list",
		"/api/admin/overview":
		return true
	default:
		return false
	}
}

func (s *Server) enqueueAPIRequestLog(in database.APIRequestLogInput) {
	if s == nil || s.db == nil || s.requestLogQueue == nil {
		return
	}
	select {
	case s.requestLogQueue <- in:
	default:
		s.requestLogDrops.Add(1)
	}
}

func (s *Server) runRequestLogWorker() {
	for in := range s.requestLogQueue {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if err := s.db.RecordAPIRequestLog(ctx, in); err != nil {
			slog.Warn("developer_request_log_failed", "error", err)
		}
		cancel()
	}
}

func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.Header.Get("X-Request-Id"))
		if id == "" {
			id = database.NewID()
		}
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDContextKey{}, id)))
	})
}
