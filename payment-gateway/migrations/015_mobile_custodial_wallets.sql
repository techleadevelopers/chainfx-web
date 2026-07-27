-- Provision one system-owned EVM wallet per mobile user.
-- Safe to re-run.

CREATE TABLE IF NOT EXISTS mobile_wallet_keys (
  user_id               UUID        PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  wallet_address        TEXT        NOT NULL UNIQUE,
  encrypted_private_key TEXT        NOT NULL,
  custody_mode          TEXT        NOT NULL DEFAULT 'system_custody',
  network               TEXT        NOT NULL DEFAULT 'EVM',
  created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_users_wallet_unique
  ON users (lower(wallet_address))
  WHERE wallet_address IS NOT NULL AND wallet_address <> '';

CREATE UNIQUE INDEX IF NOT EXISTS idx_mobile_wallet_keys_address
  ON mobile_wallet_keys (lower(wallet_address));
