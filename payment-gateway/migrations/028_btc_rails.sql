-- ============================================================
-- Migration 028 — Rail Bitcoin isolada
-- Três tabelas: endereços, UTXOs e transações.
-- Não toca em nenhuma tabela EVM/NFC/signer existente.
-- ============================================================

-- ─── btc_wallet_addresses ────────────────────────────────────
-- Um endereço P2WPKH por usuário por rede.
-- UNIQUE(network, address) garante que dois usuários nunca
-- compartilhem o mesmo endereço.
-- UNIQUE(network, derivation_index) garante que o mesmo índice
-- não seja alocado para dois usuários (mesmo em concorrência).

CREATE TABLE IF NOT EXISTS btc_wallet_addresses (
    id               TEXT        PRIMARY KEY,
    user_id          TEXT        NOT NULL,
    network          TEXT        NOT NULL CHECK (network IN ('mainnet','testnet','signet','regtest')),
    address          TEXT        NOT NULL,
    derivation_path  TEXT        NOT NULL,
    derivation_index INTEGER     NOT NULL,
    address_type     TEXT        NOT NULL DEFAULT 'p2wpkh',
    status           TEXT        NOT NULL DEFAULT 'active' CHECK (status IN ('active','archived')),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT uq_btc_address_network      UNIQUE (network, address),
    CONSTRAINT uq_btc_derivation_index     UNIQUE (network, derivation_index)
);

CREATE INDEX IF NOT EXISTS idx_btc_wallet_addresses_user_network
    ON btc_wallet_addresses (user_id, network, status);

-- ─── btc_utxos ───────────────────────────────────────────────
-- Cada linha representa um UTXO rastreado pela rail.
-- Status lifecycle: pending → confirmed → reserved → spent | orphaned

CREATE TABLE IF NOT EXISTS btc_utxos (
    id               TEXT        PRIMARY KEY,
    network          TEXT        NOT NULL,
    user_id          TEXT        NOT NULL,
    wallet_address_id TEXT       NOT NULL REFERENCES btc_wallet_addresses(id),
    txid             TEXT        NOT NULL,
    vout             INTEGER     NOT NULL,
    value_sats       BIGINT      NOT NULL CHECK (value_sats > 0),
    script_pub_key   TEXT        NOT NULL DEFAULT '',
    block_height     BIGINT      NOT NULL DEFAULT 0,
    confirmations    INTEGER     NOT NULL DEFAULT 0,
    status           TEXT        NOT NULL DEFAULT 'pending'
                                 CHECK (status IN ('pending','confirmed','reserved','spent','orphaned')),
    spent_by_txid    TEXT,
    detected_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    confirmed_at     TIMESTAMPTZ,
    spent_at         TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT uq_btc_utxo UNIQUE (network, txid, vout)
);

CREATE INDEX IF NOT EXISTS idx_btc_utxos_user_network_status
    ON btc_utxos (user_id, network, status);

CREATE INDEX IF NOT EXISTS idx_btc_utxos_address
    ON btc_utxos (wallet_address_id, status);

-- ─── btc_transactions ────────────────────────────────────────
-- Rastreia saques e depósitos BTC com idempotência.
-- Status lifecycle: created → building → signed → broadcast → pending → confirmed | failed | replaced | dropped

