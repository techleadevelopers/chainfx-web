-- Hardening for custodial EVM/BASE and SOL wallet key rotation.
-- Existing wallet addresses are not changed by this migration.

ALTER TABLE mobile_wallet_keys
  ADD COLUMN IF NOT EXISTS encryption_key_id TEXT NOT NULL DEFAULT 'legacy';

ALTER TABLE mobile_wallet_keys
  ADD COLUMN IF NOT EXISTS encryption_version INTEGER NOT NULL DEFAULT 1;

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1
      FROM pg_constraint
     WHERE conname = 'chk_mobile_wallet_keys_encryption_key_id_nonempty'
  ) THEN
    ALTER TABLE mobile_wallet_keys
      ADD CONSTRAINT chk_mobile_wallet_keys_encryption_key_id_nonempty
      CHECK (length(trim(encryption_key_id)) > 0);
  END IF;

  IF NOT EXISTS (
    SELECT 1
      FROM pg_constraint
     WHERE conname = 'chk_mobile_wallet_keys_encryption_version_positive'
  ) THEN
    ALTER TABLE mobile_wallet_keys
      ADD CONSTRAINT chk_mobile_wallet_keys_encryption_version_positive
      CHECK (encryption_version > 0);
  END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_mobile_wallet_keys_encryption_key_id
  ON mobile_wallet_keys (encryption_key_id);

CREATE INDEX IF NOT EXISTS idx_sol_wallet_addresses_derivation_key_id
  ON sol_wallet_addresses (derivation_key_id)
  WHERE status = 'active';
