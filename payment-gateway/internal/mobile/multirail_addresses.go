package mobile

import (
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"math/big"
	"net/http"
	"strings"

	"golang.org/x/crypto/sha3"
)

func (s *Server) handleSolanaAddress(w http.ResponseWriter, r *http.Request) {
	svc := s.solanaSvcOrErr(w)
	if svc == nil {
		return
	}
	addr, err := svc.GetOrCreateAddress(r.Context(), userIDFromCtx(r))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"code": "SOL_ADDRESS_ERROR", "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"address":        addr.Address,
		"wallet_address": addr.Address,
		"network":        "SOLANA",
		"custody":        "server_derived",
		"created_at":     addr.CreatedAt,
	})
}

func (s *Server) handleAptosAddress(w http.ResponseWriter, r *http.Request) {
	s.handleDerivedRailAddress(w, r, "APTOS", "aptos_wallet_addresses", deriveAptosAddress)
}

func (s *Server) handleDerivedRailAddress(w http.ResponseWriter, r *http.Request, network, table string, derive func([]byte, string) string) {
	if !s.mobileNetworkEnabled(network) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"code": "RAIL_DISABLED", "message": network + " nao esta habilitada"})
		return
	}
	uid := userIDFromCtx(r)
	secret := s.railAddressDerivationSecret()
	if len(secret) < 32 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"code": "RAIL_SIGNER_NOT_CONFIGURED", "message": network + " requer SIGNER_HMAC_SECRET ou LGPD_SECRET com pelo menos 32 bytes"})
		return
	}
	address, err := s.getOrCreateDerivedRailAddress(r.Context(), uid, network, table, derive(secret, uid))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"code": "RAIL_ADDRESS_ERROR", "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"address":        address,
		"wallet_address": address,
		"network":        network,
		"custody":        "server_derived",
	})
}

func (s *Server) railAddressDerivationSecret() []byte {
	if s == nil || s.cfg == nil {
		return nil
	}
	if secret := strings.TrimSpace(s.cfg.SignerHmacSecret); secret != "" {
		return []byte(secret)
	}
	return []byte(strings.TrimSpace(s.cfg.LGPDSecret))
}

func (s *Server) getOrCreateDerivedRailAddress(ctx context.Context, userID, network, table, address string) (string, error) {
	var existing string
	err := s.db.SQL.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT address
		FROM %s
		WHERE user_id=$1 AND status='active'
		ORDER BY created_at DESC
		LIMIT 1`, table), userID).Scan(&existing)
	if err == nil && strings.TrimSpace(existing) != "" {
		return strings.TrimSpace(existing), nil
	}
	if err != nil && err != sql.ErrNoRows {
		return "", err
	}
	_, err = s.db.SQL.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (user_id, network, address, derivation_key_id, status)
		VALUES ($1, $2, $3, $4, 'active')
		ON CONFLICT (network, address) DO NOTHING`, table),
		userID, network, address, railDerivationKeyID(s.railAddressDerivationSecret()))
	if err != nil {
		return "", err
	}
	return address, nil
}

func deriveSolanaAddress(secret []byte, userID string) string {
	pub := derivedEd25519PublicKey(secret, "chainfx-solana-wallet-v1:"+strings.TrimSpace(userID))
	return base58EncodeMobile(pub)
}

func deriveAptosAddress(secret []byte, userID string) string {
	pub := derivedEd25519PublicKey(secret, "chainfx-aptos-wallet-v1:"+strings.TrimSpace(userID))
	payload := append(append([]byte{}, pub...), 0x00)
	sum := sha3.Sum256(payload)
	return "0x" + hex.EncodeToString(sum[:])
}

func derivedEd25519PublicKey(secret []byte, label string) ed25519.PublicKey {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(label))
	seed := mac.Sum(nil)
	priv := ed25519.NewKeyFromSeed(seed[:32])
	return priv.Public().(ed25519.PublicKey)
}

func railDerivationKeyID(secret []byte) string {
	sum := sha256.Sum256(secret)
	return "hmac-sha256:" + hex.EncodeToString(sum[:8])
}

func base58EncodeMobile(data []byte) string {
	const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	x := new(big.Int).SetBytes(data)
	base := big.NewInt(58)
	zero := big.NewInt(0)
	mod := new(big.Int)
	var out []byte
	for x.Cmp(zero) > 0 {
		x.DivMod(x, base, mod)
		out = append(out, alphabet[mod.Int64()])
	}
	for _, b := range data {
		if b != 0 {
			break
		}
		out = append(out, alphabet[0])
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}
