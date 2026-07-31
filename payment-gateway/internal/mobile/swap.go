package mobile

// Phase 5 direct crypto-to-crypto swap endpoints for mobile.

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"payment-gateway/internal/models"

	"github.com/lib/pq"
)

const defaultSwapFeeBPS = 50  // 0.50%
const defaultSlippage = 0.005 // 0.5%

func mobileProductError(code, message string) map[string]any {
	return map[string]any{"code": code, "message": message, "error": message}
}

func (s *Server) handleSwapQuote(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FromAsset string  `json:"from_asset"`
		ToAsset   string  `json:"to_asset"`
		Amount    float64 `json:"amount"`
		Slippage  float64 `json:"slippage"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Amount <= 0 || req.FromAsset == "" || req.ToAsset == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "from_asset, to_asset e amount obrigatorios"})
		return
	}
	req.FromAsset = strings.ToUpper(req.FromAsset)
	req.ToAsset = strings.ToUpper(req.ToAsset)
	if req.FromAsset == req.ToAsset {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "from_asset e to_asset devem ser diferentes"})
		return
	}
	if req.Slippage <= 0 {
		req.Slippage = defaultSlippage
	}
	if s.cfg == nil || !s.cfg.MobileSwapPancakeEnabled {
		writeJSON(w, http.StatusServiceUnavailable, mobileProductError("ROUTE_UNAVAILABLE", "Swap indisponivel no momento."))
		return
	}

	fee := req.Amount * float64(defaultSwapFeeBPS) / 10_000
	netFrom := req.Amount - fee
	if s.cfg != nil && s.cfg.MobileSwapPancakeEnabled {
		pancakeQuote, err := s.mobilePancakeQuote(r.Context(), req.FromAsset, req.ToAsset, netFrom, req.Slippage)
		if err == nil {
			estimatedTo := pancakeQuote.EstimatedTo
			rate := estimatedTo / req.Amount
			minReceived := estimatedTo * (1 - req.Slippage)
			expiresAt := time.Now().Add(60 * time.Second).UTC()
			quoteID, err := s.issueMobileQuote(mobileQuoteClaims{
				Side:           "swap",
				Asset:          req.FromAsset + ":" + req.ToAsset,
				Amount:         req.Amount,
				Rate:           rate,
				Fee:            fee,
				Total:          estimatedTo,
				ExpiresAt:      expiresAt.Unix(),
				Network:        "BSC",
				Provider:       "pancakeswap_v2",
				Router:         pancakeQuote.Router,
				Path:           pancakeQuote.Path,
				AmountInRaw:    pancakeQuote.AmountInRaw,
				ExpectedOutRaw: pancakeQuote.AmountOutRaw,
				MinReceivedRaw: pancakeQuote.MinOutRaw,
				SlippageBPS:    pancakeQuote.SlippageBPS,
			})
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"code": "SWAP_QUOTE_ERROR", "message": "erro interno ao assinar cotacao"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"quote_id":         quoteID,
				"expires_at":       expiresAt.Format(time.RFC3339),
				"from_asset":       req.FromAsset,
				"to_asset":         req.ToAsset,
				"from_amount":      req.Amount,
				"estimated_to":     estimatedTo,
				"min_received":     minReceived,
				"rate":             rate,
				"fee_bps":          defaultSwapFeeBPS,
				"fee_amount":       fee,
				"slippage":         req.Slippage,
				"provider":         "pancakeswap_v2",
				"network":          "BSC",
				"router":           pancakeQuote.Router,
				"path":             pancakeQuote.Path,
				"amount_in_raw":    pancakeQuote.AmountInRaw,
				"amount_out_raw":   pancakeQuote.AmountOutRaw,
				"min_received_raw": pancakeQuote.MinOutRaw,
				"slippage_bps":     pancakeQuote.SlippageBPS,
			})
			return
		}
		slog.Warn("Pancake quote indisponivel", "from", req.FromAsset, "to", req.ToAsset, "err", err)
		writeJSON(w, http.StatusServiceUnavailable, mobileProductError("ROUTE_UNAVAILABLE", "Swap indisponivel no momento."))
		return
	}

	writeJSON(w, http.StatusServiceUnavailable, mobileProductError("ROUTE_UNAVAILABLE", "Swap indisponivel no momento."))

}

func mobileSwapQuoteID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err == nil {
		return "swq_" + hex.EncodeToString(b[:])
	}
	return "swq_" + strings.ToLower(hex.EncodeToString([]byte(time.Now().UTC().Format("20060102150405.000000000"))))
}

func (s *Server) handleSwapExecute(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	var req struct {
		FromAsset string  `json:"from_asset"`
		ToAsset   string  `json:"to_asset"`
		Amount    float64 `json:"amount"`
		Slippage  float64 `json:"slippage"`
		QuoteID   string  `json:"quote_id"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Amount <= 0 || req.FromAsset == "" || req.ToAsset == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "INVALID_PAYLOAD", "message": "from_asset, to_asset e amount obrigatorios"})
		return
	}
	req.FromAsset = strings.ToUpper(req.FromAsset)
	req.ToAsset = strings.ToUpper(req.ToAsset)
	if req.FromAsset == req.ToAsset {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "INVALID_PAYLOAD", "message": "from_asset e to_asset devem ser diferentes"})
		return
	}
	if req.Slippage <= 0 {
		req.Slippage = defaultSlippage
	}

	// Reject executions that arrive without a signed quote — prevents price
	// bypass (where a client sends an arbitrary amount without fetching a quote)
	// and protects against replay of stale or tampered quotes.
	if strings.TrimSpace(req.QuoteID) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"code":    "SWAP_QUOTE_REQUIRED",
			"message": "quote_id obrigatorio; obtenha uma cotacao em /api/mobile/swap/quote primeiro",
		})
		return
	}
	claims, err := s.verifyMobileQuote(req.QuoteID, "swap", req.FromAsset+":"+req.ToAsset, req.Amount, time.Now())
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{
			"code":    "SWAP_QUOTE_INVALID",
			"message": "Cotacao expirada ou invalida.",
		})
		return
	}
	if s.cfg == nil || !s.cfg.MobileSwapPancakeEnabled ||
		!strings.EqualFold(claims.Provider, "pancakeswap_v2") ||
		!strings.EqualFold(claims.Network, "BSC") {
		writeJSON(w, http.StatusServiceUnavailable, mobileProductError("ROUTE_UNAVAILABLE", "Swap indisponivel no momento."))
		return
	}

	fromAsset, _, err := s.mobileAssetBySymbol(r.Context(), req.FromAsset)
	if err != nil && fromAsset == nil {
		slog.Error("erro interno", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro interno"})
		return
	}
	if fromAsset == nil || !fromAsset.Active {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "ativo de origem invalido ou inativo"})
		return
	}
	toAsset, _, err := s.mobileAssetBySymbol(r.Context(), req.ToAsset)
	if err != nil && toAsset == nil {
		slog.Error("erro interno", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro interno"})
		return
	}
	if toAsset == nil || !toAsset.Active {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "ativo de destino invalido ou inativo"})
		return
	}

	quoteMeta := &mobileSwapQuoteMetadata{
		Provider:       claims.Provider,
		Network:        claims.Network,
		Router:         claims.Router,
		Path:           claims.Path,
		AmountInRaw:    claims.AmountInRaw,
		ExpectedOutRaw: claims.ExpectedOutRaw,
		MinReceivedRaw: claims.MinReceivedRaw,
		SlippageBPS:    claims.SlippageBPS,
		ExpiresAt:      time.Unix(claims.ExpiresAt, 0).UTC(),
	}
	swap, err := mobileDB(s.db).CreateSwap(r.Context(), uid, req.FromAsset, req.ToAsset, req.Amount, req.Slippage, defaultSwapFeeBPS, req.QuoteID, quoteMeta)
	if err != nil {
		if pqErr, ok := err.(*pq.Error); ok && pqErr.Code == "23505" {
			writeJSON(w, http.StatusConflict, map[string]any{
				"code":    "SWAP_QUOTE_ALREADY_USED",
				"message": "quote_id ja usado em outro swap",
			})
			return
		}
		slog.Error("erro interno", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro interno"})
		return
	}

	if s.workers != nil && s.workers.Bus != nil {
		s.workers.Bus.Publish(workerEvent("swap.created", map[string]any{
			"swap_id":    swap.ID,
			"user_id":    uid,
			"from_asset": req.FromAsset,
			"to_asset":   req.ToAsset,
			"amount":     req.Amount,
		}))
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"id":             swap.ID,
		"swap_id":        swap.ID,
		"status":         swap.Status,
		"from_asset":     swap.FromAsset,
		"to_asset":       swap.ToAsset,
		"from_amount":    swap.FromAmount,
		"to_amount":      swap.ToAmount,
		"rate":           swap.Rate,
		"fee_bps":        swap.FeeBPS,
		"tx_hash":        swap.TxHash,
		"provider":       "pancakeswap_v2",
		"network":        "BSC",
		"tracking_route": "/api/mobile/swap/" + swap.ID,
		"swap":           swap,
		"message":        "Swap em processamento. Acompanhe o status pelo ID.",
	})
}

func (s *Server) handleGetSwap(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	id := r.PathValue("id")
	swap, err := mobileDB(s.db).GetSwap(r.Context(), id)
	if err != nil {
		slog.Error("erro interno", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro interno"})
		return
	}
	if swap == nil || swap.UserID != uid {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "swap nao encontrado"})
		return
	}
	writeJSON(w, http.StatusOK, swap)
}

func (s *Server) handleListSwaps(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	swaps, err := mobileDB(s.db).ListSwapsByUser(r.Context(), uid, 20)
	if err != nil {
		slog.Error("erro interno", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro interno"})
		return
	}
	if swaps == nil {
		swaps = make([]models.Swap, 0)
	}
	writeJSON(w, http.StatusOK, map[string]any{"swaps": swaps, "count": len(swaps)})
}
