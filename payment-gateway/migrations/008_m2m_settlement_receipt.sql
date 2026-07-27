ALTER TABLE agent_payment_intents
  ADD COLUMN IF NOT EXISTS settlement_receipt_url TEXT,
  ADD COLUMN IF NOT EXISTS settlement_receipt_note TEXT;
