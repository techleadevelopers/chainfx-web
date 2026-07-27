ALTER TABLE quotes
  ADD COLUMN IF NOT EXISTS network VARCHAR(32) NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_quotes_asset_network_expires
  ON quotes(asset, network, expires_at)
  WHERE consumed_at IS NULL;
