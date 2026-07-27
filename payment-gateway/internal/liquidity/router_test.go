package liquidity

import (
	"context"
	"errors"
	"testing"
)

type quoteProvider struct {
	name  string
	quote Quote
	err   error
}

func (p quoteProvider) Name() string { return p.name }

func (p quoteProvider) Quote(context.Context, Request) (Quote, error) {
	if p.err != nil {
		return Quote{}, p.err
	}
	return p.quote, nil
}

type executableProvider struct {
	quoteProvider
	exec Execution
	err  error
}

func (p executableProvider) Execute(context.Context, Request, Quote) (Execution, error) {
	if p.err != nil {
		return Execution{}, p.err
	}
	return p.exec, nil
}

func TestRouterSelectsLowestNetCostWithSLA(t *testing.T) {
	router := NewRouter(
		quoteProvider{name: "slow-cheap", quote: Quote{TotalCostBRL: 100, CryptoAmount: 0.001, DeliverySLASeconds: 1200, ReliabilityBps: 9000}},
		quoteProvider{name: "fast-fair", quote: Quote{TotalCostBRL: 101, CryptoAmount: 0.001, DeliverySLASeconds: 60, ReliabilityBps: 9800}},
		quoteProvider{name: "broken", err: errors.New("down")},
	)

	best, quotes, err := router.BestQuote(context.Background(), Request{
		OrderID:         "buy-1",
		Asset:           "btc",
		Network:         "bitcoin",
		AmountBRL:       110,
		CryptoAmount:    0.001,
		QuoteLockedRate: 100000,
	})
	if err != nil {
		t.Fatalf("BestQuote returned error: %v", err)
	}
	if len(quotes) != 2 {
		t.Fatalf("expected 2 usable quotes, got %d", len(quotes))
	}
	if best.Provider != "fast-fair" {
		t.Fatalf("expected fast-fair to win after SLA penalty, got %s", best.Provider)
	}
	if best.Asset != "BTC" || best.Network != "BITCOIN" {
		t.Fatalf("quote was not normalized: %+v", best)
	}
}

func TestRouterExecuteBestRequiresExecutableProvider(t *testing.T) {
	router := NewRouter(quoteProvider{name: "quote-only", quote: Quote{TotalCostBRL: 100, CryptoAmount: 0.001}})

	best, quotes, _, err := router.ExecuteBest(context.Background(), Request{
		OrderID:         "buy-1",
		Asset:           "BTC",
		Network:         "BITCOIN",
		AmountBRL:       100,
		CryptoAmount:    0.001,
		QuoteLockedRate: 100000,
	})
	if !errors.Is(err, ErrNoExecutable) {
		t.Fatalf("expected ErrNoExecutable, got %v", err)
	}
	if best.Provider != "quote-only" || len(quotes) != 1 {
		t.Fatalf("expected selected quote to be returned for audit, best=%+v quotes=%d", best, len(quotes))
	}
}

func TestRouterExecuteBestReturnsExecution(t *testing.T) {
	router := NewRouter(executableProvider{
		quoteProvider: quoteProvider{name: "partner-a", quote: Quote{TotalCostBRL: 100, CryptoAmount: 0.001}},
		exec:          Execution{Status: "sent", TxHash: "0xabc"},
	})

	best, _, exec, err := router.ExecuteBest(context.Background(), Request{
		OrderID:         "buy-1",
		Asset:           "BTC",
		Network:         "BITCOIN",
		AmountBRL:       100,
		CryptoAmount:    0.001,
		QuoteLockedRate: 100000,
	})
	if err != nil {
		t.Fatalf("ExecuteBest returned error: %v", err)
	}
	if best.Provider != "partner-a" || exec.Provider != "partner-a" || exec.TxHash != "0xabc" {
		t.Fatalf("unexpected execution: best=%+v exec=%+v", best, exec)
	}
}

func TestRouterExecuteBestFallsBackToNextExecutableQuote(t *testing.T) {
	router := NewRouter(
		executableProvider{
			quoteProvider: quoteProvider{name: "cheap-down", quote: Quote{TotalCostBRL: 99, CryptoAmount: 1, DeliverySLASeconds: 60}},
			err:           errors.New("provider execute down"),
		},
		executableProvider{
			quoteProvider: quoteProvider{name: "backup", quote: Quote{TotalCostBRL: 101, CryptoAmount: 1, DeliverySLASeconds: 60}},
			exec:          Execution{Status: "sent", TxHash: "sig-backup"},
		},
	)

	best, quotes, exec, err := router.ExecuteBest(context.Background(), Request{
		OrderID:      "buy-sol",
		Asset:        "SOL",
		Network:      "SOLANA",
		CryptoAmount: 1,
	})
	if err != nil {
		t.Fatalf("ExecuteBest returned error: %v", err)
	}
	if len(quotes) != 2 {
		t.Fatalf("expected both provider quotes for audit, got %d", len(quotes))
	}
	if best.Provider != "backup" || exec.Provider != "backup" || exec.TxHash != "sig-backup" {
		t.Fatalf("expected backup execution, best=%+v exec=%+v", best, exec)
	}
}

