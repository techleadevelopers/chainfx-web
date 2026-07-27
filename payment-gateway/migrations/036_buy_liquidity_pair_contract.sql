ALTER TABLE buy_liquidity_quotes
  ADD COLUMN IF NOT EXISTS token_contract TEXT;

ALTER TABLE buy_liquidity_quotes
  ADD COLUMN IF NOT EXISTS token_decimals INTEGER NOT NULL DEFAULT 18;
