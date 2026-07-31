-- Bind mobile financial idempotency keys to the original request payload.
-- Existing historical rows may remain NULL because the original request body is
-- not reconstructable. New/retried rows are populated by the mobile middleware.

ALTER TABLE operation_ids
  ADD COLUMN IF NOT EXISTS request_hash TEXT;

DO $$
BEGIN
  IF EXISTS (
    SELECT 1
    FROM operation_ids
    WHERE request_hash IS NOT NULL
      AND length(request_hash) <> 64
  ) THEN
    RAISE EXCEPTION 'operation_ids contains invalid request_hash values';
  END IF;
END $$;

ALTER TABLE operation_ids
  DROP CONSTRAINT IF EXISTS chk_operation_ids_request_hash;

ALTER TABLE operation_ids
  ADD CONSTRAINT chk_operation_ids_request_hash
  CHECK (request_hash IS NULL OR request_hash ~ '^[0-9a-f]{64}$')
  NOT VALID;
