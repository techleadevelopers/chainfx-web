package mobile

import (
	"encoding/json"
	"io"
	"net/http"
)

// handlePixGenerate — POST /api/mobile/pix/generate
// Creates a sell order and returns the PIX deposit address + QR data.
func (s *Server) handlePixGenerate(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	var req struct {
		AmountBRL float64 `json:"amount_brl"`
		PixKey    string  `json:"pix_key"`
		PixCpf    string  `json:"pix_cpf"`
		PixPhone  string  `json:"pix_phone"`
	}
	if err := decodeJSON(r, &req); err != nil || req.AmountBRL <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "amount_brl obrigatório"})
		return
	}
	pixKey := req.PixKey
	if pixKey == "" {
		pixKey = req.PixPhone
	}
	if pixKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "pix_key ou pix_phone obrigatório"})
		return
	}

	payload := map[string]any{
		"amountBRL": req.AmountBRL,
		"pixPhone":  pixKey,
		"pixCpf":    req.PixCpf,
	}
	resp, err := forwardToInternal(r, "POST", s.internalBase(r)+"/api/order", payload, s.internalAPIKey())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var result map[string]any
	if json.Unmarshal(body, &result) == nil {
		if id, ok := result["id"].(string); ok && id != "" {
			_ = mobileDB(s.db).TagOrderUser(r.Context(), id, uid)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}

// handlePixConfirm — POST /api/mobile/pix/confirm
// Webhook-style endpoint for PIX payment confirmation (no auth required — called by provider).
func (s *Server) handlePixConfirm(w http.ResponseWriter, r *http.Request) {
	resp, err := forwardToInternal(r, "POST", s.internalBase(r)+"/api/pix/webhook", nil, "")
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}

// handlePixStatus — GET /api/mobile/pix/status/{id}
func (s *Server) handlePixStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	resp, err := forwardToInternal(r, "GET", s.internalBase(r)+"/api/order/"+id, nil, s.internalAPIKey())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}

// handlePixCopy — POST /api/mobile/pix/copy
// Returns the PIX copia-e-cola string for a given order.
func (s *Server) handlePixCopy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OrderID string `json:"order_id"`
	}
	if err := decodeJSON(r, &req); err != nil || req.OrderID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "order_id obrigatório"})
		return
	}
	resp, err := forwardToInternal(r, "GET", s.internalBase(r)+"/api/order/"+req.OrderID, nil, s.internalAPIKey())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}
