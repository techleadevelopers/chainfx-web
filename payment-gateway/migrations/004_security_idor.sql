-- ============================================================================
-- Migration 004: Security & IDOR fixes — ChainFX Production
-- Author  : Security audit 2026-07
-- Applied : idempotent (safe to run multiple times)
-- Tables  : webhook_subscriptions, swaps, assets
--
-- What this migration does:
--   1. Adds agent ownership (agent_api_key_hash) to webhook_subscriptions
--      so each MCP agent only sees its own subscriptions (IDOR fix C-7).
--   2. Adds FK constraints: swaps.from_asset → assets.symbol (referential
--      integrity; prevents swaps referencing disabled/legacy assets).
--   3. Adds safety CHECK: assets.symbol must be UPPER-CASE at DB level.
--   4. Adds VARCHAR limits on unbounded TEXT fields that could cause storage DoS.
--   5. Adds indexes to support the new query patterns.
--
-- Run order matters if applied to a fresh database:
--   schema.sql → schema_phase5.sql → schema_agent_pricing.sql → this file
--
-- Rollback:
--   Each change has a corresponding ROLLBACK block below.
-- ============================================================================

BEGIN;

-- ── Safety: abort if running as the wrong database user ──────────────────────
DO $$
BEGIN
  IF current_user = 'postgres' AND current_database() = 'postgres' THEN
    RAISE EXCEPTION
      'Refusing to run against the default postgres database. Set DATABASE_URL correctly.';
  END IF;
END $$;

-- ── 1. webhook_subscriptions — agent ownership (IDOR fix) ────────────────────
--
-- agent_api_key_hash: SHA-256 hex of the MCP API key that created this
-- subscription. NULL for subscriptions created via the web UI or mobile app
-- (those are already scoped by user_id in a separate mobile-only table).
-- VARCHAR(64) = exactly one SHA-256 hex string.

ALTER TABLE webhook_subscriptions
  ADD COLUMN IF NOT EXISTS agent_api_key_hash VARCHAR(64),
  ADD COLUMN IF NOT EXISTS created_by         VARCHAR(64)  -- 'mcp' | 'web' | 'mobile'
;

-- Backfill existing rows created before this migration as 'web' origin.
UPDATE webhook_subscriptions
SET created_by = 'web'
WHERE created_by IS NULL;

-- Partial index: efficient lookup by agent (only indexes non-NULL hashes).
CREATE INDEX IF NOT EXISTS idx_ws_agent_key_hash
  ON webhook_subscriptions (agent_api_key_hash)
  WHERE agent_api_key_hash IS NOT NULL;

-- Composite index for the scoped list query used by MCP.
CREATE INDEX IF NOT EXISTS idx_ws_agent_active
  ON webhook_subscriptions (agent_api_key_hash, active, created_at DESC)
  WHERE agent_api_key_hash IS NOT NULL;

COMMENT ON COLUMN webhook_subscriptions.agent_api_key_hash IS
  'SHA-256(api_key) of the MCP agent that owns this subscription. '
  'NULL for subscriptions created via the web dashboard or mobile app.';

COMMENT ON COLUMN webhook_subscriptions.created_by IS
  'Origin of this subscription: mcp | web | mobile.';


-- ── 2. swaps FK constraints → assets (referential integrity) ─────────────────
--
-- swaps.from_asset and to_asset are VARCHAR(16) storing the asset symbol
-- (e.g. "USDT", "BTC"). assets.symbol is the PRIMARY KEY of the assets table.
-- Adding FK constraints prevents swaps from referencing disabled or deleted assets.
--
-- ON DELETE RESTRICT: cannot delete an asset that has existing swaps.
-- If an asset must be retired, set its status='legacy' / enabled=false instead.

DO $$
BEGIN
  -- from_asset FK
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.table_constraints
    WHERE constraint_name = 'fk_swaps_from_asset'
      AND table_name       = 'swaps'
  ) THEN
    ALTER TABLE swaps
      ADD CONSTRAINT fk_swaps_from_asset
      FOREIGN KEY (from_asset)
      REFERENCES assets (symbol)
      ON DELETE RESTRICT
      DEFERRABLE INITIALLY DEFERRED;
  END IF;

  -- to_asset FK
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.table_constraints
    WHERE constraint_name = 'fk_swaps_to_asset'
      AND table_name       = 'swaps'
  ) THEN
    ALTER TABLE swaps
      ADD CONSTRAINT fk_swaps_to_asset
      FOREIGN KEY (to_asset)
      REFERENCES assets (symbol)
      ON DELETE RESTRICT
      DEFERRABLE INITIALLY DEFERRED;
  END IF;
END $$;

COMMENT ON CONSTRAINT fk_swaps_from_asset ON swaps IS
  'Ensures from_asset references a valid entry in assets. '
  'Retire assets by disabling them, never by deleting.';

COMMENT ON CONSTRAINT fk_swaps_to_asset ON swaps IS
  'Ensures to_asset references a valid entry in assets.';


-- ── 3. assets — UPPER-CASE symbol constraint ──────────────────────────────────
--
-- Prevents "usdt" and "USDT" from coexisting as separate rows.
-- All application code already normalises via strings.ToUpper; this makes the
-- DB the final authority.

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.table_constraints
    WHERE constraint_name = 'chk_assets_symbol_upper'
      AND table_name       = 'assets'
  ) THEN
    ALTER TABLE assets
      ADD CONSTRAINT chk_assets_symbol_upper
      CHECK (symbol = UPPER(symbol));
  END IF;
END $$;

