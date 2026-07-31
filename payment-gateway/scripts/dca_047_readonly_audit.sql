-- Read-only audit for migration 047 before any write.
-- Run with: psql -v ON_ERROR_STOP=1 "$DATABASE_URL" -f scripts/dca_047_readonly_audit.sql

\echo 'dca strategy duplicate groups - historical vs executable'
WITH grouped AS (
  SELECT
    user_id,
    token_symbol,
    network,
    amount_brl,
    frequency,
    COUNT(*) AS historical_group_size,
    COUNT(*) FILTER (
      WHERE active = true
        AND cancelled_at IS NULL
        AND reconciliation_hold_at IS NULL
    ) AS canonical_executable_count,
    COUNT(*) FILTER (
      WHERE cancelled_at IS NULL
        AND reconciliation_hold_at IS NOT NULL
    ) AS held_duplicate_count,
    jsonb_agg(jsonb_build_object(
      'id', id,
      'active', active,
      'executable', active = true AND cancelled_at IS NULL AND reconciliation_hold_at IS NULL,
      'created_at', created_at,
      'updated_at', updated_at,
      'next_execution', next_execution,
      'cancelled_at', cancelled_at,
      'reconciliation_hold_at', reconciliation_hold_at,
      'canonical_strategy_id', canonical_strategy_id
    ) ORDER BY created_at ASC, id ASC) AS strategies
  FROM dca_strategies
  WHERE cancelled_at IS NULL
  GROUP BY user_id, token_symbol, network, amount_brl, frequency
  HAVING COUNT(*) > 1
)
SELECT
  user_id,
  token_symbol,
  network,
  amount_brl,
  frequency,
  historical_group_size,
  canonical_executable_count,
  held_duplicate_count,
  CASE WHEN canonical_executable_count > 1 THEN 1 ELSE 0 END AS executable_duplicate_group,
  strategies
FROM grouped
ORDER BY historical_group_size DESC, user_id, token_symbol, network, amount_brl, frequency;

\echo 'dca strategy duplicate summary'
WITH grouped AS (
  SELECT
    COUNT(*) AS historical_group_size,
    COUNT(*) FILTER (
      WHERE active = true
        AND cancelled_at IS NULL
        AND reconciliation_hold_at IS NULL
    ) AS canonical_executable_count,
    COUNT(*) FILTER (
      WHERE cancelled_at IS NULL
        AND reconciliation_hold_at IS NOT NULL
    ) AS held_duplicate_count
  FROM dca_strategies
  WHERE cancelled_at IS NULL
  GROUP BY user_id, token_symbol, network, amount_brl, frequency
  HAVING COUNT(*) > 1
)
SELECT
  COALESCE(SUM(historical_group_size), 0) AS historical_strategy_count,
  COALESCE(SUM(canonical_executable_count), 0) AS canonical_executable_count,
  COALESCE(SUM(held_duplicate_count), 0) AS held_duplicate_count,
  COALESCE(SUM(CASE WHEN canonical_executable_count > 1 THEN 1 ELSE 0 END), 0) AS executable_duplicate_groups
FROM grouped;

\echo 'dca execution duplicate economic identities'
SELECT strategy_id, scheduled_at, COUNT(*) AS execution_count,
       jsonb_agg(jsonb_build_object(
         'id', id,
         'status', status,
         'operation_id', operation_id,
         'buy_order_id', buy_order_id,
         'created_at', created_at,
         'updated_at', updated_at,
         'submitted_at', submitted_at,
         'completed_at', completed_at
       ) ORDER BY created_at ASC, id ASC) AS executions
FROM dca_executions
WHERE scheduled_at IS NOT NULL
GROUP BY strategy_id, scheduled_at
HAVING COUNT(*) > 1
ORDER BY execution_count DESC, strategy_id, scheduled_at;

\echo 'dca duplicate operation ids'
SELECT operation_id, COUNT(*) AS execution_count,
       jsonb_agg(jsonb_build_object(
         'id', id,
         'strategy_id', strategy_id,
         'scheduled_at', scheduled_at,
         'status', status,
         'buy_order_id', buy_order_id
       ) ORDER BY created_at ASC, id ASC) AS executions
FROM dca_executions
WHERE operation_id IS NOT NULL
GROUP BY operation_id
HAVING COUNT(*) > 1
ORDER BY execution_count DESC, operation_id;

\echo 'dca constraints and indexes'
SELECT schemaname, tablename, indexname, indexdef
FROM pg_indexes
WHERE tablename IN ('dca_strategies','dca_executions','mobile_wallet_ledger_entries')
ORDER BY tablename, indexname;

SELECT conrelid::regclass AS table_name, conname, pg_get_constraintdef(oid) AS definition
FROM pg_constraint
WHERE conrelid IN ('dca_strategies'::regclass, 'dca_executions'::regclass, 'mobile_wallet_ledger_entries'::regclass)
ORDER BY table_name::text, conname;

\echo 'partial migration columns'
SELECT table_name, column_name, data_type, is_nullable
FROM information_schema.columns
WHERE table_name IN ('dca_strategies','dca_executions','mobile_wallet_ledger_entries','dca_strategy_reconciliation_reviews')
ORDER BY table_name, ordinal_position;

\echo 'DCA executions by status'
SELECT status, COUNT(*) AS count
FROM dca_executions
GROUP BY status
ORDER BY status;

\echo 'DCA ledger entries by source'
SELECT source, COUNT(*) AS count, SUM(available_delta_micro) AS available_delta_micro, SUM(locked_delta_micro) AS locked_delta_micro
FROM mobile_wallet_ledger_entries
WHERE source LIKE 'dca_execution_%'
GROUP BY source
ORDER BY source;
