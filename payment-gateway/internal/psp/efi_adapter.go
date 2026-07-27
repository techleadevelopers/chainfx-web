// Package psp — efi_adapter.go
// EfiAdapter wraps Efí Bank PIX behind the Provider interface.
// Production hardening applied:
//   - OAuth token is cached and reused until 5 min before expiry (avoids a
//     fresh round-trip on every webhook / charge call).
//   - Amount parsing uses strconv.ParseFloat (not fmt.Sscanf).
//   - ParseWebhookAll processes every pix[] entry independently.
//   - getToken uses sync.Mutex — safe under concurrent webhook bursts.
package psp

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// EfiAdapter implements Provider for Efí Bank PIX.
type EfiAdapter struct {
	clientID     string
	clientSecret string
	pixKey       string
	baseURL      string
	httpClient   *http.Client

	// Token cache — avoids a round-trip to /oauth/token on every call.
	tokenMu      sync.Mutex
	cachedToken  string
	tokenExpires time.Time // zero = not set
}

// NewEfiAdapter creates an EfiAdapter.
// tlsCfg may be nil for development (no mutual TLS).
func NewEfiAdapter(clientID, clientSecret, pixKey, baseURL string, tlsCfg *tls.Config) *EfiAdapter {
	transport := &http.Transport{TLSClientConfig: tlsCfg}
	return &EfiAdapter{
		clientID:     clientID,
		clientSecret: clientSecret,
		pixKey:       pixKey,
		baseURL:      baseURL,
		httpClient:   &http.Client{Transport: transport, Timeout: 15 * time.Second},
	}
}

func (e *EfiAdapter) Name() string { return "efi" }

// CreateCharge creates an immediate PIX charge via Efí Bank /v2/cob endpoint.
func (e *EfiAdapter) CreateCharge(ctx context.Context, charge PixCharge) (*PixChargeResult, error) {
	token, err := e.getToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("efi: auth: %w", err)
	}

	expiry := charge.ExpirySec
	if expiry <= 0 {
		expiry = 3600
	}

	// Efí requires CPF/CNPJ only when present; omit empty fields.
	devedor := map[string]any{"nome": charge.PayerName}
	if charge.PayerCPF != "" {
		devedor["cpf"] = charge.PayerCPF
	}

	payload := map[string]any{
		"calendario":         map[string]any{"expiracao": expiry},
		"devedor":            devedor,
		"valor":              map[string]any{"original": fmt.Sprintf("%.2f", charge.AmountBRL)},
		"chave":              e.pixKey,
		"solicitacaoPagador": charge.Description,
		"infoAdicionais": []map[string]any{
			{"nome": "ID", "valor": charge.ExternalID},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/v2/cob", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("efi: CreateCharge request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		var errBody map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		return nil, fmt.Errorf("efi: CreateCharge HTTP %d: %v", resp.StatusCode, errBody)
	}

	var result struct {
		TxID          string `json:"txid"`
		PixCopiaECola string `json:"pixCopiaECola"`
		Calendario    struct {
			Criacao   string `json:"criacao"`
			Expiracao int    `json:"expiracao"`
		} `json:"calendario"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("efi: decode CreateCharge response: %w", err)
	}

	expiresAt := time.Now().Add(time.Duration(result.Calendario.Expiracao) * time.Second)
	return &PixChargeResult{
		Provider:     e.Name(),
		TXID:         result.TxID,
		PixCopyPaste: result.PixCopiaECola,
		AmountBRL:    charge.AmountBRL,
		ExpiresAt:    expiresAt,
	}, nil
}

// ParseWebhook parses an Efí Bank webhook and returns the first pix entry.
// For full multi-entry support use ParseWebhookAll.
func (e *EfiAdapter) ParseWebhook(ctx context.Context, body []byte, _ string) (*PixWebhookPayload, error) {
	all, err := e.ParseWebhookAll(ctx, body, "")
	if err != nil {
		return nil, err
	}
	return &all[0], nil
}

// ParseWebhookAll parses an Efí Bank webhook and returns one PixWebhookPayload
// per pix[] entry. Efí sends batches; each entry may belong to a different order.
//
// Amount format: Efí sends "valor" as a decimal string "50.00" — parsed with
// strconv.ParseFloat (never fmt.Sscanf) to guarantee no silent zero on parse error.
func (e *EfiAdapter) ParseWebhookAll(_ context.Context, body []byte, _ string) ([]PixWebhookPayload, error) {
	var raw struct {
		Pix []struct {
			EndToEndID        string `json:"endToEndId"`
			TXID              string `json:"txid"`
			Valor             string `json:"valor"`
			HorarioLiquidacao string `json:"horarioLiquidacao"`
			Pagador           struct {
				Nome  string `json:"nome"`
				Chave string `json:"chave"`
			} `json:"pagador"`
		} `json:"pix"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("efi: ParseWebhookAll: unmarshal: %w", err)
	}
	if len(raw.Pix) == 0 {
		return nil, fmt.Errorf("efi: ParseWebhookAll: no pix entries in payload")
	}

	out := make([]PixWebhookPayload, 0, len(raw.Pix))
	for i, p := range raw.Pix {
		// strconv.ParseFloat — never fmt.Sscanf (silent zero on error).
		amount, err := strconv.ParseFloat(p.Valor, 64)
		if err != nil || amount <= 0 {
			return nil, fmt.Errorf("efi: ParseWebhookAll: entry %d has invalid valor %q: %w", i, p.Valor, err)
		}

		paidAt, _ := time.Parse(time.RFC3339, p.HorarioLiquidacao)
		if paidAt.IsZero() {
			paidAt = time.Now().UTC()
		}

		out = append(out, PixWebhookPayload{
			Provider:   e.Name(),
			TXID:       p.TXID,
			EndToEndID: p.EndToEndID,
			AmountBRL:  amount,
			PaidAt:     paidAt,
			PayerName:  p.Pagador.Nome,
			PayerKey:   p.Pagador.Chave,
		})
	}
	return out, nil
}

