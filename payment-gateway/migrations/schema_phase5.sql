-- =============================================================================
-- Phase 5: Mobile Expansion — Incremental schema additions
-- Safe to run on top of existing schema.sql (all statements are idempotent).
-- =============================================================================

-- ─── Multi-Asset ─────────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS assets (
  symbol           VARCHAR(16)    PRIMARY KEY,
  name             VARCHAR(64)    NOT NULL,
  network          VARCHAR(32)    NOT NULL DEFAULT 'BSC',
  contract_address TEXT,
  decimals         INT            NOT NULL DEFAULT 18,
  min_amount       NUMERIC(28,8)  NOT NULL DEFAULT 1,
  max_amount       NUMERIC(28,8)  NOT NULL DEFAULT 100000,
  daily_limit      NUMERIC(28,8)  NOT NULL DEFAULT 50000,
  monthly_limit    NUMERIC(28,8)  NOT NULL DEFAULT 500000,
  fee_bps          INT            NOT NULL DEFAULT 100,
  active           BOOLEAN        NOT NULL DEFAULT true,
  created_at       TIMESTAMPTZ    NOT NULL DEFAULT now()
);

-- Seed default assets
INSERT INTO assets (symbol, name, network, contract_address, decimals, min_amount, max_amount, fee_bps)
VALUES
  ('USDT',  'Tether USD',       'BSC', '0x55d398326f99059fF775485246999027B3197955', 18, 1,     100000, 100),
  ('USDC',  'USD Coin',         'BSC', '0x8AC76a51cc950d9822D68b83fE1Ad97B32Cd580d', 18, 1,     100000, 100),
  ('BTCB',  'Bitcoin BEP20',    'BSC', '0x7130d2A12B9BCbFAe4f2634d864A1Ee1Ce3Ead9c', 18, 0.001,   10,   150),
  ('ETH',   'Ethereum BEP20',   'BSC', '0x2170Ed0880ac9A755fd29B2688956BD959F933F8', 18, 0.01,  1000,   150),
  ('BUSD',  'Binance USD',      'BSC', '0xe9e7CEA3DedcA5984780Bafc599bD69ADd087D56', 18, 1,     100000, 100),
  ('EURC',  'Euro Coin',        'BSC', NULL,                                           18, 1,     100000, 100)
ON CONFLICT (symbol) DO NOTHING;

-- ─── Multi-Country ────────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS countries (
  code       VARCHAR(4)   PRIMARY KEY,
  name       VARCHAR(64)  NOT NULL,
  currency   VARCHAR(8)   NOT NULL,
  language   VARCHAR(8)   NOT NULL DEFAULT 'pt-BR',
  active     BOOLEAN      NOT NULL DEFAULT true,
  created_at TIMESTAMPTZ  NOT NULL DEFAULT now()
);

INSERT INTO countries (code, name, currency, language)
VALUES
  ('BR', 'Brasil',         'BRL', 'pt-BR'),
  ('AR', 'Argentina',      'ARS', 'es-AR'),
  ('CL', 'Chile',          'CLP', 'es-CL'),
  ('CO', 'Colômbia',       'COP', 'es-CO'),
  ('MX', 'México',         'MXN', 'es-MX'),
  ('PE', 'Peru',           'PEN', 'es-PE'),
  ('US', 'United States',  'USD', 'en-US'),
  ('EU', 'European Union', 'EUR', 'en-EU')
ON CONFLICT (code) DO NOTHING;

-- ─── Multi-Rail ───────────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS payment_rails (
  id           VARCHAR(32)  PRIMARY KEY,
  country_code VARCHAR(4)   REFERENCES countries(code),
  name         VARCHAR(64)  NOT NULL,
  currency     VARCHAR(8)   NOT NULL,
  active       BOOLEAN      NOT NULL DEFAULT true,
  metadata     JSONB,
  created_at   TIMESTAMPTZ  NOT NULL DEFAULT now()
);

INSERT INTO payment_rails (id, country_code, name, currency, metadata)
VALUES
  ('pix',    'BR', 'PIX',     'BRL', '{"supports_qrcode": true,  "instant": true}'),
  ('spei',   'MX', 'SPEI',    'MXN', '{"supports_clabe": true,   "instant": true}'),
  ('fednow', 'US', 'FedNow',  'USD', '{"instant": true}'),
  ('sepa',   'EU', 'SEPA',    'EUR', '{"instant": false, "days": 1}'),
  ('pse',    'CO', 'PSE',     'COP', '{"instant": false}'),
  ('khipu',  'CL', 'Khipu',   'CLP', '{"instant": true}')
ON CONFLICT (id) DO NOTHING;

