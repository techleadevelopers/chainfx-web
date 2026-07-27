package liquidity

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBingXBaseURL = "https://open-api.bingx.com"
	bingXProviderName   = "bingx"
)

type BingXProvider struct {
	BaseURL             string
	APIKey              string
	APISecret           string
	RecvWindowMS        int
	AllowedAssets       string
	AllowedNetworks     string
	TakerFeeBps         int
	WithdrawFeeUSDT     float64
	BTCWithdrawFeeBTC   float64
	BTCWithdrawMinBTC   float64
	BTCMinBuyBRL        float64
	MarketBuyMode       string
	TradeEnabled        bool
	WithdrawEnabled     bool
	Client              *http.Client
	Now                 func() time.Time
	QuoteTTL            time.Duration
	DeliverySLASeconds  int
	ReliabilityBps      int
	MinProviderCostUSDT float64
}

func (p *BingXProvider) Name() string {
	return bingXProviderName
}

func (p *BingXProvider) Quote(ctx context.Context, req Request) (Quote, error) {
	req = normalizeRequest(req)
	if err := p.validateRequest(req); err != nil {
		return Quote{}, err
	}
	symbol := bingXSpotSymbol(req.Asset)
	askUSDT := 1.0
	if req.Asset != "USDT" {
		var err error
		askUSDT, err = p.depthAskUSDT(ctx, symbol)
		if err != nil {
			return Quote{}, err
		}
	}
	cryptoAmount := req.CryptoAmount
	if cryptoAmount <= 0 && req.AmountBRL > 0 && req.QuoteLockedRate > 0 {
		cryptoAmount = req.AmountBRL / req.QuoteLockedRate
	}
	if cryptoAmount <= 0 {
		return Quote{}, fmt.Errorf("bingx: crypto amount ausente")
	}
	withdrawFeeNative, withdrawMinNative := p.withdrawFeeNative(req.Asset, req.Network)
	if p.btcMinBuyBRL(req.Asset, req.Network) > 0 && req.AmountBRL < p.btcMinBuyBRL(req.Asset, req.Network) {
		return Quote{}, fmt.Errorf("bingx: compra BTC abaixo do minimo BRL")
	}
	if withdrawFeeNative > 0 && cryptoAmount < withdrawMinNative {
		return Quote{}, fmt.Errorf("bingx: valor BTC abaixo do minimo de saque")
	}
	withdrawAmount := cryptoAmount
	if withdrawFeeNative > 0 {
		withdrawAmount = cryptoAmount + withdrawFeeNative
	}
	providerCostUSDT := askUSDT * withdrawAmount
	if p.MinProviderCostUSDT > 0 && providerCostUSDT < p.MinProviderCostUSDT {
		return Quote{}, fmt.Errorf("bingx: provider cost below minimum")
	}
	usdtBRL := 0.0
	if providerCostUSDT > 0 && req.AmountBRL > 0 {
		usdtBRL = req.AmountBRL / providerCostUSDT
	}
	providerFeeBRL := req.AmountBRL * math.Max(0, float64(p.TakerFeeBps)) / 10000
	networkFeeBRL := p.WithdrawFeeUSDT * usdtBRL
	if withdrawFeeNative > 0 {
		networkFeeBRL = withdrawFeeNative * askUSDT * usdtBRL
	}
	totalCost := req.AmountBRL + providerFeeBRL + networkFeeBRL
	now := p.now()
	ttl := p.QuoteTTL
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	sla := p.DeliverySLASeconds
	if sla <= 0 {
		sla = 900
	}
	reliability := p.ReliabilityBps
	if reliability <= 0 {
		reliability = 9200
	}
	return Quote{
		Provider:           p.Name(),
		ProviderType:       "exchange",
		ExternalQuoteID:    "bingx_" + safeQuoteIDPart(req.OrderID) + "_" + symbol + "_" + strconv.FormatInt(now.UnixMilli(), 10),
		Asset:              req.Asset,
		Network:            req.Network,
		TokenContract:      req.TokenContract,
		TokenDecimals:      req.TokenDecimals,
		DestAddress:        req.DestAddress,
		FiatCostBRL:        req.AmountBRL,
		ProviderFeeBRL:     providerFeeBRL,
		NetworkFeeBRL:      networkFeeBRL,
		TotalCostBRL:       totalCost,
		CryptoAmount:       withdrawAmount,
		DeliverySLASeconds: sla,
		ReliabilityBps:     reliability,
		DirectDelivery:     true,
		ExpiresAt:          now.Add(ttl),
		Metadata: map[string]any{
			"symbol":              symbol,
			"askUSDT":             askUSDT,
			"providerCostUSDT":    providerCostUSDT,
			"withdrawFeeNative":   withdrawFeeNative,
			"withdrawMinNative":   withdrawMinNative,
			"withdrawGrossAmount": withdrawAmount,
			"receiveAmount":       cryptoAmount,
			"tradeEnabled":        p.TradeEnabled,
			"withdrawEnabled":     p.WithdrawEnabled,
			"withdrawNetwork":     bingXWithdrawNetwork(req.Network),
		},
	}, nil
}

