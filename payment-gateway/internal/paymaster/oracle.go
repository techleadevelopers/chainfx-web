// Package paymaster implements the Gas Station (Paymaster) engine.
// oracle.go — BNB/USD and POL/USD price feed with 60-second cache and
// last-known-good fallback. Uses Binance public REST API; no API key required.
package paymaster

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	binanceTickerURL   = "https://api.binance.com/api/v3/ticker/price?symbol=%s"
	coingeckoSimpleURL = "https://api.coingecko.com/api/v3/simple/price?ids=%s&vs_currencies=usd"
	oracleCacheTTL     = 60 * time.Second
	oracleFetchTimeout = 8 * time.Second
	oracleOutlierPct   = 0.15
)

// PriceOracle fetches native-token/USD prices with an in-memory cache and
// graceful last-known-good fallback.
type PriceOracle struct {
	mu         sync.RWMutex
	prices     map[string]float64 // symbol → USD price
	fetchedAt  map[string]time.Time
	httpClient *http.Client
}

// NewPriceOracle creates a ready-to-use oracle.
func NewPriceOracle() *PriceOracle {
	return &PriceOracle{
		prices:     make(map[string]float64),
		fetchedAt:  make(map[string]time.Time),
		httpClient: &http.Client{Timeout: oracleFetchTimeout},
	}
}

// BNBPrice returns the current BNB/USD price, using the cache when fresh.
func (o *PriceOracle) BNBPrice(ctx context.Context) (float64, error) {
	return o.price(ctx, "BNBUSDT")
}

// POLPrice returns the current POL/USD price (Polygon native token).
func (o *PriceOracle) POLPrice(ctx context.Context) (float64, error) {
	return o.price(ctx, "POLUSDT")
}

func (o *PriceOracle) price(ctx context.Context, symbol string) (float64, error) {
	// Fast path — serve from cache if still fresh.
	o.mu.RLock()
	p, ok := o.prices[symbol]
	fetchedAt := o.fetchedAt[symbol]
	o.mu.RUnlock()

	if ok && time.Since(fetchedAt) < oracleCacheTTL {
		return p, nil
	}

	// Slow path — fetch from Binance.
	fetched, source, err := o.fetchPrice(ctx, symbol, p, ok && time.Since(fetchedAt) < oracleCacheTTL)
	if err != nil {
		if ok {
			// Fallback to last-known-good value.
			slog.Warn("oracle: all sources failed, using last known price",
				"symbol", symbol,
				"price", p,
				"error", err,
			)
			return p, nil
		}
		return 0, fmt.Errorf("oracle: no price available for %s: %w", symbol, err)
	}

	o.mu.Lock()
	o.prices[symbol] = fetched
	o.fetchedAt[symbol] = time.Now()
	o.mu.Unlock()

	slog.Debug("oracle: price updated", "symbol", symbol, "price", fetched, "source", source)
	return fetched, nil
}

type binanceTicker struct {
	Symbol string `json:"symbol"`
	Price  string `json:"price"`
}

func (o *PriceOracle) fetchFromBinance(ctx context.Context, symbol string) (float64, error) {
	url := fmt.Sprintf(binanceTickerURL, symbol)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "ChainFX-Oracle/1.0")

	resp, err := o.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("binance returned status %d", resp.StatusCode)
	}

	var ticker binanceTicker
	if err := json.NewDecoder(resp.Body).Decode(&ticker); err != nil {
		return 0, fmt.Errorf("decode ticker: %w", err)
	}

	price, err := strconv.ParseFloat(ticker.Price, 64)
	if err != nil {
		return 0, fmt.Errorf("parse price %q: %w", ticker.Price, err)
	}
	if price <= 0 {
		return 0, fmt.Errorf("invalid price %f for %s", price, symbol)
	}
	return price, nil
}

func (o *PriceOracle) fetchPrice(ctx context.Context, symbol string, last float64, hasRecentLast bool) (float64, string, error) {
	type source struct {
		name string
		fn   func(context.Context, string) (float64, error)
	}
	var errs []string
	for _, src := range []source{
		{name: "binance", fn: o.fetchFromBinance},
		{name: "coingecko", fn: o.fetchFromCoinGecko},
	} {
		price, err := src.fn(ctx, symbol)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", src.name, err))
			continue
		}
		if hasRecentLast && isOracleOutlier(last, price) {
			errs = append(errs, fmt.Sprintf("%s: outlier %.8f vs %.8f", src.name, price, last))
			continue
		}
		return price, src.name, nil
	}
	return 0, "", fmt.Errorf("%s", strings.Join(errs, "; "))
}

func (o *PriceOracle) fetchFromCoinGecko(ctx context.Context, symbol string) (float64, error) {
	id, err := coinGeckoID(symbol)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf(coingeckoSimpleURL, id), nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "ChainFX-Oracle/1.0")

	resp, err := o.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("coingecko returned status %d", resp.StatusCode)
	}

	var out map[string]map[string]float64
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, fmt.Errorf("decode coingecko: %w", err)
	}
	price := out[id]["usd"]
	if price <= 0 {
		return 0, fmt.Errorf("invalid coingecko price for %s", symbol)
	}
	return price, nil
}

func coinGeckoID(symbol string) (string, error) {
	switch symbol {
	case "BNBUSDT":
		return "binancecoin", nil
	case "POLUSDT":
		return "polygon-ecosystem-token", nil
	default:
		return "", fmt.Errorf("unsupported oracle symbol %s", symbol)
	}
}

func isOracleOutlier(last, next float64) bool {
	if last <= 0 || next <= 0 {
		return false
	}
	diff := (next - last) / last
	if diff < 0 {
		diff = -diff
	}
	return diff > oracleOutlierPct
}
