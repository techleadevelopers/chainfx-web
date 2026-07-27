package workers

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

	"payment-gateway/internal/httpclient"
)

type PriceWorker struct {
	bus       *EventBus
	client    *http.Client
	mu        sync.RWMutex
	prices    map[string]float64
	updatedAt time.Time
	source    string
}

type PriceSnapshot struct {
	Currency  string
	Price     float64
	UpdatedAt time.Time
	Source    string
}

type CoinGeckoResponse struct {
	Tether struct {
		Brl          float64 `json:"brl"`
		Usd          float64 `json:"usd"`
		Eur          float64 `json:"eur"`
		Brl24hChange float64 `json:"brl_24h_change"`
	} `json:"tether"`
	Bitcoin struct {
		Usd          float64 `json:"usd"`
		Brl24hChange float64 `json:"brl_24h_change"`
	} `json:"bitcoin"`
	Ethereum struct {
		Usd          float64 `json:"usd"`
		Brl24hChange float64 `json:"brl_24h_change"`
	} `json:"ethereum"`
	Binancecoin struct {
		Usd          float64 `json:"usd"`
		Brl24hChange float64 `json:"brl_24h_change"`
	} `json:"binancecoin"`
	Chainlink struct {
		Usd          float64 `json:"usd"`
		Brl24hChange float64 `json:"brl_24h_change"`
	} `json:"chainlink"`
	Avalanche struct {
		Usd          float64 `json:"usd"`
		Brl24hChange float64 `json:"brl_24h_change"`
	} `json:"avalanche-2"`
	Solana struct {
		Usd          float64 `json:"usd"`
		Brl24hChange float64 `json:"brl_24h_change"`
	} `json:"solana"`
	Aptos struct {
		Usd          float64 `json:"usd"`
		Brl24hChange float64 `json:"brl_24h_change"`
	} `json:"aptos"`
}

type BinanceTickerResponse struct {
	Symbol string `json:"symbol"`
	Price  string `json:"price"`
}

type Binance24hTickerResponse struct {
	Symbol             string `json:"symbol"`
	LastPrice          string `json:"lastPrice"`
	PriceChangePercent string `json:"priceChangePercent"`
}

func NewPriceWorker(bus *EventBus) *PriceWorker {
	return &PriceWorker{
		bus:    bus,
		client: httpclient.Default(),
		prices: make(map[string]float64),
	}
}

func (pw *PriceWorker) GetCurrentPrice() float64 {
	return pw.GetPrice("BRL")
}

func (pw *PriceWorker) GetPrice(currency string) float64 {
	snapshot := pw.GetSnapshot(currency)
	return snapshot.Price
}

func (pw *PriceWorker) GetSnapshot(currency string) PriceSnapshot {
	pw.mu.RLock()
	defer pw.mu.RUnlock()
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if currency == "" {
		currency = "BRL"
	}
	return PriceSnapshot{
		Currency:  currency,
		Price:     pw.prices[currency],
		UpdatedAt: pw.updatedAt,
		Source:    pw.source,
	}
}

func (pw *PriceWorker) Start(ctx context.Context) {
	slog.Info("PriceWorker inicializado")
	pw.fetchPrice()

	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("PriceWorker encerrado")
			return
		case <-ticker.C:
			pw.fetchPrice()
		}
	}
}

func (pw *PriceWorker) fetchPrice() {
	start := time.Now()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	prices, source, err := pw.fetchCoinGeckoPrices(ctx)
	cancel()
	if err != nil {
		slog.Warn("CoinGecko indisponivel, tentando fallback Binance", "error", err)
		ctx, cancel = context.WithTimeout(context.Background(), 8*time.Second)
		prices, source, err = pw.fetchBinancePrices(ctx)
		cancel()
	}
	if err != nil {
		slog.Error("Erro ao atualizar cotacoes", "error", err)
		return
	}

	pw.mu.Lock()
	// Só publica evento se houve mudança significativa (evita spam)
	changed := false
	for key, value := range prices {
		isChange24h := strings.HasSuffix(key, "_CHANGE24H")
		if value > 0 || isChange24h {
			if old, exists := pw.prices[key]; !exists || abs(value-old) > 0.0001 {
				pw.prices[key] = value
				changed = true
			}
		}
	}
	pw.updatedAt = time.Now().UTC()
	pw.source = source

	if changed {
		payload := make(map[string]any, len(pw.prices))
		for key, value := range pw.prices {
			payload[key] = value
		}
		pw.mu.Unlock()

		slog.Info("Cotacoes atualizadas", "source", source, "duration_ms", time.Since(start).Milliseconds())
		pw.bus.Publish(Event{Type: "price.updated", Payload: payload})
	} else {
		pw.mu.Unlock()
		slog.Debug("Cotacoes sem alteracoes significativas", "source", source)
	}
}