func (p *BingXProvider) Execute(ctx context.Context, req Request, quote Quote) (Execution, error) {
	req = normalizeRequest(req)
	if err := p.validateRequest(req); err != nil {
		return Execution{}, err
	}
	if !p.TradeEnabled {
		return Execution{}, fmt.Errorf("bingx: trade disabled")
	}
	if strings.TrimSpace(p.APIKey) == "" || strings.TrimSpace(p.APISecret) == "" {
		return Execution{}, fmt.Errorf("bingx: api key/secret ausentes")
	}
	symbol := firstString(quote.Metadata, "symbol")
	if symbol == "" {
		symbol = bingXSpotSymbol(req.Asset)
	}
	amount := quote.CryptoAmount
	if amount <= 0 {
		amount = req.CryptoAmount
	}
	if amount <= 0 {
		return Execution{}, fmt.Errorf("bingx: quantidade invalida")
	}
	withdrawFeeNative, withdrawMinNative := p.withdrawFeeNative(req.Asset, req.Network)
	if withdrawFeeNative > 0 && req.CryptoAmount > 0 && req.CryptoAmount < withdrawMinNative {
		return Execution{}, fmt.Errorf("bingx: valor BTC abaixo do minimo de saque")
	}
	deliveredAmount := amount
	if withdrawFeeNative > 0 {
		deliveredAmount = math.Max(0, amount-withdrawFeeNative)
	}
	orderID := ""
	orderPayload := map[string]any{}
	if req.Asset != "USDT" {
		var err error
		orderID, orderPayload, err = p.placeMarketBuy(ctx, symbol, amount, parseFloatAny(quote.Metadata["providerCostUSDT"]))
		if err != nil {
			return Execution{}, err
		}
	}
	exec := Execution{
		Provider:        p.Name(),
		ExternalOrderID: orderID,
		Status:          "pending_withdrawal",
		Asset:           req.Asset,
		Network:         req.Network,
		TokenContract:   req.TokenContract,
		DestAddress:     req.DestAddress,
		DeliveredAmount: deliveredAmount,
		Metadata: map[string]any{
			"symbol":            symbol,
			"order":             orderPayload,
			"withdrawEnabled":   p.WithdrawEnabled,
			"grossAmount":       amount,
			"withdrawFeeNative": withdrawFeeNative,
			"withdrawMinNative": withdrawMinNative,
		},
	}
	if !p.WithdrawEnabled {
		return exec, nil
	}
	withdrawID, txHash, withdrawPayload, err := p.withdraw(ctx, req.Asset, req.Network, req.DestAddress, amount)
	if err != nil {
		exec.Status = "trade_filled_withdraw_failed"
		exec.Metadata["withdrawError"] = err.Error()
		return exec, err
	}
	exec.ExternalOrderID = firstNonEmpty(orderID, withdrawID)
	exec.TxHash = txHash
	exec.Status = "submitted"
	if txHash != "" {
		exec.Status = "sent"
	}
	exec.Metadata["withdrawID"] = withdrawID
	exec.Metadata["withdraw"] = withdrawPayload
	return exec, nil
}

func (p *BingXProvider) withdrawFeeNative(asset, network string) (float64, float64) {
	if strings.EqualFold(strings.TrimSpace(asset), "BTC") && normalizeNetwork(network) == "BITCOIN" {
		fee := p.BTCWithdrawFeeBTC
		if fee <= 0 {
			fee = 0.00005
		}
		minAmount := p.BTCWithdrawMinBTC
		if minAmount <= 0 {
			minAmount = 0.000056
		}
		return fee, minAmount
	}
	return 0, 0
}

func (p *BingXProvider) btcMinBuyBRL(asset, network string) float64 {
	if strings.EqualFold(strings.TrimSpace(asset), "BTC") && normalizeNetwork(network) == "BITCOIN" {
		if p.BTCMinBuyBRL > 0 {
			return p.BTCMinBuyBRL
		}
		return 150
	}
	return 0
}

func (p *BingXProvider) validateRequest(req Request) error {
	if req.Asset == "" || req.Network == "" {
		return fmt.Errorf("bingx: asset/network ausentes")
	}
	if !csvAllows(p.AllowedAssets, req.Asset) {
		return fmt.Errorf("bingx: asset nao permitido")
	}
	if !csvAllows(p.AllowedNetworks, req.Network) {
		return fmt.Errorf("bingx: network nao permitida")
	}
	if req.DestAddress == "" {
		return fmt.Errorf("bingx: destino ausente")
	}
	return nil
}

