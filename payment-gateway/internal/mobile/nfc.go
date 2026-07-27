package mobile

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"payment-gateway/internal/database"
	"payment-gateway/internal/nfc"
)

func (s *Server) handleNFCCard(w http.ResponseWriter, r *http.Request) {
	if !s.nfcReady(w) {
		return
	}
	user, ok := s.nfcUserWithWallet(w, r)
	if !ok {
		return
	}
	network := "BSC"
	bal, err := s.db.GetNFCBalance(r.Context(), *user.WalletAddress, network)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"card": map[string]any{
			"type":           "chainfx_tap_usdt",
			"display_name":   "ChainFX Tap",
			"wallet_address": *user.WalletAddress,
			"network":        network,
			"asset":          "USDT",
			"aid":            nfc.ChainFXAIDHex,
			"hce":            true,
			"scheme":         "chainfx_own_closed_loop",
			"card_network":   "none",
			"fiat_settlement": map[string]any{
				"rail":     "efi_pix",
				"provider": "efi",
				"mode":     "chainfx_terminal_to_chainfx_backend",
			},
			"crypto_debit": map[string]any{
				"asset":  "USDT",
				"source": "nfc_internal_usdt_ledger",
			},
		},
		"balance": nfcBalanceForMobile(bal),
	})
}

