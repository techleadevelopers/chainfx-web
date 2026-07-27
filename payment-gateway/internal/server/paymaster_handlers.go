package server

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"payment-gateway/internal/paymaster"

	"github.com/ethereum/go-ethereum/common"
)

// ── GET /v1/gas/status ─────────────────────────────────────────────────────────

func (s *Server) handleGasStatus(w http.ResponseWriter, r *http.Request) {
	if s.paymaster == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"enabled": false,
			"reason":  "gas station not initialised",
		})
		return
	}
	writeJSON(w, s.paymaster.HTTPStatus(), s.paymaster.StatusJSON(r.Context()))
}

// ── POST /v1/gas/quote ─────────────────────────────────────────────────────────

func (s *Server) handleGasQuote(w http.ResponseWriter, r *http.Request) {
	if !s.gasStationReady(w) {
		return
	}

	var body struct {
		UserAddress string `json:"user_address"`
		TxTo        string `json:"tx_to"`
		TxData      string `json:"tx_data"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON"})
		return
	}
	if body.UserAddress == "" || body.TxTo == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "user_address and tx_to are required"})
		return
	}

	quote, err := s.paymaster.Quote(r.Context(), body.UserAddress, body.TxTo, nil)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, quote)
}

// ── POST /v1/gas/relay ─────────────────────────────────────────────────────────
// Rate-limited by tier:
//   no key      → 401
//   sk_test_*   → 10 req/min
//   sk_live_*   → 60 req/min

func (s *Server) handleGasRelay(w http.ResponseWriter, r *http.Request) {
	if !s.gasStationReady(w) {
		return
	}

	apiKey := chainFXAPIKeyFromHeader(r)
	if apiKey == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"error": "Authorization header with Bearer sk_test_* or sk_live_* required",
		})
		return
	}
	auth, ok := s.authorizeChainFX(w, r)
	if !ok {
		return
	}
	keyMode := "live"
	if auth.Sandbox {
		keyMode = "test"
	}

	// Tier-specific rate limit.
	var limit int
	switch keyMode {
	case "test":
		limit = 10
	case "live":
		limit = 60
	default:
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unrecognised API key prefix"})
		return
	}

	keyHash := auth.APIKeyLogHash
	if keyHash == "" {
		keyHash = shortSecretHash(apiKey)
	}
	rlKey := "paymaster:relay:" + keyHash + ":" + keyMode
	if allowed, _, _ := s.limiter.AllowN(rlKey, limit); !allowed {
		w.Header().Set("X-RateLimit-Limit", itoa(limit))
		w.Header().Set("X-RateLimit-Window", "60")
		w.Header().Set("Retry-After", "60")
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error": "rate limit exceeded",
			"code":  "RATE_LIMITED",
		})
		return
	}

	var req paymaster.RelayRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON"})
		return
	}
	if err := validateRelayRequest(&req); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": err.Error(), "code": "VALIDATION_ERROR"})
		return
	}

	resp, err := s.paymaster.SubmitRelay(r.Context(), &req)
	if err != nil {
		if err == paymaster.ErrDuplicateSig {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": "relay already submitted with this signature",
				"code":  "DUPLICATE_SIG",
			})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusAccepted, resp)
}

// ── GET /v1/gas/relay/{id} ─────────────────────────────────────────────────────

func (s *Server) handleGasRelayGet(w http.ResponseWriter, r *http.Request) {
	if !s.gasStationReady(w) {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "id is required"})
		return
	}
	relay, err := s.paymaster.GetRelay(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, relay)
}

// ── GET /api/admin/gas-station ─────────────────────────────────────────────────

func (s *Server) handleAdminGasStation(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authorizeAdmin(w, r); !ok {
		return
	}
	if s.paymaster == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	stats, err := s.paymaster.Stats(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": s.paymaster.StatusJSON(r.Context()),
		"stats":  stats,
	})
}

// ── GET /api/admin/sweeper ─────────────────────────────────────────────────────

func (s *Server) handleAdminSweeper(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authorizeAdmin(w, r); !ok {
		return
	}
	runs, err := s.db.ListAutoSweeperRuns(r.Context(), 50)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	stats, err := s.db.AutoSweeperStats(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"runs":  runs,
		"stats": stats,
	})
}

// ── helpers ────────────────────────────────────────────────────────────────────

func (s *Server) gasStationReady(w http.ResponseWriter) bool {
	if s.paymaster == nil || !s.paymaster.IsEnabled() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": "gas station is not enabled",
			"code":  "GAS_STATION_DISABLED",
			"hint":  "Set GAS_STATION_ENABLED=true and restart the server",
		})
		return false
	}
	return true
}

func validateRelayRequest(req *paymaster.RelayRequest) error {
	req.UserAddress = strings.TrimSpace(req.UserAddress)
	req.TxTo = strings.TrimSpace(req.TxTo)
	req.TxData = strings.TrimSpace(req.TxData)
	req.SigR = strings.TrimSpace(req.SigR)
	req.SigS = strings.TrimSpace(req.SigS)
	req.SigV = strings.TrimSpace(req.SigV)
	req.Amount = strings.TrimSpace(req.Amount)
	req.TokenAddr = strings.TrimSpace(req.TokenAddr)
	req.Network = strings.ToUpper(strings.TrimSpace(req.Network))

	if req.UserAddress == "" {
		return errField("user_address")
	}
	if !common.IsHexAddress(req.UserAddress) {
		return fmt.Errorf("invalid field: user_address must be an EVM address")
	}
	if req.TxTo == "" {
		return errField("tx_to")
	}
	if !common.IsHexAddress(req.TxTo) {
		return fmt.Errorf("invalid field: tx_to must be an EVM address")
	}
	if req.TokenAddr == "" {
		return errField("token_addr")
	}
	if !common.IsHexAddress(req.TokenAddr) {
		return fmt.Errorf("invalid field: token_addr must be an EVM address")
	}
	if req.SigR == "" || req.SigS == "" || req.SigV == "" {
		return errField("sig_r / sig_s / sig_v")
	}
	if !isFixedHexBytes(req.SigR, 32) || !isFixedHexBytes(req.SigS, 32) {
		return fmt.Errorf("invalid signature: sig_r and sig_s must be 32-byte hex values")
	}
	if !isAcceptedSignatureV(req.SigV) {
		return fmt.Errorf("invalid signature: sig_v must be 0, 1, 27, or 28")
	}
	if req.TxData != "" && !isHexPayload(req.TxData) {
		return fmt.Errorf("invalid field: tx_data must be hex")
	}
	if req.Amount == "" {
		return errField("amount")
	}
	if req.Network == "" {
		req.Network = "BSC"
	}
	if req.Network != "BSC" && req.Network != "POLYGON" {
		return fmt.Errorf("invalid field: network must be BSC or POLYGON")
	}
	return nil
}

func isFixedHexBytes(value string, wantBytes int) bool {
	raw := strings.TrimPrefix(strings.TrimPrefix(value, "0x"), "0X")
	if len(raw) != wantBytes*2 {
		return false
	}
	_, err := hex.DecodeString(raw)
	return err == nil
}

func isHexPayload(value string) bool {
	raw := strings.TrimPrefix(strings.TrimPrefix(value, "0x"), "0X")
	if raw == "" || len(raw)%2 != 0 {
		return false
	}
	_, err := hex.DecodeString(raw)
	return err == nil
}

func isAcceptedSignatureV(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "0", "1", "27", "28", "0x0", "0x1", "0x1b", "0x1c":
		return true
	default:
		return false
	}
}

func errField(name string) error {
	return &fieldError{name: name}
}

type fieldError struct{ name string }

func (e *fieldError) Error() string {
	return "missing required field: " + e.name
}

func itoa(i int) string {
	return strconv.Itoa(i)
}
