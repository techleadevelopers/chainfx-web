-- Guardrails for real mobile swap execution.
-- Stores signed quote parameters and prevents one quote from broadcasting twice.

ALTER TABLE swaps ADD COLUMN IF NOT EXISTS quote_id TEXT;
ALTER TABLE swaps ADD COLUMN IF NOT EXISTS provider TEXT;
ALTER TABLE swaps ADD COLUMN IF NOT EXISTS network TEXT;
ALTER TABLE swaps ADD COLUMN IF NOT EXISTS router TEXT;
ALTER TABLE swaps ADD COLUMN IF NOT EXISTS path TEXT[];
ALTER TABLE swaps ADD COLUMN IF NOT EXISTS amount_in_raw TEXT;
ALTER TABLE swaps ADD COLUMN IF NOT EXISTS expected_out_raw TEXT;
ALTER TABLE swaps ADD COLUMN IF NOT EXISTS min_received_raw TEXT;
ALTER TABLE swaps ADD COLUMN IF NOT EXISTS slippage_bps INT;
ALTER TABLE swaps ADD COLUMN IF NOT EXISTS quote_expires_at TIMESTAMPTZ;
ALTER TABLE swaps ADD COLUMN IF NOT EXISTS execution_id TEXT;
ALTER TABLE swaps ADD COLUMN IF NOT EXISTS broadcast_at TIMESTAMPTZ;
ALTER TABLE swaps ADD COLUMN IF NOT EXISTS confirmed_at TIMESTAMPTZ;

CREATE UNIQUE INDEX IF NOT EXISTS idx_swaps_quote_id_unique
  ON swaps(quote_id)
  WHERE quote_id IS NOT NULL AND quote_id <> '';

CREATE UNIQUE INDEX IF NOT EXISTS idx_swaps_execution_id_unique
  ON swaps(execution_id)
  WHERE execution_id IS NOT NULL AND execution_id <> '';

CREATE INDEX IF NOT EXISTS idx_swaps_recovery_status
  ON swaps(status, updated_at)
  WHERE status IN ('execution_requested','signing','broadcast','confirming');
