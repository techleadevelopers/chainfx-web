package mobile

// assets.go — Phase 5: Multi-Asset endpoints (mobile-only)
//
//	GET  /api/mobile/assets            — list active assets
//	GET  /api/mobile/assets/{symbol}   — single asset config
//	GET  /api/mobile/assets/{symbol}/rate — live price in BRL/USD

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"payment-gateway/internal/models"
)

// handleListAssets — GET /api/mobile/assets
func (s *Server) handleListAssets(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", mobileStaticCacheControl)
	if cached, ok := s.getMobileCache("assets:list"); ok {
		writeJSON(w, http.StatusOK, cached)
		return
	}
	assets, err := mobileDB(s.db).ListAssets(r.Context(), true)
	if err != nil {
		slog.Warn("mobile_assets_fallback", "err", err)
		assets = s.fallbackMobileAssets()
	}
	if len(assets) == 0 {
		assets = s.fallbackMobileAssets()
	}
	assets = s.ensureCoreMobileAssets(assets)

	// Enrich with live price if PriceWorker available
	pw := s.PriceCache()
	type assetWithRate struct {
		Symbol   string  `json:"symbol"`
		Name     string  `json:"name"`
		Network  string  `json:"network"`
		Decimals int     `json:"decimals"`
		MinBRL   float64 `json:"min_amount_brl"`
		MaxBRL   float64 `json:"max_amount_brl"`
		FeeBPS   int     `json:"fee_bps"`
		PriceBRL float64 `json:"price_brl,omitempty"`
		Change24 float64 `json:"change_24h"`
	}
	out := make([]assetWithRate, 0)
	for _, a := range assets {
		row := assetWithRate{
			Symbol:   a.Symbol,
			Name:     a.Name,
			Network:  a.Network,
			Decimals: a.Decimals,
			MinBRL:   a.MinAmount,
			MaxBRL:   a.MaxAmount,
			FeeBPS:   a.FeeBPS,
		}
		row.PriceBRL = mobileAssetPriceBRL(pw, a.Symbol)
		row.Change24 = mobileAssetChange24h(pw, a.Symbol)
		out = append(out, row)
	}
	response := map[string]any{
		"assets":                     out,
		"count":                      len(out),
		"supported_networks":         s.mobileSupportedNetworks(),
		"supported_pairs":            s.mobileLiquiditySupportedPairs(),
		"liquidity_supported_pairs":  s.mobileLiquiditySupportedPairs(),
		"liquidity_supported_tokens": s.mobileLiquiditySupportedTokens(),
	}
	s.setMobileCache("assets:list", response, mobileHotCacheTTL)
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) fallbackMobileAssets() []models.Asset {
	now := time.Now()
	usdtContract := ""
	if s != nil && s.cfg != nil {
		usdtContract = strings.TrimSpace(s.cfg.BscUsdtContract)
	}
	return []models.Asset{
		{Symbol: "USDT", Name: "Tether USD", Network: "BSC", ContractAddress: stringPtrOrNil(usdtContract), Decimals: 18, MinAmount: 10, MaxAmount: 50000, DailyLimit: 50000, MonthlyLimit: 500000, FeeBPS: 60, Active: true, CreatedAt: now},
		{Symbol: "BTC", Name: "Bitcoin", Network: "BITCOIN", Decimals: 8, MinAmount: 10, MaxAmount: 100000, DailyLimit: 100000, MonthlyLimit: 1000000, FeeBPS: 60, Active: true, CreatedAt: now},
		{Symbol: "USDC", Name: "USD Coin", Network: "BASE", Decimals: 6, MinAmount: 10, MaxAmount: 100000, DailyLimit: 100000, MonthlyLimit: 1000000, FeeBPS: 60, Active: true, CreatedAt: now},
		{Symbol: "ETH", Name: "Ethereum", Network: "BSC", Decimals: 18, MinAmount: 10, MaxAmount: 100000, DailyLimit: 100000, MonthlyLimit: 1000000, FeeBPS: 60, Active: true, CreatedAt: now},
		{Symbol: "BNB", Name: "BNB", Network: "BSC", Decimals: 18, MinAmount: 10, MaxAmount: 10000, DailyLimit: 10000, MonthlyLimit: 100000, FeeBPS: 60, Active: true, CreatedAt: now},
		{Symbol: "LINK", Name: "Chainlink", Network: "BSC", Decimals: 18, MinAmount: 10, MaxAmount: 100000, DailyLimit: 100000, MonthlyLimit: 1000000, FeeBPS: 60, Active: true, CreatedAt: now},
		{Symbol: "AVAX", Name: "Avalanche", Network: "BSC", Decimals: 18, MinAmount: 10, MaxAmount: 100000, DailyLimit: 100000, MonthlyLimit: 1000000, FeeBPS: 60, Active: true, CreatedAt: now},
		{Symbol: "SOL", Name: "Solana", Network: "SOLANA", Decimals: 9, MinAmount: 10, MaxAmount: 100000, DailyLimit: 100000, MonthlyLimit: 1000000, FeeBPS: 60, Active: true, CreatedAt: now},
		{Symbol: "APT", Name: "Aptos", Network: "APTOS", Decimals: 8, MinAmount: 10, MaxAmount: 100000, DailyLimit: 100000, MonthlyLimit: 1000000, FeeBPS: 60, Active: true, CreatedAt: now},
	}
}

