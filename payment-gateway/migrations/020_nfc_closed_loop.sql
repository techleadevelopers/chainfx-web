-- Closed-loop NFC rail.
-- This is not an EMV card-network issuer integration. It stores opaque HCE
-- tokens for ChainFX-owned readers/terminals and records authorization holds.

CREATE TABLE IF NOT EXISTS nfc_tokens (
  token_id TEXT PRIMARY KEY,
  token_hash TEXT NOT NULL UNIQUE,
  wallet_address TEXT NOT NULL,
  device_id TEXT,
  network TEXT NOT NULL DEFAULT 'BSC',
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','revoked','expired')),
  expires_at TIMESTAMPTZ NOT NULL,
  last_used_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_nfc_tokens_wallet ON nfc_tokens(LOWER(wallet_address), created_at DESC);
CREATE INDEX IF NOT EXISTS idx_nfc_tokens_expires ON nfc_tokens(expires_at);

CREATE TABLE IF NOT EXISTS nfc_wallet_balances (
  wallet_address TEXT NOT NULL,
  network TEXT NOT NULL DEFAULT 'BSC',
  asset TEXT NOT NULL DEFAULT 'USDT',
  available_usdt_micro BIGINT NOT NULL DEFAULT 0 CHECK (available_usdt_micro >= 0),
  locked_usdt_micro BIGINT NOT NULL DEFAULT 0 CHECK (locked_usdt_micro >= 0),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (wallet_address, network, asset)
);

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

CREATE TABLE IF NOT EXISTS nfc_authorizations (
  id TEXT PRIMARY KEY,
  idempotency_key TEXT NOT NULL,
  token_id TEXT NOT NULL,
  token_hash TEXT NOT NULL,
  wallet_address TEXT NOT NULL,
  network TEXT NOT NULL DEFAULT 'BSC',
  merchant_id TEXT NOT NULL,
  terminal_id TEXT NOT NULL,
  external_ref TEXT,
  amount_brl_minor BIGINT NOT NULL CHECK (amount_brl_minor > 0),
  fee_brl_minor BIGINT NOT NULL DEFAULT 0 CHECK (fee_brl_minor >= 0),
  total_brl_minor BIGINT NOT NULL DEFAULT 0 CHECK (total_brl_minor >= 0),
  fee_bps INT NOT NULL DEFAULT 0 CHECK (fee_bps >= 0),
  usdt_rate NUMERIC(20,8) NOT NULL,
  required_usdt_micro BIGINT NOT NULL CHECK (required_usdt_micro > 0),
  status TEXT NOT NULL CHECK (status IN ('approved','declined','requires_funding','reversed','captured','expired')),
  response_code TEXT NOT NULL,
  reason TEXT,
  hold_expires_at TIMESTAMPTZ,
  reversed_at TIMESTAMPTZ,
  captured_at TIMESTAMPTZ,
  expired_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_nfc_auth_terminal_idempotency ON nfc_authorizations(terminal_id, idempotency_key);
CREATE INDEX IF NOT EXISTS idx_nfc_auth_wallet_created ON nfc_authorizations(LOWER(wallet_address), created_at DESC);
CREATE INDEX IF NOT EXISTS idx_nfc_auth_merchant_created ON nfc_authorizations(merchant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_nfc_auth_status_hold ON nfc_authorizations(status, hold_expires_at) WHERE status = 'approved';
