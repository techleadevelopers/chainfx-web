-- ============================================================
-- Migration 050 — BTC Treasury operacional da plataforma
--
-- Cria infraestrutura para a Treasury BTC usada como fonte
-- prioritária de liquidez nos fluxos BUY BTC (web + mobile).
--
-- SEPARADO de:
--   btc_wallet_addresses / btc_utxos  — wallets custodiais dos usuários
--   SELL_BTC_WALLET_ADDRESS           — endereço de recebimento SELL web
--
-- Não toca em nenhuma tabela existente.
-- ============================================================

-- ─── btc_treasury_utxos ─────────────────────────────────────
-- UTXOs no endereço da Treasury operacional da plataforma.
-- Completamente separado dos UTXOs dos usuários em btc_utxos.
-- Status lifecycle: pending → confirmed → reserved → spent | orphaned

CREATE TABLE IF NOT EXISTS btc_treasury_utxos (
    id               TEXT        PRIMARY KEY,
    network          TEXT        NOT NULL CHECK (network IN ('mainnet','testnet','signet','regtest')),
    address          TEXT        NOT NULL,
    txid             TEXT        NOT NULL,
    vout             INTEGER     NOT NULL,
    value_sats       BIGINT      NOT NULL CHECK (value_sats > 0),
    script_pub_key   TEXT        NOT NULL DEFAULT '',
    block_height     BIGINT      NOT NULL DEFAULT 0,
    confirmations    INTEGER     NOT NULL DEFAULT 0,
    status           TEXT        NOT NULL DEFAULT 'pending'
                                 CHECK (status IN ('pending','confirmed','reserved','spent','orphaned')),
    reserved_by_op   TEXT,       -- btc_treasury_operations.id que reservou este UTXO
    spent_by_txid    TEXT,
    detected_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    confirmed_at     TIMESTAMPTZ,
    spent_at         TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT uq_btc_treasury_utxo UNIQUE (network, txid, vout)
);

CREATE INDEX IF NOT EXISTS idx_btc_treasury_utxos_network_status
    ON btc_treasury_utxos (network, status)
    WHERE status IN ('confirmed', 'reserved');

CREATE INDEX IF NOT EXISTS idx_btc_treasury_utxos_address
    ON btc_treasury_utxos (network, address, status);

-- ─── btc_treasury_operations ─────────────────────────────────
-- Audit trail de cada tentativa de entrega BTC via Treasury.
-- Uma linha por ordem BUY BTC processada pelo BTCFundingRouter.
--
-- Invariante crítico: uma ordem BUY BTC gera NO MÁXIMO uma operação.
-- A coluna idempotency_key = 'btc_buy:<order_id>' garante isso.
--
-- Status lifecycle:
--   pending → processing → signed → broadcast → broadcast_unknown
--           → confirmed | failed | manual_review
--
-- funding_source indica qual rota foi usada:
--   treasury — enviado via BTC_TREASURY_ADDRESS
--   bingx    — fallback BingX (registrado para auditoria, mas executado pelo liquidity router)

CREATE TABLE IF NOT EXISTS btc_treasury_operations (
    id                    TEXT        PRIMARY KEY,
    order_id              TEXT        NOT NULL,  -- buy_orders.id
    asset                 TEXT        NOT NULL DEFAULT 'BTC',
    network               TEXT        NOT NULL DEFAULT 'BITCOIN',
    funding_source        TEXT        NOT NULL DEFAULT 'treasury'
                                      CHECK (funding_source IN ('treasury','bingx','unknown')),

    -- Endereços
    treasury_address      TEXT,       -- BTC_TREASURY_ADDRESS no momento da operação
    destination_address   TEXT        NOT NULL,

    -- Valores (sempre em satoshis — nunca float)
    amount_sats           BIGINT      NOT NULL CHECK (amount_sats > 0),
    fee_sats              BIGINT      NOT NULL DEFAULT 0,

    -- Assinatura e broadcast
    txid                  TEXT        NOT NULL DEFAULT '',
    raw_tx_hash           TEXT,       -- raw hex apenas antes do broadcast; pode ser limpo após confirmação
    signer_operation_id   TEXT        NOT NULL DEFAULT '', -- BTC_TREASURY_SIGNER_KEY_ID para auditoria

    -- Estado
    status                TEXT        NOT NULL DEFAULT 'pending'
                                      CHECK (status IN (
                                          'pending','processing','signed',
                                          'broadcast','broadcast_unknown',
                                          'confirmed','failed','manual_review'
                                      )),
    error_code            TEXT,
    error_message         TEXT,

    -- Idempotência: 'btc_buy:<order_id>'
    idempotency_key       TEXT        NOT NULL,

    -- Timestamps
    signed_at             TIMESTAMPTZ,
    broadcast_at          TIMESTAMPTZ,
    confirmed_at          TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Garantias
    CONSTRAINT uq_btc_treasury_op_idempotency UNIQUE (idempotency_key),
    CONSTRAINT uq_btc_treasury_op_order       UNIQUE (order_id)  -- uma operação por ordem
);

CREATE INDEX IF NOT EXISTS idx_btc_treasury_ops_order
    ON btc_treasury_operations (order_id);

CREATE INDEX IF NOT EXISTS idx_btc_treasury_ops_status
    ON btc_treasury_operations (status)
    WHERE status IN ('pending','processing','signed','broadcast','broadcast_unknown');

CREATE INDEX IF NOT EXISTS idx_btc_treasury_ops_txid
    ON btc_treasury_operations (txid)
    WHERE txid != '';
