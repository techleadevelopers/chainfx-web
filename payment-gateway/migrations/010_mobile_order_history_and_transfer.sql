-- Mobile order ownership and history support.
-- Lets /api/mobile/orders and /api/mobile/wallet/history read both sell orders
-- and buy orders without depending on legacy amount_usdt column names.

ALTER TABLE orders
  ADD COLUMN IF NOT EXISTS user_id UUID REFERENCES users(id);

CREATE INDEX IF NOT EXISTS idx_orders_user_created
  ON orders(user_id, created_at DESC);

ALTER TABLE buy_orders
  ADD COLUMN IF NOT EXISTS user_id UUID REFERENCES users(id);

CREATE INDEX IF NOT EXISTS idx_buy_orders_user_created
  ON buy_orders(user_id, created_at DESC);