// HealthCheck validates the Efí OAuth + mTLS path only.
//
// Do not probe /v2/cob/{txid} with a synthetic TXID: Efí may return 400 for
// provider-side business validation even when auth, mTLS and API reachability
// are healthy, which creates noisy false alarms and can trigger failover.
func (e *EfiAdapter) HealthCheck(ctx context.Context) error {
	if _, err := e.getToken(ctx); err != nil {
		return fmt.Errorf("efi health: auth failed: %w", err)
	}
	return nil
}

// ── OAuth token cache ──────────────────────────────────────────────────────────

// getToken returns a valid bearer token, reusing the cached one when possible.
// Thread-safe: uses a Mutex so concurrent webhook bursts don't each fetch a
// fresh token simultaneously.
func (e *EfiAdapter) getToken(ctx context.Context) (string, error) {
	e.tokenMu.Lock()
	defer e.tokenMu.Unlock()

	// Reuse cached token if it's still valid (with 5 min safety margin).
	if e.cachedToken != "" && time.Now().Before(e.tokenExpires.Add(-5*time.Minute)) {
		return e.cachedToken, nil
	}

	// Fetch a fresh token from Efí OAuth endpoint.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		e.baseURL+"/oauth/token",
		bytes.NewBufferString("grant_type=client_credentials"),
	)
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(e.clientID, e.clientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("efi: getToken request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("efi: getToken returned HTTP %d", resp.StatusCode)
	}

	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"` // seconds; Efí typically 3600
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return "", fmt.Errorf("efi: getToken decode: %w", err)
	}
	if tok.AccessToken == "" {
		return "", fmt.Errorf("efi: getToken: empty access_token in response")
	}

	expiry := tok.ExpiresIn
	if expiry <= 0 {
		expiry = 3600 // default 1 h if Efí omits the field
	}
	e.cachedToken = tok.AccessToken
	e.tokenExpires = time.Now().Add(time.Duration(expiry) * time.Second)

	return e.cachedToken, nil
}
