-- Canonical channel separation for trade orders.
-- side/source stays buy/sell; channel separates web/mobile/api/agent.

ALTER TABLE orders
  ADD COLUMN IF NOT EXISTS channel TEXT NOT NULL DEFAULT 'web';

ALTER TABLE buy_orders
  ADD COLUMN IF NOT EXISTS channel TEXT NOT NULL DEFAULT 'web';

UPDATE orders SET channel = 'web' WHERE channel IS NULL OR channel = '';
UPDATE buy_orders SET channel = 'web' WHERE channel IS NULL OR channel = '';

CREATE INDEX IF NOT EXISTS idx_orders_channel_created
  ON orders(channel, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_buy_orders_channel_created
  ON buy_orders(channel, created_at DESC);
