ALTER TABLE agent_payment_intents
  ADD COLUMN IF NOT EXISTS payment_link TEXT,
  ADD COLUMN IF NOT EXISTS barcode TEXT,
  ADD COLUMN IF NOT EXISTS beneficiary_name TEXT,
  ADD COLUMN IF NOT EXISTS due_date TEXT;
