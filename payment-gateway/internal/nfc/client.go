package nfc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type TerminalClient struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
}

type AuthorizationRequest struct {
	Token          string `json:"token"`
	AmountBRL      string `json:"amount_brl"`
	Currency       string `json:"currency"`
	MerchantID     string `json:"merchant_id"`
	TerminalID     string `json:"terminal_id"`
	ExternalRef    string `json:"external_ref,omitempty"`
	IdempotencyKey string `json:"idempotency_key"`
}

type AuthorizationResponse struct {
	AuthorizationID string `json:"authorization_id"`
	TokenID         string `json:"token_id"`
	WalletAddress   string `json:"wallet_address"`
	Network         string `json:"network"`
	MerchantID      string `json:"merchant_id"`
	TerminalID      string `json:"terminal_id"`
	ExternalRef     string `json:"external_ref,omitempty"`
	AmountBRL       string `json:"amount_brl"`
	RequiredUSDT    string `json:"required_usdt"`
	USDTRate        string `json:"usdt_rate"`
	Status          string `json:"status"`
	ResponseCode    string `json:"response_code"`
	Reason          string `json:"reason,omitempty"`
	Idempotent      bool   `json:"idempotent,omitempty"`
}

func (c TerminalClient) Authorize(ctx context.Context, req AuthorizationRequest) (*AuthorizationResponse, error) {
	if strings.TrimSpace(c.BaseURL) == "" {
		return nil, fmt.Errorf("nfc terminal: base URL is required")
	}
	if strings.TrimSpace(c.APIKey) == "" {
		return nil, fmt.Errorf("nfc terminal: API key is required")
	}
	if strings.TrimSpace(req.Currency) == "" {
		req.Currency = "BRL"
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 1500 * time.Millisecond}
	}
	url := strings.TrimRight(c.BaseURL, "/") + "/api/nfc/authorize"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.APIKey))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Idempotency-Key", strings.TrimSpace(req.IdempotencyKey))

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var out AuthorizationResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &out, fmt.Errorf("nfc terminal: authorization HTTP %d response_code=%s status=%s", resp.StatusCode, out.ResponseCode, out.Status)
	}
	return &out, nil
}
