-- M2M Agent Payment Intents
-- Applies on top of schema.sql + schema_phase5.sql
-- Run: psql $DATABASE_URL -f schema_m2m.sql

-- ============================================================
-- agent_payment_intents
-- Tracks every M2M payment intent created by an AI agent.
-- Flow: pending_deposit → paid_crypto → settling → settled
--                      └──────────────────────────→ failed
--       pending_deposit → expired (TTL enforced on read/cleanup)
-- ============================================================
CREATE TABLE IF NOT EXISTS agent_payment_intents (
    id                  TEXT        PRIMARY KEY,
    idempotency_key     TEXT        NOT NULL UNIQUE,
    agent_wallet        TEXT        NOT NULL,
    payment_type        TEXT        NOT NULL CHECK (payment_type IN ('pix','credit_card')),
    pix_key             TEXT,                             -- destination PIX key (required when payment_type='pix')
    payment_link        TEXT,                             -- destination payment URL for credit-card/bill rails
    barcode             TEXT,                             -- barcode/linha digitavel for invoice/card bill payment
    beneficiary_name    TEXT,                             -- beneficiary/merchant name for operator/provider checks
    due_date            TEXT,                             -- optional due date supplied by the agent
    amount_brl          NUMERIC(18,2) NOT NULL,           -- net BRL that will be paid to the recipient
    fee_bps             INT          NOT NULL,            -- fee in basis-points applied (1000=10%, 1900=19%)
    fee_usdt            NUMERIC(18,6) NOT NULL,           -- fee portion in USDT charged to agent
    gross_usdt          NUMERIC(18,6) NOT NULL,           -- BRL→USDT base (before fee)
    required_usdt       NUMERIC(18,6) NOT NULL,           -- gross_usdt + fee_usdt (what agent must deposit)
    usdt_rate           NUMERIC(18,6) NOT NULL,           -- USDT/BRL spot at intent creation
    payment_address     TEXT         NOT NULL,            -- TREASURY_HOT (on-chain deposit target)
    payment_network     TEXT         NOT NULL DEFAULT 'BSC',
    status              TEXT         NOT NULL DEFAULT 'pending_deposit',
    deposit_tx          TEXT,                             -- on-chain tx hash that funded this intent
    deposit_amount_usdt NUMERIC(18,6),                   -- actual deposited amount
    efi_end_to_end_id   TEXT,                             -- Efí endToEndId after PIX settlement
    efi_status          TEXT,                             -- Efí returned pix status
    settlement_receipt_url  TEXT,                         -- receipt URL or uploaded proof for manual/RPA settlement
    settlement_receipt_note TEXT,                         -- receipt text/reference for manual/RPA settlement
    error_message       TEXT,
    attempts            INT          NOT NULL DEFAULT 0,
    request_hash        TEXT         NOT NULL,            -- SHA-256 of canonical request body for audit
    expires_at          TIMESTAMPTZ  NOT NULL,
    settled_at          TIMESTAMPTZ,
    created_at          TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_m2m_intents_status
    ON agent_payment_intents(status);

CREATE INDEX IF NOT EXISTS idx_m2m_intents_agent_wallet
    ON agent_payment_intents(agent_wallet);

CREATE INDEX IF NOT EXISTS idx_m2m_intents_payment_address_status
    ON agent_payment_intents(payment_address, status)
    WHERE status = 'pending_deposit';

ALTER TABLE agent_payment_intents ADD COLUMN IF NOT EXISTS payment_network TEXT NOT NULL DEFAULT 'BSC';

CREATE INDEX IF NOT EXISTS idx_m2m_intents_payment_network_address_status
    ON agent_payment_intents(payment_network, payment_address, status)
    WHERE status = 'pending_deposit';

-- Older hardening migrations briefly enforced one pending intent per deposit
-- address. ChainFX now supports shared configured deposit addresses and
-- reconciles payments by amount, tx hash and expiry window.
DROP INDEX IF EXISTS uq_m2m_pending_payment_address;

CREATE INDEX IF NOT EXISTS idx_m2m_intents_expires_at
    ON agent_payment_intents(expires_at)
    WHERE status = 'pending_deposit';

-- ============================================================
-- agent_payment_audit_log
-- Immutable append-only log for every status transition and
-- settlement attempt on an intent. Never delete rows.
-- ============================================================
CREATE TABLE IF NOT EXISTS agent_payment_audit_log (
    id          BIGSERIAL    PRIMARY KEY,
    intent_id   TEXT         NOT NULL REFERENCES agent_payment_intents(id),
    event       TEXT         NOT NULL,   -- 'created','deposit_confirmed','settlement_started',
                                         -- 'settlement_succeeded','settlement_failed','expired'
    payload     JSONB,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_m2m_audit_intent_id
    ON agent_payment_audit_log(intent_id, created_at);
