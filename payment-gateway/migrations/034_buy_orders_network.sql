ALTER TABLE buy_orders
  ADD COLUMN IF NOT EXISTS network TEXT NOT NULL DEFAULT 'BSC';

UPDATE buy_orders
   SET network = 'BSC'
 WHERE network IS NULL OR btrim(network) = '';

CREATE INDEX IF NOT EXISTS idx_buy_orders_user_network_created
  ON buy_orders(user_id, network, created_at DESC);