func (p *BingXProvider) depthAskUSDT(ctx context.Context, symbol string) (float64, error) {
	path := "/openApi/spot/v1/market/depth"
	raw, err := p.publicGET(ctx, path, map[string]string{"symbol": symbol, "limit": "5"})
	if err == nil {
		if ask, parseErr := parseBingXAsk(raw); parseErr == nil && ask > 0 {
			return ask, nil
		}
	}
	raw, err = p.publicGET(ctx, "/openApi/spot/v1/ticker/price", map[string]string{"symbol": symbol})
	if err != nil {
		return 0, err
	}
	return parseBingXPrice(raw)
}

func (p *BingXProvider) placeMarketBuy(ctx context.Context, symbol string, amount, providerCostUSDT float64) (string, map[string]any, error) {
	params := map[string]string{
		"symbol": symbol,
		"side":   "BUY",
		"type":   "MARKET",
	}
	if strings.EqualFold(strings.TrimSpace(p.MarketBuyMode), "quote_order_qty") {
		if providerCostUSDT <= 0 {
			return "", nil, fmt.Errorf("bingx: providerCostUSDT ausente para quote_order_qty")
		}
		params["quoteOrderQty"] = formatBingXAmount(providerCostUSDT)
	} else {
		params["quantity"] = formatBingXAmount(amount)
	}
	raw, err := p.signedRequest(ctx, http.MethodPost, "/openApi/spot/v1/trade/order", params)
	if err != nil {
		return "", nil, err
	}
	payload, err := parseBingXEnvelope(raw)
	if err != nil {
		return "", nil, err
	}
	orderID := findString(payload, "orderId", "orderID", "id", "clientOrderID", "clientOrderId")
	if orderID == "" {
		orderID = findString(nestedMap(payload, "data"), "orderId", "orderID", "id", "clientOrderID", "clientOrderId")
	}
	return orderID, payload, nil
}

func (p *BingXProvider) withdraw(ctx context.Context, asset, network, address string, amount float64) (string, string, map[string]any, error) {
	params := map[string]string{
		"coin":    strings.ToUpper(strings.TrimSpace(asset)),
		"network": bingXWithdrawNetwork(network),
		"address": strings.TrimSpace(address),
		"amount":  formatBingXAmount(amount),
	}
	raw, err := p.signedRequest(ctx, http.MethodPost, "/openApi/wallets/v1/capital/withdraw/apply", params)
	if err != nil {
		return "", "", nil, err
	}
	payload, err := parseBingXEnvelope(raw)
	if err != nil {
		return "", "", nil, err
	}
	data := nestedMap(payload, "data")
	withdrawID := firstNonEmpty(findString(payload, "withdrawId", "withdrawID", "id"), findString(data, "withdrawId", "withdrawID", "id"))
	txHash := firstNonEmpty(findString(payload, "txId", "txHash", "transactionId"), findString(data, "txId", "txHash", "transactionId"))
	return withdrawID, txHash, payload, nil
}

func (p *BingXProvider) publicGET(ctx context.Context, path string, params map[string]string) ([]byte, error) {
	endpoint, err := p.url(path, params)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	return p.do(req)
}

func (p *BingXProvider) PublicGETRaw(ctx context.Context, path string, params map[string]string) ([]byte, error) {
	return p.publicGET(ctx, path, params)
}

func (p *BingXProvider) SignedGETRaw(ctx context.Context, path string, params map[string]string) ([]byte, error) {
	return p.signedRequest(ctx, http.MethodGet, path, params)
}

func (p *BingXProvider) signedRequest(ctx context.Context, method, path string, params map[string]string) ([]byte, error) {
	if params == nil {
		params = map[string]string{}
	}
	params["timestamp"] = strconv.FormatInt(p.now().UnixMilli(), 10)
	if p.RecvWindowMS > 0 {
		params["recvWindow"] = strconv.Itoa(p.RecvWindowMS)
	}
	query := encodeSorted(params)
	params["signature"] = bingXSign(query, p.APISecret)
	endpoint, err := p.url(path, params)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-BX-APIKEY", strings.TrimSpace(p.APIKey))
	return p.do(req)
}

func (p *BingXProvider) do(req *http.Request) ([]byte, error) {
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		return nil, readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("bingx: status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return raw, nil
}

func (p *BingXProvider) url(path string, params map[string]string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(firstNonEmpty(p.BaseURL, defaultBingXBaseURL)), "/")
	if base == "" {
		return "", fmt.Errorf("bingx: base url ausente")
	}
	u, err := url.Parse(base + path)
	if err != nil {
		return "", err
	}
	if len(params) > 0 {
		u.RawQuery = encodeSorted(params)
	}
	return u.String(), nil
}