CREATE TABLE IF NOT EXISTS btc_transactions (
    id                  TEXT        PRIMARY KEY,
    user_id             TEXT        NOT NULL,
    network             TEXT        NOT NULL,
    direction           TEXT        NOT NULL CHECK (direction IN ('deposit','withdrawal','internal')),
    txid                TEXT        NOT NULL DEFAULT '',
    raw_tx_hash         TEXT,                  -- raw hex armazenado só antes do broadcast; pode ser limpo após
    destination_address TEXT,
    amount_sats         BIGINT      NOT NULL CHECK (amount_sats >= 0),
    fee_sats            BIGINT      NOT NULL DEFAULT 0,
    fee_rate_sat_vbyte  BIGINT      NOT NULL DEFAULT 0,
    status              TEXT        NOT NULL
                                    CHECK (status IN ('created','building','signed','broadcast','pending','confirmed','failed','replaced','dropped')),
    confirmations       INTEGER     NOT NULL DEFAULT 0,
    block_height        BIGINT      NOT NULL DEFAULT 0,
    idempotency_key     TEXT        NOT NULL,
    request_hash        TEXT,
    error_code          TEXT,
    error_message       TEXT,
    broadcast_at        TIMESTAMPTZ,
    confirmed_at        TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT uq_btc_tx_network_txid       UNIQUE (network, txid) DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT uq_btc_tx_idempotency        UNIQUE (user_id, idempotency_key)
);

CREATE INDEX IF NOT EXISTS idx_btc_transactions_user_network
    ON btc_transactions (user_id, network, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_btc_transactions_status_network
    ON btc_transactions (network, status)
    WHERE status IN ('broadcast','pending');

-- ─── btc_transaction_inputs ──────────────────────────────────
-- Registra cada UTXO usado como input em uma transação BTC.
-- Permite auditoria completa de double-spend e reconciliação.

CREATE TABLE IF NOT EXISTS btc_transaction_inputs (
    id             TEXT    PRIMARY KEY,
    transaction_id TEXT    NOT NULL REFERENCES btc_transactions(id),
    utxo_id        TEXT    NOT NULL REFERENCES btc_utxos(id),
    txid           TEXT    NOT NULL,   -- txid do UTXO gasto
    vout           INTEGER NOT NULL,
    value_sats     BIGINT  NOT NULL CHECK (value_sats > 0),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT uq_btc_tx_input UNIQUE (transaction_id, txid, vout)
);

CREATE INDEX IF NOT EXISTS idx_btc_tx_inputs_transaction
    ON btc_transaction_inputs (transaction_id);

-- ─── btc_transaction_outputs ─────────────────────────────────
-- Registra cada output de uma transação BTC:
-- destino principal, change e tipo de output.

CREATE TABLE IF NOT EXISTS btc_transaction_outputs (
    id             TEXT    PRIMARY KEY,
    transaction_id TEXT    NOT NULL REFERENCES btc_transactions(id),
    vout           INTEGER NOT NULL,
    address        TEXT    NOT NULL,
    value_sats     BIGINT  NOT NULL CHECK (value_sats >= 0),
    output_type    TEXT    NOT NULL DEFAULT 'destination'
                           CHECK (output_type IN ('destination','change','fee_bump')),
    script_pub_key TEXT    NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT uq_btc_tx_output UNIQUE (transaction_id, vout)
);

CREATE INDEX IF NOT EXISTS idx_btc_tx_outputs_transaction
    ON btc_transaction_outputs (transaction_id);

-- ─── btc_wallet_state ────────────────────────────────────────
-- Estado global da rail BTC por rede:
--   next_derivation_index — próximo índice HD a ser alocado
--   last_scanned_block    — último bloco processado pelo deposit scanner
--   last_scan_at          — timestamp do último scan bem-sucedido
--   scanner_status        — 'idle' | 'running' | 'error'
-- Uma linha por rede, inserida na primeira inicialização.

CREATE TABLE IF NOT EXISTS btc_wallet_state (
    network               TEXT        PRIMARY KEY
                                      CHECK (network IN ('mainnet','testnet','signet','regtest')),
    next_derivation_index INTEGER     NOT NULL DEFAULT 0,
    last_scanned_block    BIGINT      NOT NULL DEFAULT 0,
    last_scan_at          TIMESTAMPTZ,
    scanner_status        TEXT        NOT NULL DEFAULT 'idle'
                                      CHECK (scanner_status IN ('idle','running','error')),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Inicializa as quatro redes com estado zero (idempotente).
INSERT INTO btc_wallet_state (network) VALUES
    ('mainnet'), ('testnet'), ('signet'), ('regtest')
ON CONFLICT (network) DO NOTHING;
