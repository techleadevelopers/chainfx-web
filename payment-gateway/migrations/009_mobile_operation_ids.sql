-- Mobile idempotency table for money-moving app routes.
-- Safe to re-run.

CREATE TABLE IF NOT EXISTS operation_ids (
  operation_id   TEXT        NOT NULL,
  user_id        UUID        NOT NULL,
  operation_type TEXT        NOT NULL,
  status         TEXT        NOT NULL DEFAULT 'pending',
  result_ref     TEXT,
  completed_at   TIMESTAMPTZ,
  expires_at     TIMESTAMPTZ NOT NULL DEFAULT now() + INTERVAL '24 hours',
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (operation_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_operation_ids_expires_at ON operation_ids(expires_at);
CREATE INDEX IF NOT EXISTS idx_operation_ids_status ON operation_ids(status);
