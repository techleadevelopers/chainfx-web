CREATE TABLE IF NOT EXISTS eip712_intent_nonces (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  signer TEXT NOT NULL,
  intent_type TEXT NOT NULL,
  nonce TEXT NOT NULL,
  digest TEXT NOT NULL,
  chain_id BIGINT NOT NULL,
  status TEXT NOT NULL DEFAULT 'verified',
  expires_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (signer, intent_type, nonce, chain_id)
);

CREATE INDEX IF NOT EXISTS idx_eip712_intent_nonces_signer_created
  ON eip712_intent_nonces (signer, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_eip712_intent_nonces_expires
  ON eip712_intent_nonces (expires_at);
