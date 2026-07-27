package mobile

// kyc_v2.go — Phase 5: Async KYC (non-blocking) endpoints (mobile-only)
//
// KYC never blocks transactions. Users trade immediately at Level 0 limits
// (R$ 500/day). Submitting documents upgrades limits asynchronously.
//
//	POST /api/mobile/kyc/submit         — submit KYC for a given level
//	GET  /api/mobile/kyc/status         — latest KYC request status + limits
//	GET  /api/mobile/kyc/history        — all KYC requests
//	GET  /api/mobile/kyc/limits         — current daily limits per KYC level

import (
	"log/slog"
	"net/http"

	"payment-gateway/internal/models"
)

// handleKYCSubmit — POST /api/mobile/kyc/submit
// Registers a new KYC request. Never blocks user from trading.
func (s *Server) handleKYCSubmit(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	var req struct {
		Level             int    `json:"level"`         // 1, 2 or 3
		DocumentType      string `json:"document_type"` // rg, cnh, passport
		DocumentURL       string `json:"document_url"`
		DocumentFrontURL  string `json:"document_front_url"`
		DocumentBackURL   string `json:"document_back_url"`
		SelfieURL         string `json:"selfie_url"`
		FacialVideoURL    string `json:"facial_video_url"`
		ProofAddrURL      string `json:"proof_of_address_url"`
		ProofIncURL       string `json:"proof_of_income_url"`
		DeviceFingerprint string `json:"device_fingerprint"`
		DocumentOnly      bool   `json:"document_only"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "payload inválido"})
		return
	}
	if req.Level < 1 || req.Level > 3 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "level deve ser 1, 2 ou 3"})
		return
	}
	if req.DocumentURL == "" {
		req.DocumentURL = req.DocumentFrontURL
	}
	if req.SelfieURL == "" {
		req.SelfieURL = req.FacialVideoURL
	}

	// Validate required fields per level
	if req.Level >= 1 && (req.DocumentURL == "" || req.DocumentBackURL == "" || (!req.DocumentOnly && req.FacialVideoURL == "")) {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "Level 1 requer frente, verso e video facial",
		})
		return
	}
	if req.Level >= 2 && req.ProofAddrURL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "Level 2 requer proof_of_address_url",
		})
		return
	}
	if req.Level >= 3 && req.ProofIncURL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "Level 3 requer proof_of_income_url",
		})
		return
	}

	nullableStr := func(s string) *string {
		if s == "" {
			return nil
		}
		return &s
	}

	kyc, err := mobileDB(s.db).CreateKYCRequest(r.Context(), uid,
		models.KYCLevel(req.Level),
		nullableStr(req.DocumentType),
		nullableStr(req.DocumentURL),
		nullableStr(req.DocumentBackURL),
		nullableStr(req.SelfieURL),
		nullableStr(req.FacialVideoURL),
		nullableStr(req.ProofAddrURL),
		nullableStr(req.ProofIncURL),
	)
	if err != nil {
		slog.Error("erro interno", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro interno"})
		return
	}

	// Publish event so KYCWorker can pick it up immediately (async, non-blocking)
	if s.workers != nil {
		s.workers.Bus.Publish(workerEvent("kyc.submitted", map[string]any{
			"kyc_request_id":     kyc.ID,
			"user_id":            uid,
			"level":              req.Level,
			"ip":                 clientIP(r),
			"user_agent":         r.UserAgent(),
			"device_fingerprint": req.DeviceFingerprint,
		}))
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"kyc_request":     kyc,
		"non_blocking":    true,
		"message":         "KYC em análise. Você pode continuar operando com os limites atuais.",
		"current_limit":   models.KYCDailyLimits[models.KYCLevel(req.Level-1)],
		"potential_limit": models.KYCDailyLimits[models.KYCLevel(req.Level)],
	})
}

// handleKYCStatusV2 — GET /api/mobile/kyc/status
// Returns the user's current approved KYC level, latest request status, and daily limits.
// The two independent DB queries run in parallel to halve latency.
func (s *Server) handleKYCStatusV2(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	db := mobileDB(s.db)

	type levelResult struct {
		level models.KYCLevel
		err   error
	}
	type latestResult struct {
		req *models.KYCRequest
		err error
	}

	levelCh := make(chan levelResult, 1)
	latestCh := make(chan latestResult, 1)

	go func() {
		level, err := db.GetApprovedKYCLevel(r.Context(), uid)
		levelCh <- levelResult{level, err}
	}()
	go func() {
		req, err := db.GetLatestKYCByUser(r.Context(), uid)
		latestCh <- latestResult{req, err}
	}()

	lr := <-levelCh
	if lr.err != nil {
		slog.Error("erro interno", "err", lr.err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro interno"})
		return
	}
	la := <-latestCh

	dailyLimit := models.KYCDailyLimits[lr.level]

	writeJSON(w, http.StatusOK, map[string]any{
		"approved_level":  int(lr.level),
		"daily_limit_brl": dailyLimit,
		"latest_request":  la.req,
		"levels": []map[string]any{
			{"level": 0, "label": "Sem KYC", "daily_limit": models.KYCDailyLimits[0], "requirements": []string{"email", "telefone"}},
			{"level": 1, "label": "KYC Básico", "daily_limit": models.KYCDailyLimits[1], "requirements": []string{"documento", "selfie"}},
			{"level": 2, "label": "KYC Completo", "daily_limit": models.KYCDailyLimits[2], "requirements": []string{"comprovante_residência", "prova_renda"}},
			{"level": 3, "label": "KYC Premium", "daily_limit": models.KYCDailyLimits[3], "requirements": []string{"análise_manual", "entrevista"}},
		},
	})
}

// handleKYCHistory — GET /api/mobile/kyc/history
func (s *Server) handleKYCHistory(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	list, err := mobileDB(s.db).ListKYCByUser(r.Context(), uid, 20)
	if err != nil {
		slog.Error("erro interno", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro interno"})
		return
	}
	if list == nil {
		list = []models.KYCRequest{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": list, "count": len(list)})
}

// handleKYCLimits — GET /api/mobile/kyc/limits
// Public — returns the KYC level table so the app can display upgrade prompts.
func (s *Server) handleKYCLimits(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", mobileStaticCacheControl)
	type level struct {
		Level      string  `json:"level"`
		Label      string  `json:"label"`
		DailyLimit float64 `json:"daily_limit_brl"`
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"levels": []level{
			{"0", "Sem KYC", models.KYCDailyLimits[0]},
			{"1", "KYC Básico", models.KYCDailyLimits[1]},
			{"2", "KYC Completo", models.KYCDailyLimits[2]},
			{"3", "KYC Premium", models.KYCDailyLimits[3]},
		},
	})
}
