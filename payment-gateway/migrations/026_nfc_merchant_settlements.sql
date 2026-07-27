-- Durable merchant settlement queue for ChainFX Tap captures.
-- Authorize and capture stay internal; this table drives asynchronous Efí Pix
-- Send payouts after the customer has already left the POS.

CREATE TABLE IF NOT EXISTS merchant_settlements (
  id TEXT PRIMARY KEY,
  merchant_id TEXT NOT NULL REFERENCES nfc_merchants(id),
  terminal_id TEXT NOT NULL,
  authorization_id TEXT NOT NULL UNIQUE REFERENCES nfc_authorizations(id),
  capture_id TEXT NOT NULL,
  amount_brl_minor BIGINT NOT NULL CHECK (amount_brl_minor > 0),
  fee_brl_minor BIGINT NOT NULL DEFAULT 0 CHECK (fee_brl_minor >= 0),
  provider TEXT NOT NULL DEFAULT 'efi',
  rail TEXT NOT NULL DEFAULT 'pix_send',
  status TEXT NOT NULL CHECK (status IN ('MANUAL_REQUIRED','PENDING','PROCESSING','SUBMITTED','SUBMISSION_UNKNOWN','CONFIRMED','REJECTED','RETRYABLE','MANUAL_REVIEW','CANCELED')),
  provider_reference TEXT,
  provider_e2e_id TEXT,
  provider_id_envio TEXT,
  provider_status TEXT,
  txid TEXT,
  idempotency_key TEXT NOT NULL UNIQUE,
  target_pix_key TEXT,
  target_document TEXT,
  retry_count INT NOT NULL DEFAULT 0,
  next_retry_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  claimed_at TIMESTAMPTZ,
  claimed_by TEXT,
  error_message TEXT,
  submitted_at TIMESTAMPTZ,
  confirmed_at TIMESTAMPTZ,
  failed_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_merchant_settlements_status_retry
  ON merchant_settlements(status, next_retry_at, created_at)
  WHERE status IN ('PENDING','RETRYABLE','SUBMITTED','SUBMISSION_UNKNOWN');

CREATE INDEX IF NOT EXISTS idx_merchant_settlements_merchant_created
  ON merchant_settlements(merchant_id, created_at DESC);

CREATE TABLE IF NOT EXISTS merchant_settlement_provider_events (
  id BIGSERIAL PRIMARY KEY,
  settlement_id TEXT NOT NULL REFERENCES merchant_settlements(id),
  provider TEXT NOT NULL,
  id_envio TEXT NOT NULL DEFAULT '',
  e2e_id TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  payload JSONB,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (provider, id_envio, e2e_id, status)
);

CREATE INDEX IF NOT EXISTS idx_merchant_settlement_provider_events_settlement
  ON merchant_settlement_provider_events(settlement_id, created_at DESC);

CREATE TABLE IF NOT EXISTS nfc_settlement_reconciliation_anomalies (
  id BIGSERIAL PRIMARY KEY,
  anomaly_key TEXT NOT NULL UNIQUE,
  anomaly_type TEXT NOT NULL,
  severity TEXT NOT NULL,
  authorization_id TEXT,
  settlement_id TEXT,
  merchant_id TEXT,
  details JSONB,
  status TEXT NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN','RESOLVED')),
  first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  resolved_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_nfc_settlement_anomalies_status
  ON nfc_settlement_reconciliation_anomalies(status, last_seen_at DESC);

ALTER TABLE merchant_settlements ADD COLUMN IF NOT EXISTS provider_e2e_id TEXT;
ALTER TABLE merchant_settlements ADD COLUMN IF NOT EXISTS provider_id_envio TEXT;
ALTER TABLE merchant_settlements ADD COLUMN IF NOT EXISTS claimed_at TIMESTAMPTZ;
ALTER TABLE merchant_settlements ADD COLUMN IF NOT EXISTS claimed_by TEXT;

ALTER TABLE merchant_settlements DROP CONSTRAINT IF EXISTS merchant_settlements_status_check;
ALTER TABLE merchant_settlements ADD CONSTRAINT merchant_settlements_status_check
  CHECK (status IN ('MANUAL_REQUIRED','PENDING','PROCESSING','SUBMITTED','SUBMISSION_UNKNOWN','CONFIRMED','REJECTED','RETRYABLE','MANUAL_REVIEW','CANCELED'));
