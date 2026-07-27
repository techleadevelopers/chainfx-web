package liquidity

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	ErrNoProviderQuote = errors.New("liquidity: no provider quote available")
	ErrNoExecutable    = errors.New("liquidity: selected provider cannot execute")
)

type Request struct {
	OrderID         string
	UserID          string
	Asset           string
	Network         string
	TokenContract   string
	TokenDecimals   int
	FiatCurrency    string
	AmountBRL       float64
	CryptoAmount    float64
	QuoteLockedRate float64
	DestAddress     string
	CreatedAt       time.Time
}

type Quote struct {
	Provider           string
	ProviderType       string
	ExternalQuoteID    string
	Asset              string
	Network            string
	TokenContract      string
	TokenDecimals      int
	DestAddress        string
	FiatCostBRL        float64
	ProviderFeeBRL     float64
	NetworkFeeBRL      float64
	SpreadBRL          float64
	TotalCostBRL       float64
	CryptoAmount       float64
	DeliverySLASeconds int
	ReliabilityBps     int
	DirectDelivery     bool
	ExpiresAt          time.Time
	Metadata           map[string]any
}

type Execution struct {
	Provider        string
	ExternalOrderID string
	Status          string
	TxHash          string
	Asset           string
	Network         string
	TokenContract   string
	DestAddress     string
	DeliveredAmount float64
	Metadata        map[string]any
}

type Provider interface {
	Name() string
	Quote(ctx context.Context, req Request) (Quote, error)
}

type Executor interface {
	Execute(ctx context.Context, req Request, quote Quote) (Execution, error)
}

type Router struct {
	providers []Provider
	now       func() time.Time
}

func NewRouter(providers ...Provider) *Router {
	clean := make([]Provider, 0, len(providers))
	for _, provider := range providers {
		if provider != nil {
			clean = append(clean, provider)
		}
	}
	return &Router{providers: clean, now: time.Now}
}

func (r *Router) QuoteAll(ctx context.Context, req Request) []Quote {
	if r == nil || len(r.providers) == 0 {
		return nil
	}
	req = normalizeRequest(req)
	out := make([]Quote, 0, len(r.providers))
	ch := make(chan Quote, len(r.providers))
	var wg sync.WaitGroup
	for _, provider := range r.providers {
		wg.Add(1)
		go func(provider Provider) {
			defer wg.Done()
			quote, err := provider.Quote(ctx, req)
			if err != nil {
				return
			}
			quote = normalizeQuote(req, quote, provider.Name())
			if quote.TotalCostBRL <= 0 || quote.CryptoAmount <= 0 {
				return
			}
			if !quoteMatchesRequest(req, quote) {
				return
			}
			ch <- quote
		}(provider)
	}
	wg.Wait()
	close(ch)
	for quote := range ch {
		out = append(out, quote)
	}
	sortQuotes(out)
	return out
}

func (r *Router) BestQuote(ctx context.Context, req Request) (Quote, []Quote, error) {
	quotes := r.QuoteAll(ctx, req)
	if len(quotes) == 0 {
		return Quote{}, nil, ErrNoProviderQuote
	}
	return quotes[0], quotes, nil
}

func (r *Router) ExecuteBest(ctx context.Context, req Request) (Quote, []Quote, Execution, error) {
	best, quotes, err := r.BestQuote(ctx, req)
	if err != nil {
		return Quote{}, quotes, Execution{}, err
	}
	providersByName := make(map[string]Provider, len(r.providers))
	for _, provider := range r.providers {
		providersByName[strings.ToUpper(strings.TrimSpace(provider.Name()))] = provider
	}
	var firstErr error
	for _, quote := range quotes {
		provider := providersByName[strings.ToUpper(strings.TrimSpace(quote.Provider))]
		if provider == nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%w: %s", ErrNoExecutable, quote.Provider)
			}
			continue
		}
		executor, ok := provider.(Executor)
		if !ok {
			if firstErr == nil {
				firstErr = fmt.Errorf("%w: %s", ErrNoExecutable, quote.Provider)
			}
			continue
		}
		exec, err := executor.Execute(ctx, normalizeRequest(req), quote)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if strings.TrimSpace(exec.Provider) == "" {
			exec.Provider = quote.Provider
		}
		if !executionMatchesRequest(req, exec) {
			if firstErr == nil {
				firstErr = fmt.Errorf("liquidity: provider execution mismatch")
			}
			continue
		}
		return quote, quotes, exec, nil
	}
	if firstErr != nil {
		return best, quotes, Execution{}, firstErr
	}
	return best, quotes, Execution{}, fmt.Errorf("%w: %s", ErrNoExecutable, best.Provider)
}

func sortQuotes(quotes []Quote) {
	sort.SliceStable(quotes, func(i, j int) bool {
		left := quoteScore(quotes[i])
		right := quoteScore(quotes[j])
		if left == right {
			return quotes[i].Provider < quotes[j].Provider
		}
		return left < right
	})
}

func quoteScore(q Quote) float64 {
	slaPenalty := math.Max(0, float64(q.DeliverySLASeconds-60)) * 0.002
	reliabilityDiscount := math.Max(0, float64(q.ReliabilityBps-9000)) * 0.0001
	return q.TotalCostBRL + slaPenalty - reliabilityDiscount
}

