-- ============================================================
-- Agent Pricing Policies — per-agent fee overrides for M2M rails
-- Apply after schema.sql and schema_phase5.sql
-- ============================================================

CREATE TABLE IF NOT EXISTS agent_pricing_policies (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_wallet        TEXT NOT NULL,                     -- lower-cased EVM address
    environment         TEXT NOT NULL DEFAULT 'sandbox',   -- sandbox | production
    pix_fee_bps         INTEGER,                           -- NULL = use global M2M_PIX_FEE_BPS
    credit_card_fee_bps INTEGER,                           -- NULL = use global M2M_CREDIT_FEE_BPS
    capability_take_bps INTEGER,                           -- NULL = use plan take_rate_bps
    daily_max_brl       NUMERIC(18,2),                     -- NULL = use M2M_MAX_DAILY_OUTFLOW_BRL
    monthly_max_brl     NUMERIC(18,2),                     -- NULL = no extra monthly cap
    notes               TEXT,
    status              TEXT NOT NULL DEFAULT 'active',    -- active | paused | disabled
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS agent_pricing_policies_wallet_env_idx
    ON agent_pricing_policies (lower(agent_wallet), environment);

COMMENT ON TABLE agent_pricing_policies IS
    'Per-agent fee overrides for M2M PIX, credit-card rails and capability take-rate. '
    'NULL fields fall back to the global env-var defaults.';
