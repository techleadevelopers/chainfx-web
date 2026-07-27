package workers

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"time"

	"payment-gateway/internal/config"
	"payment-gateway/internal/database"
	kycengine "payment-gateway/internal/mobile/kyc_engine"
)

type KYCWorker struct {
	bus *EventBus
	db  *database.DB
	cfg *config.Config
}

func NewKYCWorker(bus *EventBus, db *database.DB, cfg *config.Config) *KYCWorker {
	return &KYCWorker{bus: bus, db: db, cfg: cfg}
}

func (w *KYCWorker) Start(ctx context.Context) {
	slog.Info("KYCWorker iniciado")
	if w.bus == nil || w.db == nil || w.db.SQL == nil {
		slog.Warn("KYCWorker sem dependencias, encerrando")
		return
	}
	if err := w.ensureSchema(ctx); err != nil {
		slog.Error("KYCWorker: schema indisponivel", "error", err)
		return
	}

	kycCh := w.bus.Subscribe("kyc.submitted")
	defer w.bus.Unsubscribe("kyc.submitted", kycCh)

	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("KYCWorker encerrado")
			return
		case ev, ok := <-kycCh:
			if !ok {
				return
			}
			if id, ok := ev.Payload["kyc_request_id"].(string); ok {
				w.processRequest(ctx, id, ev.Payload)
			}
		case <-ticker.C:
			w.processPending(ctx)
		}
	}
}

func (w *KYCWorker) processRequest(ctx context.Context, id string, metadata map[string]any) {
	if _, err := w.db.SQL.ExecContext(ctx, `
		UPDATE kyc_requests SET status='pending_processing', updated_at=NOW()
		WHERE id=$1 AND status IN ('pending','pending_processing')`, id); err != nil {
		slog.Error("KYCWorker: erro ao marcar pending_processing", "id", id, "error", err)
		return
	}

	req, err := w.loadRequest(ctx, id)
	if err != nil {
		slog.Error("KYCWorker: erro ao buscar request", "id", id, "error", err)
		return
	}
	req.IPAddress = stringFromAny(metadata["ip"])
	req.UserAgent = stringFromAny(metadata["user_agent"])
	req.DeviceFingerprint = stringFromAny(metadata["device_fingerprint"])

	secret := w.biometrySecret()
	engine := kycengine.NewFromEnv(secret)
	result := engine.Analyze(ctx, req)

	duplicateCount, err := w.countDuplicateFaces(ctx, req.UserID, result.EmbeddingHash)
	if err != nil {
		slog.Warn("KYCWorker: erro ao checar duplicidade facial", "request_id", id, "error", err)
	}
	if duplicateCount > 0 {
		result.Flags = append(result.Flags, "same_face_seen_on_other_account")
		result.DuplicateScore = 0
		result.Score = minInt(result.Score, 74)
		if result.Decision == "approved" {
			result.Decision = "manual_review"
		}
		result.Details["duplicate_face_accounts"] = duplicateCount
	}

	if err := w.saveAnalysis(ctx, result); err != nil {
		slog.Error("KYCWorker: erro ao salvar analise", "request_id", id, "error", err)
		return
	}

	newStatus := result.Decision
	reviewedAt := sql.NullTime{Time: time.Now(), Valid: newStatus == "approved" || newStatus == "rejected"}
	if _, err := w.db.SQL.ExecContext(ctx, `
		UPDATE kyc_requests
		   SET status=$1, reviewer_notes=$2, reviewed_at=$3, updated_at=NOW()
		 WHERE id=$4`,
		newStatus, kycengine.EncodeDetails(result), reviewedAt, id); err != nil {
		slog.Error("KYCWorker: erro ao atualizar status", "id", id, "error", err)
		return
	}

	switch newStatus {
	case "approved":
		if err := w.saveBiometricProfile(ctx, req.UserID, id, result); err != nil {
			slog.Error("KYCWorker: erro ao salvar biometria", "user_id", req.UserID, "error", err)
			return
		}
		_, _ = w.db.SQL.ExecContext(ctx, "UPDATE users SET kyc_status='approved', updated_at=NOW() WHERE id=$1", req.UserID)
		w.bus.Publish(Event{Type: "kyc.approved", Payload: map[string]any{"user_id": req.UserID, "level": req.Level, "request_id": id, "score": result.Score}})
	case "rejected":
		_, _ = w.db.SQL.ExecContext(ctx, "UPDATE users SET kyc_status='rejected', updated_at=NOW() WHERE id=$1", req.UserID)
		w.bus.Publish(Event{Type: "kyc.rejected", Payload: map[string]any{"user_id": req.UserID, "level": req.Level, "request_id": id, "score": result.Score}})
	default:
		_, _ = w.db.SQL.ExecContext(ctx, "UPDATE users SET kyc_status='submitted', updated_at=NOW() WHERE id=$1", req.UserID)
		w.bus.Publish(Event{Type: "kyc.manual_review", Payload: map[string]any{"user_id": req.UserID, "level": req.Level, "request_id": id, "score": result.Score}})
	}

	slog.Info("KYCWorker: analise concluida", "request_id", id, "user_id", req.UserID, "decision", result.Decision, "score", result.Score, "latency_ms", result.LatencyMS)
}

