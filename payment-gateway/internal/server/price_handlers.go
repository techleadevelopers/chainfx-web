package server

import "net/http"

func (s *Server) handlePrice(w http.ResponseWriter, r *http.Request) {
	price := s.workers.PriceWorker.GetCurrentPrice()
	if price <= 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "preÃ§o ainda não carregado"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"brl":          price,
		"usd":          s.workers.PriceWorker.GetPrice("USD"),
		"eur":          s.workers.PriceWorker.GetPrice("EUR"),
		"usdtbrl":      s.workers.PriceWorker.GetPrice("USDTBRL"),
		"sellUsdtBrl":  s.sellRate(price),
		"sellWallet":   s.cfg.SellWalletAddress,
		"sellNetwork":  "BEP20",
		"sellNetworks": s.supportedSellNetworks(),
		"eurusd":       s.workers.PriceWorker.GetPrice("EURUSD"),
		"btcusdt":      s.workers.PriceWorker.GetPrice("BTCUSDT"),
		"bnbusdt":      s.workers.PriceWorker.GetPrice("BNBUSDT"),
		"solusdt":      s.workers.PriceWorker.GetPrice("SOLUSDT"),
		"linkusdt":     s.workers.PriceWorker.GetPrice("LINKUSDT"),
		"avaxusdt":     s.workers.PriceWorker.GetPrice("AVAXUSDT"),
	})
}

// ChainFX Phase 1 exposes the infrastructure API without changing the legacy /api surface.