func (s *Server) ensureCoreMobileAssets(assets []models.Asset) []models.Asset {
	seen := make(map[string]bool, len(assets))
	for i := range assets {
		symbol := strings.ToUpper(strings.TrimSpace(assets[i].Symbol))
		assets[i].Symbol = symbol
		if symbol != "" {
			seen[symbol] = true
		}
	}
	for _, fallback := range s.fallbackMobileAssets() {
		if !seen[fallback.Symbol] {
			assets = append(assets, fallback)
			seen[fallback.Symbol] = true
		}
	}
	return assets
}

func stringPtrOrNil(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

type mobileAssetLookup struct {
	asset    *models.Asset
	fallback bool
}

func (s *Server) mobileAssetBySymbol(ctx context.Context, symbol string) (*models.Asset, bool, error) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if symbol == "" {
		return nil, false, nil
	}
	cacheKey := "assets:model:" + symbol
	if cached, ok := s.getMobileCache(cacheKey); ok {
		if lookup, ok := cached.(mobileAssetLookup); ok {
			return lookup.asset, lookup.fallback, nil
		}
	}
	asset, err := mobileDB(s.db).GetAsset(ctx, symbol)
	if err == nil && asset != nil {
		s.setMobileCache(cacheKey, mobileAssetLookup{asset: asset}, mobileHotCacheTTL)
		return asset, false, nil
	}
	for _, fallback := range s.fallbackMobileAssets() {
		if fallback.Symbol == symbol {
			asset := fallback
			s.setMobileCache(cacheKey, mobileAssetLookup{asset: &asset, fallback: true}, mobileHotCacheTTL)
			return &asset, true, nil
		}
	}
	return asset, false, err
}

// handleGetAsset — GET /api/mobile/assets/{symbol}
func (s *Server) handleGetAsset(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", mobileStaticCacheControl)
	symbol := strings.ToUpper(r.PathValue("symbol"))
	cacheKey := "assets:get:" + symbol
	if cached, ok := s.getMobileCache(cacheKey); ok {
		writeJSON(w, http.StatusOK, cached)
		return
	}
	asset, fallback, err := s.mobileAssetBySymbol(r.Context(), symbol)
	if err != nil && asset == nil {
		slog.Error("erro interno", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro interno"})
		return
	}
	if asset == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "ativo não encontrado"})
		return
	}
	response := map[string]any{"asset": asset, "fallback": fallback}
	s.setMobileCache(cacheKey, response, mobileCatalogCacheTTL)
	writeJSON(w, http.StatusOK, response)
}

