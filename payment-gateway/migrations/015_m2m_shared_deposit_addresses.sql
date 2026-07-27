-- M2M payment intents may share a configured deposit address. Matching is
-- reconciled by required amount, tx hash and expiry window, so a unique
-- pending-address index blocks valid concurrent intents in production.
DROP INDEX IF EXISTS uq_m2m_pending_payment_address;

CREATE INDEX IF NOT EXISTS idx_m2m_intents_payment_address_status
    ON agent_payment_intents (payment_address, status)
    WHERE status = 'pending_deposit';
