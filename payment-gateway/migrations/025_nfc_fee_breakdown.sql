-- Persist ChainFX Tap fee breakdown.
-- amount_brl_minor is the merchant sale amount.
-- fee_brl_minor is the ChainFX fee charged to the user.
-- total_brl_minor is the BRL basis converted to USDT and held in the ledger.

ALTER TABLE nfc_authorizations ADD COLUMN IF NOT EXISTS fee_brl_minor BIGINT NOT NULL DEFAULT 0;
ALTER TABLE nfc_authorizations ADD COLUMN IF NOT EXISTS total_brl_minor BIGINT NOT NULL DEFAULT 0;
ALTER TABLE nfc_authorizations ADD COLUMN IF NOT EXISTS fee_bps INT NOT NULL DEFAULT 0;

UPDATE nfc_authorizations
SET total_brl_minor = amount_brl_minor
WHERE total_brl_minor = 0;
