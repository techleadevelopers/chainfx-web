package database

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// ── Types ──────────────────────────────────────────────────────────────────────

// GasRelayRequest represents a row in gas_relay_requests.
type GasRelayRequest struct {
	ID           string
	UserAddress  string
	SigR         string
	SigS         string
	SigHash      string
	TxTo         string
	TxData       string
	FeeUSDT      float64
	GasPriceGwei float64
	GasLimit     int64
	Status       string
	TxHash       *string
	Attempts     int
	NextRetryAt  *time.Time
	DLQAt        *time.Time
	LastError    *string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// AutoSweeperRun represents a row in auto_sweeper_runs.
type AutoSweeperRun struct {
	ID          string
	Network     string
	HotWallet   string
	ColdWallet  string
	BalanceUSDT float64
	SweptUSDT   float64
	TxHash      *string
	Status      string
	ErrorMsg    *string
	RanAt       time.Time
}

// CreateGasRelayParams holds the inputs for CreateGasRelayRequest.
type CreateGasRelayParams struct {
	UserAddress  string
	SigR         string
	SigS         string
	SigHash      string
	TxTo         string
	TxData       string
	FeeUSDT      float64
	GasPriceGwei float64
	GasLimit     int64
}

// ── CRUD ───────────────────────────────────────────────────────────────────────

// CreateGasRelayRequest inserts a new relay request and returns its UUID.
// ON CONFLICT on sig_hash is idempotent — returns existing ID.
func (db *DB) CreateGasRelayRequest(ctx context.Context, p CreateGasRelayParams) (string, error) {
	const q = `
		INSERT INTO gas_relay_requests
			(user_address, sig_r, sig_s, sig_hash, tx_to, tx_data,
			 fee_usdt, gas_price_gwei, gas_limit, status)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'pending')
		ON CONFLICT (sig_hash) DO NOTHING
		RETURNING id`
	var id string
	err := db.SQL.QueryRowContext(ctx, q,
		p.UserAddress, p.SigR, p.SigS, p.SigHash, p.TxTo, p.TxData,
		p.FeeUSDT, p.GasPriceGwei, p.GasLimit,
	).Scan(&id)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("CreateGasRelayRequest: duplicate sig_hash")
	}
	if err != nil {
		return "", fmt.Errorf("CreateGasRelayRequest: %w", err)
	}
	return id, nil
}

