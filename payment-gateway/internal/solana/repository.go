package solana

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type repository struct {
	sql *sql.DB
}

func (r *repository) ensureSchema(ctx context.Context) error {
	_, err := r.sql.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS sol_wallet_addresses (
    id                TEXT PRIMARY KEY DEFAULT ('sol_' || md5(random()::text || clock_timestamp()::text)),
    user_id           TEXT NOT NULL,
    network           TEXT NOT NULL DEFAULT 'SOLANA' CHECK (network IN ('SOLANA')),
    address           TEXT NOT NULL,
    derivation_key_id TEXT NOT NULL DEFAULT '',
    status            TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','archived')),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_sol_address_network UNIQUE (network, address),
    CONSTRAINT uq_sol_user_network_active UNIQUE (user_id, network, status)
);
CREATE INDEX IF NOT EXISTS idx_sol_wallet_addresses_user_network ON sol_wallet_addresses (user_id, network, status);
CREATE TABLE IF NOT EXISTS sol_transactions (
    id                  TEXT PRIMARY KEY DEFAULT ('soltx_' || md5(random()::text || clock_timestamp()::text)),
    user_id             TEXT NOT NULL,
    network             TEXT NOT NULL DEFAULT 'SOLANA',
    signature           TEXT NOT NULL DEFAULT '',
    asset               TEXT NOT NULL DEFAULT 'SOL',
    mint_address        TEXT NOT NULL DEFAULT '',
    direction           TEXT NOT NULL CHECK (direction IN ('deposit','withdrawal','router_delivery','internal')),
    amount_raw          NUMERIC(78,0) NOT NULL DEFAULT 0,
    decimals            INTEGER NOT NULL DEFAULT 9,
    status              TEXT NOT NULL DEFAULT 'pending',
    confirmations       INTEGER NOT NULL DEFAULT 0,
    slot                BIGINT NOT NULL DEFAULT 0,
    operation_id        TEXT NOT NULL DEFAULT '',
    idempotency_key     TEXT NOT NULL DEFAULT '',
    request_hash        TEXT NOT NULL DEFAULT '',
    source_address      TEXT NOT NULL DEFAULT '',
    destination_address TEXT NOT NULL DEFAULT '',
    fee_lamports        NUMERIC(78,0) NOT NULL DEFAULT 0,
    reserved_lamports   NUMERIC(78,0) NOT NULL DEFAULT 0,
    signed_raw_tx       TEXT NOT NULL DEFAULT '',
    fee_payer           TEXT NOT NULL DEFAULT '',
    recent_blockhash    TEXT NOT NULL DEFAULT '',
    last_valid_block_height BIGINT NOT NULL DEFAULT 0,
    receive_key         TEXT NOT NULL DEFAULT '',
    signed_at           TIMESTAMPTZ,
    submitted_at        TIMESTAMPTZ,
    confirmed_at        TIMESTAMPTZ,
    metadata_json       JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_sol_transactions_user_created ON sol_transactions (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_sol_transactions_status ON sol_transactions (network, status, updated_at);
ALTER TABLE sol_transactions DROP CONSTRAINT IF EXISTS uq_sol_signature;
ALTER TABLE sol_transactions ADD COLUMN IF NOT EXISTS operation_id TEXT NOT NULL DEFAULT '';
ALTER TABLE sol_transactions ADD COLUMN IF NOT EXISTS idempotency_key TEXT NOT NULL DEFAULT '';
ALTER TABLE sol_transactions ADD COLUMN IF NOT EXISTS request_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE sol_transactions ADD COLUMN IF NOT EXISTS source_address TEXT NOT NULL DEFAULT '';
ALTER TABLE sol_transactions ADD COLUMN IF NOT EXISTS destination_address TEXT NOT NULL DEFAULT '';
ALTER TABLE sol_transactions ADD COLUMN IF NOT EXISTS fee_lamports NUMERIC(78,0) NOT NULL DEFAULT 0;
ALTER TABLE sol_transactions ADD COLUMN IF NOT EXISTS reserved_lamports NUMERIC(78,0) NOT NULL DEFAULT 0;
ALTER TABLE sol_transactions ADD COLUMN IF NOT EXISTS signed_raw_tx TEXT NOT NULL DEFAULT '';
ALTER TABLE sol_transactions ADD COLUMN IF NOT EXISTS fee_payer TEXT NOT NULL DEFAULT '';
ALTER TABLE sol_transactions ADD COLUMN IF NOT EXISTS recent_blockhash TEXT NOT NULL DEFAULT '';
ALTER TABLE sol_transactions ADD COLUMN IF NOT EXISTS last_valid_block_height BIGINT NOT NULL DEFAULT 0;
ALTER TABLE sol_transactions ADD COLUMN IF NOT EXISTS receive_key TEXT NOT NULL DEFAULT '';
ALTER TABLE sol_transactions ADD COLUMN IF NOT EXISTS signed_at TIMESTAMPTZ;
ALTER TABLE sol_transactions ADD COLUMN IF NOT EXISTS submitted_at TIMESTAMPTZ;
ALTER TABLE sol_transactions ADD COLUMN IF NOT EXISTS confirmed_at TIMESTAMPTZ;
UPDATE sol_transactions
SET idempotency_key=COALESCE(NULLIF(idempotency_key, ''), metadata_json->>'idempotency_key', ''),
    request_hash=COALESCE(NULLIF(request_hash, ''), metadata_json->>'request_hash', ''),
    source_address=COALESCE(NULLIF(source_address, ''), metadata_json->>'from', ''),
    destination_address=COALESCE(NULLIF(destination_address, ''), metadata_json->>'to', ''),
    fee_lamports=CASE
      WHEN fee_lamports=0 AND (metadata_json ? 'fee_lamports') AND (metadata_json->>'fee_lamports') ~ '^[0-9]+$'
      THEN (metadata_json->>'fee_lamports')::numeric
      ELSE fee_lamports
    END
WHERE network='SOLANA';
UPDATE sol_transactions
SET receive_key=signature || ':balance_delta:' || COALESCE(NULLIF(destination_address, ''), metadata_json->>'address', '')
WHERE network='SOLANA'
  AND direction='deposit'
  AND receive_key=''
  AND signature <> ''
  AND COALESCE(NULLIF(destination_address, ''), metadata_json->>'address', '') <> '';
CREATE UNIQUE INDEX IF NOT EXISTS uq_sol_withdrawal_idempotency
    ON sol_transactions (user_id, idempotency_key)
    WHERE network='SOLANA' AND direction='withdrawal' AND idempotency_key <> '';
CREATE UNIQUE INDEX IF NOT EXISTS uq_sol_operation_id
    ON sol_transactions (network, operation_id)
    WHERE operation_id <> '';
CREATE UNIQUE INDEX IF NOT EXISTS uq_sol_withdrawal_signature
    ON sol_transactions (network, signature)
    WHERE direction='withdrawal' AND signature <> '';
CREATE UNIQUE INDEX IF NOT EXISTS uq_sol_deposit_receive_key
    ON sol_transactions (network, receive_key)
    WHERE direction='deposit' AND receive_key <> '';
CREATE TABLE IF NOT EXISTS sol_cursors (
    network          TEXT PRIMARY KEY,
    last_signature   TEXT NOT NULL DEFAULT '',
    last_slot        BIGINT NOT NULL DEFAULT 0,
    scanner_status   TEXT NOT NULL DEFAULT 'idle',
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO sol_cursors (network) VALUES ('SOLANA') ON CONFLICT (network) DO NOTHING;`)
	return err
}

func (r *repository) getAddress(ctx context.Context, userID string) (*Address, error) {
	if err := r.ensureSchema(ctx); err != nil {
		return nil, err
	}
	addr := &Address{}
	err := r.sql.QueryRowContext(ctx, `
SELECT id, user_id, network, address, derivation_key_id, status, created_at, updated_at
FROM sol_wallet_addresses
WHERE user_id=$1 AND network='SOLANA' AND status='active'
ORDER BY created_at DESC
LIMIT 1`, userID).Scan(&addr.ID, &addr.UserID, &addr.Network, &addr.Address, &addr.DerivationKeyID, &addr.Status, &addr.CreatedAt, &addr.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return addr, err
}

func (r *repository) insertAddress(ctx context.Context, userID, address, keyID string) (*Address, error) {
	if err := r.ensureSchema(ctx); err != nil {
		return nil, err
	}
	addr := &Address{}
	err := r.sql.QueryRowContext(ctx, `
INSERT INTO sol_wallet_addresses (user_id, network, address, derivation_key_id, status)
VALUES ($1, 'SOLANA', $2, $3, 'active')
ON CONFLICT (network, address) DO UPDATE SET updated_at=NOW()
RETURNING id, user_id, network, address, derivation_key_id, status, created_at, updated_at`, userID, address, keyID).
		Scan(&addr.ID, &addr.UserID, &addr.Network, &addr.Address, &addr.DerivationKeyID, &addr.Status, &addr.CreatedAt, &addr.UpdatedAt)
	return addr, err
}

func (r *repository) listActiveAddresses(ctx context.Context) ([]Address, error) {
	if err := r.ensureSchema(ctx); err != nil {
		return nil, err
	}
	rows, err := r.sql.QueryContext(ctx, `
SELECT id, user_id, network, address, derivation_key_id, status, created_at, updated_at
FROM sol_wallet_addresses
WHERE network='SOLANA' AND status='active'
ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Address
	for rows.Next() {
		var addr Address
		if err := rows.Scan(&addr.ID, &addr.UserID, &addr.Network, &addr.Address, &addr.DerivationKeyID, &addr.Status, &addr.CreatedAt, &addr.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, addr)
	}
	return out, rows.Err()
}

func (r *repository) insertTransaction(ctx context.Context, tx Transaction, metadata map[string]any) error {
	if err := r.ensureSchema(ctx); err != nil {
		return err
	}
	payload, _ := json.Marshal(metadata)
	_, err := r.sql.ExecContext(ctx, `
INSERT INTO sol_transactions
  (user_id, network, signature, asset, mint_address, direction, amount_raw, decimals, status, confirmations, slot,
   idempotency_key, request_hash, source_address, destination_address, fee_lamports, reserved_lamports,
   signed_raw_tx, fee_payer, recent_blockhash, last_valid_block_height, receive_key, metadata_json)
VALUES ($1, 'SOLANA', $2, 'SOL', '', $3, $4, 9, $5, $6, $7,
        $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, COALESCE($8::jsonb, '{}'::jsonb))
ON CONFLICT (network, receive_key) WHERE direction='deposit' AND receive_key <> '' DO UPDATE SET
  status=EXCLUDED.status,
  confirmations=GREATEST(sol_transactions.confirmations, EXCLUDED.confirmations),
  slot=GREATEST(sol_transactions.slot, EXCLUDED.slot),
  updated_at=NOW()`,
		tx.UserID, tx.Signature, tx.Direction, tx.AmountRaw, tx.Status, tx.Confirmations, tx.Slot, string(payload),
		tx.IdempotencyKey, tx.RequestHash, tx.SourceAddress, tx.DestinationAddress, tx.FeeLamports, tx.ReservedLamports,
		tx.SignedRawTx, tx.FeePayer, tx.RecentBlockhash, tx.LastValidBlockHeight, depositReceiveKey(tx))
	return err
}

func (r *repository) transactionByIdempotency(ctx context.Context, userID, key string) (*Transaction, string, error) {
	if strings.TrimSpace(key) == "" {
		return nil, "", nil
	}
	if err := r.ensureSchema(ctx); err != nil {
		return nil, "", err
	}
	tx := &Transaction{}
	var requestHash sql.NullString
	var feeRaw, reservedRaw string
	err := r.sql.QueryRowContext(ctx, `
SELECT id, user_id, network, signature, asset, mint_address, direction, amount_raw::text, decimals, status, confirmations, slot, operation_id,
       idempotency_key, request_hash, source_address, destination_address, fee_lamports::text, reserved_lamports::text,
       signed_raw_tx, fee_payer, recent_blockhash, last_valid_block_height, signed_at, submitted_at, confirmed_at, created_at, updated_at,
       request_hash
FROM sol_transactions
WHERE user_id=$1 AND direction='withdrawal' AND idempotency_key=$2
ORDER BY created_at DESC
LIMIT 1`, userID, key).Scan(scanTransactionWithHash(tx, &feeRaw, &reservedRaw, &requestHash)...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", nil
	}
	tx.FeeLamports = parseAmountRaw(feeRaw)
	tx.ReservedLamports = parseAmountRaw(reservedRaw)
	return tx, requestHash.String, err
}

func (r *repository) claimWithdrawal(ctx context.Context, req SendRequest) (*Transaction, bool, error) {
	if err := r.ensureSchema(ctx); err != nil {
		return nil, false, err
	}
	key := strings.TrimSpace(req.IdempotencyKey)
	if key == "" {
		return nil, false, fmt.Errorf("solana: idempotency key obrigatoria")
	}
	opID := "solop_" + requestHash(req.UserID, req.ToAddress, req.AmountLamports, key)[:24]
	tx := &Transaction{}
	var feeRaw, reservedRaw string
	err := r.sql.QueryRowContext(ctx, `
INSERT INTO sol_transactions
  (user_id, network, operation_id, idempotency_key, request_hash, signature, asset, direction, amount_raw, decimals, status, destination_address, metadata_json)
VALUES ($1, 'SOLANA', $2, $3, $4, '', 'SOL', 'withdrawal', $5, 9, $6, $7,
        jsonb_build_object('idempotency_key',$3,'request_hash',$4,'to',$7))
ON CONFLICT (user_id, idempotency_key) WHERE network='SOLANA' AND direction='withdrawal' AND idempotency_key <> '' DO NOTHING
RETURNING id, user_id, network, signature, asset, mint_address, direction, amount_raw::text, decimals, status, confirmations, slot, operation_id,
          idempotency_key, request_hash, source_address, destination_address, fee_lamports::text, reserved_lamports::text,
          signed_raw_tx, fee_payer, recent_blockhash, last_valid_block_height, signed_at, submitted_at, confirmed_at, created_at, updated_at`,
		req.UserID, opID, key, req.RequestHash, req.AmountLamports, StatusCreated, req.ToAddress).Scan(scanTransaction(tx, &feeRaw, &reservedRaw)...)
	if err == nil {
		tx.FeeLamports = parseAmountRaw(feeRaw)
		tx.ReservedLamports = parseAmountRaw(reservedRaw)
		return tx, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	existing, _, err := r.transactionByIdempotency(ctx, req.UserID, key)
	return existing, false, err
}

func (r *repository) reserveWithdrawal(ctx context.Context, txID, userID, source string, amount, fee, balance int64) error {
	if err := r.ensureSchema(ctx); err != nil {
		return err
	}
	dbtx, err := r.sql.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer dbtx.Rollback()
	var lockKey int64
	lockKey = int64(binaryHash(userID + "|SOLANA"))
	if _, err := dbtx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, lockKey); err != nil {
		return err
	}
	var reservedRaw sql.NullString
	if err := dbtx.QueryRowContext(ctx, `
SELECT COALESCE(SUM(reserved_lamports),0)::text
FROM sol_transactions
WHERE user_id=$1 AND network='SOLANA' AND direction='withdrawal'
  AND status IN ('created','reserved','signed','submitted','submit_unknown')`, userID).Scan(&reservedRaw); err != nil {
		return err
	}
	reserved := parseAmountRaw(reservedRaw.String)
	need := amount + fee
	if need <= 0 || balance-reserved < need {
		_, _ = dbtx.ExecContext(ctx, `UPDATE sol_transactions SET status=$2, updated_at=NOW() WHERE id=$1 AND status IN ('created','reserved')`, txID, StatusFailedBeforeSubmit)
		return ErrInsufficientFunds
	}
	res, err := dbtx.ExecContext(ctx, `
UPDATE sol_transactions
SET status=$2, source_address=$3, fee_lamports=$4, reserved_lamports=$5, updated_at=NOW()
WHERE id=$1 AND status='created'`, txID, StatusReserved, source, fee, need)
	if err != nil {
		return err
	}
	rows, _ := res.RowsAffected()
	if rows != 1 {
		return fmt.Errorf("solana: reservation conflict")
	}
	return dbtx.Commit()
}

func (r *repository) persistSigned(ctx context.Context, txID, signature string, rawTx []byte, feePayer, blockhash string, lastValidBlockHeight int64) error {
	if err := r.ensureSchema(ctx); err != nil {
		return err
	}
	_, err := r.sql.ExecContext(ctx, `
UPDATE sol_transactions
SET signature=$2, signed_raw_tx=$3, fee_payer=$4, recent_blockhash=$5, last_valid_block_height=$6,
    status=$7, signed_at=COALESCE(signed_at, NOW()), updated_at=NOW()
WHERE id=$1 AND status='reserved'`,
		txID, signature, base64.StdEncoding.EncodeToString(rawTx), feePayer, blockhash, lastValidBlockHeight, StatusSigned)
	return err
}

func (r *repository) markSubmitStatus(ctx context.Context, signature, status string) error {
	if err := r.ensureSchema(ctx); err != nil {
		return err
	}
	_, err := r.sql.ExecContext(ctx, `
UPDATE sol_transactions
SET status=$2,
    submitted_at=CASE WHEN $2 IN ('submitted','submit_unknown') THEN COALESCE(submitted_at, NOW()) ELSE submitted_at END,
    updated_at=NOW()
WHERE network='SOLANA' AND signature=$1`, signature, status)
	return err
}

func (r *repository) listUserTransactions(ctx context.Context, userID string, limit int) ([]Transaction, error) {
	if err := r.ensureSchema(ctx); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := r.sql.QueryContext(ctx, `
SELECT id, user_id, network, signature, asset, mint_address, direction, amount_raw::text, decimals, status, confirmations, slot, operation_id,
       idempotency_key, request_hash, source_address, destination_address, fee_lamports::text, reserved_lamports::text,
       signed_raw_tx, fee_payer, recent_blockhash, last_valid_block_height, signed_at, submitted_at, confirmed_at, created_at, updated_at
FROM sol_transactions
WHERE user_id=$1 AND network='SOLANA'
ORDER BY created_at DESC
LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Transaction
	for rows.Next() {
		var tx Transaction
		var feeRaw, reservedRaw string
		if err := rows.Scan(scanTransaction(&tx, &feeRaw, &reservedRaw)...); err != nil {
			return nil, err
		}
		tx.FeeLamports = parseAmountRaw(feeRaw)
		tx.ReservedLamports = parseAmountRaw(reservedRaw)
		out = append(out, tx)
	}
	return out, rows.Err()
}

func (r *repository) getUserTransaction(ctx context.Context, userID, id string) (*Transaction, error) {
	if err := r.ensureSchema(ctx); err != nil {
		return nil, err
	}
	tx := &Transaction{}
	var feeRaw, reservedRaw string
	err := r.sql.QueryRowContext(ctx, `
SELECT id, user_id, network, signature, asset, mint_address, direction, amount_raw::text, decimals, status, confirmations, slot, operation_id,
       idempotency_key, request_hash, source_address, destination_address, fee_lamports::text, reserved_lamports::text,
       signed_raw_tx, fee_payer, recent_blockhash, last_valid_block_height, signed_at, submitted_at, confirmed_at, created_at, updated_at
FROM sol_transactions
WHERE user_id=$1 AND network='SOLANA'
  AND (id=$2 OR signature=$2 OR operation_id=$2 OR idempotency_key=$2)
ORDER BY created_at DESC
LIMIT 1`, userID, strings.TrimSpace(id)).Scan(scanTransaction(tx, &feeRaw, &reservedRaw)...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	tx.FeeLamports = parseAmountRaw(feeRaw)
	tx.ReservedLamports = parseAmountRaw(reservedRaw)
	return tx, err
}

func (r *repository) pendingWithdrawals(ctx context.Context) ([]Transaction, error) {
	if err := r.ensureSchema(ctx); err != nil {
		return nil, err
	}
	rows, err := r.sql.QueryContext(ctx, `
SELECT id, user_id, network, signature, asset, mint_address, direction, amount_raw::text, decimals, status, confirmations, slot, operation_id,
       idempotency_key, request_hash, source_address, destination_address, fee_lamports::text, reserved_lamports::text,
       signed_raw_tx, fee_payer, recent_blockhash, last_valid_block_height, signed_at, submitted_at, confirmed_at, created_at, updated_at
FROM sol_transactions
WHERE network='SOLANA' AND direction='withdrawal' AND status IN ('reserved','signed','submitted','submit_unknown','broadcast')
ORDER BY updated_at ASC
LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Transaction
	for rows.Next() {
		var tx Transaction
		var feeRaw, reservedRaw string
		if err := rows.Scan(scanTransaction(&tx, &feeRaw, &reservedRaw)...); err != nil {
			return nil, err
		}
		tx.FeeLamports = parseAmountRaw(feeRaw)
		tx.ReservedLamports = parseAmountRaw(reservedRaw)
		out = append(out, tx)
	}
	return out, rows.Err()
}

func (r *repository) updateTransactionStatus(ctx context.Context, signature, status string, confirmations int) error {
	if err := r.ensureSchema(ctx); err != nil {
		return err
	}
	_, err := r.sql.ExecContext(ctx, `
UPDATE sol_transactions
SET status=$2, confirmations=GREATEST(confirmations, $3),
    confirmed_at=CASE WHEN $2 IN ('confirmed','finalized') THEN COALESCE(confirmed_at, NOW()) ELSE confirmed_at END,
    updated_at=NOW()
WHERE network='SOLANA' AND signature=$1`, signature, status, confirmations)
	return err
}

func (r *repository) seenReceiveKey(ctx context.Context, receiveKey string) (bool, error) {
	if err := r.ensureSchema(ctx); err != nil {
		return false, err
	}
	var exists bool
	err := r.sql.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sol_transactions WHERE network='SOLANA' AND receive_key=$1)`, receiveKey).Scan(&exists)
	return exists, err
}

func lamportsString(lamports int64) string {
	return strconv.FormatInt(lamports, 10)
}

func parseAmountRaw(raw string) int64 {
	value, _ := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	return value
}

func requestHash(userID, to string, amount int64, key string) string {
	return fmt.Sprintf("%x", sha256Bytes(userID+"|"+to+"|"+strconv.FormatInt(amount, 10)+"|"+key))
}

func depositReceiveKey(tx Transaction) string {
	if tx.Direction != DirectionDeposit {
		return ""
	}
	address := strings.TrimSpace(tx.DestinationAddress)
	if address == "" {
		address = strings.TrimSpace(tx.SourceAddress)
	}
	if address == "" {
		return ""
	}
	return tx.Signature + ":balance_delta:" + address
}

func scanTransaction(tx *Transaction, feeRaw, reservedRaw *string) []any {
	return []any{
		&tx.ID, &tx.UserID, &tx.Network, &tx.Signature, &tx.Asset, &tx.MintAddress, &tx.Direction, &tx.AmountRaw,
		&tx.Decimals, &tx.Status, &tx.Confirmations, &tx.Slot, &tx.OperationID, &tx.IdempotencyKey, &tx.RequestHash, &tx.SourceAddress,
		&tx.DestinationAddress, feeRaw, reservedRaw, &tx.SignedRawTx, &tx.FeePayer, &tx.RecentBlockhash,
		&tx.LastValidBlockHeight, &tx.SignedAt, &tx.SubmittedAt, &tx.ConfirmedAt, &tx.CreatedAt, &tx.UpdatedAt,
	}
}

func scanTransactionWithHash(tx *Transaction, feeRaw, reservedRaw *string, hash *sql.NullString) []any {
	items := scanTransaction(tx, feeRaw, reservedRaw)
	return append(items, hash)
}

func binaryHash(value string) uint64 {
	sum := sha256Bytes(value)
	var out uint64
	for _, b := range sum[:8] {
		out = (out << 8) | uint64(b)
	}
	return out
}
