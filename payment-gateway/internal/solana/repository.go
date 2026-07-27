package solana

import (
	"context"
	"database/sql"
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
    metadata_json       JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_sol_signature UNIQUE (network, signature)
);
CREATE INDEX IF NOT EXISTS idx_sol_transactions_user_created ON sol_transactions (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_sol_transactions_status ON sol_transactions (network, status, updated_at);
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
  (user_id, network, signature, asset, mint_address, direction, amount_raw, decimals, status, confirmations, slot, metadata_json)
VALUES ($1, 'SOLANA', $2, 'SOL', '', $3, $4, 9, $5, $6, $7, COALESCE($8::jsonb, '{}'::jsonb))
ON CONFLICT (network, signature) DO UPDATE SET
  status=EXCLUDED.status,
  confirmations=GREATEST(sol_transactions.confirmations, EXCLUDED.confirmations),
  slot=GREATEST(sol_transactions.slot, EXCLUDED.slot),
  updated_at=NOW()`,
		tx.UserID, tx.Signature, tx.Direction, tx.AmountRaw, tx.Status, tx.Confirmations, tx.Slot, string(payload))
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
	err := r.sql.QueryRowContext(ctx, `
SELECT id, user_id, network, signature, asset, mint_address, direction, amount_raw::text, decimals, status, confirmations, slot, created_at, updated_at,
       metadata_json->>'request_hash'
FROM sol_transactions
WHERE user_id=$1 AND direction='withdrawal' AND metadata_json->>'idempotency_key'=$2
ORDER BY created_at DESC
LIMIT 1`, userID, key).Scan(&tx.ID, &tx.UserID, &tx.Network, &tx.Signature, &tx.Asset, &tx.MintAddress, &tx.Direction, &tx.AmountRaw, &tx.Decimals, &tx.Status, &tx.Confirmations, &tx.Slot, &tx.CreatedAt, &tx.UpdatedAt, &requestHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", nil
	}
	return tx, requestHash.String, err
}

func (r *repository) listUserTransactions(ctx context.Context, userID string, limit int) ([]Transaction, error) {
	if err := r.ensureSchema(ctx); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := r.sql.QueryContext(ctx, `
SELECT id, user_id, network, signature, asset, mint_address, direction, amount_raw::text, decimals, status, confirmations, slot, created_at, updated_at
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
		if err := rows.Scan(&tx.ID, &tx.UserID, &tx.Network, &tx.Signature, &tx.Asset, &tx.MintAddress, &tx.Direction, &tx.AmountRaw, &tx.Decimals, &tx.Status, &tx.Confirmations, &tx.Slot, &tx.CreatedAt, &tx.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, tx)
	}
	return out, rows.Err()
}

func (r *repository) pendingWithdrawals(ctx context.Context) ([]Transaction, error) {
	if err := r.ensureSchema(ctx); err != nil {
		return nil, err
	}
	rows, err := r.sql.QueryContext(ctx, `
SELECT id, user_id, network, signature, asset, mint_address, direction, amount_raw::text, decimals, status, confirmations, slot, created_at, updated_at
FROM sol_transactions
WHERE network='SOLANA' AND direction='withdrawal' AND status IN ('pending','broadcast')
ORDER BY updated_at ASC
LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Transaction
	for rows.Next() {
		var tx Transaction
		if err := rows.Scan(&tx.ID, &tx.UserID, &tx.Network, &tx.Signature, &tx.Asset, &tx.MintAddress, &tx.Direction, &tx.AmountRaw, &tx.Decimals, &tx.Status, &tx.Confirmations, &tx.Slot, &tx.CreatedAt, &tx.UpdatedAt); err != nil {
			return nil, err
		}
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
SET status=$2, confirmations=GREATEST(confirmations, $3), updated_at=NOW()
WHERE network='SOLANA' AND signature=$1`, signature, status, confirmations)
	return err
}

func (r *repository) seenSignature(ctx context.Context, signature string) (bool, error) {
	if err := r.ensureSchema(ctx); err != nil {
		return false, err
	}
	var exists bool
	err := r.sql.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sol_transactions WHERE network='SOLANA' AND signature=$1)`, signature).Scan(&exists)
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