-- ─── KYC Async ───────────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS kyc_requests (
  id                   UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id              UUID          NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  level                INT           NOT NULL DEFAULT 1,
  status               VARCHAR(32)   NOT NULL DEFAULT 'pending',
  document_type        VARCHAR(32),
  document_url         TEXT,
  selfie_url           TEXT,
  proof_of_address_url TEXT,
  proof_of_income_url  TEXT,
  reviewer_notes       TEXT,
  submitted_at         TIMESTAMPTZ   NOT NULL DEFAULT now(),
  reviewed_at          TIMESTAMPTZ,
  created_at           TIMESTAMPTZ   NOT NULL DEFAULT now(),
  updated_at           TIMESTAMPTZ   NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_kyc_requests_user    ON kyc_requests(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_kyc_requests_pending ON kyc_requests(status, submitted_at) WHERE status = 'pending';

-- ─── Swaps ────────────────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS swaps (
  id                 UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id            UUID          NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  from_asset         VARCHAR(16)   NOT NULL,
  to_asset           VARCHAR(16)   NOT NULL,
  from_amount        NUMERIC(28,8) NOT NULL,
  to_amount          NUMERIC(28,8),
  rate               NUMERIC(28,8),
  fee_bps            INT           NOT NULL DEFAULT 50,
  slippage_tolerance NUMERIC(6,4)  NOT NULL DEFAULT 0.005,
  status             VARCHAR(32)   NOT NULL DEFAULT 'pending',
  tx_hash            TEXT,
  error              TEXT,
  created_at         TIMESTAMPTZ   NOT NULL DEFAULT now(),
  updated_at         TIMESTAMPTZ   NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_swaps_user   ON swaps(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_swaps_status ON swaps(status) WHERE status IN ('pending', 'executing');

-- ─── DCA Strategies (previously referenced in code but missing from schema) ──

CREATE TABLE IF NOT EXISTS dca_strategies (
  id             UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id        UUID          NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_symbol   VARCHAR(16)   NOT NULL,
  network        TEXT          NOT NULL DEFAULT 'BSC',
  amount_brl     NUMERIC(18,2) NOT NULL,
  frequency      VARCHAR(16)   NOT NULL DEFAULT 'weekly',
  active         BOOLEAN       NOT NULL DEFAULT true,
  total_invested NUMERIC(18,2) NOT NULL DEFAULT 0,
  total_tokens   NUMERIC(28,8) NOT NULL DEFAULT 0,
  next_execution TIMESTAMPTZ,
  created_at     TIMESTAMPTZ   NOT NULL DEFAULT now(),
  updated_at     TIMESTAMPTZ   NOT NULL DEFAULT now()
);

ALTER TABLE dca_strategies ADD COLUMN IF NOT EXISTS network TEXT NOT NULL DEFAULT 'BSC';
CREATE INDEX IF NOT EXISTS idx_dca_user   ON dca_strategies(user_id);
CREATE INDEX IF NOT EXISTS idx_dca_user_network ON dca_strategies(user_id, token_symbol, network);
CREATE INDEX IF NOT EXISTS idx_dca_active ON dca_strategies(active, next_execution) WHERE active = true;

-- ─── Webhook Subscriptions ────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS webhook_subscriptions (
  id                UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id           UUID        REFERENCES users(id) ON DELETE CASCADE,
  provider          VARCHAR(32) NOT NULL DEFAULT 'generic',
  target_url        TEXT        NOT NULL,
  events            TEXT[]      NOT NULL DEFAULT '{}',
  secret            TEXT,
  active            BOOLEAN     NOT NULL DEFAULT true,
  description       TEXT,
  created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_triggered_at TIMESTAMPTZ,
  last_status_code  INT,
  last_error        TEXT,
  failure_count     INT         NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_webhook_subs_user   ON webhook_subscriptions(user_id);
CREATE INDEX IF NOT EXISTS idx_webhook_subs_active ON webhook_subscriptions(active) WHERE active = true;
CREATE INDEX IF NOT EXISTS idx_webhook_subs_events ON webhook_subscriptions USING gin(events);

-- ─── Webhook Deliveries ───────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS webhook_deliveries (
  id              UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
  subscription_id UUID          NOT NULL REFERENCES webhook_subscriptions(id) ON DELETE CASCADE,
  event           VARCHAR(64)   NOT NULL,
  payload         JSONB         NOT NULL,
  status_code     INT           NOT NULL DEFAULT 0,
  ok              BOOLEAN       NOT NULL DEFAULT false,
  error           TEXT,
  attempt         INT           NOT NULL DEFAULT 1,
  created_at      TIMESTAMPTZ   NOT NULL DEFAULT now(),
  updated_at      TIMESTAMPTZ   NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_webhook_del_sub    ON webhook_deliveries(subscription_id, created_at DESC);

-- ─── Agent API Marketplace / M2M Access ─────────────────────────────────────

CREATE TABLE IF NOT EXISTS api_products (
  id               TEXT        PRIMARY KEY,
  name             TEXT        NOT NULL,
  description      TEXT        NOT NULL,
  unit             TEXT        NOT NULL DEFAULT 'request',
  quota_units      INT         NOT NULL,
  price_usdt       NUMERIC(28,8) NOT NULL,
  duration_seconds INT         NOT NULL,
  provider_name    TEXT        NOT NULL DEFAULT 'ChainFX',
  provider_url     TEXT,
  active           BOOLEAN     NOT NULL DEFAULT true,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO api_products (id, name, description, unit, quota_units, price_usdt, duration_seconds, provider_name, provider_url)
VALUES
  ('chainfx-mcp-basic', 'ChainFX MCP Basic', 'Autonomous agent access to ChainFX MCP tools, rates, prompts and automation hooks.', 'tool_call', 10000, 10.00, 2592000, 'ChainFX', 'https://www.chainfx.store'),
  ('api-credit-basic', 'API Credit Basic', 'General API access credits for machine-to-machine stablecoin-paid usage.', 'request', 10000, 10.00, 2592000, 'ChainFX', 'https://www.chainfx.store')
ON CONFLICT (id) DO NOTHING;

CREATE TABLE IF NOT EXISTS agent_wallets (
  address          TEXT        PRIMARY KEY,
  first_seen_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  reputation_tier  TEXT        NOT NULL DEFAULT 'tier0',
  total_spent_usdt NUMERIC(28,8) NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS api_payments (
  id                   UUID        PRIMARY KEY,
  product_id           TEXT        NOT NULL REFERENCES api_products(id),
  buyer_wallet         TEXT        NOT NULL,
  amount_usdt          NUMERIC(28,8) NOT NULL,
  chainfx_fee_usdt     NUMERIC(28,8) NOT NULL,
  provider_amount_usdt NUMERIC(28,8) NOT NULL,
  asset                TEXT        NOT NULL DEFAULT 'USDT',
  network              TEXT        NOT NULL DEFAULT 'BSC',
  payment_address      TEXT        NOT NULL,
  memo                 TEXT        NOT NULL UNIQUE,
  nonce                TEXT        NOT NULL,
  request_hash         TEXT        NOT NULL,
  status               TEXT        NOT NULL DEFAULT 'pending',
  tx_hash              TEXT        UNIQUE,
  idempotency_key      TEXT        UNIQUE,
  quote_expires_at     TIMESTAMPTZ NOT NULL,
  confirmed_at         TIMESTAMPTZ,
  created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_api_payments_wallet ON api_payments(buyer_wallet, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_api_payments_status ON api_payments(status, quote_expires_at);

CREATE TABLE IF NOT EXISTS api_access_grants (
  id                UUID        PRIMARY KEY,
  payment_id        UUID        NOT NULL UNIQUE REFERENCES api_payments(id),
  product_id        TEXT        NOT NULL REFERENCES api_products(id),
  buyer_wallet      TEXT        NOT NULL,
  access_token_hash TEXT        NOT NULL UNIQUE,
  quota_total       INT         NOT NULL,
  quota_remaining   INT         NOT NULL,
  expires_at        TIMESTAMPTZ NOT NULL,
  status            TEXT        NOT NULL DEFAULT 'active',
  created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_api_access_grants_wallet ON api_access_grants(buyer_wallet, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_api_access_grants_status ON api_access_grants(status, expires_at);

CREATE TABLE IF NOT EXISTS api_usage_events (
  id              UUID        PRIMARY KEY,
  grant_id        UUID        NOT NULL REFERENCES api_access_grants(id),
  product_id      TEXT        NOT NULL REFERENCES api_products(id),
  units           INT         NOT NULL,
  request_hash    TEXT,
  idempotency_key TEXT        NOT NULL,
  metadata        JSONB,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (grant_id, idempotency_key)
);

CREATE INDEX IF NOT EXISTS idx_api_usage_events_grant ON api_usage_events(grant_id, created_at DESC);

CREATE TABLE IF NOT EXISTS api_request_logs (
  id            UUID        PRIMARY KEY,
  request_id    TEXT        NOT NULL,
  method        VARCHAR(12) NOT NULL,
  path          TEXT        NOT NULL,
  route_class   VARCHAR(32) NOT NULL,
  status_code   INT         NOT NULL,
  duration_ms   BIGINT      NOT NULL,
  api_key_hash  TEXT,
  api_key_scope VARCHAR(32),
  auth_mode     VARCHAR(32),
  client_ip     TEXT,
  user_agent    TEXT,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_api_request_logs_created ON api_request_logs(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_api_request_logs_route ON api_request_logs(route_class, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_api_request_logs_key ON api_request_logs(api_key_hash, created_at DESC) WHERE api_key_hash IS NOT NULL;

CREATE TABLE IF NOT EXISTS mcp_tool_logs (
  id            UUID        PRIMARY KEY,
  request_id    TEXT,
  tool_name     TEXT        NOT NULL,
  status        VARCHAR(32) NOT NULL,
  error_message TEXT,
  duration_ms   BIGINT      NOT NULL DEFAULT 0,
  api_key_hash  TEXT,
  auth_mode     VARCHAR(32),
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_mcp_tool_logs_created ON mcp_tool_logs(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_mcp_tool_logs_tool ON mcp_tool_logs(tool_name, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_mcp_tool_logs_key ON mcp_tool_logs(api_key_hash, created_at DESC) WHERE api_key_hash IS NOT NULL;

CREATE TABLE IF NOT EXISTS agent_supported_assets (
  symbol           TEXT        NOT NULL,
  network          TEXT        NOT NULL DEFAULT 'BSC',
  contract_address TEXT        NOT NULL,
  decimals         INT         NOT NULL DEFAULT 18,
  fee_bps          INT         NOT NULL DEFAULT 600,
  min_amount       NUMERIC(28,8) NOT NULL DEFAULT 5,
  status           TEXT        NOT NULL DEFAULT 'active',
  enabled          BOOLEAN     NOT NULL DEFAULT true,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (symbol, network)
);

ALTER TABLE agent_supported_assets ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'active';

INSERT INTO agent_supported_assets (symbol, network, contract_address, decimals, fee_bps, min_amount, status, enabled)
VALUES
  ('USDT', 'BSC', '0x55d398326f99059fF775485246999027B3197955', 18, 600, 5.00, 'active', true),
  ('USDC', 'BSC', '0x8ac76a51cc950d9822d68b83fe1ad97b32cd580d', 18, 600, 5.00, 'active', true),
  ('USDT', 'POLYGON', '0xc2132D05D31c914a87C6611C10748AEb04B58e8F', 6, 600, 5.00, 'active', true),
  ('USDC', 'POLYGON', '0x2791Bca1f2de4661ED88A30C99A7a9449Aa84174', 6, 600, 5.00, 'active', true),
  ('BUSD', 'BSC', '0xe9e7cea3dedca5984780bafc599b69add087d56', 18, 600, 5.00, 'legacy', false)
ON CONFLICT (symbol, network) DO NOTHING;

UPDATE agent_supported_assets SET status = 'legacy', enabled = false WHERE symbol = 'BUSD' AND network = 'BSC';

CREATE TABLE IF NOT EXISTS agent_trade_intents (
  id                     UUID        PRIMARY KEY,
  agent_wallet           TEXT        NOT NULL,
  pay_asset              TEXT        NOT NULL,
  receive_asset          TEXT        NOT NULL,
  pay_amount             NUMERIC(28,8) NOT NULL,
  receive_amount         NUMERIC(28,8) NOT NULL,
  chainfx_fee_amount     NUMERIC(28,8) NOT NULL,
  fee_bps                INT         NOT NULL,
  network                TEXT        NOT NULL DEFAULT 'BSC',
  payment_address        TEXT        NOT NULL,
  destination_wallet     TEXT        NOT NULL,
  pay_token_contract     TEXT        NOT NULL,
  receive_token_contract TEXT        NOT NULL,
  nonce                  TEXT        NOT NULL,
  request_hash           TEXT        NOT NULL,
  status                 TEXT        NOT NULL DEFAULT 'pending',
  tx_hash                TEXT,
  chain_id               BIGINT,
  log_index              INT,
  block_number           BIGINT,
  block_hash             TEXT,
  transfer_from          TEXT,
  transfer_to            TEXT,
  transfer_amount_raw    TEXT,
  overpayment_amount     NUMERIC(28,8) NOT NULL DEFAULT 0,
  settlement_tx_hash     TEXT,
  idempotency_key        TEXT        UNIQUE,
  expires_at             TIMESTAMPTZ NOT NULL,
  paid_at                TIMESTAMPTZ,
  settled_at             TIMESTAMPTZ,
  created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at             TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE agent_trade_intents DROP CONSTRAINT IF EXISTS agent_trade_intents_tx_hash_key;
ALTER TABLE agent_trade_intents ADD COLUMN IF NOT EXISTS chain_id BIGINT;
ALTER TABLE agent_trade_intents ADD COLUMN IF NOT EXISTS log_index INT;
ALTER TABLE agent_trade_intents ADD COLUMN IF NOT EXISTS block_number BIGINT;
ALTER TABLE agent_trade_intents ADD COLUMN IF NOT EXISTS block_hash TEXT;
ALTER TABLE agent_trade_intents ADD COLUMN IF NOT EXISTS transfer_from TEXT;
ALTER TABLE agent_trade_intents ADD COLUMN IF NOT EXISTS transfer_to TEXT;
ALTER TABLE agent_trade_intents ADD COLUMN IF NOT EXISTS transfer_amount_raw TEXT;
ALTER TABLE agent_trade_intents ADD COLUMN IF NOT EXISTS overpayment_amount NUMERIC(28,8) NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_agent_trade_wallet ON agent_trade_intents(agent_wallet, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_agent_trade_status ON agent_trade_intents(status, expires_at);
CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_trade_payment_log ON agent_trade_intents(chain_id, tx_hash, log_index) WHERE tx_hash IS NOT NULL AND log_index IS NOT NULL;

-- Premium Marketplace / Agent API Commerce
CREATE TABLE IF NOT EXISTS marketplace_providers (
  id TEXT PRIMARY KEY,
  slug TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  description TEXT NOT NULL,
  website_url TEXT,
  settlement_wallet TEXT NOT NULL,
  settlement_asset TEXT NOT NULL DEFAULT 'USDT',
  settlement_network TEXT NOT NULL DEFAULT 'BSC',
  status TEXT NOT NULL DEFAULT 'pending',
  metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_marketplace_providers_settlement_wallet_evm') THEN
    ALTER TABLE marketplace_providers
      ADD CONSTRAINT chk_marketplace_providers_settlement_wallet_evm
      CHECK (settlement_wallet ~* '^0x[0-9a-f]{40}$');
  END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_mp_providers_status ON marketplace_providers(status);

CREATE TABLE IF NOT EXISTS marketplace_capabilities (
  id TEXT PRIMARY KEY,
  slug TEXT NOT NULL UNIQUE,
  display_name TEXT NOT NULL,
  description TEXT NOT NULL,
  category TEXT NOT NULL,
  routing_mode TEXT NOT NULL DEFAULT 'best_available',
  status TEXT NOT NULL DEFAULT 'active',
  operations_json JSONB NOT NULL DEFAULT '[]'::jsonb,
  metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_mp_capabilities_category_status ON marketplace_capabilities(category, status);
CREATE INDEX IF NOT EXISTS idx_mp_capabilities_routing ON marketplace_capabilities(routing_mode, status);

CREATE TABLE IF NOT EXISTS marketplace_capability_contracts (
  id TEXT PRIMARY KEY,
  capability_id TEXT NOT NULL REFERENCES marketplace_capabilities(id),
  version TEXT NOT NULL DEFAULT 'v1',
  status TEXT NOT NULL DEFAULT 'active',
  input_schema_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  output_schema_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  examples_json JSONB NOT NULL DEFAULT '[]'::jsonb,
  metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(capability_id, version)
);

CREATE INDEX IF NOT EXISTS idx_mp_capability_contracts_lookup ON marketplace_capability_contracts(capability_id, status, version);

CREATE TABLE IF NOT EXISTS marketplace_capability_providers (
  id TEXT PRIMARY KEY,
  capability_id TEXT NOT NULL REFERENCES marketplace_capabilities(id),
  provider_id TEXT REFERENCES marketplace_providers(id),
  provider_slug TEXT NOT NULL,
  provider_name TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'planned',
  routing_priority INT NOT NULL DEFAULT 100,
  metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(capability_id, provider_slug)
);

CREATE INDEX IF NOT EXISTS idx_mp_capability_providers_capability_status ON marketplace_capability_providers(capability_id, status);

CREATE TABLE IF NOT EXISTS marketplace_routes (
  id TEXT PRIMARY KEY,
  capability_id TEXT NOT NULL REFERENCES marketplace_capabilities(id),
  route_name TEXT NOT NULL,
  routing_mode TEXT NOT NULL DEFAULT 'best_available',
  status TEXT NOT NULL DEFAULT 'active',
  fallback_enabled BOOLEAN NOT NULL DEFAULT true,
  metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(capability_id, route_name)
);

CREATE INDEX IF NOT EXISTS idx_mp_routes_capability_status ON marketplace_routes(capability_id, status);

CREATE TABLE IF NOT EXISTS marketplace_provider_policies (
  id TEXT PRIMARY KEY,
  capability_id TEXT NOT NULL REFERENCES marketplace_capabilities(id),
  provider_slug TEXT NOT NULL,
  priority INT NOT NULL DEFAULT 100,
  cost_score INT NOT NULL DEFAULT 100,
  latency_ms INT NOT NULL DEFAULT 1000,
  quality_score INT NOT NULL DEFAULT 50,
  success_rate_bps INT NOT NULL DEFAULT 10000,
  region TEXT NOT NULL DEFAULT 'global',
  fallback_order INT NOT NULL DEFAULT 100,
  max_units_per_request INT,
  status TEXT NOT NULL DEFAULT 'active',
  policy_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(capability_id, provider_slug)
);

CREATE INDEX IF NOT EXISTS idx_mp_provider_policies_capability_status ON marketplace_provider_policies(capability_id, status);
CREATE INDEX IF NOT EXISTS idx_mp_provider_policies_routing ON marketplace_provider_policies(capability_id, status, priority, cost_score, latency_ms, quality_score);

ALTER TABLE marketplace_provider_policies ADD COLUMN IF NOT EXISTS cost_score INT NOT NULL DEFAULT 100;
ALTER TABLE marketplace_provider_policies ADD COLUMN IF NOT EXISTS latency_ms INT NOT NULL DEFAULT 1000;
ALTER TABLE marketplace_provider_policies ADD COLUMN IF NOT EXISTS quality_score INT NOT NULL DEFAULT 50;
ALTER TABLE marketplace_provider_policies ADD COLUMN IF NOT EXISTS success_rate_bps INT NOT NULL DEFAULT 10000;
ALTER TABLE marketplace_provider_policies ADD COLUMN IF NOT EXISTS region TEXT NOT NULL DEFAULT 'global';
ALTER TABLE marketplace_provider_policies ADD COLUMN IF NOT EXISTS fallback_order INT NOT NULL DEFAULT 100;

CREATE TABLE IF NOT EXISTS marketplace_products (
  id TEXT PRIMARY KEY,
  provider_id TEXT NOT NULL REFERENCES marketplace_providers(id),
  slug TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  description TEXT NOT NULL,
  category TEXT NOT NULL,
  delivery_type TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'draft',
  capability TEXT NOT NULL,
  documentation_url TEXT,
  endpoint_base_url TEXT,
  metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE marketplace_products ADD COLUMN IF NOT EXISTS capability_id TEXT REFERENCES marketplace_capabilities(id);
CREATE INDEX IF NOT EXISTS idx_mp_products_lookup ON marketplace_products(category, status);
CREATE INDEX IF NOT EXISTS idx_mp_products_provider_status ON marketplace_products(provider_id, status);
CREATE INDEX IF NOT EXISTS idx_mp_products_capability_status ON marketplace_products(capability_id, status);

CREATE TABLE IF NOT EXISTS marketplace_plans (
  id TEXT PRIMARY KEY,
  product_id TEXT NOT NULL REFERENCES marketplace_products(id),
  slug TEXT NOT NULL,
  name TEXT NOT NULL,
  price_amount NUMERIC(28,6) NOT NULL,
  payment_asset TEXT NOT NULL,
  network TEXT NOT NULL DEFAULT 'BSC',
  take_rate_bps INT NOT NULL DEFAULT 2000,
  quota INT NOT NULL,
  validity_seconds INT NOT NULL,
  status TEXT NOT NULL DEFAULT 'active',
  metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(product_id, slug)
);

CREATE INDEX IF NOT EXISTS idx_mp_plans_product_status ON marketplace_plans(product_id, status);
CREATE INDEX IF NOT EXISTS idx_mp_plans_asset_network_status ON marketplace_plans(payment_asset, network, status);

CREATE TABLE IF NOT EXISTS marketplace_purchases (
  id TEXT PRIMARY KEY,
  provider_id TEXT NOT NULL REFERENCES marketplace_providers(id),
  product_id TEXT NOT NULL REFERENCES marketplace_products(id),
  plan_id TEXT NOT NULL REFERENCES marketplace_plans(id),
  agent_wallet TEXT NOT NULL,
  payer_wallet TEXT NOT NULL,
  payment_address TEXT NOT NULL,
  payment_asset TEXT NOT NULL,
  payment_contract TEXT NOT NULL,
  network TEXT NOT NULL DEFAULT 'BSC',
  chain_id BIGINT NOT NULL DEFAULT 56,
  gross_amount NUMERIC(28,6) NOT NULL,
  chainfx_amount NUMERIC(28,6) NOT NULL,
  provider_amount NUMERIC(28,6) NOT NULL,
  take_rate_bps INT NOT NULL,
  request_hash TEXT NOT NULL UNIQUE,
  nonce TEXT NOT NULL,
  idempotency_key TEXT UNIQUE,
  expires_at TIMESTAMPTZ NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending_payment',
  tx_hash TEXT,
  tx_log_index INT,
  tx_block_number BIGINT,
  tx_block_hash TEXT,
  transfer_from TEXT,
  transfer_to TEXT,
  transfer_amount_raw TEXT,
  overpayment_amount NUMERIC(28,6) NOT NULL DEFAULT 0,
  paid_at TIMESTAMPTZ,
  granted_at TIMESTAMPTZ,
  failed_at TIMESTAMPTZ,
  failure_code TEXT,
  failure_message TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_marketplace_purchase_nonce ON marketplace_purchases(nonce);
CREATE UNIQUE INDEX IF NOT EXISTS idx_marketplace_purchase_payment_log ON marketplace_purchases(chain_id, tx_hash, tx_log_index) WHERE tx_hash IS NOT NULL AND tx_log_index IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_marketplace_purchases_wallet ON marketplace_purchases(agent_wallet, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_marketplace_purchases_status ON marketplace_purchases(status, expires_at);

CREATE TABLE IF NOT EXISTS marketplace_provider_settlements (
  id TEXT PRIMARY KEY,
  provider_id TEXT NOT NULL REFERENCES marketplace_providers(id),
  purchase_id TEXT NOT NULL UNIQUE REFERENCES marketplace_purchases(id),
  asset TEXT NOT NULL,
  network TEXT NOT NULL,
  gross_amount NUMERIC(28,6) NOT NULL,
  chainfx_amount NUMERIC(28,6) NOT NULL,
  provider_amount NUMERIC(28,6) NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending',
  settlement_wallet TEXT NOT NULL,
  tx_hash TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  settled_at TIMESTAMPTZ,
  failed_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS marketplace_agent_identities (
  agent_id TEXT PRIMARY KEY,
  wallet TEXT,
  name TEXT,
  api_key_hash TEXT NOT NULL UNIQUE,
  capabilities_json JSONB NOT NULL DEFAULT '[]'::jsonb,
  status TEXT NOT NULL DEFAULT 'active',
  metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_mp_agent_identities_wallet ON marketplace_agent_identities(wallet) WHERE wallet IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_mp_agent_identities_status ON marketplace_agent_identities(status);

CREATE TABLE IF NOT EXISTS marketplace_execution_events (
  id TEXT PRIMARY KEY,
  capability_id TEXT NOT NULL REFERENCES marketplace_capabilities(id),
  grant_id UUID NOT NULL REFERENCES api_access_grants(id),
  product_id TEXT NOT NULL,
  provider_slug TEXT NOT NULL,
  provider_name TEXT NOT NULL,
  route_name TEXT NOT NULL,
  routing_mode TEXT NOT NULL,
  operation TEXT NOT NULL,
  request_id TEXT NOT NULL,
  idempotency_key TEXT NOT NULL UNIQUE,
  units_consumed INT NOT NULL,
  quota_remaining INT NOT NULL,
  status TEXT NOT NULL,
  input_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  output_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  latency_ms INT NOT NULL DEFAULT 0,
  error_code TEXT,
  error_message TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_mp_execution_events_capability ON marketplace_execution_events(capability_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_mp_execution_events_grant ON marketplace_execution_events(grant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_mp_execution_events_provider ON marketplace_execution_events(provider_slug, created_at DESC);

ALTER TABLE marketplace_execution_events ADD COLUMN IF NOT EXISTS latency_ms INT NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS marketplace_memory_entries (
  id TEXT PRIMARY KEY,
  namespace TEXT NOT NULL,
  memory_key TEXT NOT NULL,
  content TEXT NOT NULL,
  metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  status TEXT NOT NULL DEFAULT 'active',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(namespace, memory_key)
);

CREATE INDEX IF NOT EXISTS idx_mp_memory_entries_namespace_status ON marketplace_memory_entries(namespace, status, updated_at DESC);

ALTER TABLE api_access_grants DROP CONSTRAINT IF EXISTS api_access_grants_payment_id_fkey;
ALTER TABLE api_access_grants DROP CONSTRAINT IF EXISTS api_access_grants_product_id_fkey;
ALTER TABLE api_access_grants ALTER COLUMN payment_id DROP NOT NULL;
ALTER TABLE api_access_grants ADD COLUMN IF NOT EXISTS purchase_id TEXT UNIQUE;
ALTER TABLE api_access_grants ADD COLUMN IF NOT EXISTS plan_id TEXT;
ALTER TABLE api_access_grants ADD COLUMN IF NOT EXISTS quota_used INT NOT NULL DEFAULT 0;
ALTER TABLE api_access_grants ADD COLUMN IF NOT EXISTS valid_from TIMESTAMPTZ NOT NULL DEFAULT now();
ALTER TABLE api_usage_events DROP CONSTRAINT IF EXISTS api_usage_events_product_id_fkey;

INSERT INTO marketplace_providers (id, slug, name, description, website_url, settlement_wallet, settlement_asset, settlement_network, status)
VALUES
  ('provider_chainfx_demo', 'chainfx-demo', 'ChainFX Demo Provider', 'Demo provider for premium agent marketplace capabilities.', 'https://www.chainfx.store', '0x000000000000000000000000000000000000dEaD', 'USDT', 'BSC', 'active')
ON CONFLICT (id) DO NOTHING;

INSERT INTO marketplace_capabilities (id, slug, display_name, description, category, routing_mode, status, operations_json)
VALUES
  ('semantic_memory', 'semantic-memory', 'Memory', 'Persistent and semantic memory primitives for AI agents.', 'ai', 'best_available', 'active', '["save_memory","get_memory","semantic_search","knowledge_lookup"]'::jsonb),
  ('llm_chat', 'llm-chat', 'Chat LLM', 'Provider-routed text generation, chat, summarization and classification.', 'ai', 'best_available', 'active', '["generate_text","chat","summarize","classify"]'::jsonb),
  ('document_ocr', 'document-ocr', 'Document OCR', 'Extract and structure text from documents and invoices.', 'ai', 'best_available', 'active', '["extract_text","parse_invoice","parse_document"]'::jsonb),
  ('payments_fx', 'payments-fx', 'Payments / FX', 'Agent payment, FX quote, wallet and settlement capabilities.', 'finance', 'best_available', 'active', '["create_payment","quote_fx","settle_provider","wallet_balance"]'::jsonb),
  ('capability_discovery', 'capability-discovery', 'Discovery', 'Capability search, route estimation and provider choice for agents.', 'data', 'best_available', 'active', '["search_capability","list_providers","estimate_cost","choose_route"]'::jsonb),
  ('aml_screening', 'aml-screening', 'AML Screening', 'Compliance screening capability for wallet and payment workflows.', 'security', 'best_available', 'active', '["screen_wallet","screen_counterparty","check_sanctions"]'::jsonb)
ON CONFLICT (id) DO UPDATE SET
  display_name = EXCLUDED.display_name,
  description = EXCLUDED.description,
  category = EXCLUDED.category,
  routing_mode = EXCLUDED.routing_mode,
  status = EXCLUDED.status,
  operations_json = EXCLUDED.operations_json,
  updated_at = now();

INSERT INTO marketplace_capability_contracts (id, capability_id, version, status, input_schema_json, output_schema_json, examples_json, metadata_json)
VALUES
  ('mcc_semantic_memory_v1', 'semantic_memory', 'v1', 'active',
   '{"type":"object","properties":{"operation":{"type":"string","enum":["save_memory","get_memory","semantic_search","knowledge_lookup"]},"namespace":{"type":"string"},"key":{"type":"string"},"content":{"type":"string"},"query":{"type":"string"}},"additionalProperties":true}'::jsonb,
   '{"type":"object","properties":{"mode":{"type":"string"},"provider":{"type":"string"},"operation":{"type":"string"},"memoryId":{"type":"string"},"saved":{"type":"boolean"},"found":{"type":"boolean"},"content":{"type":"string"},"results":{"type":"array"}},"additionalProperties":true}'::jsonb,
   '[{"operation":"save_memory","input":{"namespace":"agent","key":"deal-1","content":"Client prefers USDT settlement."}},{"operation":"semantic_search","input":{"namespace":"agent","query":"settlement"}}]'::jsonb,
   '{"positioning":"native ChainFX memory for agents"}'::jsonb),
  ('mcc_llm_chat_v1', 'llm_chat', 'v1', 'active',
   '{"type":"object","properties":{"operation":{"type":"string","enum":["generate_text","chat","summarize","classify"]},"prompt":{"type":"string"},"messages":{"type":"array"},"text":{"type":"string"}},"additionalProperties":true}'::jsonb,
   '{"type":"object","properties":{"mode":{"type":"string"},"provider":{"type":"string"},"operation":{"type":"string"},"text":{"type":"string"}},"required":["text"],"additionalProperties":true}'::jsonb,
   '[{"operation":"summarize","input":{"text":"Long document text"}}]'::jsonb,
   '{"providerClass":"openai_compatible"}'::jsonb),
  ('mcc_document_ocr_v1', 'document_ocr', 'v1', 'active',
   '{"type":"object","properties":{"operation":{"type":"string","enum":["extract_text","parse_invoice","parse_document"]},"fileUrl":{"type":"string"},"fileBase64":{"type":"string"},"mimeType":{"type":"string"}},"additionalProperties":true}'::jsonb,
   '{"type":"object","properties":{"mode":{"type":"string"},"provider":{"type":"string"},"operation":{"type":"string"},"text":{"type":"string"},"pages":{"type":"integer"},"fields":{"type":"object"}},"additionalProperties":true}'::jsonb,
   '[{"operation":"extract_text","input":{"fileUrl":"https://example.com/invoice.pdf"}}]'::jsonb,
   '{"providerClass":"http_adapter"}'::jsonb),
  ('mcc_payments_fx_v1', 'payments_fx', 'v1', 'active',
   '{"type":"object","properties":{"operation":{"type":"string","enum":["create_payment","quote_fx","settle_provider","wallet_balance"]},"asset":{"type":"string"},"amount":{"type":"string"},"wallet":{"type":"string"}},"additionalProperties":true}'::jsonb,
   '{"type":"object","properties":{"mode":{"type":"string"},"provider":{"type":"string"},"message":{"type":"string"},"status":{"type":"string"}},"additionalProperties":true}'::jsonb,
   '[{"operation":"quote_fx","input":{"asset":"USDT","amount":"100.000000"}}]'::jsonb,
   '{"realSettlement":"agent_rail"}'::jsonb),
  ('mcc_capability_discovery_v1', 'capability_discovery', 'v1', 'active',
   '{"type":"object","properties":{"operation":{"type":"string","enum":["search_capability","list_providers","estimate_cost","choose_route"]},"query":{"type":"string"},"capability":{"type":"string"}},"additionalProperties":true}'::jsonb,
   '{"type":"object","properties":{"mode":{"type":"string"},"results":{"type":"array"},"providers":{"type":"array"},"selected":{"type":"object"}},"additionalProperties":true}'::jsonb,
   '[{"operation":"search_capability","input":{"query":"market data"}}]'::jsonb,
   '{"discovery":"capability_network"}'::jsonb),
  ('mcc_aml_screening_v1', 'aml_screening', 'v1', 'active',
   '{"type":"object","properties":{"operation":{"type":"string","enum":["screen_wallet","screen_counterparty","check_sanctions"]},"wallet":{"type":"string"},"counterparty":{"type":"string"}},"additionalProperties":true}'::jsonb,
   '{"type":"object","properties":{"mode":{"type":"string"},"risk":{"type":"string"},"matches":{"type":"array"},"status":{"type":"string"}},"additionalProperties":true}'::jsonb,
   '[{"operation":"screen_wallet","input":{"wallet":"0x0000000000000000000000000000000000000000"}}]'::jsonb,
   '{"compliance":"demo"}'::jsonb)
ON CONFLICT (capability_id, version) DO UPDATE SET
  status = EXCLUDED.status,
  input_schema_json = EXCLUDED.input_schema_json,
  output_schema_json = EXCLUDED.output_schema_json,
  examples_json = EXCLUDED.examples_json,
  metadata_json = EXCLUDED.metadata_json,
  updated_at = now();

INSERT INTO marketplace_capability_providers (id, capability_id, provider_id, provider_slug, provider_name, status, routing_priority)
VALUES
  ('mcp_llm_openai', 'llm_chat', NULL, 'openai', 'OpenAI', 'active', 10),
  ('mcp_llm_anthropic', 'llm_chat', NULL, 'anthropic', 'Anthropic', 'planned', 20),
  ('mcp_llm_gemini', 'llm_chat', NULL, 'gemini', 'Gemini', 'planned', 30),
  ('mcp_ocr_http', 'document_ocr', 'provider_chainfx_demo', 'chainfx-ocr-http', 'ChainFX OCR HTTP Adapter', 'active', 5),
  ('mcp_ocr_google', 'document_ocr', NULL, 'google-vision', 'Google Vision', 'planned', 10),
  ('mcp_ocr_azure', 'document_ocr', NULL, 'azure-vision', 'Azure Vision', 'planned', 20),
  ('mcp_ocr_aws', 'document_ocr', NULL, 'aws-textract', 'AWS Textract', 'planned', 30),
  ('mcp_memory_chainfx', 'semantic_memory', 'provider_chainfx_demo', 'chainfx-memory', 'ChainFX Memory', 'active', 10),
  ('mcp_payments_chainfx', 'payments_fx', 'provider_chainfx_demo', 'chainfx-rail', 'ChainFX Agent Rail', 'active', 10),
  ('mcp_discovery_chainfx', 'capability_discovery', 'provider_chainfx_demo', 'chainfx-discovery', 'ChainFX Discovery', 'active', 10),
  ('mcp_aml_chainfx', 'aml_screening', 'provider_chainfx_demo', 'chainfx-aml-demo', 'ChainFX AML Demo', 'active', 10)
ON CONFLICT (capability_id, provider_slug) DO UPDATE SET
  provider_name = EXCLUDED.provider_name,
  status = EXCLUDED.status,
  routing_priority = EXCLUDED.routing_priority,
  updated_at = now();

INSERT INTO marketplace_routes (id, capability_id, route_name, routing_mode, status, fallback_enabled)
VALUES
  ('mpr_semantic_memory_default', 'semantic_memory', 'default', 'best_available', 'active', true),
  ('mpr_llm_chat_default', 'llm_chat', 'default', 'best_available', 'active', true),
  ('mpr_document_ocr_default', 'document_ocr', 'default', 'best_available', 'active', true),
  ('mpr_payments_fx_default', 'payments_fx', 'default', 'best_available', 'active', true),
  ('mpr_capability_discovery_default', 'capability_discovery', 'default', 'best_available', 'active', true),
  ('mpr_aml_screening_default', 'aml_screening', 'default', 'best_available', 'active', true)
ON CONFLICT (capability_id, route_name) DO UPDATE SET
  routing_mode = EXCLUDED.routing_mode,
  status = EXCLUDED.status,
  fallback_enabled = EXCLUDED.fallback_enabled,
  updated_at = now();

INSERT INTO marketplace_provider_policies (id, capability_id, provider_slug, priority, cost_score, latency_ms, quality_score, success_rate_bps, region, fallback_order, status, policy_json)
VALUES
  ('mpp_llm_openai', 'llm_chat', 'openai', 10, 35, 650, 92, 9900, 'global', 10, 'active', '{"execution":"openai_compatible","env":"OPENAI_API_KEY","fallback":"mock"}'::jsonb),
  ('mpp_llm_anthropic', 'llm_chat', 'anthropic', 20, 45, 700, 94, 9800, 'global', 20, 'planned', '{"execution":"mock"}'::jsonb),
  ('mpp_llm_gemini', 'llm_chat', 'gemini', 30, 25, 800, 88, 9750, 'global', 30, 'planned', '{"execution":"mock"}'::jsonb),
  ('mpp_ocr_http', 'document_ocr', 'chainfx-ocr-http', 5, 20, 500, 80, 9900, 'global', 5, 'active', '{"execution":"http_adapter","env":"CAPABILITY_OCR_URL","fallback":"mock"}'::jsonb),
  ('mpp_ocr_google', 'document_ocr', 'google-vision', 10, 40, 700, 90, 9850, 'global', 10, 'planned', '{"execution":"mock"}'::jsonb),
  ('mpp_ocr_azure', 'document_ocr', 'azure-vision', 20, 42, 750, 89, 9825, 'global', 20, 'planned', '{"execution":"mock"}'::jsonb),
  ('mpp_ocr_aws', 'document_ocr', 'aws-textract', 30, 38, 800, 91, 9800, 'global', 30, 'planned', '{"execution":"mock"}'::jsonb),
  ('mpp_memory_chainfx', 'semantic_memory', 'chainfx-memory', 10, 5, 40, 75, 9990, 'global', 10, 'active', '{"execution":"native_postgres"}'::jsonb),
  ('mpp_payments_chainfx', 'payments_fx', 'chainfx-rail', 10, 10, 120, 90, 9950, 'global', 10, 'active', '{"execution":"mock","realSettlement":"agent_rail"}'::jsonb),
  ('mpp_discovery_chainfx', 'capability_discovery', 'chainfx-discovery', 10, 5, 50, 85, 9990, 'global', 10, 'active', '{"execution":"mock"}'::jsonb),
  ('mpp_aml_chainfx', 'aml_screening', 'chainfx-aml-demo', 10, 30, 600, 78, 9900, 'global', 10, 'active', '{"execution":"mock"}'::jsonb)
ON CONFLICT (capability_id, provider_slug) DO UPDATE SET
  priority = EXCLUDED.priority,
  cost_score = EXCLUDED.cost_score,
  latency_ms = EXCLUDED.latency_ms,
  quality_score = EXCLUDED.quality_score,
  success_rate_bps = EXCLUDED.success_rate_bps,
  region = EXCLUDED.region,
  fallback_order = EXCLUDED.fallback_order,
  status = EXCLUDED.status,
  policy_json = EXCLUDED.policy_json,
  updated_at = now();

INSERT INTO marketplace_products (id, provider_id, slug, name, description, category, delivery_type, status, capability, documentation_url, endpoint_base_url)
VALUES
  ('prod_fx_enterprise', 'provider_chainfx_demo', 'fx-enterprise-api', 'FX Enterprise API', 'Enterprise FX rates and settlement data for agents.', 'finance', 'api_access', 'active', 'fx_enterprise', 'https://www.chainfx.store/developers', 'https://www.chainfx.store'),
  ('prod_ocr_enterprise', 'provider_chainfx_demo', 'ocr-enterprise', 'OCR Enterprise', 'Document OCR capability for autonomous workflows.', 'ai', 'api_access', 'active', 'document_ocr', 'https://www.chainfx.store/developers', 'https://www.chainfx.store'),
  ('prod_aml_screening', 'provider_chainfx_demo', 'aml-screening', 'AML Screening', 'AML screening capability for payment and wallet workflows.', 'security', 'api_access', 'active', 'aml_screening', 'https://www.chainfx.store/developers', 'https://www.chainfx.store'),
  ('prod_gpt_business', 'provider_chainfx_demo', 'gpt-business-credits', 'GPT Business Credits', 'Business-grade AI credits for agent workloads.', 'ai', 'usage_credits', 'active', 'gpt_business_credits', 'https://www.chainfx.store/developers', 'https://www.chainfx.store')
ON CONFLICT (id) DO NOTHING;

UPDATE marketplace_products SET capability_id = 'payments_fx', capability = 'payments_fx' WHERE id = 'prod_fx_enterprise';
UPDATE marketplace_products SET capability_id = 'document_ocr', capability = 'document_ocr' WHERE id = 'prod_ocr_enterprise';
UPDATE marketplace_products SET capability_id = 'aml_screening', capability = 'aml_screening' WHERE id = 'prod_aml_screening';
UPDATE marketplace_products SET capability_id = 'llm_chat', capability = 'llm_chat' WHERE id = 'prod_gpt_business';

INSERT INTO marketplace_plans (id, product_id, slug, name, price_amount, payment_asset, network, take_rate_bps, quota, validity_seconds, status)
VALUES
  ('plan_fx_400', 'prod_fx_enterprise', 'enterprise-400', 'Enterprise Pack', 400.000000, 'USDT', 'BSC', 2000, 100000, 2592000, 'active'),
  ('plan_ocr_80', 'prod_ocr_enterprise', 'enterprise-80', 'Enterprise Pack', 80.000000, 'USDT', 'BSC', 2000, 1000, 2592000, 'active'),
  ('plan_aml_600', 'prod_aml_screening', 'enterprise-600', 'Enterprise Pack', 600.000000, 'USDT', 'BSC', 2000, 10000, 2592000, 'active'),
  ('plan_gpt_300', 'prod_gpt_business', 'business-300', 'Business Credits', 300.000000, 'USDT', 'BSC', 2000, 100000, 2592000, 'active')
ON CONFLICT (id) DO NOTHING;
