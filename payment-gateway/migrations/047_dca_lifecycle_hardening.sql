-- DCA lifecycle hardening.
-- Canonical economic identity: (strategy_id, scheduled_at).
-- Safe to re-run against a partially migrated database.
-- Existing object names are intentionally preserved.

BEGIN;

-- ============================================================
-- DCA STRATEGIES
-- ============================================================

ALTER TABLE dca_strategies
  ADD COLUMN IF NOT EXISTS cancelled_at TIMESTAMPTZ;

ALTER TABLE dca_strategies
  ADD COLUMN IF NOT EXISTS reconciliation_hold_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS reconciliation_reason TEXT,
  ADD COLUMN IF NOT EXISTS canonical_strategy_id UUID;

-- Ensure canonical_strategy_id FK exists even if the column was created
-- by an earlier partial execution.
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1
    FROM pg_constraint c
    WHERE c.conrelid = 'public.dca_strategies'::regclass
      AND c.contype = 'f'
      AND pg_get_constraintdef(c.oid)
          LIKE 'FOREIGN KEY (canonical_strategy_id)%'
  ) THEN
    ALTER TABLE dca_strategies
      ADD CONSTRAINT dca_strategies_canonical_strategy_id_fkey
      FOREIGN KEY (canonical_strategy_id)
      REFERENCES dca_strategies(id);
  END IF;
END $$;

ALTER TABLE dca_strategies
  ALTER COLUMN amount_brl TYPE NUMERIC(38,18),
  ALTER COLUMN total_invested TYPE NUMERIC(38,18),
  ALTER COLUMN total_tokens TYPE NUMERIC(38,18);

-- ============================================================
-- DCA RECONCILIATION REVIEW
-- ============================================================