func normalizeRequest(req Request) Request {
	req.Asset = strings.ToUpper(strings.TrimSpace(req.Asset))
	req.Network = normalizeNetwork(req.Network)
	req.TokenContract = strings.TrimSpace(req.TokenContract)
	if req.TokenDecimals <= 0 {
		req.TokenDecimals = 18
	}
	req.FiatCurrency = strings.ToUpper(strings.TrimSpace(req.FiatCurrency))
	if req.FiatCurrency == "" {
		req.FiatCurrency = "BRL"
	}
	if req.CreatedAt.IsZero() {
		req.CreatedAt = time.Now().UTC()
	}
	return req
}

func normalizeQuote(req Request, quote Quote, providerName string) Quote {
	quote.Provider = strings.TrimSpace(firstNonEmpty(quote.Provider, providerName))
	quote.ProviderType = strings.ToLower(strings.TrimSpace(quote.ProviderType))
	quote.Asset = strings.ToUpper(strings.TrimSpace(firstNonEmpty(quote.Asset, req.Asset)))
	quote.Network = normalizeNetwork(firstNonEmpty(quote.Network, req.Network))
	quote.TokenContract = strings.TrimSpace(firstNonEmpty(quote.TokenContract, req.TokenContract))
	quote.DestAddress = strings.TrimSpace(firstNonEmpty(quote.DestAddress, req.DestAddress))
	if quote.TokenDecimals <= 0 {
		quote.TokenDecimals = req.TokenDecimals
	}
	if quote.CryptoAmount <= 0 {
		quote.CryptoAmount = req.CryptoAmount
	}
	if quote.TotalCostBRL <= 0 {
		quote.TotalCostBRL = quote.FiatCostBRL + quote.ProviderFeeBRL + quote.NetworkFeeBRL + quote.SpreadBRL
	}
	if quote.ExpiresAt.IsZero() {
		quote.ExpiresAt = time.Now().UTC().Add(2 * time.Minute)
	}
	if quote.ReliabilityBps <= 0 {
		quote.ReliabilityBps = 9000
	}
	return quote
}

func quoteMatchesRequest(req Request, quote Quote) bool {
	if !strings.EqualFold(quote.Asset, req.Asset) {
		return false
	}
	if normalizeNetwork(quote.Network) != normalizeNetwork(req.Network) {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(quote.TokenContract), strings.TrimSpace(req.TokenContract)) {
		return false
	}
	if strings.TrimSpace(quote.DestAddress) != "" && !strings.EqualFold(strings.TrimSpace(quote.DestAddress), strings.TrimSpace(req.DestAddress)) {
		return false
	}
	if req.TokenDecimals > 0 && quote.TokenDecimals != req.TokenDecimals {
		return false
	}
	if req.CryptoAmount > 0 && quote.CryptoAmount+cryptoAmountTolerance(req.CryptoAmount) < req.CryptoAmount {
		return false
	}
	return true
}

func executionMatchesRequest(req Request, exec Execution) bool {
	if strings.TrimSpace(exec.Asset) != "" && !strings.EqualFold(exec.Asset, req.Asset) {
		return false
	}
	if strings.TrimSpace(exec.Network) != "" && normalizeNetwork(exec.Network) != normalizeNetwork(req.Network) {
		return false
	}
	if strings.TrimSpace(exec.TokenContract) != "" && !strings.EqualFold(strings.TrimSpace(exec.TokenContract), strings.TrimSpace(req.TokenContract)) {
		return false
	}
	if strings.TrimSpace(exec.DestAddress) != "" && !strings.EqualFold(strings.TrimSpace(exec.DestAddress), strings.TrimSpace(req.DestAddress)) {
		return false
	}
	if req.CryptoAmount > 0 && exec.DeliveredAmount > 0 && exec.DeliveredAmount+cryptoAmountTolerance(req.CryptoAmount) < req.CryptoAmount {
		return false
	}
	return true
}

func cryptoAmountTolerance(amount float64) float64 {
	if amount <= 0 {
		return 0
	}
	return amount * 0.005
}

func NormalizeNetwork(network string) string {
	switch strings.ToUpper(strings.TrimSpace(network)) {
	case "BEP20", "BEP-20", "BINANCE", "BINANCE_SMART_CHAIN", "BNB", "BSC":
		return "BSC"
	case "POL", "MATIC", "POLYGON":
		return "POLYGON"
	case "BASE", "BASE_MAINNET":
		return "BASE"
	case "ARB", "ARBITRUM", "ARBITRUM_ONE":
		return "ARBITRUM"
	case "ETH", "ETHEREUM", "ERC20", "ERC-20":
		return "ETHEREUM"
	case "BTC", "BITCOIN", "BTC_MAINNET", "MAINNET":
		return "BITCOIN"
	case "SOL", "SOLANA":
		return "SOLANA"
	case "APT", "APTOS":
		return "APTOS"
	default:
		return strings.ToUpper(strings.TrimSpace(network))
	}
}

func normalizeNetwork(network string) string {
	return NormalizeNetwork(network)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
