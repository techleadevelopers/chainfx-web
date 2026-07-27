CREATE TABLE IF NOT EXISTS settlement_instructions (
    id UUID PRIMARY KEY,
    operation_id BYTEA NOT NULL UNIQUE,
    settlement_intent_id TEXT NOT NULL,
    order_id TEXT NOT NULL,
    side TEXT,
    source_channel TEXT,

    network TEXT NOT NULL,
    chain_id BIGINT NOT NULL,
    vault_address TEXT NOT NULL,
    token_address TEXT NOT NULL,
    recipient_address TEXT NOT NULL,
    amount_raw NUMERIC(78, 0) NOT NULL,

    policy_version TEXT NOT NULL,
    network_policy_version TEXT,
    risk_policy_version TEXT,
    contract_version TEXT,

    authorization_status TEXT NOT NULL,
    execution_status TEXT NOT NULL,

    authorized_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ,
    consumed_at TIMESTAMPTZ,

    tx_hash TEXT,
    block_number BIGINT,
    confirmations INTEGER,
    failure_code TEXT,
    failure_reason TEXT,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_settlement_instructions_order
    ON settlement_instructions(order_id);

CREATE INDEX IF NOT EXISTS idx_settlement_instructions_status
    ON settlement_instructions(authorization_status, execution_status);

CREATE INDEX IF NOT EXISTS idx_settlement_instructions_network
    ON settlement_instructions(network, chain_id, token_address);
