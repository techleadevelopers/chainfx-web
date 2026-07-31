ALTER TABLE auto_sweeper_runs ADD COLUMN IF NOT EXISTS operation_id TEXT NOT NULL DEFAULT '';
ALTER TABLE auto_sweeper_runs ADD COLUMN IF NOT EXISTS chain_id BIGINT NOT NULL DEFAULT 0;
ALTER TABLE auto_sweeper_runs ADD COLUMN IF NOT EXISTS token_contract TEXT NOT NULL DEFAULT '';
ALTER TABLE auto_sweeper_runs ADD COLUMN IF NOT EXISTS amount_raw TEXT NOT NULL DEFAULT '';
ALTER TABLE auto_sweeper_runs ADD COLUMN IF NOT EXISTS signer_status TEXT NOT NULL DEFAULT '';
ALTER TABLE auto_sweeper_runs ADD COLUMN IF NOT EXISTS nonce BIGINT NOT NULL DEFAULT 0;

DO $$
DECLARE c record;
BEGIN
  FOR c IN
    SELECT conname
    FROM pg_constraint
    WHERE conrelid = 'auto_sweeper_runs'::regclass
      AND contype = 'c'
      AND pg_get_constraintdef(oid) ILIKE '%status%'
  LOOP
    EXECUTE format('ALTER TABLE auto_sweeper_runs DROP CONSTRAINT IF EXISTS %I', c.conname);
  END LOOP;
  ALTER TABLE auto_sweeper_runs
    ADD CONSTRAINT auto_sweeper_runs_status_check
    CHECK (status IN ('ok','skipped','error','broadcast','broadcast_unknown','confirmed','manual_review','signed'));
END $$;

CREATE INDEX IF NOT EXISTS idx_auto_sweeper_runs_operation_id ON auto_sweeper_runs(operation_id) WHERE operation_id <> '';
