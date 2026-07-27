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
  score INT NOT NULL CHECK (score BETWEEN 0 AND 100),
  document_score INT NOT NULL CHECK (document_score BETWEEN 0 AND 100),
  face_match_score INT NOT NULL CHECK (face_match_score BETWEEN 0 AND 100),
  liveness_score INT NOT NULL CHECK (liveness_score BETWEEN 0 AND 100),
  replay_risk_score INT NOT NULL CHECK (replay_risk_score BETWEEN 0 AND 100),
  duplicate_score INT NOT NULL CHECK (duplicate_score BETWEEN 0 AND 100),
  risk_score INT NOT NULL CHECK (risk_score BETWEEN 0 AND 100),
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
  risk_score INT NOT NULL CHECK (risk_score BETWEEN 0 AND 100),
  request_ip TEXT,
  device_fingerprint TEXT,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_kyc_risk_events_user_created
  ON kyc_risk_events(user_id, created_at DESC);
