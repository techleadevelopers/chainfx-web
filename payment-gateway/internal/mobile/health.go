package mobile

import (
	"context"
	"net/http"
	"time"
)

// handleHealth — GET /api/mobile/health
// Returns the health of all critical dependencies.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()

	checks := map[string]any{}

	// Database
	dbOK := s != nil && s.db != nil && s.db.SQL != nil && s.db.SQL.PingContext(ctx) == nil
	checks["database"] = healthStatus(dbOK, "")

	// Price worker (cache staleness)
	price := mobileAssetPriceBRL(s.PriceCache(), "USDT")
	checks["price_worker"] = healthStatus(price > 0, map[string]any{
		"usdt_brl": price,
	})

	// RPC pool (BSC)
	checks["bsc_rpc"] = map[string]any{
		"configured": s != nil && s.cfg != nil && s.cfg.BscRpcUrls != "",
		"contract":   s != nil && s.cfg != nil && s.cfg.BscUsdtContract != "",
	}

	// Worker event bus
	eventBusOK := s != nil && s.workers != nil && s.workers.Bus != nil && !s.workers.Bus.Metrics().Closed
	checks["event_bus"] = map[string]any{"ok": eventBusOK}

	// JWT config
	checks["jwt"] = healthStatus(s != nil && s.mcfg != nil && len(s.mcfg.JWTSecret) >= 32, "")

	allOK := dbOK && price > 0
	statusCode := http.StatusOK
	if !allOK {
		statusCode = http.StatusServiceUnavailable
	}
	writeJSON(w, statusCode, map[string]any{
		"ok":        allOK,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"checks":    checks,
	})
}

func healthStatus(ok bool, detail any) map[string]any {
	status := "ok"
	if !ok {
		status = "degraded"
	}
	m := map[string]any{"status": status}
	if detail != nil {
		m["detail"] = detail
	}
	return m
}