func (w *KYCWorker) processPending(ctx context.Context) {
	rows, err := w.db.SQL.QueryContext(ctx, `
		SELECT id FROM kyc_requests
		WHERE status IN ('pending','pending_processing') AND submitted_at < NOW() - INTERVAL '30 seconds'
		ORDER BY submitted_at ASC
		LIMIT 10`)
	if err != nil {
		if err != sql.ErrNoRows {
			slog.Warn("KYCWorker: erro no poll", "error", err)
		}
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			w.processRequest(ctx, id, nil)
		}
	}
}

func (w *KYCWorker) loadRequest(ctx context.Context, id string) (kycengine.Input, error) {
	var in kycengine.Input
	var docURL, docBackURL, selfieURL, facialVideoURL sql.NullString
	err := w.db.SQL.QueryRowContext(ctx, `
		SELECT id,user_id,level,document_url,document_back_url,selfie_url,facial_video_url
		  FROM kyc_requests WHERE id=$1`, id).Scan(
		&in.RequestID, &in.UserID, &in.Level, &docURL, &docBackURL, &selfieURL, &facialVideoURL)
	in.DocumentURL = docURL.String
	in.DocumentBackURL = docBackURL.String
	in.SelfieURL = selfieURL.String
	in.FacialVideoURL = facialVideoURL.String
	return in, err
}

func (w *KYCWorker) saveAnalysis(ctx context.Context, r kycengine.Result) error {
	details, _ := json.Marshal(r)
	_, err := w.db.SQL.ExecContext(ctx, `
		INSERT INTO kyc_analysis_results
		  (kyc_request_id,user_id,provider,model_version,decision,score,document_score,face_match_score,
		   liveness_score,replay_risk_score,duplicate_score,risk_score,latency_ms,embedding_hash,flags,details)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16::jsonb)
		ON CONFLICT (kyc_request_id) DO UPDATE SET
		   provider=$3, model_version=$4, decision=$5, score=$6, document_score=$7,
		   face_match_score=$8, liveness_score=$9, replay_risk_score=$10, duplicate_score=$11,
		   risk_score=$12, latency_ms=$13, embedding_hash=$14, flags=$15, details=$16::jsonb, created_at=NOW()`,
		r.RequestID, r.UserID, r.Provider, r.ModelVersion, r.Decision, r.Score, r.DocumentScore, r.FaceMatchScore,
		r.LivenessScore, r.ReplayRiskScore, r.DuplicateScore, r.RiskScore, r.LatencyMS, r.EmbeddingHash, strings.Join(r.Flags, ","), string(details))
	return err
}

func (w *KYCWorker) saveBiometricProfile(ctx context.Context, userID, requestID string, r kycengine.Result) error {
	encrypted, err := kycengine.EncryptEmbedding(w.biometrySecret(), r.Embedding)
	if err != nil {
		return err
	}
	_, err = w.db.SQL.ExecContext(ctx, `
		INSERT INTO user_face_biometrics
		  (user_id, latest_kyc_request_id, face_embedding_encrypted, embedding_hash, model_version, consent_at)
		VALUES ($1,$2,$3,$4,$5,NOW())
		ON CONFLICT (user_id) DO UPDATE SET
		  latest_kyc_request_id=$2,
		  face_embedding_encrypted=$3,
		  embedding_hash=$4,
		  model_version=$5,
		  updated_at=NOW(),
		  deleted_at=NULL`,
		userID, requestID, encrypted, r.EmbeddingHash, r.ModelVersion)
	return err
}

func (w *KYCWorker) countDuplicateFaces(ctx context.Context, userID, embeddingHash string) (int, error) {
	if embeddingHash == "" {
		return 0, nil
	}
	var count int
	err := w.db.SQL.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM user_face_biometrics
		 WHERE embedding_hash=$1 AND user_id<>$2::uuid AND deleted_at IS NULL`,
		embeddingHash, userID).Scan(&count)
	return count, err
}

func (w *KYCWorker) ensureSchema(ctx context.Context) error {
	_, err := w.db.SQL.ExecContext(ctx, kycEngineSchemaSQL)
	return err
}

func (w *KYCWorker) biometrySecret() string {
	if secret := strings.TrimSpace(os.Getenv("FACE_BIOMETRY_SECRET")); secret != "" {
		return secret
	}
	if w.cfg != nil {
		if secret := strings.TrimSpace(w.cfg.LGPDSecret); secret != "" {
			return secret
		}
		if secret := strings.TrimSpace(w.cfg.WebhookSecret); secret != "" {
			return secret
		}
	}
	return strings.TrimSpace(os.Getenv("MOBILE_JWT_SECRET"))
}

func stringFromAny(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

const kycEngineSchemaSQL = `
ALTER TABLE kyc_requests
  ADD COLUMN IF NOT EXISTS document_back_url VARCHAR(2048),
  ADD COLUMN IF NOT EXISTS facial_video_url VARCHAR(2048);

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

CREATE INDEX IF NOT EXISTS idx_user_face_biometrics_embedding_hash
  ON user_face_biometrics(embedding_hash)
  WHERE deleted_at IS NULL;

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

CREATE INDEX IF NOT EXISTS idx_kyc_analysis_results_user_created
  ON kyc_analysis_results(user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS kyc_risk_events (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id UUID REFERENCES users(id) ON DELETE SET NULL,
  event_type TEXT NOT NULL,
  risk_score INT NOT NULL,
  request_ip TEXT,
  device_fingerprint TEXT,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_kyc_risk_events_user_created
  ON kyc_risk_events(user_id, created_at DESC);
`