func (pw *PriceWorker) fetchCoinGeckoPrices(ctx context.Context) (map[string]float64, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.coingecko.com/api/v3/simple/price?ids=tether,bitcoin,ethereum,binancecoin,chainlink,avalanche-2,solana,aptos&vs_currencies=brl,usd,eur&include_24hr_change=true", nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "PaymentGateway/1.0") // Bom pra evitar bloqueios

	resp, err := pw.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("coingecko status %d", resp.StatusCode)
	}

	var data CoinGeckoResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, "", err
	}

	prices := map[string]float64{
		"BRL":             data.Tether.Brl,
		"USD":             data.Tether.Usd,
		"EUR":             data.Tether.Eur,
		"BTCUSDT":         data.Bitcoin.Usd / data.Tether.Usd,
		"ETHUSDT":         data.Ethereum.Usd / data.Tether.Usd,
		"BNBUSDT":         data.Binancecoin.Usd / data.Tether.Usd,
		"EURUSD":          data.Tether.Usd / data.Tether.Eur,
		"USDTBRL":         data.Tether.Brl,
		"USDTUSD":         data.Tether.Usd,
		"USDTEUR":         data.Tether.Eur,
		"BTCUSDT_SOURCE":  data.Bitcoin.Usd,
		"ETHUSDT_SOURCE":  data.Ethereum.Usd,
		"BNBUSDT_SOURCE":  data.Binancecoin.Usd,
		"LINKUSDT_SOURCE": data.Chainlink.Usd,
		"AVAXUSDT_SOURCE": data.Avalanche.Usd,
		"SOLUSDT_SOURCE":  data.Solana.Usd,
		"APTUSDT_SOURCE":  data.Aptos.Usd,
		"USDT_CHANGE24H":  data.Tether.Brl24hChange,
		"BTC_CHANGE24H":   data.Bitcoin.Brl24hChange,
		"ETH_CHANGE24H":   data.Ethereum.Brl24hChange,
		"BNB_CHANGE24H":   data.Binancecoin.Brl24hChange,
		"LINK_CHANGE24H":  data.Chainlink.Brl24hChange,
		"AVAX_CHANGE24H":  data.Avalanche.Brl24hChange,
		"SOL_CHANGE24H":   data.Solana.Brl24hChange,
		"APT_CHANGE24H":   data.Aptos.Brl24hChange,
	}

	if prices["BRL"] <= 0 || prices["USD"] <= 0 {
		return nil, "", fmt.Errorf("coingecko payload sem USDT BRL/USD")
	}
	if prices["EUR"] <= 0 {
		delete(prices, "EUR")
		delete(prices, "EURUSD")
		delete(prices, "USDTEUR")
	}
	if data.Bitcoin.Usd <= 0 {
		delete(prices, "BTCUSDT")
		delete(prices, "BTCUSDT_SOURCE")
	}
	if data.Ethereum.Usd <= 0 {
		delete(prices, "ETHUSDT")
		delete(prices, "ETHUSDT_SOURCE")
	}
	if data.Binancecoin.Usd <= 0 {
		delete(prices, "BNBUSDT")
		delete(prices, "BNBUSDT_SOURCE")
	}
	if data.Chainlink.Usd <= 0 {
		delete(prices, "LINKUSDT_SOURCE")
	}
	if data.Avalanche.Usd <= 0 {
		delete(prices, "AVAXUSDT_SOURCE")
	}
	if data.Solana.Usd <= 0 {
		delete(prices, "SOLUSDT_SOURCE")
	}
	if data.Aptos.Usd <= 0 {
		delete(prices, "APTUSDT_SOURCE")
	}
	return prices, "coingecko", nil
}

