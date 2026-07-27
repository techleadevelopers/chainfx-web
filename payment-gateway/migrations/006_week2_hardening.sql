-- Migration 006: Week 2 production hardening.
-- Idempotent; safe to re-run.

-- M2M deposits must be matched by a unique payment address, never by amount
-- proximity across multiple pending intents sharing the same address.
CREATE UNIQUE INDEX IF NOT EXISTS uq_m2m_pending_payment_address
    ON agent_payment_intents (LOWER(payment_address))
    WHERE status = 'pending_deposit';

CREATE UNIQUE INDEX IF NOT EXISTS uq_m2m_deposit_tx
    ON agent_payment_intents (deposit_tx)
    WHERE deposit_tx IS NOT NULL;

-- Persistent worker DLQ for manual reconciliation after restarts.
CREATE TABLE IF NOT EXISTS worker_dlq (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    event_type  TEXT        NOT NULL,
    order_id    TEXT,
    attempts    INT         NOT NULL DEFAULT 0,
    reason      TEXT        NOT NULL,
    payload     JSONB       NOT NULL DEFAULT '{}'::jsonb,
    status      TEXT        NOT NULL DEFAULT 'open'
                CHECK (status IN ('open','resolved','ignored')),
    failed_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_worker_dlq_status_failed_at
    ON worker_dlq(status, failed_at DESC);
CREATE INDEX IF NOT EXISTS idx_worker_dlq_order_id
    ON worker_dlq(order_id)
    WHERE order_id IS NOT NULL;

-- Distributed EIP-712 signature locks for the paymaster.
CREATE TABLE IF NOT EXISTS paymaster_sig_locks (
    sig_hash    TEXT        PRIMARY KEY,
    acquired_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at  TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_paymaster_sig_locks_expires_at
    ON paymaster_sig_locks(expires_at);
