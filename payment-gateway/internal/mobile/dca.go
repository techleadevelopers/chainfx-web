package mobile

import (
	"net/http"
	"strings"

	"payment-gateway/internal/models"
)

// handleDCACreate — POST /api/mobile/dca/create
func (s *Server) handleDCACreate(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	var req struct {
		TokenSymbol string  `json:"token_symbol"`
		Network     string  `json:"network"`
		AmountBRL   float64 `json:"amount_brl"`
		Frequency   string  `json:"frequency"` // daily | weekly | monthly
	}
	if err := decodeJSON(r, &req); err != nil || req.AmountBRL <= 0 || req.TokenSymbol == "" || req.Frequency == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "token_symbol, amount_brl e frequency obrigatórios"})
		return
	}
	freq := models.DCAFrequency(req.Frequency)
	if freq != models.DCADaily && freq != models.DCAWeekly && freq != models.DCAMonthly {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "frequency deve ser daily, weekly ou monthly"})
		return
	}
	tokenSymbol := strings.ToUpper(strings.TrimSpace(req.TokenSymbol))
	network := normalizeMobileBuyNetwork(req.Network)
	if network == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "network nao suportada para DCA"})
		return
	}
	if !s.mobileBuyLiquidityPairSupported(tokenSymbol, network) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "par token_symbol/network nao suportado para DCA"})
		return
	}
	if network == "SOLANA" {
		if s.solSvc == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "Solana rail nao configurada para DCA"})
			return
		}
		if _, err := s.solSvc.GetOrCreateAddress(r.Context(), uid); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao preparar endereco Solana para DCA"})
			return
		}
	}
	asset, _, err := s.mobileAssetBySymbol(r.Context(), tokenSymbol)
	if err != nil && asset == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro interno"})
		return
	}
	if asset == nil || !asset.Active {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "token_symbol invalido ou inativo"})
		return
	}
	minBRL := 0.0
	if s != nil && s.cfg != nil {
		minBRL = float64(s.cfg.OrderMinBrl)
	}
	if minBRL > 0 && req.AmountBRL < minBRL {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "amount_brl abaixo do mínimo"})
		return
	}
	strategy, err := mobileDB(s.db).CreateDCA(r.Context(), uid, tokenSymbol, network, req.AmountBRL, freq)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, strategy)
}

// handleDCAList — GET /api/mobile/dca/strategies
func (s *Server) handleDCAList(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	list, err := mobileDB(s.db).ListDCA(r.Context(), uid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	strategies, summary := s.enrichDCAList(list)
	writeJSON(w, http.StatusOK, map[string]any{"strategies": strategies, "count": len(strategies), "summary": summary})
}

// handleDCADashboard — GET /api/mobile/dca/dashboard
func (s *Server) handleDCADashboard(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	list, err := mobileDB(s.db).ListDCA(r.Context(), uid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	strategies, summary := s.enrichDCAList(list)
	writeJSON(w, http.StatusOK, map[string]any{
		"strategies":   strategies,
		"count":        len(strategies),
		"summary":      summary,
		"chart_points": summary["chart_points"],
	})
}

// handleDCAUpdate — PUT /api/mobile/dca/{id}
func (s *Server) handleDCAUpdate(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	id := r.PathValue("id")
	var req struct {
		Active    *bool                `json:"active"`
		AmountBRL *float64             `json:"amount_brl"`
		Frequency *models.DCAFrequency `json:"frequency"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "payload inválido"})
		return
	}
	if err := mobileDB(s.db).UpdateDCA(r.Context(), id, uid, req.Active, req.AmountBRL, req.Frequency); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	strategy, _ := mobileDB(s.db).GetDCAByUser(r.Context(), id, uid)
	writeJSON(w, http.StatusOK, strategy)
}

// handleDCADelete — DELETE /api/mobile/dca/{id}
func (s *Server) handleDCADelete(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	id := r.PathValue("id")
	if err := mobileDB(s.db).DeleteDCA(r.Context(), id, uid); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleDCAStatus — GET /api/mobile/dca/{id}/status
func (s *Server) handleDCAStatus(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	id := r.PathValue("id")
	strategy, err := mobileDB(s.db).GetDCAByUser(r.Context(), id, uid)
	if err != nil || strategy == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "estratégia não encontrada"})
		return
	}
	price := mobileAssetPriceBRL(s.PriceCache(), strategy.TokenSymbol)
	currentValue := strategy.TotalTokens * price
	pnl := currentValue - strategy.TotalInvested
	writeJSON(w, http.StatusOK, map[string]any{
		"id":                strategy.ID,
		"token_symbol":      strategy.TokenSymbol,
		"network":           strategy.Network,
		"active":            strategy.Active,
		"total_invested":    strategy.TotalInvested,
		"total_tokens":      strategy.TotalTokens,
		"next_execution":    strategy.NextExecution,
		"current_price_brl": price,
		"current_value_brl": currentValue,
		"pnl_brl":           pnl,
		"roi":               dcaROI(pnl, strategy.TotalInvested),
		"chart_points":      dcaChartPoints(strategy.TotalInvested, currentValue),
	})
}

func (s *Server) enrichDCAList(list []models.DCAStrategy) ([]map[string]any, map[string]any) {
	out := make([]map[string]any, 0, len(list))
	totalInvested := 0.0
	currentValue := 0.0
	for _, strategy := range list {
		price := mobileAssetPriceBRL(s.PriceCache(), strategy.TokenSymbol)
		value := strategy.TotalTokens * price
		pnl := value - strategy.TotalInvested
		totalInvested += strategy.TotalInvested
		currentValue += value
		out = append(out, map[string]any{
			"id":                strategy.ID,
			"user_id":           strategy.UserID,
			"token_symbol":      strategy.TokenSymbol,
			"network":           strategy.Network,
			"amount_brl":        strategy.AmountBRL,
			"frequency":         strategy.Frequency,
			"active":            strategy.Active,
			"total_invested":    strategy.TotalInvested,
			"total_tokens":      strategy.TotalTokens,
			"next_execution":    strategy.NextExecution,
			"created_at":        strategy.CreatedAt,
			"current_price_brl": price,
			"current_value_brl": value,
			"pnl_brl":           pnl,
			"roi":               dcaROI(pnl, strategy.TotalInvested),
			"chart_points":      dcaChartPoints(strategy.TotalInvested, value),
		})
	}
	pnl := currentValue - totalInvested
	return out, map[string]any{
		"total_invested_brl":    totalInvested,
		"current_value_brl":     currentValue,
		"pnl_brl":               pnl,
		"roi":                   dcaROI(pnl, totalInvested),
		"chart_points":          dcaChartPoints(totalInvested, currentValue),
		"supported_tokens":      s.mobileLiquiditySupportedTokens(),
		"supported_networks":    s.mobileLiquiditySupportedNetworks(),
		"supported_pairs":       s.mobileLiquiditySupportedPairs(),
		"supported_pair_policy": "DCA usa apenas pares com compra real habilitada",
	}
}

func dcaROI(pnl, invested float64) float64 {
	if invested <= 0 {
		return 0
	}
	return (pnl / invested) * 100
}

func dcaChartPoints(invested, currentValue float64) []float64 {
	if invested <= 0 && currentValue <= 0 {
		return []float64{}
	}
	if currentValue <= 0 {
		currentValue = invested
	}
	points := make([]float64, 0, 7)
	for i := 0; i < 7; i++ {
		t := float64(i) / 6
		points = append(points, invested+(currentValue-invested)*t)
	}
	return points
}