// handleGetAssetRate — GET /api/mobile/assets/{symbol}/rate
// Returns live BRL/USD price for the requested asset.
func (s *Server) handleGetAssetRate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", mobileRateCacheControl)
	symbol := strings.ToUpper(r.PathValue("symbol"))
	cacheKey := "assets:rate:" + symbol
	if cached, ok := s.getMobileCache(cacheKey); ok {
		writeJSON(w, http.StatusOK, cached)
		return
	}

	asset, _, err := s.mobileAssetBySymbol(r.Context(), symbol)
	if err != nil && asset == nil {
		slog.Error("erro interno", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro interno"})
		return
	}
	if asset == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "ativo não encontrado"})
		return
	}

	pw := s.PriceCache()
	priceBRL := mobileAssetPriceBRL(pw, symbol)
	priceUSD := mobileAssetPriceUSD(pw, symbol)

	response := map[string]any{
		"symbol":     symbol,
		"price_brl":  priceBRL,
		"price_usd":  priceUSD,
		"change_24h": mobileAssetChange24h(pw, symbol),
		"fee_bps":    asset.FeeBPS,
		"min_amount": asset.MinAmount,
		"max_amount": asset.MaxAmount,
		"updated_at": time.Now().Unix(),
	}
	s.setMobileCache(cacheKey, response, mobileHotCacheTTL)
	writeJSON(w, http.StatusOK, response)
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// assetPriceInBRL returns the BRL price for a given asset symbol using the
// PriceWorker's cached prices.
func assetPriceInBRL(pw interface{ GetPrice(string) float64 }, symbol string) float64 {
	if pw == nil {
		return 0
	}
	switch strings.ToUpper(symbol) {
	case "USDT", "USDC", "BUSD":
		return pw.GetPrice("BRL") // USDT≈1 USD
	case "BTCB", "BTC":
		btcUSD := pw.GetPrice("BTCUSDT_SOURCE")
		usdtBRL := pw.GetPrice("BRL")
		if btcUSD > 0 && usdtBRL > 0 {
			return btcUSD * usdtBRL
		}
	case "ETH":
		ethUSD := pw.GetPrice("ETHUSDT_SOURCE")
		usdtBRL := pw.GetPrice("BRL")
		if ethUSD > 0 && usdtBRL > 0 {
			return ethUSD * usdtBRL
		}
	case "LINK":
		linkUSD := pw.GetPrice("LINKUSDT_SOURCE")
		usdtBRL := pw.GetPrice("BRL")
		if linkUSD > 0 && usdtBRL > 0 {
			return linkUSD * usdtBRL
		}
	case "AVAX":
		avaxUSD := pw.GetPrice("AVAXUSDT_SOURCE")
		usdtBRL := pw.GetPrice("BRL")
		if avaxUSD > 0 && usdtBRL > 0 {
			return avaxUSD * usdtBRL
		}
	case "BNB":
		bnbUSD := pw.GetPrice("BNBUSDT_SOURCE")
		usdtBRL := pw.GetPrice("BRL")
		if bnbUSD > 0 && usdtBRL > 0 {
			return bnbUSD * usdtBRL
		}
	case "SOL":
		solUSD := pw.GetPrice("SOLUSDT_SOURCE")
		usdtBRL := pw.GetPrice("BRL")
		if solUSD > 0 && usdtBRL > 0 {
			return solUSD * usdtBRL
		}
	case "APT":
		aptUSD := pw.GetPrice("APTUSDT_SOURCE")
		usdtBRL := pw.GetPrice("BRL")
		if aptUSD > 0 && usdtBRL > 0 {
			return aptUSD * usdtBRL
		}
	case "EURC":
		usdtEUR := pw.GetPrice("USDTEUR")
		usdtBRL := pw.GetPrice("BRL")
		if usdtEUR > 0 && usdtBRL > 0 {
			return (1 / usdtEUR) * usdtBRL
		}
	}
	return 0
}

func assetPriceInUSD(pw interface{ GetPrice(string) float64 }, symbol string) float64 {
	if pw == nil {
		return 0
	}
	switch strings.ToUpper(symbol) {
	case "USDT", "USDC", "BUSD":
		return 1
	case "BTCB", "BTC":
		return pw.GetPrice("BTCUSDT_SOURCE")
	case "ETH":
		return pw.GetPrice("ETHUSDT_SOURCE")
	case "LINK":
		return pw.GetPrice("LINKUSDT_SOURCE")
	case "AVAX":
		return pw.GetPrice("AVAXUSDT_SOURCE")
	case "BNB":
		return pw.GetPrice("BNBUSDT_SOURCE")
	case "SOL":
		return pw.GetPrice("SOLUSDT_SOURCE")
	case "APT":
		return pw.GetPrice("APTUSDT_SOURCE")
	case "EURC":
		if usdtEUR := pw.GetPrice("USDTEUR"); usdtEUR > 0 {
			return 1 / usdtEUR
		}
	}
	return 0
}

func mobileAssetPriceBRL(pw interface{ GetPrice(string) float64 }, symbol string) float64 {
	price := assetPriceInBRL(pw, symbol)
	if price > 0 {
		return price
	}
	switch strings.ToUpper(symbol) {
	case "USDT", "USDC", "BUSD":
		if pw != nil {
			if brl := pw.GetPrice("BRL"); brl > 0 {
				return brl
			}
		}
		return 1
	}
	return 0
}

func mobileAssetPriceUSD(pw interface{ GetPrice(string) float64 }, symbol string) float64 {
	price := assetPriceInUSD(pw, symbol)
	if price > 0 {
		return price
	}
	switch strings.ToUpper(symbol) {
	case "USDT", "USDC", "BUSD":
		return 1
	}
	return 0
}
func mobileAssetChange24h(pw interface{ GetPrice(string) float64 }, symbol string) float64 {
	if pw == nil {
		return 0
	}
	switch strings.ToUpper(symbol) {
	case "USDT", "USDC", "BUSD":
		return pw.GetPrice("USDT_CHANGE24H")
	case "BTCB", "BTC":
		return pw.GetPrice("BTC_CHANGE24H")
	case "ETH":
		return pw.GetPrice("ETH_CHANGE24H")
	case "BNB":
		return pw.GetPrice("BNB_CHANGE24H")
	case "LINK":
		return pw.GetPrice("LINK_CHANGE24H")
	case "AVAX":
		return pw.GetPrice("AVAX_CHANGE24H")
	case "SOL":
		return pw.GetPrice("SOL_CHANGE24H")
	case "APT":
		return pw.GetPrice("APT_CHANGE24H")
	}
	return 0
}
