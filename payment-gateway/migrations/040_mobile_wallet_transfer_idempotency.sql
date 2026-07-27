-- Ensure mobile wallet transfers are idempotent per user before broadcast.
CREATE UNIQUE INDEX IF NOT EXISTS uq_mobile_wallet_transfers_user_idempotency
  ON mobile_wallet_transfers (user_id, idempotency_key)
  WHERE idempotency_key IS NOT NULL;

