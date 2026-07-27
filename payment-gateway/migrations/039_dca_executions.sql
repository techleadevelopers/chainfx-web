-- Track individual DCA cycle executions for accurate financial accounting.
-- total_invested and total_tokens on dca_strategies are ONLY updated after
-- buy.sent is confirmed, preventing phantom token balances on delivery failure.
CREATE TABLE IF NOT EXISTS dca_executions (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    strategy_id   UUID NOT NULL REFERENCES dca_strategies(id) ON DELETE CASCADE,
    buy_order_id  UUID REFERENCES buy_orders(id) ON DELETE SET NULL,
    status        TEXT NOT NULL DEFAULT 'pending',   -- pending | completed | failed
    amount_brl    NUMERIC(38,18) NOT NULL,
    crypto_amount NUMERIC(38,18),
    rate_brl      NUMERIC(38,18),
    error_message TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_dca_executions_strategy
    ON dca_executions(strategy_id);

CREATE INDEX IF NOT EXISTS idx_dca_executions_buy_order
    ON dca_executions(buy_order_id)
    WHERE buy_order_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_dca_executions_pending
    ON dca_executions(status)
    WHERE status = 'pending';