func TestRouterRejectsMismatchedProviderQuote(t *testing.T) {
	router := NewRouter(
		quoteProvider{name: "wrong-network", quote: Quote{Asset: "BTC", Network: "SOLANA", TotalCostBRL: 100, CryptoAmount: 0.001}},
		quoteProvider{name: "underfilled", quote: Quote{Asset: "BTC", Network: "BITCOIN", TotalCostBRL: 99, CryptoAmount: 0.0001}},
	)

	_, _, err := router.BestQuote(context.Background(), Request{
		OrderID:      "buy-1",
		Asset:        "BTC",
		Network:      "BITCOIN",
		CryptoAmount: 0.001,
	})
	if !errors.Is(err, ErrNoProviderQuote) {
		t.Fatalf("expected ErrNoProviderQuote, got %v", err)
	}
}

func TestRouterRejectsMismatchedDestinationQuote(t *testing.T) {
	router := NewRouter(quoteProvider{name: "wrong-dest", quote: Quote{
		Asset:         "SOL",
		Network:       "SOLANA",
		DestAddress:   "8dQ7uLxkVQ2mN4wH3rP9tB6sC1zY5aE7gF2hJ3kL4mN5",
		TotalCostBRL:  100,
		CryptoAmount:  1,
		TokenDecimals: 9,
	}})

	_, _, err := router.BestQuote(context.Background(), Request{
		OrderID:       "buy-sol",
		Asset:         "SOL",
		Network:       "SOLANA",
		DestAddress:   "5Q7uLxkVQ2mN4wH3rP9tB6sC1zY5aE7gF2hJ3kL4mN5A",
		CryptoAmount:  1,
		TokenDecimals: 9,
	})
	if !errors.Is(err, ErrNoProviderQuote) {
		t.Fatalf("expected ErrNoProviderQuote, got %v", err)
	}
}

func TestRouterRejectsTokenContractMismatchIncludingNativePairs(t *testing.T) {
	router := NewRouter(
		quoteProvider{name: "native-with-contract", quote: Quote{Asset: "ETH", Network: "BASE", TokenContract: "0x0000000000000000000000000000000000000001", TotalCostBRL: 100, CryptoAmount: 0.01, TokenDecimals: 18}},
		quoteProvider{name: "wrong-usdc-contract", quote: Quote{Asset: "USDC", Network: "BASE", TokenContract: "0x0000000000000000000000000000000000000002", TotalCostBRL: 100, CryptoAmount: 10, TokenDecimals: 6}},
	)

	if _, _, err := router.BestQuote(context.Background(), Request{Asset: "ETH", Network: "BASE", CryptoAmount: 0.01, TokenDecimals: 18}); !errors.Is(err, ErrNoProviderQuote) {
		t.Fatalf("native pair should reject provider contract, got %v", err)
	}
	if _, _, err := router.BestQuote(context.Background(), Request{Asset: "USDC", Network: "BASE", TokenContract: "0x0000000000000000000000000000000000000001", CryptoAmount: 10, TokenDecimals: 6}); !errors.Is(err, ErrNoProviderQuote) {
		t.Fatalf("ERC20 pair should reject wrong contract, got %v", err)
	}
}

func TestRouterRejectsMismatchedExecution(t *testing.T) {
	router := NewRouter(executableProvider{
		quoteProvider: quoteProvider{name: "partner-a", quote: Quote{Asset: "SOL", Network: "SOLANA", TotalCostBRL: 100, CryptoAmount: 1}},
		exec:          Execution{Status: "sent", TxHash: "sig", Network: "APTOS"},
	})

	_, _, _, err := router.ExecuteBest(context.Background(), Request{
		OrderID:      "buy-1",
		Asset:        "SOL",
		Network:      "SOLANA",
		CryptoAmount: 1,
	})
	if err == nil {
		t.Fatal("expected execution mismatch error")
	}
}

func TestPairPolicyAllowsOnlyExplicitAssetNetworkPairs(t *testing.T) {
	policy := NewPairPolicy("USDT:BSC:0x55d398326f99059ff775485246999027b3197955:18,AVAX:BSC:0x0000000000000000000000000000000000000001:18,ETH:POLYGON:0x7ceb23fd6bc0add59e62ac25578270cff1b9f619:18")

	avaxBSC, ok := policy.Resolve("avax", "bep-20")
	if !ok {
		t.Fatal("expected AVAX/BSC to be allowed")
	}
	if avaxBSC.Asset != "AVAX" || avaxBSC.Network != "BSC" || avaxBSC.ContractAddress == "" || avaxBSC.Decimals != 18 {
		t.Fatalf("unexpected AVAX/BSC pair: %+v", avaxBSC)
	}
	if policy.Allows("AVAX", "AVALANCHE") {
		t.Fatal("AVAX native network must not be allowed unless explicitly configured")
	}
	if policy.Allows("AVAX", "POLYGON") {
		t.Fatal("AVAX/POLYGON must not be allowed unless explicitly configured")
	}
	if !policy.Allows("ETH", "MATIC") {
		t.Fatal("expected ETH/POLYGON alias to resolve")
	}
}
