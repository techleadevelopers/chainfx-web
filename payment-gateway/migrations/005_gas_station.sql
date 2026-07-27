-- Migration 005: Gas Station (Paymaster) + Auto-Sweeper
-- Idempotent — safe to re-run.

-- ── gas_relay_requests ────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS gas_relay_requests (
    id               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_address     TEXT        NOT NULL,
    sig_r            TEXT        NOT NULL,
    sig_s            TEXT        NOT NULL,
    sig_hash         TEXT        NOT NULL,           -- SHA-256(r||s) idempotency key
    tx_to            TEXT        NOT NULL,           -- destination contract/address
    tx_data          TEXT        NOT NULL DEFAULT '', -- hex-encoded calldata
    fee_usdt         NUMERIC(20,8) NOT NULL DEFAULT 0,
    gas_price_gwei   NUMERIC(20,8),
    gas_limit        BIGINT,
    status           TEXT        NOT NULL DEFAULT 'pending'
                     CHECK (status IN ('pending','processing','sent','failed','dlq')),
    tx_hash          TEXT,
    attempts         INT         NOT NULL DEFAULT 0,
    next_retry_at    TIMESTAMPTZ,
    dlq_at           TIMESTAMPTZ,
    last_error       TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Unique constraint to enforce EIP-712 sig idempotency at DB level
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'uq_grr_sig_hash'
    ) THEN
        ALTER TABLE gas_relay_requests
            ADD CONSTRAINT uq_grr_sig_hash UNIQUE (sig_hash);
    END IF;
END;
$$;

-- ── auto_sweeper_runs ─────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS auto_sweeper_runs (
    id              UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    network         TEXT         NOT NULL DEFAULT 'BSC',
    hot_wallet      TEXT         NOT NULL,
    cold_wallet     TEXT         NOT NULL,
    balance_usdt    NUMERIC(20,8) NOT NULL,
    swept_usdt      NUMERIC(20,8) NOT NULL DEFAULT 0,
    tx_hash         TEXT,
    status          TEXT         NOT NULL DEFAULT 'ok'
                    CHECK (status IN ('ok','skipped','error')),
    error_msg       TEXT,
    ran_at          TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

-- ── Indexes ───────────────────────────────────────────────────────────────────
CREATE INDEX IF NOT EXISTS idx_grr_status
    ON gas_relay_requests(status);
CREATE INDEX IF NOT EXISTS idx_grr_user_address
    ON gas_relay_requests(user_address);
CREATE INDEX IF NOT EXISTS idx_grr_created_at
    ON gas_relay_requests(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_grr_retry_eligible
    ON gas_relay_requests(status, next_retry_at)
    WHERE status IN ('pending','failed');
CREATE INDEX IF NOT EXISTS idx_asr_ran_at
    ON auto_sweeper_runs(ran_at DESC);

-- ── updated_at trigger ────────────────────────────────────────────────────────
CREATE OR REPLACE FUNCTION chainfx_set_updated_at()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_trigger WHERE tgname = 'trg_grr_updated_at'
    ) THEN
        CREATE TRIGGER trg_grr_updated_at
            BEFORE UPDATE ON gas_relay_requests
            FOR EACH ROW EXECUTE FUNCTION chainfx_set_updated_at();
    END IF;
END;
$$;

-- ── Rollback (commented out) ──────────────────────────────────────────────────
-- DROP TRIGGER IF EXISTS trg_grr_updated_at ON gas_relay_requests;
-- DROP FUNCTION IF EXISTS chainfx_set_updated_at();
-- DROP TABLE IF EXISTS auto_sweeper_runs;
-- DROP TABLE IF EXISTS gas_relay_requests;