func (s *Server) handleNFCProvision(w http.ResponseWriter, r *http.Request) {
	if !s.nfcReady(w) {
		return
	}
	user, ok := s.nfcUserWithWallet(w, r)
	if !ok {
		return
	}
	var req struct {
		DeviceID   string `json:"device_id"`
		Network    string `json:"network"`
		TTLSeconds int    `json:"ttl_seconds"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON payload"})
		return
	}
	req.DeviceID = strings.TrimSpace(req.DeviceID)
	req.Network = strings.ToUpper(strings.TrimSpace(req.Network))
	if req.Network == "" {
		req.Network = "BSC"
	}
	if req.DeviceID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "device_id obrigatorio"})
		return
	}
	if req.Network != "BSC" && req.Network != "POLYGON" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "network deve ser BSC ou POLYGON"})
		return
	}
	ttl := time.Duration(s.cfg.NFCTokenTTLSeconds) * time.Second
	if req.TTLSeconds > 0 && req.TTLSeconds <= 300 {
		ttl = time.Duration(req.TTLSeconds) * time.Second
	}
	now := time.Now().UTC()
	token, claims, err := nfc.IssueToken(s.cfg.NFCTokenSecret, *user.WalletAddress, req.DeviceID, req.Network, ttl, now)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": err.Error()})
		return
	}
	if err := s.db.StoreNFCToken(r.Context(), database.NFCTokenInput{
		TokenID:   claims.TokenID,
		TokenHash: nfc.TokenHash(token),
		Wallet:    claims.Wallet,
		DeviceID:  claims.DeviceID,
		Network:   claims.Network,
		ExpiresAt: time.Unix(claims.ExpiresAtUnix, 0),
	}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":      token,
		"token_id":   claims.TokenID,
		"expires_at": time.Unix(claims.ExpiresAtUnix, 0).UTC(),
		"aid":        nfc.ChainFXAIDHex,
		"network":    claims.Network,
		"apdu": map[string]any{
			"response_template": "70",
			"token_tag":         "DF01",
			"version_tag":       "DF02",
		},
	})
}

func (s *Server) nfcReady(w http.ResponseWriter) bool {
	if s == nil || s.cfg == nil || !s.cfg.NFCEnabled {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "NFC desabilitado"})
		return false
	}
	if strings.TrimSpace(s.cfg.NFCTokenSecret) == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "NFC_TOKEN_SECRET nao configurado"})
		return false
	}
	return true
}

// nfcWalletCacheTTL is how long we cache a user's wallet address in memory.
// Wallet address is immutable once assigned, so 5 minutes is safe.
const nfcWalletCacheTTL = 5 * time.Minute

// nfcUserWithWallet resolves the authenticated user's wallet address.
// Uses a lightweight single-column query and caches the result to avoid
// a full user row fetch on every NFC request.
func (s *Server) nfcUserWithWallet(w http.ResponseWriter, r *http.Request) (*databaseUserView, bool) {
	uid := userIDFromCtx(r)

	// Fast path: wallet address already cached.
	cacheKey := "nfc_wallet:" + uid
	if cached, ok := s.getMobileCache(cacheKey); ok {
		if addr, _ := cached.(string); addr != "" {
			return &databaseUserView{WalletAddress: &addr}, true
		}
		// Cached empty means no wallet yet — skip DB, return conflict.
		writeJSON(w, http.StatusConflict, map[string]any{"error": "wallet do usuario nao registrada"})
		return nil, false
	}

	// Slow path: fetch wallet_address only (not full user row).
	addr, err := mobileDB(s.db).GetUserWalletAddress(r.Context(), uid)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "usuario nao encontrado"})
		return nil, false
	}
	if strings.TrimSpace(addr) == "" {
		// Cache the miss briefly so repeated requests don't hammer DB.
		s.setMobileCache(cacheKey, "", 10*time.Second)
		writeJSON(w, http.StatusConflict, map[string]any{"error": "wallet do usuario nao registrada"})
		return nil, false
	}

	s.setMobileCache(cacheKey, addr, nfcWalletCacheTTL)
	return &databaseUserView{WalletAddress: &addr}, true
}

type databaseUserView struct {
	WalletAddress *string
	FullName      string
}

// nfcWalletInfo is what we store in the server-level cache for NFC user lookups.
// Caching both fields avoids a DB hit on every NFC request for the same user.
type nfcWalletInfo struct {
	addr string
	name string
}


// handleNFCHistory returns the authenticated user's NFC tap history (authorizations)
// ordered newest-first. The user can only see their own wallet's records.
func (s *Server) handleNFCHistory(w http.ResponseWriter, r *http.Request) {
	if !s.nfcReady(w) {
		return
	}
	user, ok := s.nfcUserWithWallet(w, r)
	if !ok {
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	auths, err := s.db.ListNFCAuthorizationsByWallet(r.Context(), *user.WalletAddress, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao carregar historico NFC"})
		return
	}
	if auths == nil {
		auths = []*database.NFCAuthorization{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"authorizations": auths,
		"count":          len(auths),
	})
}

// handleNFCAuthorizationStatus returns the status of a single NFC authorization
// belonging to the authenticated user. Used by the mobile app for adaptive polling
// after a tap while waiting for capture/reverse/expiry.
func (s *Server) handleNFCAuthorizationStatus(w http.ResponseWriter, r *http.Request) {
	if !s.nfcReady(w) {
		return
	}
	user, ok := s.nfcUserWithWallet(w, r)
	if !ok {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "authorization id obrigatorio"})
		return
	}
	auth, err := s.db.GetNFCAuthorization(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao buscar autorizacao NFC"})
		return
	}
	if auth == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "autorizacao nao encontrada"})
		return
	}
	// Security: user may only read their own authorizations.
	if !strings.EqualFold(auth.Wallet, *user.WalletAddress) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "autorizacao nao encontrada"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":              auth.ID,
		"status":          auth.Status,
		"amount_brl":      fmt.Sprintf("%.2f", float64(auth.AmountBRLMinor)/100),
		"fee_brl":         fmt.Sprintf("%.2f", float64(auth.FeeBRLMinor)/100),
		"total_brl":       fmt.Sprintf("%.2f", float64(auth.TotalBRLMinor)/100),
		"merchant_id":     auth.MerchantID,
		"terminal_id":     auth.TerminalID,
		"network":         auth.Network,
		"hold_expires_at": auth.HoldExpiresAt,
		"created_at":      auth.CreatedAt,
		"updated_at":      auth.UpdatedAt,
	})
}

func nfcBalanceForMobile(b *database.NFCBalance) map[string]any {
	return map[string]any{
		"available_usdt": float64(b.AvailableMicro) / 1_000_000,
		"locked_usdt":    float64(b.LockedMicro) / 1_000_000,
		"network":        b.Network,
		"asset":          b.Asset,
	}
}
