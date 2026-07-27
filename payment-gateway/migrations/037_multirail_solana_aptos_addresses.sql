-- Migration 037 - Solana/Aptos rail address registries.
-- Keeps new rails isolated from EVM users.wallet_address and BTC tables.

CREATE TABLE IF NOT EXISTS sol_wallet_addresses (
    id                TEXT        PRIMARY KEY DEFAULT ('sol_' || md5(random()::text || clock_timestamp()::text)),
    user_id           TEXT        NOT NULL,
    network           TEXT        NOT NULL DEFAULT 'SOLANA' CHECK (network IN ('SOLANA')),
    address           TEXT        NOT NULL,
    derivation_key_id TEXT        NOT NULL DEFAULT '',
    status            TEXT        NOT NULL DEFAULT 'active' CHECK (status IN ('active','archived')),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT uq_sol_address_network UNIQUE (network, address),
    CONSTRAINT uq_sol_user_network_active UNIQUE (user_id, network, status)
);

CREATE INDEX IF NOT EXISTS idx_sol_wallet_addresses_user_network
    ON sol_wallet_addresses (user_id, network, status);

CREATE TABLE IF NOT EXISTS sol_transactions (
    id                  TEXT        PRIMARY KEY DEFAULT ('soltx_' || md5(random()::text || clock_timestamp()::text)),
    user_id             TEXT        NOT NULL,
    network             TEXT        NOT NULL DEFAULT 'SOLANA',
    signature           TEXT        NOT NULL DEFAULT '',
    asset               TEXT        NOT NULL DEFAULT 'SOL',
    mint_address        TEXT        NOT NULL DEFAULT '',
    direction           TEXT        NOT NULL CHECK (direction IN ('deposit','withdrawal','router_delivery','internal')),
    amount_raw          NUMERIC(78,0) NOT NULL DEFAULT 0,
    decimals            INTEGER     NOT NULL DEFAULT 9,
    status              TEXT        NOT NULL DEFAULT 'pending',
    confirmations       INTEGER     NOT NULL DEFAULT 0,
    slot                BIGINT      NOT NULL DEFAULT 0,
    metadata_json       JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT uq_sol_signature UNIQUE (network, signature)
);

CREATE INDEX IF NOT EXISTS idx_sol_transactions_user_created
    ON sol_transactions (user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS sol_cursors (
    network          TEXT        PRIMARY KEY,
    last_signature   TEXT        NOT NULL DEFAULT '',
    last_slot        BIGINT      NOT NULL DEFAULT 0,
    scanner_status   TEXT        NOT NULL DEFAULT 'idle',
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO sol_cursors (network) VALUES ('SOLANA')
ON CONFLICT (network) DO NOTHING;

CREATE TABLE IF NOT EXISTS aptos_wallet_addresses (
    id                TEXT        PRIMARY KEY DEFAULT ('apt_' || md5(random()::text || clock_timestamp()::text)),
    user_id           TEXT        NOT NULL,
    network           TEXT        NOT NULL DEFAULT 'APTOS' CHECK (network IN ('APTOS')),
    address           TEXT        NOT NULL,
    derivation_key_id TEXT        NOT NULL DEFAULT '',
    status            TEXT        NOT NULL DEFAULT 'active' CHECK (status IN ('active','archived')),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT uq_aptos_address_network UNIQUE (network, address),
    CONSTRAINT uq_aptos_user_network_active UNIQUE (user_id, network, status)
);

CREATE INDEX IF NOT EXISTS idx_aptos_wallet_addresses_user_network
    ON aptos_wallet_addresses (user_id, network, status);

CREATE TABLE IF NOT EXISTS aptos_transactions (
    id                  TEXT        PRIMARY KEY DEFAULT ('apttx_' || md5(random()::text || clock_timestamp()::text)),
    user_id             TEXT        NOT NULL,
    network             TEXT        NOT NULL DEFAULT 'APTOS',
    tx_hash             TEXT        NOT NULL DEFAULT '',
    version             BIGINT      NOT NULL DEFAULT 0,
    asset               TEXT        NOT NULL DEFAULT 'APT',
    type_tag            TEXT        NOT NULL DEFAULT '',
    direction           TEXT        NOT NULL CHECK (direction IN ('deposit','withdrawal','router_delivery','internal')),
    amount_raw          NUMERIC(78,0) NOT NULL DEFAULT 0,
    decimals            INTEGER     NOT NULL DEFAULT 8,
    status              TEXT        NOT NULL DEFAULT 'pending',
    metadata_json       JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT uq_aptos_tx_hash UNIQUE (network, tx_hash)
);

CREATE INDEX IF NOT EXISTS idx_aptos_transactions_user_created
    ON aptos_transactions (user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS aptos_cursors (
    network          TEXT        PRIMARY KEY,
    last_version     BIGINT      NOT NULL DEFAULT 0,
    scanner_status   TEXT        NOT NULL DEFAULT 'idle',
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO aptos_cursors (network) VALUES ('APTOS')
ON CONFLICT (network) DO NOTHING;
