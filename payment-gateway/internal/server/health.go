package server

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"payment-gateway/internal/database"
)

func (s *Server) handleWebAvailability(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"surface":   "web",
		"path":      r.URL.Path,
		"ready":     "/readyz",
		"health":    "/healthz",
		"dashboard": "/developers/dashboard",
	})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if status, payload, ok := s.cachedReady(time.Now(), time.Second); ok {
		writeJSON(w, status, payload)
		return
	}
	status, payload := s.computeReady(r)
	if status == http.StatusServiceUnavailable {
		if staleStatus, stalePayload, ok := s.cachedReady(time.Now(), 10*time.Second); ok && staleStatus == http.StatusOK {
			stalePayload["stale"] = true
			writeJSON(w, staleStatus, stalePayload)
			return
		}
	}
	s.storeReady(time.Now(), status, payload)
	writeJSON(w, status, payload)
}

func (s *Server) computeReady(r *http.Request) (int, map[string]any) {
	ctx, cancel := context.WithTimeout(r.Context(), 300*time.Millisecond)
	defer cancel()
	if s == nil || s.db == nil {
		return http.StatusServiceUnavailable, map[string]any{"ok": false, "db": false, "error": "database not configured"}
	}
	if err := s.db.Ping(ctx); err != nil {
		return http.StatusServiceUnavailable, map[string]any{"ok": false, "db": false, "error": err.Error()}
	}
	certOK, certErr := s.efiCertificateReady()
	gaps := s.operationalGapsWithCertificate(certOK)
	status := http.StatusOK
	if s.cfg.IsProduction() && (len(gaps) > 0 || !certOK) {
		status = http.StatusServiceUnavailable
	}
	return status, map[string]any{
		"ok":              len(gaps) == 0 && certOK,
		"db":              true,
		"network":         s.deliveryNetwork(),
		"bsc":             s.cfg.BscRpcUrls != "" && s.cfg.BscUsdtContract != "",
		"pix":             s.efiPixConfigured() && certOK && defaultString(s.cfg.PixWebhookSecret, s.cfg.WebhookSecret) != "",
		"efi_card":        s.efiChargesConfigured() && certOK,
		"efi_certificate": certOK,
		"efi_cert_source": s.efiCertificateSource(),
		"efi_cert_path":   s.cfg.EfiCertificatePath,
		"efi_cert_error":  certErr,
		"signer":          s.cfg.SignerUrl != "" && s.cfg.SignerHmacSecret != "",
		"webhooks":        s.webhooks.Status(),
		"mode":            s.cfg.Environment,
		"warnings":        gaps,
	}
}

func (s *Server) cachedReady(now time.Time, maxAge time.Duration) (int, map[string]any, bool) {
	if s == nil {
		return 0, nil, false
	}
	s.readyMu.Lock()
	defer s.readyMu.Unlock()
	if s.readyPayload == nil || s.readyStatus == 0 || now.Sub(s.readyChecked) > maxAge {
		return 0, nil, false
	}
	return s.readyStatus, cloneMap(s.readyPayload), true
}

func (s *Server) storeReady(now time.Time, status int, payload map[string]any) {
	if s == nil {
		return
	}
	s.readyMu.Lock()
	s.readyChecked = now
	s.readyStatus = status
	s.readyPayload = cloneMap(payload)
	s.readyMu.Unlock()
}

func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func (s *Server) operationalGaps() []string {
	certOK, _ := s.efiCertificateReady()
	return s.operationalGapsWithCertificate(certOK)
}

func (s *Server) operationalGapsWithCertificate(certOK bool) []string {
	checks := map[string]bool{
		"pix_provider":    s.efiPixConfigured(),
		"efi_certificate": certOK,
		"pix_webhook":     defaultString(s.cfg.PixWebhookSecret, s.cfg.WebhookSecret) != "",
		"efi_card":        s.efiChargesConfigured(),
		"signer":          s.cfg.SignerUrl != "" && s.cfg.SignerHmacSecret != "",
		"signer_private":  !strings.Contains(strings.ToLower(strings.TrimSpace(s.cfg.SignerUrl)), "up.railway.app"),
		"lgpd_secret":     s.cfg.LGPDSecret != "",
		"no_simulations":  !s.cfg.AllowSimulations,
		"sweep_not_stub":  !s.cfg.EnableSweepStub,
		"treasury_hot":    s.cfg.TreasuryHot != "",
	}
	if strings.EqualFold(s.cfg.SignerNetwork, "bsc") || strings.EqualFold(s.cfg.SignerNetwork, "evm") {
		checks["signer_bsc"] = true
		checks["bsc_contract"] = s.cfg.BscUsdtContract != ""
		checks["bsc_rpc_urls"] = s.cfg.BscRpcUrls != ""
	}
	var gaps []string
	for name, ok := range checks {
		if !ok {
			gaps = append(gaps, name)
		}
	}
	return gaps
}

