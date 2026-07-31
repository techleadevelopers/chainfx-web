-- ============================================================
-- Migration 029 - BTC wallet hardening
-- Idempotent send claims, explicit broadcast_unknown, signed tx
-- persistence metadata, and richer input audit data.
-- ============================================================

ALTER TABLE btc_transactions
    DROP CONSTRAINT IF EXISTS uq_btc_tx_network_txid;

CREATE UNIQUE INDEX IF NOT EXISTS uq_btc_tx_network_txid_nonempty
    ON btc_transactions (network, txid)
    WHERE txid <> '';

ALTER TABLE btc_transactions
    DROP CONSTRAINT IF EXISTS btc_transactions_status_check;

ALTER TABLE btc_transactions
    ADD CONSTRAINT btc_transactions_status_check
    CHECK (status IN (
        'created',
        'building',
        'signed',
        'broadcast_unknown',
        'broadcast',
        'pending',
        'confirmed',
        'failed',
        'failed_before_sign',
        'failed_before_broadcast',
        'manual_review',
        'conflicted',
        'replaced',
        'dropped'
    ));

ALTER TABLE btc_transactions
    ADD COLUMN IF NOT EXISTS signed_at TIMESTAMPTZ;

ALTER TABLE btc_utxos
    DROP CONSTRAINT IF EXISTS btc_utxos_status_check;

ALTER TABLE btc_utxos
    ADD CONSTRAINT btc_utxos_status_check
    CHECK (status IN (
        'pending',
        'reorg_pending',
        'confirmed',
        'reserved',
        'spent',
        'orphaned',
        'manual_review'
    ));

ALTER TABLE btc_transaction_inputs
    ADD COLUMN IF NOT EXISTS user_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS wallet_address_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS address TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS derivation_path TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS derivation_index INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS script_pub_key TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS active_spend BOOLEAN NOT NULL DEFAULT true;

CREATE UNIQUE INDEX IF NOT EXISTS uq_btc_active_utxo_spend
    ON btc_transaction_inputs (utxo_id)
    WHERE active_spend = true;
