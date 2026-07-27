-- Existing installs may already have 039 applied with NUMERIC(18,8).
-- DCA can target assets such as SOL with 9+ decimals, so keep enough precision.
ALTER TABLE dca_executions
  ALTER COLUMN amount_brl TYPE NUMERIC(38,18),
  ALTER COLUMN crypto_amount TYPE NUMERIC(38,18),
  ALTER COLUMN rate_brl TYPE NUMERIC(38,18);

