CREATE TABLE IF NOT EXISTS buy_liquidity_quotes (
  id UUID PRIMARY KEY,
  buy_order_id UUID NOT NULL REFERENCES buy_orders(id) ON DELETE CASCADE,
  provider TEXT NOT NULL,
  provider_type TEXT NOT NULL DEFAULT 'liquidity_provider',
  external_quote_id TEXT,
  asset TEXT NOT NULL,
  network TEXT NOT NULL,
  token_contract TEXT,
  token_decimals INTEGER NOT NULL DEFAULT 18,
  fiat_cost_brl NUMERIC(18,2) NOT NULL DEFAULT 0,
  provider_fee_brl NUMERIC(18,2) NOT NULL DEFAULT 0,
  network_fee_brl NUMERIC(18,2) NOT NULL DEFAULT 0,
  spread_brl NUMERIC(18,2) NOT NULL DEFAULT 0,
  total_cost_brl NUMERIC(18,2) NOT NULL DEFAULT 0,
  crypto_amount NUMERIC(28,8) NOT NULL DEFAULT 0,
  delivery_sla_seconds INTEGER NOT NULL DEFAULT 300,
  reliability_bps INTEGER NOT NULL DEFAULT 9000,
  direct_delivery BOOLEAN NOT NULL DEFAULT false,
  selected BOOLEAN NOT NULL DEFAULT false,
  expires_at TIMESTAMPTZ,
  payload JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (buy_order_id, provider, external_quote_id)
);

CREATE INDEX IF NOT EXISTS idx_buy_liquidity_quotes_order_selected
  ON buy_liquidity_quotes(buy_order_id, selected, created_at DESC);

ALTER TABLE buy_liquidity_quotes ADD COLUMN IF NOT EXISTS token_contract TEXT;
ALTER TABLE buy_liquidity_quotes ADD COLUMN IF NOT EXISTS token_decimals INTEGER NOT NULL DEFAULT 18;

CREATE TABLE IF NOT EXISTS buy_liquidity_executions (
  id UUID PRIMARY KEY,
  buy_order_id UUID NOT NULL REFERENCES buy_orders(id) ON DELETE CASCADE,
  quote_id UUID REFERENCES buy_liquidity_quotes(id) ON DELETE SET NULL,
  provider TEXT NOT NULL,
  status TEXT NOT NULL,
  external_order_id TEXT,
  tx_hash TEXT,
  error TEXT,
  payload JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_buy_liquidity_executions_order_created
  ON buy_liquidity_executions(buy_order_id, created_at DESC);