func (p *BingXProvider) now() time.Time {
	if p.Now != nil {
		return p.Now().UTC()
	}
	return time.Now().UTC()
}

func parseBingXAsk(raw []byte) (float64, error) {
	payload, err := parseBingXEnvelope(raw)
	if err != nil {
		return 0, err
	}
	data := nestedMap(payload, "data")
	if ask := firstOrderbookPrice(data["asks"]); ask > 0 {
		return ask, nil
	}
	if ask := firstOrderbookPrice(payload["asks"]); ask > 0 {
		return ask, nil
	}
	return 0, fmt.Errorf("bingx: ask ausente")
}

func parseBingXPrice(raw []byte) (float64, error) {
	payload, err := parseBingXEnvelope(raw)
	if err != nil {
		return 0, err
	}
	data := nestedMap(payload, "data")
	for _, key := range []string{"price", "lastPrice", "close", "askPrice"} {
		if price := parseFloatAny(data[key]); price > 0 {
			return price, nil
		}
		if price := parseFloatAny(payload[key]); price > 0 {
			return price, nil
		}
	}
	return 0, fmt.Errorf("bingx: price ausente")
}

func parseBingXEnvelope(raw []byte) (map[string]any, error) {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	code := parseFloatAny(payload["code"])
	if code != 0 {
		success, hasSuccess := payload["success"].(bool)
		if !hasSuccess || !success {
			return nil, fmt.Errorf("bingx: code %v msg %s", payload["code"], findString(payload, "msg", "message"))
		}
	}
	return payload, nil
}

func firstOrderbookPrice(value any) float64 {
	items, ok := value.([]any)
	if !ok || len(items) == 0 {
		return 0
	}
	first := items[0]
	switch row := first.(type) {
	case []any:
		if len(row) == 0 {
			return 0
		}
		return parseFloatAny(row[0])
	case map[string]any:
		for _, key := range []string{"price", "p"} {
			if price := parseFloatAny(row[key]); price > 0 {
				return price
			}
		}
	}
	return 0
}

func parseFloatAny(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case string:
		out, _ := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return out
	case json.Number:
		out, _ := v.Float64()
		return out
	default:
		return 0
	}
}

func encodeSorted(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	values := url.Values{}
	for _, key := range keys {
		values.Set(key, params[key])
	}
	return values.Encode()
}

func bingXSign(payload, secret string) string {
	mac := hmac.New(sha256.New, []byte(strings.TrimSpace(secret)))
	_, _ = mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

func bingXSpotSymbol(asset string) string {
	asset = strings.ToUpper(strings.TrimSpace(asset))
	if asset == "USDT" {
		return "USDT"
	}
	return asset + "-USDT"
}

func bingXWithdrawNetwork(network string) string {
	switch normalizeNetwork(network) {
	case "BSC":
		return "BEP20"
	case "POLYGON":
		return "MATIC"
	case "ETHEREUM":
		return "ERC20"
	case "SOLANA":
		return "SOL"
	case "BITCOIN":
		return "BTC"
	case "ARBITRUM":
		return "ARBITRUM"
	case "BASE":
		return "BASE"
	default:
		return normalizeNetwork(network)
	}
}

func csvAllows(raw, value string) bool {
	items := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
	if len(items) == 0 {
		return true
	}
	value = strings.ToUpper(strings.TrimSpace(value))
	for _, item := range items {
		if strings.ToUpper(strings.TrimSpace(item)) == value {
			return true
		}
	}
	return false
}

func formatBingXAmount(amount float64) string {
	return strings.TrimRight(strings.TrimRight(strconv.FormatFloat(amount, 'f', 10, 64), "0"), ".")
}

func findString(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := payload[key]; ok {
			switch v := value.(type) {
			case string:
				if strings.TrimSpace(v) != "" {
					return strings.TrimSpace(v)
				}
			case float64:
				if v > 0 {
					return strconv.FormatInt(int64(v), 10)
				}
			}
		}
	}
	return ""
}

func firstString(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	value, _ := payload[key].(string)
	return strings.TrimSpace(value)
}

func nestedMap(payload map[string]any, key string) map[string]any {
	if payload == nil {
		return nil
	}
	if nested, ok := payload[key].(map[string]any); ok {
		return nested
	}
	return nil
}

func safeQuoteIDPart(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "order"
	}
	replacer := strings.NewReplacer(" ", "_", "/", "_", "\\", "_", ":", "_")
	return replacer.Replace(value)
}
