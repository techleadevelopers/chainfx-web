package liquidity

import (
	"context"
	"errors"
	"strings"
	"time"
)

type StaticProvider struct {
	ProviderName       string
	ProviderType       string
	Enabled            bool
	Assets             []string
	Networks           []string
	FeeBRL             float64
	NetworkFeeBRL      float64
	SpreadBps          int
	DeliverySLASeconds int
	ReliabilityBps     int
	DirectDelivery     bool
}

func (p StaticProvider) Name() string {
	if strings.TrimSpace(p.ProviderName) == "" {
		return "static"
	}
	return strings.TrimSpace(p.ProviderName)
}

func (p StaticProvider) Quote(_ context.Context, req Request) (Quote, error) {
	if !p.Enabled {
		return Quote{}, errors.New("provider disabled")
	}
	req = normalizeRequest(req)
	if !containsNormalized(p.Assets, req.Asset) || !containsNormalizedNetwork(p.Networks, req.Network) {
		return Quote{}, errors.New("asset/network unsupported")
	}
	baseCost := req.CryptoAmount * req.QuoteLockedRate
	if baseCost <= 0 {
		baseCost = req.AmountBRL
	}
	spread := baseCost * float64(maxInt(p.SpreadBps, 0)) / 10000
	sla := p.DeliverySLASeconds
	if sla <= 0 {
		sla = 180
	}
	return Quote{
		Provider:           p.Name(),
		ProviderType:       firstNonEmpty(p.ProviderType, "liquidity_provider"),
		ExternalQuoteID:    "static-" + req.OrderID,
		Asset:              req.Asset,
		Network:            req.Network,
		TokenContract:      req.TokenContract,
		TokenDecimals:      req.TokenDecimals,
		FiatCostBRL:        baseCost,
		ProviderFeeBRL:     p.FeeBRL,
		NetworkFeeBRL:      p.NetworkFeeBRL,
		SpreadBRL:          spread,
		TotalCostBRL:       baseCost + p.FeeBRL + p.NetworkFeeBRL + spread,
		CryptoAmount:       req.CryptoAmount,
		DeliverySLASeconds: sla,
		ReliabilityBps:     maxInt(p.ReliabilityBps, 9000),
		DirectDelivery:     p.DirectDelivery,
		ExpiresAt:          time.Now().UTC().Add(2 * time.Minute),
		Metadata:           map[string]any{"mode": "static_quote_only"},
	}, nil
}

func containsNormalized(values []string, needle string) bool {
	if len(values) == 0 {
		return true
	}
	needle = strings.ToUpper(strings.TrimSpace(needle))
	for _, value := range values {
		if strings.ToUpper(strings.TrimSpace(value)) == needle {
			return true
		}
	}
	return false
}

func containsNormalizedNetwork(values []string, needle string) bool {
	if len(values) == 0 {
		return true
	}
	needle = normalizeNetwork(needle)
	for _, value := range values {
		if normalizeNetwork(value) == needle {
			return true
		}
	}
	return false
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
