-- ChainFX Tap terminal registry and terminal-scoped idempotency.

CREATE TABLE IF NOT EXISTS nfc_merchants (
  id TEXT PRIMARY KEY,
  display_name TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
  settlement_pix_key TEXT,
  settlement_document TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS nfc_terminals (
  id TEXT NOT NULL,
  merchant_id TEXT NOT NULL REFERENCES nfc_merchants(id),
  api_key_hash TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
  max_amount_brl_minor BIGINT NOT NULL DEFAULT 0,
  daily_limit_brl_minor BIGINT NOT NULL DEFAULT 0,
  risk_policy_version TEXT NOT NULL DEFAULT 'default',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (merchant_id, id)
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_nfc_terminal_api_key_hash ON nfc_terminals(api_key_hash);
CREATE INDEX IF NOT EXISTS idx_nfc_terminals_status ON nfc_terminals(status, merchant_id);

ALTER TABLE nfc_authorizations DROP CONSTRAINT IF EXISTS nfc_authorizations_status_check;
ALTER TABLE nfc_authorizations ADD CONSTRAINT nfc_authorizations_status_check
  CHECK (status IN ('approved','declined','requires_funding','reversed','captured','expired'));
ALTER TABLE nfc_authorizations ADD COLUMN IF NOT EXISTS expired_at TIMESTAMPTZ;
ALTER TABLE nfc_authorizations ADD COLUMN IF NOT EXISTS fee_brl_minor BIGINT NOT NULL DEFAULT 0;
ALTER TABLE nfc_authorizations ADD COLUMN IF NOT EXISTS total_brl_minor BIGINT NOT NULL DEFAULT 0;
ALTER TABLE nfc_authorizations ADD COLUMN IF NOT EXISTS fee_bps INT NOT NULL DEFAULT 0;
UPDATE nfc_authorizations SET total_brl_minor = amount_brl_minor WHERE total_brl_minor = 0;
ALTER TABLE nfc_authorizations DROP CONSTRAINT IF EXISTS nfc_authorizations_idempotency_key_key;
CREATE UNIQUE INDEX IF NOT EXISTS uq_nfc_auth_terminal_idempotency ON nfc_authorizations(terminal_id, idempotency_key);