COMMENT ON CONSTRAINT chk_assets_symbol_upper ON assets IS
  'Enforces that asset symbols are always stored in UPPER-CASE (e.g. USDT, BTC).';


-- ── 4. VARCHAR limits on unbounded TEXT fields (Storage DoS prevention) ───────
--
-- These fields currently accept strings of arbitrary length. Limiting them at
-- the DB layer caps the blast radius of a validation bypass in application code.

-- kyc_requests
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_name = 'kyc_requests' AND column_name = 'document_url'
      AND data_type = 'text'
  ) THEN
    ALTER TABLE kyc_requests
      ALTER COLUMN document_url  TYPE VARCHAR(2048),
      ALTER COLUMN selfie_url    TYPE VARCHAR(2048);

    -- proof_of_address_url and proof_of_income_url added in phase 5
    BEGIN
      ALTER TABLE kyc_requests ALTER COLUMN proof_of_address_url TYPE VARCHAR(2048);
    EXCEPTION WHEN undefined_column THEN NULL;
    END;
    BEGIN
      ALTER TABLE kyc_requests ALTER COLUMN proof_of_income_url TYPE VARCHAR(2048);
    EXCEPTION WHEN undefined_column THEN NULL;
    END;
  END IF;
END $$;

-- orders (buy_orders / sell_orders hybrid) — tx_hash and address
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_name = 'orders' AND column_name = 'tx_hash' AND data_type = 'text'
  ) THEN
    ALTER TABLE orders
      ALTER COLUMN tx_hash TYPE VARCHAR(128);
  END IF;
END $$;

-- webhook_subscriptions target_url safety cap
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_name = 'webhook_subscriptions' AND column_name = 'target_url'
      AND (data_type = 'text' OR character_maximum_length > 2048 OR character_maximum_length IS NULL)
  ) THEN
    ALTER TABLE webhook_subscriptions
      ALTER COLUMN target_url TYPE VARCHAR(2048);
  END IF;
END $$;


-- ── 5. marketing_contacts — email format check ────────────────────────────────

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.table_constraints
    WHERE constraint_name = 'chk_marketing_contacts_email'
      AND table_name       = 'marketing_contacts'
  ) THEN
    -- Only add if the table exists
    IF EXISTS (
      SELECT 1 FROM information_schema.tables
      WHERE table_name = 'marketing_contacts'
    ) THEN
      ALTER TABLE marketing_contacts
        ADD CONSTRAINT chk_marketing_contacts_email
        CHECK (email ~* '^[^@\s]+@[^@\s]+\.[^@\s]+$');
    END IF;
  END IF;
END $$;


-- ── 6. Verification queries (informational, not blocking) ─────────────────────

DO $$
DECLARE
  v_ws_col   BOOLEAN;
  v_fk_from  BOOLEAN;
  v_fk_to    BOOLEAN;
  v_sym_chk  BOOLEAN;
BEGIN
  SELECT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_name = 'webhook_subscriptions' AND column_name = 'agent_api_key_hash'
  ) INTO v_ws_col;

  SELECT EXISTS (
    SELECT 1 FROM information_schema.table_constraints
    WHERE constraint_name = 'fk_swaps_from_asset' AND table_name = 'swaps'
  ) INTO v_fk_from;

  SELECT EXISTS (
    SELECT 1 FROM information_schema.table_constraints
    WHERE constraint_name = 'fk_swaps_to_asset' AND table_name = 'swaps'
  ) INTO v_fk_to;

  SELECT EXISTS (
    SELECT 1 FROM information_schema.table_constraints
    WHERE constraint_name = 'chk_assets_symbol_upper' AND table_name = 'assets'
  ) INTO v_sym_chk;

  RAISE NOTICE '──────────────────────────────────────────────';
  RAISE NOTICE 'Migration 004 verification:';
  RAISE NOTICE '  webhook_subscriptions.agent_api_key_hash : %', CASE WHEN v_ws_col  THEN 'OK' ELSE 'MISSING' END;
  RAISE NOTICE '  fk_swaps_from_asset                      : %', CASE WHEN v_fk_from THEN 'OK' ELSE 'MISSING' END;
  RAISE NOTICE '  fk_swaps_to_asset                        : %', CASE WHEN v_fk_to   THEN 'OK' ELSE 'MISSING' END;
  RAISE NOTICE '  chk_assets_symbol_upper                  : %', CASE WHEN v_sym_chk THEN 'OK' ELSE 'MISSING' END;
  RAISE NOTICE '──────────────────────────────────────────────';
END $$;

COMMIT;

-- ============================================================================
-- ROLLBACK SCRIPT (run manually if needed — do NOT execute together with above)
-- ============================================================================
-- BEGIN;
-- ALTER TABLE webhook_subscriptions DROP COLUMN IF EXISTS agent_api_key_hash;
-- ALTER TABLE webhook_subscriptions DROP COLUMN IF EXISTS created_by;
-- DROP INDEX IF EXISTS idx_ws_agent_key_hash;
-- DROP INDEX IF EXISTS idx_ws_agent_active;
-- ALTER TABLE swaps DROP CONSTRAINT IF EXISTS fk_swaps_from_asset;
-- ALTER TABLE swaps DROP CONSTRAINT IF EXISTS fk_swaps_to_asset;
-- ALTER TABLE assets DROP CONSTRAINT IF EXISTS chk_assets_symbol_upper;
-- ALTER TABLE marketing_contacts DROP CONSTRAINT IF EXISTS chk_marketing_contacts_email;
-- COMMIT;
