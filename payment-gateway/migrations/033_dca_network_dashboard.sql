ALTER TABLE dca_strategies
  ADD COLUMN IF NOT EXISTS network TEXT NOT NULL DEFAULT 'BSC';

UPDATE dca_strategies
   SET network = 'BSC'
 WHERE network IS NULL OR btrim(network) = '';

CREATE INDEX IF NOT EXISTS idx_dca_user_network
  ON dca_strategies(user_id, token_symbol, network);
