-- NFC BRL liquidity snapshots and reservations.
-- Payout remains manual by default; these tables make treasury solvency
-- observable and allow an opt-in liquidity gate without calling Efí on taps.

CREATE TABLE IF NOT EXISTS nfc_treasury_snapshots (
  id BIGSERIAL PRIMARY KEY,
  provider TEXT NOT NULL DEFAULT 'efi',
  available_brl_minor BIGINT NOT NULL DEFAULT 0,
  reserved_brl_minor BIGINT NOT NULL DEFAULT 0,
  projected_outflow_brl_minor BIGINT NOT NULL DEFAULT 0,
  minimum_buffer_brl_minor BIGINT NOT NULL DEFAULT 0,
  effective_available_brl_minor BIGINT NOT NULL DEFAULT 0,
  observed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  source TEXT NOT NULL DEFAULT 'manual_config',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_nfc_treasury_snapshots_provider_observed
  ON nfc_treasury_snapshots(provider, observed_at DESC);

CREATE TABLE IF NOT EXISTS nfc_brl_reservations (
  id TEXT PRIMARY KEY,
  authorization_id TEXT NOT NULL UNIQUE REFERENCES nfc_authorizations(id),
  merchant_id TEXT NOT NULL,
  terminal_id TEXT NOT NULL,
  amount_brl_minor BIGINT NOT NULL CHECK (amount_brl_minor > 0),
  status TEXT NOT NULL CHECK (status IN ('ACTIVE','CONSUMED','RELEASED')),
  source_snapshot_id BIGINT REFERENCES nfc_treasury_snapshots(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  consumed_at TIMESTAMPTZ,
  released_at TIMESTAMPTZ,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_nfc_brl_reservations_status_created
  ON nfc_brl_reservations(status, created_at);

CREATE INDEX IF NOT EXISTS idx_nfc_brl_reservations_merchant_created
  ON nfc_brl_reservations(merchant_id, created_at DESC);