CREATE TABLE IF NOT EXISTS dca_strategy_reconciliation_reviews (
  id TEXT PRIMARY KEY,
  user_id UUID NOT NULL,
  token_symbol TEXT NOT NULL,
  network TEXT NOT NULL,
  amount_brl NUMERIC(38,18) NOT NULL,
  frequency TEXT NOT NULL,
  strategy_ids JSONB NOT NULL,
  canonical_strategy_id UUID,
  reason TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'manual_review',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ============================================================
-- MOBILE WALLET LEDGER
-- ============================================================

CREATE TABLE IF NOT EXISTS mobile_wallet_ledger_entries (
  id TEXT PRIMARY KEY,
  wallet_address TEXT NOT NULL,
  network TEXT NOT NULL DEFAULT 'BSC',
  asset TEXT NOT NULL DEFAULT 'USDT',
  source TEXT NOT NULL,
  reference_id TEXT NOT NULL,
  available_delta_micro BIGINT NOT NULL DEFAULT 0,
  locked_delta_micro BIGINT NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- DCA worker writes:
-- dca_execution_reserve
-- dca_execution_capture
-- dca_execution_release
-- against the canonical execution id.
CREATE INDEX IF NOT EXISTS idx_mobile_wallet_ledger_reference
  ON mobile_wallet_ledger_entries(source, reference_id);

-- ============================================================
-- DCA EXECUTIONS
-- ============================================================

ALTER TABLE dca_executions
  ADD COLUMN IF NOT EXISTS scheduled_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS user_id UUID,
  ADD COLUMN IF NOT EXISTS token_symbol TEXT,
  ADD COLUMN IF NOT EXISTS network TEXT,
  ADD COLUMN IF NOT EXISTS frequency TEXT,
  ADD COLUMN IF NOT EXISTS operation_id TEXT,
  ADD COLUMN IF NOT EXISTS quote_expires_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS claimed_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS processing_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS submitted_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS completed_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS attempts INTEGER NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  ADD COLUMN IF NOT EXISTS provider_status TEXT,
  ADD COLUMN IF NOT EXISTS funding_wallet_address TEXT,
  ADD COLUMN IF NOT EXISTS required_usdt_micro BIGINT,
  ADD COLUMN IF NOT EXISTS reserved_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS captured_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS released_at TIMESTAMPTZ;

-- Ensure user FK exists even after a previous partial migration.
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1
    FROM pg_constraint c
    WHERE c.conrelid = 'public.dca_executions'::regclass
      AND c.contype = 'f'
      AND pg_get_constraintdef(c.oid)
          LIKE 'FOREIGN KEY (user_id)%'
  ) THEN
    ALTER TABLE dca_executions
      ADD CONSTRAINT dca_executions_user_id_fkey
      FOREIGN KEY (user_id)
      REFERENCES users(id);
  END IF;
END $$;

-- ============================================================
-- BACKFILL EXECUTION IDENTITY
-- ============================================================

UPDATE dca_executions e
SET scheduled_at = COALESCE(e.scheduled_at, e.created_at),
    user_id = COALESCE(e.user_id, s.user_id),
    token_symbol = COALESCE(NULLIF(e.token_symbol, ''), s.token_symbol),
    network = COALESCE(NULLIF(e.network, ''), s.network),
    frequency = COALESCE(NULLIF(e.frequency, ''), s.frequency),
    operation_id = COALESCE(
      NULLIF(e.operation_id, ''),
      'dca:' ||
      e.strategy_id::text ||
      ':' ||
      COALESCE(e.scheduled_at, e.created_at)::text
    ),
    claimed_at = COALESCE(e.claimed_at, e.created_at)
FROM dca_strategies s
WHERE s.id = e.strategy_id;

-- ============================================================
-- DETECT / RECORD DUPLICATE STRATEGIES
-- ============================================================

WITH active AS (
  SELECT
    s.id,
    s.user_id,
    s.token_symbol,
    s.network,
    s.amount_brl,
    s.frequency,
    s.created_at,

    COUNT(e.id) FILTER (
      WHERE e.status IN (
        'pending',
        'claimed',
        'processing',
        'submitted',
        'provider_unknown',
        'completed'
      )
    ) AS financial_execution_count,

    MIN(e.scheduled_at) FILTER (
      WHERE e.status IN (
        'pending',
        'claimed',
        'processing',
        'submitted',
        'provider_unknown',
        'completed'
      )
    ) AS first_financial_scheduled_at

  FROM dca_strategies s

  LEFT JOIN dca_executions e
    ON e.strategy_id = s.id

  WHERE s.cancelled_at IS NULL

  GROUP BY s.id
),

duplicate_groups AS (
  SELECT
    user_id,
    token_symbol,
    network,
    amount_brl,
    frequency,

    COUNT(*) AS strategy_count,

    COUNT(*) FILTER (
      WHERE financial_execution_count > 0
    ) AS strategies_with_financial,

    jsonb_agg(
      id
      ORDER BY created_at ASC, id ASC
    ) AS strategy_ids

  FROM active

  GROUP BY
    user_id,
    token_symbol,
    network,
    amount_brl,
    frequency

  HAVING COUNT(*) > 1
),

canonical AS (
  SELECT
    g.*,

    CASE

      -- More than one strategy already has financial evidence.
      -- Do not choose automatically.
      WHEN g.strategies_with_financial > 1
        THEN NULL::uuid

      -- Exactly one has financial evidence.
      -- Keep that one canonical.
      WHEN g.strategies_with_financial = 1
        THEN (
          SELECT a.id
          FROM active a
          WHERE a.user_id = g.user_id
            AND a.token_symbol = g.token_symbol
            AND a.network = g.network
            AND a.amount_brl = g.amount_brl
            AND a.frequency = g.frequency
            AND a.financial_execution_count > 0
          ORDER BY
            a.first_financial_scheduled_at ASC NULLS LAST,
            a.created_at ASC,
            a.id ASC
          LIMIT 1
        )

      -- No financial evidence.
      -- Keep oldest strategy.
      ELSE (
        SELECT a.id
        FROM active a
        WHERE a.user_id = g.user_id
          AND a.token_symbol = g.token_symbol
          AND a.network = g.network
          AND a.amount_brl = g.amount_brl
          AND a.frequency = g.frequency
        ORDER BY
          a.created_at ASC,
          a.id ASC
        LIMIT 1
      )

    END AS canonical_strategy_id

  FROM duplicate_groups g
),

review_rows AS (
  SELECT
    'dca-strategy-dup-' ||
      md5(
        user_id::text || ':' ||
        token_symbol || ':' ||
        network || ':' ||
        amount_brl::text || ':' ||
        frequency
      ) AS id,

    user_id,
    token_symbol,
    network,
    amount_brl,
    frequency,
    strategy_ids,
    canonical_strategy_id,

    CASE
      WHEN strategies_with_financial > 1
        THEN 'duplicate active strategy config with financial executions on multiple strategies; all held for manual reconciliation'

      WHEN strategies_with_financial = 1
        THEN 'duplicate active strategy config; non-canonical strategies held because one strategy has financial evidence'

      ELSE
        'duplicate active strategy config without executions; oldest strategy kept canonical and duplicates held'
    END AS reason,

    CASE
      WHEN strategies_with_financial > 1
        THEN 'manual_review'
      ELSE
        'auto_held'
    END AS status

  FROM canonical
)

INSERT INTO dca_strategy_reconciliation_reviews (
  id,
  user_id,
  token_symbol,
  network,
  amount_brl,
  frequency,
  strategy_ids,
  canonical_strategy_id,
  reason,
  status
)

SELECT
  id,
  user_id,
  token_symbol,
  network,
  amount_brl,
  frequency,
  strategy_ids,
  canonical_strategy_id,
  reason,
  status

FROM review_rows

ON CONFLICT (id) DO UPDATE

SET strategy_ids = EXCLUDED.strategy_ids,
    canonical_strategy_id = EXCLUDED.canonical_strategy_id,
    reason = EXCLUDED.reason,
    status = EXCLUDED.status,
    updated_at = NOW();

-- ============================================================
-- HOLD NON-CANONICAL DUPLICATES
-- ============================================================

WITH active AS (
  SELECT
    s.id,
    s.user_id,
    s.token_symbol,
    s.network,
    s.amount_brl,
    s.frequency,
    s.created_at,

    COUNT(e.id) FILTER (
      WHERE e.status IN (
        'pending',
        'claimed',
        'processing',
        'submitted',
        'provider_unknown',
        'completed'
      )
    ) AS financial_execution_count,

    MIN(e.scheduled_at) FILTER (
      WHERE e.status IN (
        'pending',
        'claimed',
        'processing',
        'submitted',
        'provider_unknown',
        'completed'
      )
    ) AS first_financial_scheduled_at

  FROM dca_strategies s

  LEFT JOIN dca_executions e
    ON e.strategy_id = s.id

  WHERE s.cancelled_at IS NULL

  GROUP BY s.id
),

duplicate_groups AS (
  SELECT
    user_id,
    token_symbol,
    network,
    amount_brl,
    frequency,

    COUNT(*) FILTER (
      WHERE financial_execution_count > 0
    ) AS strategies_with_financial

  FROM active

  GROUP BY
    user_id,
    token_symbol,
    network,
    amount_brl,
    frequency

  HAVING COUNT(*) > 1
),

canonical AS (
  SELECT
    g.*,

    CASE

      WHEN g.strategies_with_financial > 1
        THEN NULL::uuid

      WHEN g.strategies_with_financial = 1
        THEN (
          SELECT a.id
          FROM active a
          WHERE a.user_id = g.user_id
            AND a.token_symbol = g.token_symbol
            AND a.network = g.network
            AND a.amount_brl = g.amount_brl
            AND a.frequency = g.frequency
            AND a.financial_execution_count > 0
          ORDER BY
            a.first_financial_scheduled_at ASC NULLS LAST,
            a.created_at ASC,
            a.id ASC
          LIMIT 1
        )

      ELSE (
        SELECT a.id
        FROM active a
        WHERE a.user_id = g.user_id
          AND a.token_symbol = g.token_symbol
          AND a.network = g.network
          AND a.amount_brl = g.amount_brl
          AND a.frequency = g.frequency
        ORDER BY
          a.created_at ASC,
          a.id ASC
        LIMIT 1
      )

    END AS canonical_strategy_id

  FROM duplicate_groups g
)

UPDATE dca_strategies s

SET active = false,

    reconciliation_hold_at =
      COALESCE(
        s.reconciliation_hold_at,
        NOW()
      ),

    reconciliation_reason =
      CASE
        WHEN c.strategies_with_financial > 1
          THEN 'manual_review: duplicate active strategy config with financial executions on multiple strategies'

        ELSE
          'auto_hold: duplicate active strategy config; canonical strategy remains executable'
      END,

    canonical_strategy_id =
      c.canonical_strategy_id,

    updated_at = NOW()

FROM canonical c

WHERE s.user_id = c.user_id
  AND s.token_symbol = c.token_symbol
  AND s.network = c.network
  AND s.amount_brl = c.amount_brl
  AND s.frequency = c.frequency
  AND s.cancelled_at IS NULL

  AND (
    c.canonical_strategy_id IS NULL
    OR s.id <> c.canonical_strategy_id
  );

-- ============================================================
-- FAIL-FAST ECONOMIC INVARIANTS
-- ============================================================

DO $$
BEGIN

  IF EXISTS (
    SELECT 1
    FROM dca_executions
    GROUP BY strategy_id, scheduled_at
    HAVING COUNT(*) > 1
  ) THEN

    RAISE EXCEPTION
      'dca_executions contains duplicate strategy_id/scheduled_at rows; manual reconciliation required before applying hardening';

  END IF;

  IF EXISTS (
    SELECT 1
    FROM dca_executions
    WHERE operation_id IS NOT NULL
    GROUP BY operation_id
    HAVING COUNT(*) > 1
  ) THEN

    RAISE EXCEPTION
      'dca_executions contains duplicate operation_id rows; manual reconciliation required before applying hardening';

  END IF;

  IF EXISTS (
    SELECT 1
    FROM dca_strategies
    WHERE cancelled_at IS NULL
      AND reconciliation_hold_at IS NULL
    GROUP BY
      user_id,
      token_symbol,
      network,
      amount_brl,
      frequency
    HAVING COUNT(*) > 1
  ) THEN

    RAISE EXCEPTION
      'dca_strategies contains duplicate active strategy configs; manual reconciliation required before applying hardening';

  END IF;

END $$;

-- ============================================================
-- EXECUTION NOT NULL INVARIANTS
-- ============================================================

ALTER TABLE dca_executions
  ALTER COLUMN scheduled_at SET NOT NULL,
  ALTER COLUMN user_id SET NOT NULL,
  ALTER COLUMN token_symbol SET NOT NULL,
  ALTER COLUMN network SET NOT NULL,
  ALTER COLUMN frequency SET NOT NULL,
  ALTER COLUMN operation_id SET NOT NULL;

-- ============================================================
-- ECONOMIC UNIQUE INDEXES
-- ============================================================

CREATE UNIQUE INDEX IF NOT EXISTS
  uq_dca_executions_strategy_scheduled
ON dca_executions (
  strategy_id,
  scheduled_at
);

CREATE UNIQUE INDEX IF NOT EXISTS
  uq_dca_executions_operation_id
ON dca_executions (
  operation_id
);

-- Rebuild using the exact existing name.
DROP INDEX IF EXISTS uq_dca_active_strategy_config;

CREATE UNIQUE INDEX uq_dca_active_strategy_config
ON dca_strategies (
  user_id,
  token_symbol,
  network,
  amount_brl,
  frequency
)
WHERE cancelled_at IS NULL
  AND reconciliation_hold_at IS NULL;

-- ============================================================
-- RECOVERY INDEX
-- ============================================================

CREATE INDEX IF NOT EXISTS idx_dca_executions_recoverable
ON dca_executions (
  status,
  next_attempt_at,
  scheduled_at
)
WHERE status IN (
  'claimed',
  'processing',
  'retry_wait',
  'provider_unknown'
);

-- ============================================================
-- CHECK CONSTRAINTS
-- ============================================================

-- dca_strategies amount > 0
DO $$
BEGIN

  IF NOT EXISTS (
    SELECT 1
    FROM pg_constraint
    WHERE conrelid = 'public.dca_strategies'::regclass
      AND conname = 'chk_dca_strategy_amount_positive'
  ) THEN

    ALTER TABLE dca_strategies
      ADD CONSTRAINT chk_dca_strategy_amount_positive
      CHECK (amount_brl > 0)
      NOT VALID;

  END IF;

END $$;

-- dca_executions amount > 0
DO $$
BEGIN

  IF NOT EXISTS (
    SELECT 1
    FROM pg_constraint
    WHERE conrelid = 'public.dca_executions'::regclass
      AND conname = 'chk_dca_execution_amount_positive'
  ) THEN

    ALTER TABLE dca_executions
      ADD CONSTRAINT chk_dca_execution_amount_positive
      CHECK (amount_brl > 0)
      NOT VALID;

  END IF;

END $$;

-- Status constraint can be safely rebuilt using the same existing name.
ALTER TABLE dca_executions
  DROP CONSTRAINT IF EXISTS chk_dca_execution_status;

ALTER TABLE dca_executions
  ADD CONSTRAINT chk_dca_execution_status
  CHECK (
    status IN (
      'pending',
      'claimed',
      'processing',
      'submitted',
      'completed',
      'retry_wait',
      'provider_unknown',
      'failed',
      'skipped_paused',
      'manual_review'
    )
  )
  NOT VALID;

COMMIT;