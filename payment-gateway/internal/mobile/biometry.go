package mobile

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	kycengine "payment-gateway/internal/mobile/kyc_engine"
)

func (s *Server) handleKYCAnalysisStatus(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	if err := ensureMobileKYCEngineSchema(r.Context(), s.db.SQL); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "schema kyc engine indisponivel"})
		return
	}

	var payload sql.NullString
	var latency sql.NullInt64
	var decision sql.NullString
	var score sql.NullInt64
	err := s.db.SQL.QueryRowContext(r.Context(), `
		SELECT details::text, latency_ms, decision, score
		  FROM kyc_analysis_results
		 WHERE user_id=$1::uuid
		 ORDER BY created_at DESC
		 LIMIT 1`, uid).Scan(&payload, &latency, &decision, &score)
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	var details any
	_ = json.Unmarshal([]byte(payload.String), &details)
	writeJSON(w, http.StatusOK, map[string]any{
		"available":  true,
		"decision":   decision.String,
		"score":      score.Int64,
		"latency_ms": latency.Int64,
		"analysis":   details,
	})
}

func (s *Server) handleBiometryVerify(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	var req struct {
		SelfieURL         string `json:"selfie_url"`
		FacialVideoURL    string `json:"facial_video_url"`
		Action            string `json:"action"`
		DeviceFingerprint string `json:"device_fingerprint"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "payload invalido"})
		return
	}
	if strings.TrimSpace(req.SelfieURL) == "" && strings.TrimSpace(req.FacialVideoURL) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "selfie_url ou facial_video_url obrigatorio"})
		return
	}
	if err := ensureMobileKYCEngineSchema(r.Context(), s.db.SQL); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "schema kyc engine indisponivel"})
		return
	}

	var encrypted string
	err := s.db.SQL.QueryRowContext(r.Context(), `
		SELECT face_embedding_encrypted
		  FROM user_face_biometrics
		 WHERE user_id=$1::uuid AND deleted_at IS NULL`, uid).Scan(&encrypted)
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusPreconditionRequired, map[string]any{"error": "biometria facial ainda nao cadastrada"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	start := time.Now()
	secret := s.faceBiometrySecret()
	stored, err := kycengine.DecryptEmbedding(secret, encrypted)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "falha ao descriptografar biometria"})
		return
	}
	engine := kycengine.New(secret)
	probe := engine.Analyze(r.Context(), kycengine.Input{
		UserID:            uid,
		SelfieURL:         req.SelfieURL,
		FacialVideoURL:    req.FacialVideoURL,
		DeviceFingerprint: req.DeviceFingerprint,
		IPAddress:         clientIP(r),
		UserAgent:         r.UserAgent(),
	})
	similarity := kycengine.SimilarityPercent(stored, probe.Embedding)
	ok := similarity >= 84 && probe.ReplayRiskScore < 70 && probe.LivenessScore >= 70
	latency := time.Since(start).Milliseconds()

	metadata, _ := json.Marshal(map[string]any{
		"action":            req.Action,
		"similarity":        similarity,
		"liveness_score":    probe.LivenessScore,
		"replay_risk_score": probe.ReplayRiskScore,
		"ok":                ok,
	})
	_, _ = s.db.SQL.ExecContext(r.Context(), `
		INSERT INTO kyc_risk_events (user_id,event_type,risk_score,request_ip,device_fingerprint,metadata)
		VALUES ($1::uuid,$2,$3,$4,$5,$6::jsonb)`,
		uid, "biometry.verify."+firstNonEmpty(req.Action, "generic"), 100-similarity, clientIP(r), req.DeviceFingerprint, string(metadata))

	status := http.StatusOK
	if !ok {
		status = http.StatusForbidden
	}
	writeJSON(w, status, map[string]any{
		"ok":                ok,
		"similarity":        similarity,
		"liveness_score":    probe.LivenessScore,
		"replay_risk_score": probe.ReplayRiskScore,
		"latency_ms":        latency,
		"decision":          map[bool]string{true: "approved", false: "manual_review"}[ok],
	})
}

func (s *Server) handleKYCEngineMetrics(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	if err := ensureMobileKYCEngineSchema(r.Context(), s.db.SQL); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "schema kyc engine indisponivel"})
		return
	}

	rows, err := s.db.SQL.QueryContext(r.Context(), `
		SELECT decision, score, latency_ms, created_at
		  FROM kyc_analysis_results
		 WHERE user_id=$1::uuid
		 ORDER BY created_at DESC
		 LIMIT 50`, uid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	defer rows.Close()

	type sample struct {
		Decision  string    `json:"decision"`
		Score     int       `json:"score"`
		LatencyMS int       `json:"latency_ms"`
		CreatedAt time.Time `json:"created_at"`
	}
	samples := []sample{}
	for rows.Next() {
		var s sample
		if err := rows.Scan(&s.Decision, &s.Score, &s.LatencyMS, &s.CreatedAt); err == nil {
			samples = append(samples, s)
		}
	}

	totalLatency := 0
	maxLatency := 0
	latencies := make([]int, 0, len(samples))
	for _, item := range samples {
		totalLatency += item.LatencyMS
		if item.LatencyMS > maxLatency {
			maxLatency = item.LatencyMS
		}
		latencies = append(latencies, item.LatencyMS)
	}
	avgLatency := 0
	if len(samples) > 0 {
		avgLatency = totalLatency / len(samples)
	}
	var latest any
	if len(samples) > 0 {
		latest = samples[0]
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"count":          len(samples),
		"avg_latency_ms": avgLatency,
		"p95_latency_ms": percentile(latencies, 95),
		"max_latency_ms": maxLatency,
		"latest":         latest,
		"samples":        samples,
	})
}

func (s *Server) faceBiometrySecret() string {
	if secret := strings.TrimSpace(os.Getenv("FACE_BIOMETRY_SECRET")); secret != "" {
		return secret
	}
	if s != nil && s.cfg != nil {
		if secret := strings.TrimSpace(s.cfg.LGPDSecret); secret != "" {
			return secret
		}
		if secret := strings.TrimSpace(s.cfg.WebhookSecret); secret != "" {
			return secret
		}
	}
	if s != nil && s.mcfg != nil {
		return strings.TrimSpace(s.mcfg.JWTSecret)
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func percentile(values []int, p int) int {
	if len(values) == 0 {
		return 0
	}
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
	idx := (len(values)*p + 99) / 100
	if idx <= 0 {
		idx = 1
	}
	if idx > len(values) {
		idx = len(values)
	}
	return values[idx-1]
}

func clientIP(r *http.Request) string {
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); forwarded != "" {
		return strings.TrimSpace(strings.Split(forwarded, ",")[0])
	}
	return strings.TrimSpace(r.RemoteAddr)
}

func ensureMobileKYCEngineSchema(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS user_face_biometrics (
  user_id UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  latest_kyc_request_id UUID REFERENCES kyc_requests(id) ON DELETE SET NULL,
  face_embedding_encrypted TEXT NOT NULL,
  embedding_hash TEXT NOT NULL,
  model_version TEXT NOT NULL,
  consent_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  deleted_at TIMESTAMPTZ
);
CREATE TABLE IF NOT EXISTS kyc_analysis_results (
  kyc_request_id UUID PRIMARY KEY REFERENCES kyc_requests(id) ON DELETE CASCADE,
  user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  provider TEXT NOT NULL,
  model_version TEXT NOT NULL,
  decision TEXT NOT NULL,
  score INT NOT NULL,
  document_score INT NOT NULL,
  face_match_score INT NOT NULL,
  liveness_score INT NOT NULL,
  replay_risk_score INT NOT NULL,
  duplicate_score INT NOT NULL,
  risk_score INT NOT NULL,
  latency_ms INT NOT NULL,
  embedding_hash TEXT,
  flags TEXT,
  details JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS kyc_risk_events (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id UUID REFERENCES users(id) ON DELETE SET NULL,
  event_type TEXT NOT NULL,
  risk_score INT NOT NULL,
  request_ip TEXT,
  device_fingerprint TEXT,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);`)
	return err
}
