-- Custodial mobile wallet transfer audit log.
-- The encrypted signing key remains in mobile_wallet_keys; this table stores only public tx metadata.

CREATE TABLE IF NOT EXISTS mobile_wallet_transfers (
  id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id         UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  from_address    TEXT        NOT NULL,
  to_address      TEXT        NOT NULL,
  token_contract  TEXT        NOT NULL,
  asset           TEXT        NOT NULL,
  network         TEXT        NOT NULL,
  amount          TEXT        NOT NULL,
  amount_raw      TEXT        NOT NULL,
  tx_hash         TEXT        NOT NULL UNIQUE,
  idempotency_key TEXT,
  status          TEXT        NOT NULL DEFAULT 'submitted',
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_mobile_wallet_transfers_user_created
  ON mobile_wallet_transfers (user_id, created_at DESC);

CREATE UNIQUE INDEX IF NOT EXISTS uq_mobile_wallet_transfers_user_idempotency
  ON mobile_wallet_transfers (user_id, idempotency_key)
  WHERE idempotency_key IS NOT NULL;