type chainFXAuth struct {
	Valid         bool
	Sandbox       bool
	Mode          string
	ProjectID     string
	APIKeyID      string
	APIKeyLogHash string
	Scopes        []string
}

func (s *Server) authorizeAdmin(w http.ResponseWriter, r *http.Request) (*database.AdminUser, chainFXAuth, bool) {
	if !s.authorizeAdminConsoleKey(w, r) {
		return nil, chainFXAuth{}, false
	}
	token := chainFXAPIKey(r)
	user, err := s.db.ValidateAdminSession(r.Context(), token)
	if err != nil {
		writeError(w, err)
		return nil, chainFXAuth{}, false
	}
	if user != nil {
		return user, chainFXAuth{Valid: true, Mode: "admin"}, true
	}
	auth := s.chainFXAuthContext(r)
	if auth.Valid {
		if auth.Sandbox && s.cfg.IsProduction() {
			writeJSON(w, http.StatusForbidden, map[string]any{
				"error": "sandbox API keys cannot create live orders",
				"hint":  "use an admin account or a live server API key",
			})
			return nil, chainFXAuth{}, false
		}
		return &database.AdminUser{Email: "api-key", Role: auth.Mode}, auth, true
	}
	writeJSON(w, http.StatusUnauthorized, map[string]any{
		"error": "admin login required",
		"hint":  "POST /api/admin/login with email and password, then send Authorization: Bearer <token>",
	})
	return nil, chainFXAuth{}, false
}

func (s *Server) authorizeAdminConsoleKey(w http.ResponseWriter, r *http.Request) bool {
	expected := ""
	if s != nil && s.cfg != nil {
		expected = strings.TrimSpace(s.cfg.AdminConsoleKey)
	}
	if expected == "" {
		return true
	}
	got := strings.TrimSpace(r.Header.Get("X-Admin-Console-Key"))
	if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(expected)) != 1 {
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"error": "palavra-chave administrativa invalida",
			"hint":  "send X-Admin-Console-Key with the console keyword configured in ADMIN_CONSOLE_KEY",
		})
		return false
	}
	return true
}

func (s *Server) authorizeChainFX(w http.ResponseWriter, r *http.Request) (chainFXAuth, bool) {
	auth := s.chainFXAuthContext(r)
	if auth.Valid {
		if auth.Sandbox && s.cfg.IsProduction() {
			writeJSON(w, http.StatusForbidden, map[string]any{
				"error": "sandbox API keys cannot create live orders",
				"hint":  "use https://sandbox-api.chainfx.com for sk_test_xxx keys",
			})
			return chainFXAuth{}, false
		}
		return auth, true
	}
	if !s.cfg.ChainFXRequireAPIKey && !s.cfg.IsProduction() {
		return chainFXAuth{Valid: true, Sandbox: true, Mode: "development"}, true
	}
	if chainFXAPIKey(r) == "" && isAnonymousMCPAIToolCall(r) {
		return chainFXAuth{Valid: true, Sandbox: true, Mode: "anonymous-mcp-ai"}, true
	}
	writeJSON(w, http.StatusUnauthorized, map[string]any{
		"error": "API key required",
		"hint":  "send Authorization: Bearer sk_test_xxx or sk_live_xxx",
	})
	return chainFXAuth{}, false
}

func isAnonymousMCPAIToolCall(r *http.Request) bool {
	if r == nil || r.Method != http.MethodPost || r.URL.Path != "/mcp/tools/call" || r.Body == nil {
		return false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		r.Body = io.NopCloser(bytes.NewReader(body))
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	var req struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return false
	}
	switch req.Name {
	case "market_analysis", "trade_recommendation", "price_prediction", "detect_anomalies", "summarize_transactions":
		return true
	default:
		return false
	}
}
