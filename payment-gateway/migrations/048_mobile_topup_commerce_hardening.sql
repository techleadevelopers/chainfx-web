ALTER TABLE mobile_payment_quotes
  ADD COLUMN IF NOT EXISTS recipient_phone TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_mobile_payment_quotes_topup_lookup
  ON mobile_payment_quotes (user_id, product_id, provider_product_id, recipient_phone, status, expires_at)
  WHERE payment_type='gift_card' AND provider='bitrefill';

CREATE INDEX IF NOT EXISTS idx_mobile_gift_card_orders_provider_reconcile
  ON mobile_gift_card_orders (provider_id, status, updated_at)
  WHERE provider_reference <> '';

CREATE UNIQUE INDEX IF NOT EXISTS idx_mobile_gift_card_orders_provider_reference
  ON mobile_gift_card_orders (provider_id, provider_reference)
  WHERE provider_reference <> '';

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE conname = 'chk_mobile_gift_card_order_required_usdt_positive'
  ) THEN
    ALTER TABLE mobile_gift_card_orders
      ADD CONSTRAINT chk_mobile_gift_card_order_required_usdt_positive
      CHECK (required_usdt_micro > 0);
  END IF;
END $$;

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE conname = 'chk_mobile_gift_card_order_quantity_positive'
  ) THEN
    ALTER TABLE mobile_gift_card_orders
      ADD CONSTRAINT chk_mobile_gift_card_order_quantity_positive
      CHECK (quantity > 0);
  END IF;
END $$;
