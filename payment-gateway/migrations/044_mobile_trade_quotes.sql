-- Canonical lifecycle for mobile BUY/SELL trade quotes.
-- Quotes are bound to a single authenticated user and can be consumed by only
-- one economic operation/idempotency key.

CREATE TABLE IF NOT EXISTS mobile_trade_quotes (
  quote_id              TEXT        PRIMARY KEY,
  user_id               UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  side                  TEXT        NOT NULL CHECK (side IN ('buy','sell')),
  asset                 TEXT        NOT NULL,
  network               TEXT        NOT NULL DEFAULT '',
  amount_minor          BIGINT      NOT NULL CHECK (amount_minor > 0),
  amount_raw            TEXT        NOT NULL,
  rate_minor            BIGINT      NOT NULL CHECK (rate_minor > 0),
  fee_minor             BIGINT      NOT NULL DEFAULT 0 CHECK (fee_minor >= 0),
  total_minor           BIGINT      NOT NULL DEFAULT 0 CHECK (total_minor >= 0),
  expires_at            TIMESTAMPTZ NOT NULL,
  consumed_at           TIMESTAMPTZ,
  consumed_operation_id TEXT,
  idempotency_key       TEXT,
  order_id              UUID,
  created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (
    (consumed_at IS NULL AND consumed_operation_id IS NULL)
    OR
    (consumed_at IS NOT NULL AND consumed_operation_id IS NOT NULL)
  )
);

CREATE INDEX IF NOT EXISTS idx_mobile_trade_quotes_user_created
  ON mobile_trade_quotes(user_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_mobile_trade_quotes_expires
  ON mobile_trade_quotes(expires_at)
  WHERE consumed_at IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS uq_mobile_trade_quotes_consumed_operation
  ON mobile_trade_quotes(consumed_operation_id)
  WHERE consumed_operation_id IS NOT NULL;
