-- Migration 045 - BTC address provisioning hardening.
-- Adds the PostgreSQL invariant required by GetOrCreateAddress:
-- at most one active BTC receive address per (user_id, network).
--
-- This migration intentionally refuses to proceed when duplicates already
-- exist. Canonical selection for a cleanup runbook is deterministic:
-- lowest derivation_index, then oldest created_at, then lowest id.

DO $$
DECLARE
  duplicate_groups INTEGER;
  duplicate_rows INTEGER;
  duplicate_summary JSONB;
BEGIN
  SELECT COUNT(*), COALESCE(SUM(address_count), 0)
    INTO duplicate_groups, duplicate_rows
  FROM (
    SELECT user_id, network, COUNT(*) AS address_count
    FROM btc_wallet_addresses
    WHERE status = 'active'
    GROUP BY user_id, network
    HAVING COUNT(*) > 1
  ) d;

  IF duplicate_groups > 0 THEN
    SELECT jsonb_agg(group_summary ORDER BY group_summary->>'user_id', group_summary->>'network')
      INTO duplicate_summary
    FROM (
      SELECT jsonb_build_object(
        'user_id', user_id,
        'network', network,
        'active_count', COUNT(*),
        'canonical_address', (array_agg(address ORDER BY derivation_index ASC, created_at ASC, id ASC))[1],
        'canonical_derivation_index', (array_agg(derivation_index ORDER BY derivation_index ASC, created_at ASC, id ASC))[1],
        'addresses', jsonb_agg(jsonb_build_object(
          'id', id,
          'address', address,
          'derivation_index', derivation_index,
          'created_at', created_at
        ) ORDER BY derivation_index ASC, created_at ASC, id ASC)
      ) AS group_summary
      FROM btc_wallet_addresses
      WHERE status = 'active'
      GROUP BY user_id, network
      HAVING COUNT(*) > 1
      LIMIT 20
    ) s;

    RAISE EXCEPTION
      'btc_wallet_addresses has % duplicate active user/network groups (% active rows). Resolve manually before adding uq_btc_active_user_network. Suggested canonical per group: %',
      duplicate_groups, duplicate_rows, duplicate_summary;
  END IF;
END $$;

CREATE UNIQUE INDEX IF NOT EXISTS uq_btc_active_user_network
  ON btc_wallet_addresses (user_id, network)
  WHERE status = 'active';
