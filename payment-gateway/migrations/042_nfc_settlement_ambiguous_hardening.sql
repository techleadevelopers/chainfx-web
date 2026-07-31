-- Harden ChainFX Tap merchant settlement after ambiguous Efí Pix Send submits.
-- The provider_id_envio is the deterministic reconciliation key and must not be
-- replaced by retries after a possible submit.

ALTER TABLE merchant_settlements
  ADD COLUMN IF NOT EXISTS submit_started_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS submit_completed_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS submit_outcome TEXT NOT NULL DEFAULT 'not_submitted',
  ADD COLUMN IF NOT EXISTS reconciliation_attempt_count INT NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS first_ambiguous_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS last_reconciled_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS consecutive_not_found INT NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS manual_review_reason TEXT;

ALTER TABLE merchant_settlements DROP CONSTRAINT IF EXISTS merchant_settlements_submit_outcome_check;
ALTER TABLE merchant_settlements ADD CONSTRAINT merchant_settlements_submit_outcome_check
  CHECK (submit_outcome IN ('not_submitted','started','confirmed','rejected','ambiguous'));

CREATE INDEX IF NOT EXISTS idx_merchant_settlements_processing_stale
  ON merchant_settlements(status, submit_started_at, next_retry_at)
  WHERE status = 'PROCESSING';