func (pw *PriceWorker) fetchBinancePrices(ctx context.Context) (map[string]float64, string, error) {
	symbols := map[string]string{
		"BRL":      "USDTBRL",
		"USDTBRL":  "USDTBRL",
		"BTCUSDT":  "BTCUSDT",
		"ETHUSDT":  "ETHUSDT",
		"BNBUSDT":  "BNBUSDT",
		"LINKUSDT": "LINKUSDT",
		"AVAXUSDT": "AVAXUSDT",
		"SOLUSDT":  "SOLUSDT",
		"APTUSDT":  "APTUSDT",
		"EURUSD":   "EURUSDT",
	}
	prices := map[string]float64{"USD": 1, "USDTUSD": 1}

	for key, symbol := range symbols {
		price, change24h, err := pw.fetchBinance24hTicker(ctx, symbol)
		if err != nil {
			slog.Warn("Ticker Binance indisponivel", "symbol", symbol, "error", err)
			price, err = pw.fetchBinanceTicker(ctx, symbol)
			if err != nil {
				continue
			}
		}
		prices[key] = price
		if symbol == "USDTBRL" {
			prices["USDT_CHANGE24H"] = change24h
		}
		if symbol == "BTCUSDT" {
			prices["BTCUSDT_SOURCE"] = price
			prices["BTC_CHANGE24H"] = change24h
		}
		if symbol == "ETHUSDT" {
			prices["ETHUSDT_SOURCE"] = price
			prices["ETH_CHANGE24H"] = change24h
		}
		if symbol == "BNBUSDT" {
			prices["BNBUSDT_SOURCE"] = price
			prices["BNB_CHANGE24H"] = change24h
		}
		if symbol == "LINKUSDT" {
			prices["LINKUSDT_SOURCE"] = price
			prices["LINK_CHANGE24H"] = change24h
		}
		if symbol == "AVAXUSDT" {
			prices["AVAXUSDT_SOURCE"] = price
			prices["AVAX_CHANGE24H"] = change24h
		}
		if symbol == "SOLUSDT" {
			prices["SOLUSDT_SOURCE"] = price
			prices["SOL_CHANGE24H"] = change24h
		}
		if symbol == "APTUSDT" {
			prices["APTUSDT_SOURCE"] = price
			prices["APT_CHANGE24H"] = change24h
		}
		if symbol == "EURUSDT" && price > 0 {
			prices["EUR"] = 1 / price
			prices["USDTEUR"] = 1 / price
		}
	}
	if prices["BRL"] <= 0 {
		return nil, "", fmt.Errorf("fallback Binance sem USDTBRL")
	}
	return prices, "binance", nil
}

func (pw *PriceWorker) fetchBinance24hTicker(ctx context.Context, symbol string) (float64, float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.binance.com/api/v3/ticker/24hr?symbol="+symbol, nil)
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("User-Agent", "PaymentGateway/1.0")

	resp, err := pw.client.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, 0, fmt.Errorf("binance status %d", resp.StatusCode)
	}

	var data Binance24hTickerResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return 0, 0, err
	}

	price, err := strconv.ParseFloat(data.LastPrice, 64)
	if err != nil {
		return 0, 0, err
	}
	change24h, err := strconv.ParseFloat(data.PriceChangePercent, 64)
	if err != nil {
		return price, 0, nil
	}
	return price, change24h, nil
}

func (pw *PriceWorker) fetchBinanceTicker(ctx context.Context, symbol string) (float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.binance.com/api/v3/ticker/price?symbol="+symbol, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "PaymentGateway/1.0")

	resp, err := pw.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("binance status %d", resp.StatusCode)
	}

	var data BinanceTickerResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return 0, err
	}

	price, err := strconv.ParseFloat(data.Price, 64)
	if err != nil {
		return 0, err
	}
	return price, nil
}

// Helper
func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