// GetGasRelayRequest fetches a relay request by ID.
func (db *DB) GetGasRelayRequest(ctx context.Context, id string) (*GasRelayRequest, error) {
	const q = `
		SELECT id, user_address, sig_r, sig_s, sig_hash, tx_to, tx_data,
		       fee_usdt, gas_price_gwei, gas_limit, status, tx_hash,
		       attempts, next_retry_at, dlq_at, last_error, created_at, updated_at
		FROM gas_relay_requests
		WHERE id = $1`
	r := &GasRelayRequest{}
	err := db.SQL.QueryRowContext(ctx, q, id).Scan(
		&r.ID, &r.UserAddress, &r.SigR, &r.SigS, &r.SigHash, &r.TxTo, &r.TxData,
		&r.FeeUSDT, &r.GasPriceGwei, &r.GasLimit, &r.Status, &r.TxHash,
		&r.Attempts, &r.NextRetryAt, &r.DLQAt, &r.LastError, &r.CreatedAt, &r.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("relay %s not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("GetGasRelayRequest: %w", err)
	}
	return r, nil
}

// ListPendingGasRelays returns up to 50 relays in pending/processing state.
func (db *DB) ListPendingGasRelays(ctx context.Context) ([]GasRelayRequest, error) {
	const q = `
		SELECT id, user_address, sig_r, sig_s, sig_hash, tx_to, tx_data,
		       fee_usdt, gas_price_gwei, gas_limit, status, tx_hash,
		       attempts, next_retry_at, dlq_at, last_error, created_at, updated_at
		FROM gas_relay_requests
		WHERE status IN ('pending','processing')
		ORDER BY created_at ASC
		LIMIT 50`
	return db.scanRelays(ctx, q)
}

// ListRetryableRelays returns relays eligible for retry by the fallback poller.
func (db *DB) ListRetryableRelays(ctx context.Context) ([]GasRelayRequest, error) {
	const q = `
		SELECT id, user_address, sig_r, sig_s, sig_hash, tx_to, tx_data,
		       fee_usdt, gas_price_gwei, gas_limit, status, tx_hash,
		       attempts, next_retry_at, dlq_at, last_error, created_at, updated_at
		FROM gas_relay_requests
		WHERE status IN ('pending','failed')
		  AND (next_retry_at IS NULL OR next_retry_at <= NOW())
		  AND attempts < 4
		ORDER BY created_at ASC
		LIMIT 20`
	return db.scanRelays(ctx, q)
}

// UpdateGasRelayStatus updates status and optionally tx_hash.
func (db *DB) UpdateGasRelayStatus(ctx context.Context, id, status string, txHash *string) error {
	const q = `
		UPDATE gas_relay_requests
		SET status = $2, tx_hash = COALESCE($3, tx_hash), updated_at = NOW()
		WHERE id = $1`
	_, err := db.SQL.ExecContext(ctx, q, id, status, txHash)
	if err != nil {
		return fmt.Errorf("UpdateGasRelayStatus: %w", err)
	}
	return nil
}

// IncrementRelayAttempts increments the attempt counter and sets next_retry_at.
func (db *DB) IncrementRelayAttempts(ctx context.Context, id, errMsg string, nextRetry time.Time) error {
	const q = `
		UPDATE gas_relay_requests
		SET attempts = attempts + 1,
		    last_error = $2,
		    next_retry_at = $3,
		    status = 'failed',
		    updated_at = NOW()
		WHERE id = $1`
	_, err := db.SQL.ExecContext(ctx, q, id, errMsg, nextRetry)
	if err != nil {
		return fmt.Errorf("IncrementRelayAttempts: %w", err)
	}
	return nil
}

// MarkRelayDLQ marks a relay as permanently failed (dead-letter queue).
func (db *DB) MarkRelayDLQ(ctx context.Context, id, errMsg string) error {
	const q = `
		UPDATE gas_relay_requests
		SET status = 'dlq',
		    dlq_at = NOW(),
		    last_error = $2,
		    updated_at = NOW()
		WHERE id = $1`
	_, err := db.SQL.ExecContext(ctx, q, id, errMsg)
	if err != nil {
		return fmt.Errorf("MarkRelayDLQ: %w", err)
	}
	return nil
}

// GasRelayStats returns aggregate stats for the admin dashboard.
func (db *DB) GasRelayStats(ctx context.Context) (map[string]any, error) {
	const q = `
		SELECT
			COUNT(*) FILTER (WHERE status = 'pending')    AS pending,
			COUNT(*) FILTER (WHERE status = 'processing') AS processing,
			COUNT(*) FILTER (WHERE status = 'sent')       AS sent,
			COUNT(*) FILTER (WHERE status = 'failed')     AS failed,
			COUNT(*) FILTER (WHERE status = 'dlq')        AS dlq,
			COALESCE(SUM(fee_usdt) FILTER (WHERE status = 'sent'), 0) AS total_fee_usdt,
			COUNT(*) AS total
		FROM gas_relay_requests
		WHERE created_at > NOW() - INTERVAL '24 hours'`
	row := db.SQL.QueryRowContext(ctx, q)
	var pending, processing, sent, failed, dlq, total int64
	var totalFee float64
	if err := row.Scan(&pending, &processing, &sent, &failed, &dlq, &totalFee, &total); err != nil {
		return nil, fmt.Errorf("GasRelayStats: %w", err)
	}
	return map[string]any{
		"pending":        pending,
		"processing":     processing,
		"sent":           sent,
		"failed":         failed,
		"dlq":            dlq,
		"total_fee_usdt": totalFee,
		"total_24h":      total,
	}, nil
}

// ── Auto-Sweeper ───────────────────────────────────────────────────────────────

// RecordAutoSweeperRun inserts a sweeper run record.
func (db *DB) RecordAutoSweeperRun(ctx context.Context, r AutoSweeperRun) error {
	const q = `
		INSERT INTO auto_sweeper_runs
			(network, hot_wallet, cold_wallet, balance_usdt, swept_usdt, tx_hash, status, error_msg)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`
	_, err := db.SQL.ExecContext(ctx, q,
		r.Network, r.HotWallet, r.ColdWallet, r.BalanceUSDT, r.SweptUSDT,
		r.TxHash, r.Status, r.ErrorMsg,
	)
	if err != nil {
		return fmt.Errorf("RecordAutoSweeperRun: %w", err)
	}
	return nil
}

// ListAutoSweeperRuns returns the last n runs.
func (db *DB) ListAutoSweeperRuns(ctx context.Context, limit int) ([]AutoSweeperRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	const q = `
		SELECT id, network, hot_wallet, cold_wallet, balance_usdt, swept_usdt,
		       tx_hash, status, error_msg, ran_at
		FROM auto_sweeper_runs
		ORDER BY ran_at DESC
		LIMIT $1`
	rows, err := db.SQL.QueryContext(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("ListAutoSweeperRuns: %w", err)
	}
	defer rows.Close()
	var runs []AutoSweeperRun
	for rows.Next() {
		var r AutoSweeperRun
		if err := rows.Scan(
			&r.ID, &r.Network, &r.HotWallet, &r.ColdWallet,
			&r.BalanceUSDT, &r.SweptUSDT, &r.TxHash, &r.Status, &r.ErrorMsg, &r.RanAt,
		); err != nil {
			return nil, err
		}
		runs = append(runs, r)
	}
	return runs, rows.Err()
}

// AutoSweeperStats returns aggregate sweeper stats.
func (db *DB) AutoSweeperStats(ctx context.Context) (map[string]any, error) {
	const q = `
		SELECT
			COUNT(*) AS total_runs,
			COUNT(*) FILTER (WHERE status = 'ok')      AS successful,
			COUNT(*) FILTER (WHERE status = 'error')   AS errors,
			COUNT(*) FILTER (WHERE status = 'skipped') AS skipped,
			COALESCE(SUM(swept_usdt) FILTER (WHERE status = 'ok'), 0) AS total_swept_usdt
		FROM auto_sweeper_runs
		WHERE ran_at > NOW() - INTERVAL '24 hours'`
	row := db.SQL.QueryRowContext(ctx, q)
	var total, successful, errors, skipped int64
	var totalSwept float64
	if err := row.Scan(&total, &successful, &errors, &skipped, &totalSwept); err != nil {
		return nil, fmt.Errorf("AutoSweeperStats: %w", err)
	}
	return map[string]any{
		"total_runs_24h":   total,
		"successful":       successful,
		"errors":           errors,
		"skipped":          skipped,
		"total_swept_usdt": totalSwept,
	}, nil
}

// ── Scanner helper ─────────────────────────────────────────────────────────────

func (db *DB) scanRelays(ctx context.Context, query string, args ...any) ([]GasRelayRequest, error) {
	rows, err := db.SQL.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("scanRelays: %w", err)
	}
	defer rows.Close()
	var out []GasRelayRequest
	for rows.Next() {
		var r GasRelayRequest
		if err := rows.Scan(
			&r.ID, &r.UserAddress, &r.SigR, &r.SigS, &r.SigHash, &r.TxTo, &r.TxData,
			&r.FeeUSDT, &r.GasPriceGwei, &r.GasLimit, &r.Status, &r.TxHash,
			&r.Attempts, &r.NextRetryAt, &r.DLQAt, &r.LastError, &r.CreatedAt, &r.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
