-- Track the funding network for Agent Pay/M2M intents so BSC and Polygon
-- deposits cannot be reconciled against the wrong pending intent.
ALTER TABLE agent_payment_intents
    ADD COLUMN IF NOT EXISTS payment_network TEXT NOT NULL DEFAULT 'BSC';

CREATE INDEX IF NOT EXISTS idx_m2m_intents_payment_network_address_status
    ON agent_payment_intents(payment_network, payment_address, status)
    WHERE status = 'pending_deposit';
