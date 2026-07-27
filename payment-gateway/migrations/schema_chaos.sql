-- Admin Adversarial / Chaos Audit log
-- Apply: psql $DATABASE_URL -f schema_chaos.sql

CREATE TABLE IF NOT EXISTS admin_adversarial_runs (
    id               SERIAL PRIMARY KEY,
    triggered_by     VARCHAR(255) NOT NULL,
    status           VARCHAR(50)  NOT NULL DEFAULT 'RUNNING',
    scenarios_executed INT        NOT NULL DEFAULT 0,
    failures_detected  INT        NOT NULL DEFAULT 0,
    logs             TEXT,
    created_at       TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_adversarial_runs_created
    ON admin_adversarial_runs (created_at DESC);
