-- Native BTC mobile SELL funding binding.
-- Reuses btc_wallet_addresses/btc_utxos scanner and keeps payout on the existing manual PIX queue.

CREATE TABLE IF NOT EXISTS btc_sell_fundings (
  order_id UUID PRIMARY KEY REFERENCES orders(id) ON DELETE CASCADE,
  user_id UUID NOT NULL REFERENCES users(id),
  wallet_address_id TEXT NOT NULL,
  btc_address TEXT NOT NULL,
  btc_network TEXT NOT NULL CHECK (btc_network IN ('mainnet','testnet','signet','regtest')),
  expected_sats BIGINT NOT NULL CHECK (expected_sats > 0),
  received_sats BIGINT,
  txid TEXT,
  vout INTEGER,
  confirmations INTEGER NOT NULL DEFAULT 0,
  quote_id TEXT,
  status TEXT NOT NULL DEFAULT 'awaiting_deposit'
    CHECK (status IN ('awaiting_deposit','detected','pending_confirmations','confirmed','manual_review','expired','orphaned')),
  error TEXT,
  detected_at TIMESTAMPTZ,
  confirmed_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_btc_sell_fundings_lookup
  ON btc_sell_fundings(btc_network, user_id, btc_address, status, created_at);

CREATE UNIQUE INDEX IF NOT EXISTS uq_btc_sell_fundings_txid_vout
  ON btc_sell_fundings(btc_network, txid, vout)
  WHERE txid IS NOT NULL AND txid <> '' AND vout IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS uq_btc_sell_fundings_quote
  ON btc_sell_fundings(quote_id)
  WHERE quote_id IS NOT NULL AND quote_id <> '';
