package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"payment-gateway/internal/liquidity"
)

type bingXProbeRequest struct {
	Asset        string  `json:"asset"`
	Network      string  `json:"network"`
	CryptoAmount float64 `json:"cryptoAmount"`
	TimeoutMS    int     `json:"timeoutMs"`
}

func (s *Server) handleAdminBingXProbe(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authorizeAdmin(w, r); !ok {
		return
	}
	var req bingXProbeRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	asset := strings.ToUpper(strings.TrimSpace(req.Asset))
	if asset == "" {
		asset = "SOL"
	}
	network := liquidity.NormalizeNetwork(req.Network)
	if network == "" {
		network = "SOLANA"
	}
	cryptoAmount := req.CryptoAmount
	if cryptoAmount <= 0 {
		cryptoAmount = 0.01
	}
	timeout := time.Duration(req.TimeoutMS) * time.Millisecond
	if timeout <= 0 || timeout > 15*time.Second {
		timeout = 8 * time.Second
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	provider := &liquidity.BingXProvider{
		BaseURL:            s.cfg.BingXAPIBaseURL,
		APIKey:             s.cfg.BingXAPIKey,
		APISecret:          s.cfg.BingXAPISecret,
		RecvWindowMS:       s.cfg.BingXRecvWindowMs,
		AllowedAssets:      s.cfg.BingXAllowedAssets,
		AllowedNetworks:    s.cfg.BingXAllowedNetworks,
		TakerFeeBps:        s.cfg.BingXTakerFeeBps,
		WithdrawFeeUSDT:    s.cfg.BingXWithdrawFeeUSDT,
		BTCWithdrawFeeBTC:  s.cfg.BingXBTCWithdrawFeeBTC,
		BTCWithdrawMinBTC:  s.cfg.BingXBTCWithdrawMinBTC,
		BTCMinBuyBRL:       s.cfg.BuyBTCBitcoinMinBrl,
		MarketBuyMode:      s.cfg.BingXMarketBuyMode,
		TradeEnabled:       false,
		WithdrawEnabled:    false,
		DeliverySLASeconds: 900,
		ReliabilityBps:     9200,
	}
	out := map[string]any{
		"ok": false,
		"config": map[string]any{
			"enabled":           s.cfg.BingXEnabled,
			"apiBaseURL":        firstNonEmpty(s.cfg.BingXAPIBaseURL, "https://open-api.bingx.com"),
			"apiKeyConfigured":  strings.TrimSpace(s.cfg.BingXAPIKey) != "",
			"apiSecretSet":      strings.TrimSpace(s.cfg.BingXAPISecret) != "",
			"tradeEnabled":      s.cfg.BingXTradeEnabled,
			"withdrawEnabled":   s.cfg.BingXWithdrawEnabled,
			"marketBuyMode":     s.cfg.BingXMarketBuyMode,
			"allowedAssets":     s.cfg.BingXAllowedAssets,
			"allowedNetworks":   s.cfg.BingXAllowedNetworks,
			"probeTradeBlocked": true,
		},
		"request": map[string]any{
			"asset":        asset,
			"network":      network,
			"cryptoAmount": cryptoAmount,
		},
	}

	quote, quoteErr := provider.Quote(ctx, liquidity.Request{
		OrderID:         "admin-bingx-probe",
		Asset:           asset,
		Network:         network,
		CryptoAmount:    cryptoAmount,
		AmountBRL:       100,
		QuoteLockedRate: 100,
		DestAddress:     "probe-only-not-used",
		CreatedAt:       time.Now().UTC(),
	})
	if quoteErr != nil {
		out["publicMarket"] = map[string]any{"ok": false, "error": quoteErr.Error()}
	} else {
		out["publicMarket"] = map[string]any{
			"ok":               true,
			"provider":         quote.Provider,
			"symbol":           quote.Metadata["symbol"],
			"askUSDT":          quote.Metadata["askUSDT"],
			"providerCostUSDT": quote.Metadata["providerCostUSDT"],
			"expiresAt":        quote.ExpiresAt,
		}
	}

	private := probeBingXPrivate(ctx, provider)
	out["privateAuth"] = private
	out["ok"] = quoteErr == nil && boolFromMap(private, "ok")
	status := http.StatusOK
	if out["ok"] != true {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, out)
}

func probeBingXPrivate(ctx context.Context, provider *liquidity.BingXProvider) map[string]any {
	if provider == nil || strings.TrimSpace(provider.APIKey) == "" || strings.TrimSpace(provider.APISecret) == "" {
		return map[string]any{"ok": false, "error": "BINGX_API_KEY/BINGX_API_SECRET ausentes"}
	}
	candidates := []string{
		"/openApi/spot/v1/account/balance",
		"/openApi/spot/v1/account",
		"/openApi/account/v1/allAccountBalance",
	}
	attempts := make([]map[string]any, 0, len(candidates))
	for _, path := range candidates {
		start := time.Now()
		raw, err := provider.SignedGETRaw(ctx, path, nil)
		latency := time.Since(start).Milliseconds()
		attempt := map[string]any{"path": path, "latencyMs": latency}
		if err != nil {
			attempt["ok"] = false
			attempt["error"] = err.Error()
			attempts = append(attempts, attempt)
			continue
		}
		code, msg := bingXEnvelopeCodeMessage(raw)
		attempt["ok"] = code == 0
		attempt["code"] = code
		if msg != "" {
			attempt["message"] = msg
		}
		attempts = append(attempts, attempt)
		if code == 0 {
			return map[string]any{"ok": true, "acceptedPath": path, "attempts": attempts}
		}
	}
	return map[string]any{"ok": false, "attempts": attempts}
}

func bingXEnvelopeCodeMessage(raw []byte) (float64, string) {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return -1, err.Error()
	}
	code := 0.0
	switch v := payload["code"].(type) {
	case float64:
		code = v
	case string:
		if strings.TrimSpace(v) != "" && strings.TrimSpace(v) != "0" {
			code = -1
		}
	}
	msg, _ := payload["msg"].(string)
	if msg == "" {
		msg, _ = payload["message"].(string)
	}
	return code, msg
}

func boolFromMap(payload map[string]any, key string) bool {
	value, _ := payload[key].(bool)
	return value
}
