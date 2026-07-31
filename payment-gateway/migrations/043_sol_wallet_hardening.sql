-- Migration 043 - Native SOL wallet financial hardening.
-- Scope: SOL-only. Adds idempotent withdrawal claims, signed-tx recovery data,
-- conservative submit state, and receive identity keys.

ALTER TABLE sol_transactions DROP CONSTRAINT IF EXISTS uq_sol_signature;

ALTER TABLE sol_transactions ADD COLUMN IF NOT EXISTS operation_id TEXT NOT NULL DEFAULT '';
ALTER TABLE sol_transactions ADD COLUMN IF NOT EXISTS idempotency_key TEXT NOT NULL DEFAULT '';
ALTER TABLE sol_transactions ADD COLUMN IF NOT EXISTS request_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE sol_transactions ADD COLUMN IF NOT EXISTS source_address TEXT NOT NULL DEFAULT '';
ALTER TABLE sol_transactions ADD COLUMN IF NOT EXISTS destination_address TEXT NOT NULL DEFAULT '';
ALTER TABLE sol_transactions ADD COLUMN IF NOT EXISTS fee_lamports NUMERIC(78,0) NOT NULL DEFAULT 0;
ALTER TABLE sol_transactions ADD COLUMN IF NOT EXISTS reserved_lamports NUMERIC(78,0) NOT NULL DEFAULT 0;
ALTER TABLE sol_transactions ADD COLUMN IF NOT EXISTS signed_raw_tx TEXT NOT NULL DEFAULT '';
ALTER TABLE sol_transactions ADD COLUMN IF NOT EXISTS fee_payer TEXT NOT NULL DEFAULT '';
ALTER TABLE sol_transactions ADD COLUMN IF NOT EXISTS recent_blockhash TEXT NOT NULL DEFAULT '';
ALTER TABLE sol_transactions ADD COLUMN IF NOT EXISTS last_valid_block_height BIGINT NOT NULL DEFAULT 0;
ALTER TABLE sol_transactions ADD COLUMN IF NOT EXISTS receive_key TEXT NOT NULL DEFAULT '';
ALTER TABLE sol_transactions ADD COLUMN IF NOT EXISTS signed_at TIMESTAMPTZ;
ALTER TABLE sol_transactions ADD COLUMN IF NOT EXISTS submitted_at TIMESTAMPTZ;
ALTER TABLE sol_transactions ADD COLUMN IF NOT EXISTS confirmed_at TIMESTAMPTZ;

UPDATE sol_transactions
SET idempotency_key=COALESCE(NULLIF(idempotency_key, ''), metadata_json->>'idempotency_key', ''),
    request_hash=COALESCE(NULLIF(request_hash, ''), metadata_json->>'request_hash', ''),
    source_address=COALESCE(NULLIF(source_address, ''), metadata_json->>'from', ''),
    destination_address=COALESCE(NULLIF(destination_address, ''), metadata_json->>'to', ''),
    fee_lamports=CASE
      WHEN fee_lamports=0 AND (metadata_json ? 'fee_lamports') AND (metadata_json->>'fee_lamports') ~ '^[0-9]+$'
      THEN (metadata_json->>'fee_lamports')::numeric
      ELSE fee_lamports
    END
WHERE network='SOLANA';

UPDATE sol_transactions
SET receive_key=signature || ':balance_delta:' || COALESCE(NULLIF(destination_address, ''), metadata_json->>'address', '')
WHERE network='SOLANA'
  AND direction='deposit'
  AND receive_key=''
  AND signature <> ''
  AND COALESCE(NULLIF(destination_address, ''), metadata_json->>'address', '') <> '';

CREATE UNIQUE INDEX IF NOT EXISTS uq_sol_withdrawal_idempotency
  ON sol_transactions (user_id, idempotency_key)
  WHERE network='SOLANA' AND direction='withdrawal' AND idempotency_key <> '';

CREATE UNIQUE INDEX IF NOT EXISTS uq_sol_operation_id
  ON sol_transactions (network, operation_id)
  WHERE operation_id <> '';

CREATE UNIQUE INDEX IF NOT EXISTS uq_sol_withdrawal_signature
  ON sol_transactions (network, signature)
  WHERE direction='withdrawal' AND signature <> '';

CREATE UNIQUE INDEX IF NOT EXISTS uq_sol_deposit_receive_key
  ON sol_transactions (network, receive_key)
  WHERE direction='deposit' AND receive_key <> '';

CREATE INDEX IF NOT EXISTS idx_sol_withdrawal_recovery
  ON sol_transactions (network, status, updated_at)
  WHERE direction='withdrawal'
    AND status IN ('reserved','signed','submitted','submit_unknown','broadcast');
