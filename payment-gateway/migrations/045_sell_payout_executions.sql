CREATE TABLE IF NOT EXISTS sell_payout_executions (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  order_id UUID NOT NULL REFERENCES orders(id),
  provider TEXT NOT NULL,
  provider_idempotency_key TEXT NOT NULL,
  provider_id_envio TEXT NOT NULL,
  amount_brl_minor BIGINT NOT NULL,
  recipient_reference TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN (
    'pending',
    'submit_started',
    'submitted',
    'provider_pending',
    'provider_unknown',
    'completed',
    'failed',
    'manual_review'
  )),
  attempt_count INT NOT NULL DEFAULT 0,
  submit_started_at TIMESTAMPTZ,
  submit_completed_at TIMESTAMPTZ,
  submit_outcome TEXT NOT NULL DEFAULT 'not_submitted' CHECK (submit_outcome IN (
    'not_submitted',
    'started',
    'confirmed',
    'rejected',
    'ambiguous'
  )),
  first_ambiguous_at TIMESTAMPTZ,
  last_reconciled_at TIMESTAMPTZ,
  consecutive_not_found INT NOT NULL DEFAULT 0,
  next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  provider_reference TEXT,
  provider_e2e_id TEXT,
  last_error TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  completed_at TIMESTAMPTZ,
  UNIQUE (order_id),
  UNIQUE (provider_idempotency_key),
  UNIQUE (provider_id_envio)
);

CREATE INDEX IF NOT EXISTS idx_sell_payout_executions_status_retry
  ON sell_payout_executions(status, next_attempt_at, created_at)
  WHERE status IN ('pending','submit_started','submitted','provider_pending','provider_unknown');
