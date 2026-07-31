-- Migration 051: add sell_source column to btc_sell_fundings.
--
-- Distinguishes between:
--   'internal' — BTC is already held in the user's ChainFX custodial wallet;
--                the worker credits the funding from the internal balance
--                rather than waiting for an on-chain UTXO deposit.
--   'external' — user sends BTC from their own external wallet to the
--                platform's custodial receive address (original behaviour).
--
-- Default is 'external' so existing rows keep the original semantics without
-- a data migration.
--
-- NÃO executar automaticamente — aplicar manualmente em produção.

ALTER TABLE btc_sell_fundings
  ADD COLUMN IF NOT EXISTS sell_source TEXT NOT NULL DEFAULT 'external'
    CHECK (sell_source IN ('internal', 'external'));

CREATE INDEX IF NOT EXISTS idx_btc_sell_fundings_sell_source
  ON btc_sell_fundings (sell_source, status, created_at);

COMMENT ON COLUMN btc_sell_fundings.sell_source IS
  '''internal'' = BTC already in ChainFX custody (no on-chain deposit required); '
  '''external''  = user sends from their own wallet (default, original behaviour).';
