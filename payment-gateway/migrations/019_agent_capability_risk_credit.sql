CREATE TABLE IF NOT EXISTS agent_capability_credit_accounts (
  id TEXT PRIMARY KEY,
  agent_wallet TEXT NOT NULL,
  capability_id TEXT NOT NULL REFERENCES marketplace_capabilities(id),
  asset TEXT NOT NULL DEFAULT 'USDT',
  network TEXT NOT NULL DEFAULT 'BSC',
  credit_limit_micro BIGINT NOT NULL DEFAULT 5000000,
  credit_used_micro BIGINT NOT NULL DEFAULT 0,
  min_top_up_micro BIGINT NOT NULL DEFAULT 20000000,
  expires_at TIMESTAMPTZ NOT NULL DEFAULT (now() + interval '7 days'),
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','payment_required','disabled','expired')),
  last_payment_required_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(agent_wallet, capability_id, asset, network),
  CHECK (credit_limit_micro >= 0),
  CHECK (credit_used_micro >= 0),
  CHECK (min_top_up_micro >= 0)
);

CREATE INDEX IF NOT EXISTS idx_agent_cap_credit_wallet ON agent_capability_credit_accounts(agent_wallet, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_agent_cap_credit_capability ON agent_capability_credit_accounts(capability_id, status);
