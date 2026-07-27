CREATE TABLE IF NOT EXISTS settlement_receipts (
    id UUID PRIMARY KEY,
    operation_id BYTEA NOT NULL UNIQUE,
    tx_hash TEXT NOT NULL UNIQUE,

    chain_id BIGINT NOT NULL,
    block_number BIGINT,
    block_hash TEXT,
    transaction_index INTEGER,

    receipt_status BIGINT,
    confirmations BIGINT NOT NULL DEFAULT 0,

    vault_event_log_index INTEGER,
    transfer_event_log_index INTEGER,

    receipt_verified BOOLEAN NOT NULL DEFAULT FALSE,
    vault_event_verified BOOLEAN NOT NULL DEFAULT FALSE,
    transfer_event_verified BOOLEAN NOT NULL DEFAULT FALSE,
    confirmations_verified BOOLEAN NOT NULL DEFAULT FALSE,

    reconciliation_status TEXT NOT NULL DEFAULT 'PENDING',
    failure_code TEXT,
    failure_field TEXT,
    failure_details JSONB,

    first_seen_at TIMESTAMPTZ,
    confirmed_at TIMESTAMPTZ,
    reconciled_at TIMESTAMPTZ,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_settlement_receipts_status
    ON settlement_receipts(reconciliation_status, updated_at);

CREATE INDEX IF NOT EXISTS idx_settlement_receipts_chain_block
    ON settlement_receipts(chain_id, block_number);

CREATE TABLE IF NOT EXISTS settlement_reconciliation_events (
    id UUID PRIMARY KEY,
    operation_id BYTEA NOT NULL,
    tx_hash TEXT,

    event_type TEXT NOT NULL,
    previous_status TEXT,
    new_status TEXT,

    expected JSONB,
    observed JSONB,

    failure_code TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_settlement_reconciliation_events_operation
    ON settlement_reconciliation_events(operation_id, created_at);

CREATE INDEX IF NOT EXISTS idx_settlement_reconciliation_events_type
    ON settlement_reconciliation_events(event_type, created_at);
