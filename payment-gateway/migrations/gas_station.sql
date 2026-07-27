-- ChainFX Gas Station + Paymaster + Auto-Sweeper schema
-- Idempotent. Safe to run more than once:
--   psql "$DATABASE_URL" -f gas_station.sql

CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE OR REPLACE FUNCTION chainfx_set_updated_at()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$;

-- Stores paymaster relay requests before any signer/on-chain action.
-- sig_hash is SHA-256(r || s) and is globally unique to block EIP-712 replay.
CREATE TABLE IF NOT EXISTS gas_relay_requests (
    id             UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
    user_address   TEXT          NOT NULL,
    sig_r          TEXT          NOT NULL,
    sig_s          TEXT          NOT NULL,
    sig_hash       TEXT          NOT NULL,
    tx_to          TEXT          NOT NULL,
    tx_data        TEXT          NOT NULL DEFAULT '',
    fee_usdt       NUMERIC(20,8) NOT NULL DEFAULT 0,
    gas_price_gwei NUMERIC(20,8),
    gas_limit      BIGINT,
    status         TEXT          NOT NULL DEFAULT 'pending'
                   CHECK (status IN ('pending','processing','sent','failed','dlq')),
    tx_hash        TEXT,
    attempts       INT           NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_retry_at  TIMESTAMPTZ,
    dlq_at         TIMESTAMPTZ,
    last_error     TEXT,
    created_at     TIMESTAMPTZ   NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ   NOT NULL DEFAULT NOW()
);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'uq_gas_relay_requests_sig_hash'
    ) THEN
        ALTER TABLE gas_relay_requests
            ADD CONSTRAINT uq_gas_relay_requests_sig_hash UNIQUE (sig_hash);
    END IF;
END;
$$;

CREATE INDEX IF NOT EXISTS idx_gas_relay_requests_status
    ON gas_relay_requests(status);

CREATE INDEX IF NOT EXISTS idx_gas_relay_requests_user_address
    ON gas_relay_requests(LOWER(user_address), created_at DESC);

CREATE INDEX IF NOT EXISTS idx_gas_relay_requests_created_at
    ON gas_relay_requests(created_at DESC);

CREATE INDEX IF NOT EXISTS idx_gas_relay_requests_retry_eligible
    ON gas_relay_requests(status, next_retry_at, attempts, created_at)
    WHERE status IN ('pending','failed');

CREATE INDEX IF NOT EXISTS idx_gas_relay_requests_dlq
    ON gas_relay_requests(dlq_at DESC)
    WHERE status = 'dlq';

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_trigger WHERE tgname = 'trg_gas_relay_requests_updated_at'
    ) THEN
        CREATE TRIGGER trg_gas_relay_requests_updated_at
            BEFORE UPDATE ON gas_relay_requests
            FOR EACH ROW EXECUTE FUNCTION chainfx_set_updated_at();
    END IF;
END;
$$;

-- Short-lived distributed replay locks used before inserting a relay request.
-- The application deletes expired rows opportunistically during AcquireLock.
CREATE TABLE IF NOT EXISTS paymaster_sig_locks (
    sig_hash    TEXT        PRIMARY KEY,
    acquired_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at  TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_paymaster_sig_locks_expires_at
    ON paymaster_sig_locks(expires_at);

-- Audit trail for Auto-Sweeper cycles. It records successful sweeps, skips and
-- errors. The actual signer idempotency key is deterministic in code:
-- sweep-{hot_wallet}-{block_number}.
CREATE TABLE IF NOT EXISTS auto_sweeper_runs (
    id           UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
    network      TEXT          NOT NULL DEFAULT 'BSC',
    hot_wallet   TEXT          NOT NULL,
    cold_wallet  TEXT          NOT NULL,
    balance_usdt NUMERIC(20,8) NOT NULL DEFAULT 0,
    swept_usdt   NUMERIC(20,8) NOT NULL DEFAULT 0,
    tx_hash      TEXT,
    status       TEXT          NOT NULL DEFAULT 'ok'
                 CHECK (status IN ('ok','skipped','error')),
    error_msg    TEXT,
    ran_at       TIMESTAMPTZ   NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_auto_sweeper_runs_ran_at
    ON auto_sweeper_runs(ran_at DESC);

CREATE INDEX IF NOT EXISTS idx_auto_sweeper_runs_status_ran_at
    ON auto_sweeper_runs(status, ran_at DESC);

CREATE INDEX IF NOT EXISTS idx_auto_sweeper_runs_hot_wallet
    ON auto_sweeper_runs(LOWER(hot_wallet), ran_at DESC);

CREATE INDEX IF NOT EXISTS idx_auto_sweeper_runs_tx_hash
    ON auto_sweeper_runs(tx_hash)
    WHERE tx_hash IS NOT NULL;

-- Operational views for dashboards / manual reconciliation.
CREATE OR REPLACE VIEW gas_station_relay_stats_24h AS
SELECT
    COUNT(*) FILTER (WHERE status = 'pending')    AS pending,
    COUNT(*) FILTER (WHERE status = 'processing') AS processing,
    COUNT(*) FILTER (WHERE status = 'sent')       AS sent,
    COUNT(*) FILTER (WHERE status = 'failed')     AS failed,
    COUNT(*) FILTER (WHERE status = 'dlq')        AS dlq,
    COALESCE(SUM(fee_usdt) FILTER (WHERE status = 'sent'), 0) AS total_fee_usdt,
    COUNT(*) AS total_24h
FROM gas_relay_requests
WHERE created_at > NOW() - INTERVAL '24 hours';

CREATE OR REPLACE VIEW auto_sweeper_stats_24h AS
SELECT
    COUNT(*) AS total_runs_24h,
    COUNT(*) FILTER (WHERE status = 'ok')      AS successful,
    COUNT(*) FILTER (WHERE status = 'error')   AS errors,
    COUNT(*) FILTER (WHERE status = 'skipped') AS skipped,
    COALESCE(SUM(swept_usdt) FILTER (WHERE status = 'ok'), 0) AS total_swept_usdt
FROM auto_sweeper_runs
WHERE ran_at > NOW() - INTERVAL '24 hours';
