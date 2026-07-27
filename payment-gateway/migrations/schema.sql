CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS orders (
  id UUID PRIMARY KEY,
  request_id TEXT,
  status VARCHAR(32) NOT NULL,
  amount_brl NUMERIC(18,2) NOT NULL,
  btc_amount NUMERIC(28,8) NOT NULL,
  fee_brl NUMERIC(18,2),
  payout_brl NUMERIC(18,2),
  address TEXT NOT NULL,
  asset VARCHAR(16) NOT NULL DEFAULT 'USDT',
  network VARCHAR(32) NOT NULL DEFAULT 'BSC',
  rate_locked NUMERIC(28,8) NOT NULL,
  rate_lock_expires_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  tx_hash TEXT,
  error TEXT,
  deposit_tx TEXT,
  deposit_amount NUMERIC(28,8),
  pix_cpf TEXT,
  pix_phone TEXT,
  pix_cpf_hash TEXT,
  pix_phone_hash TEXT,
  derivation_index INT
);

CREATE TABLE IF NOT EXISTS order_private (
  order_id UUID PRIMARY KEY REFERENCES orders(id) ON DELETE CASCADE,
  pix_cpf_enc TEXT,
  pix_phone_enc TEXT,
  email_enc TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE order_private ADD COLUMN IF NOT EXISTS email_enc TEXT;

ALTER TABLE orders ADD COLUMN IF NOT EXISTS request_id TEXT;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS pix_cpf_hash TEXT;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS pix_phone_hash TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS idx_orders_deposit_tx_unique ON orders (deposit_tx) WHERE deposit_tx IS NOT NULL AND deposit_tx <> '';

CREATE TABLE IF NOT EXISTS order_events (
  id UUID PRIMARY KEY,
  order_id UUID REFERENCES orders(id),
  request_id TEXT,
  type VARCHAR(64) NOT NULL,
  payload JSONB,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE order_events ADD COLUMN IF NOT EXISTS request_id TEXT;

CREATE TABLE IF NOT EXISTS payouts (
  id UUID PRIMARY KEY,
  order_id UUID REFERENCES orders(id),
  pix_cpf TEXT,
  pix_key TEXT,
  status VARCHAR(32) NOT NULL,
  provider_response JSONB,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS onchain_cursor (
  id SERIAL PRIMARY KEY,
  network VARCHAR(32) NOT NULL UNIQUE,
  last_block BIGINT NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS sweeps (
  id UUID PRIMARY KEY,
  child_index INT NOT NULL,
  from_addr TEXT NOT NULL,
  to_addr TEXT NOT NULL,
  amount NUMERIC(28,8) NOT NULL,
  tx_hash TEXT,
  status VARCHAR(32) NOT NULL DEFAULT 'pending',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  idempotency_key TEXT,
  amount_BNB_fee NUMERIC(28,8),
  order_id UUID REFERENCES orders(id)
);

CREATE TABLE IF NOT EXISTS buy_orders (
  id UUID PRIMARY KEY,
  request_id TEXT,
  status VARCHAR(32) NOT NULL,
  amount_brl NUMERIC(18,2) NOT NULL,
  amount_fiat NUMERIC(18,2),
  fiat_currency VARCHAR(8) NOT NULL DEFAULT 'BRL',
  payment_method VARCHAR(32) NOT NULL DEFAULT 'pix',
  provider_payment_id TEXT,
  fee_brl NUMERIC(18,2),
  payout_brl NUMERIC(18,2),
  crypto_amount NUMERIC(28,8) NOT NULL,
  asset VARCHAR(16) NOT NULL DEFAULT 'USDT',
  network TEXT NOT NULL DEFAULT 'BSC',
  dest_address TEXT NOT NULL,
  rate_locked NUMERIC(28,8) NOT NULL,
  rate_lock_expires_at TIMESTAMPTZ NOT NULL,
  pix_payload JSONB,
  tx_hash_out TEXT,
  error TEXT,
  paid_at TIMESTAMPTZ,
  settled_at TIMESTAMPTZ,
  delivered_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE buy_orders ADD COLUMN IF NOT EXISTS amount_fiat NUMERIC(18,2);
ALTER TABLE buy_orders ADD COLUMN IF NOT EXISTS request_id TEXT;
ALTER TABLE buy_orders ADD COLUMN IF NOT EXISTS fiat_currency VARCHAR(8) NOT NULL DEFAULT 'BRL';
ALTER TABLE buy_orders ADD COLUMN IF NOT EXISTS payment_method VARCHAR(32) NOT NULL DEFAULT 'pix';
ALTER TABLE buy_orders ADD COLUMN IF NOT EXISTS provider_payment_id TEXT;
ALTER TABLE buy_orders ADD COLUMN IF NOT EXISTS paid_at TIMESTAMPTZ;
ALTER TABLE buy_orders ADD COLUMN IF NOT EXISTS settled_at TIMESTAMPTZ;
ALTER TABLE buy_orders ADD COLUMN IF NOT EXISTS delivered_at TIMESTAMPTZ;
ALTER TABLE buy_orders ADD COLUMN IF NOT EXISTS network TEXT NOT NULL DEFAULT 'BSC';
UPDATE buy_orders SET amount_fiat = amount_brl WHERE amount_fiat IS NULL;

CREATE TABLE IF NOT EXISTS buy_order_events (
  id UUID PRIMARY KEY,
  buy_order_id UUID REFERENCES buy_orders(id),
  request_id TEXT,
  type VARCHAR(64) NOT NULL,
  payload JSONB,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE buy_order_events ADD COLUMN IF NOT EXISTS request_id TEXT;

CREATE INDEX IF NOT EXISTS idx_orders_status ON orders(status);
CREATE INDEX IF NOT EXISTS idx_orders_address ON orders(address);
CREATE INDEX IF NOT EXISTS idx_orders_pix_cpf_created ON orders(pix_cpf, created_at);
CREATE INDEX IF NOT EXISTS idx_orders_pix_phone_created ON orders(pix_phone, created_at);
CREATE INDEX IF NOT EXISTS idx_orders_pix_cpf_hash_created ON orders(pix_cpf_hash, created_at);
CREATE INDEX IF NOT EXISTS idx_orders_pix_phone_hash_created ON orders(pix_phone_hash, created_at);
CREATE INDEX IF NOT EXISTS idx_orders_request_id ON orders(request_id);
CREATE INDEX IF NOT EXISTS idx_order_events_lookup ON order_events(order_id, type);
CREATE INDEX IF NOT EXISTS idx_order_events_request_id ON order_events(request_id);
CREATE INDEX IF NOT EXISTS idx_buy_orders_status ON buy_orders(status);
CREATE INDEX IF NOT EXISTS idx_buy_orders_rail ON buy_orders(payment_method, fiat_currency, status);
CREATE INDEX IF NOT EXISTS idx_buy_orders_request_id ON buy_orders(request_id);
CREATE INDEX IF NOT EXISTS idx_buy_order_events_lookup ON buy_order_events(buy_order_id, type);
CREATE INDEX IF NOT EXISTS idx_buy_order_events_request_id ON buy_order_events(request_id);
CREATE INDEX IF NOT EXISTS idx_sweeps_status ON sweeps(status);
CREATE UNIQUE INDEX IF NOT EXISTS idx_buy_webhook_provider_once ON buy_order_events (buy_order_id, (payload ->> 'providerId')) WHERE type = 'webhook.provider' AND payload ? 'providerId';
CREATE UNIQUE INDEX IF NOT EXISTS idx_order_idempotency_once ON order_events (order_id, (payload ->> 'key')) WHERE type = 'idempotency' AND payload ? 'key';

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
CREATE INDEX IF NOT EXISTS idx_buy_liquidity_quotes_order_selected ON buy_liquidity_quotes(buy_order_id, selected, created_at DESC);

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
CREATE INDEX IF NOT EXISTS idx_buy_liquidity_executions_order_created ON buy_liquidity_executions(buy_order_id, created_at DESC);

CREATE TABLE IF NOT EXISTS quotes (
  id TEXT PRIMARY KEY,
  side VARCHAR(8) NOT NULL CHECK (side IN ('buy','sell')),
  asset VARCHAR(16) NOT NULL,
  fiat_currency VARCHAR(8) NOT NULL,
  payment_method VARCHAR(32) NOT NULL DEFAULT 'pix',
  amount_minor BIGINT NOT NULL,
  crypto_amount_units TEXT NOT NULL,
  rate NUMERIC(28,8) NOT NULL,
  market_rate NUMERIC(28,8) NOT NULL DEFAULT 0,
  fee_minor BIGINT NOT NULL DEFAULT 0,
  expires_at TIMESTAMPTZ NOT NULL,
  consumed_at TIMESTAMPTZ,
  api_key_hash TEXT NOT NULL DEFAULT '',
  body_hash TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_quotes_api_key_expires ON quotes(api_key_hash, expires_at);
CREATE INDEX IF NOT EXISTS idx_quotes_consumable ON quotes(expires_at) WHERE consumed_at IS NULL;

CREATE TABLE IF NOT EXISTS idempotency_keys (
  key TEXT NOT NULL,
  operation TEXT NOT NULL,
  api_key_hash TEXT,
  body_hash TEXT NOT NULL,
  status VARCHAR(16) NOT NULL DEFAULT 'started',
  result_type TEXT,
  result_id TEXT,
  response_status INT,
  response_json JSONB,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (key, operation, api_key_hash)
);
ALTER TABLE idempotency_keys ALTER COLUMN api_key_hash SET DEFAULT '';
UPDATE idempotency_keys SET api_key_hash = '' WHERE api_key_hash IS NULL;
ALTER TABLE idempotency_keys ALTER COLUMN api_key_hash SET NOT NULL;
CREATE INDEX IF NOT EXISTS idx_idempotency_result ON idempotency_keys(result_type, result_id);

CREATE TABLE IF NOT EXISTS buy_order_private (
  buy_order_id UUID PRIMARY KEY REFERENCES buy_orders(id) ON DELETE CASCADE,
  email_enc TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS marketing_contacts (
  email TEXT PRIMARY KEY,
  source TEXT,
  subscribed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  unsubscribed_at TIMESTAMPTZ,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
