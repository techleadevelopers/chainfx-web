package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// M2MIntentStatus represents the lifecycle status of an agent payment intent.
type M2MIntentStatus string

const (
	M2MStatusPendingDeposit M2MIntentStatus = "pending_deposit"
	M2MStatusPaidCrypto     M2MIntentStatus = "paid_crypto"
	M2MStatusSettling       M2MIntentStatus = "settling"
	M2MStatusSettled        M2MIntentStatus = "settled"
	M2MStatusFailed         M2MIntentStatus = "failed"
	M2MStatusExpired        M2MIntentStatus = "expired"
)

// M2MPaymentType is the fiat rail the system will use on the recipient side.
type M2MPaymentType string

const (
	M2MTypePix        M2MPaymentType = "pix"
	M2MTypeCreditCard M2MPaymentType = "credit_card"
)

// AgentPaymentIntent is the full state of one M2M payment intent.
type AgentPaymentIntent struct {
	ID                    string          `json:"id"`
	IdempotencyKey        string          `json:"-"`
	AgentWallet           string          `json:"agent_wallet"`
	PaymentType           M2MPaymentType  `json:"payment_type"`
	PixKey                string          `json:"pix_key,omitempty"`
	PaymentLink           string          `json:"payment_link,omitempty"`
	Barcode               string          `json:"barcode,omitempty"`
	BeneficiaryName       string          `json:"beneficiary_name,omitempty"`
	DueDate               string          `json:"due_date,omitempty"`
	AmountBRL             float64         `json:"amount_brl"`
	FeeBps                int             `json:"fee_bps"`
	FeeUSDT               float64         `json:"fee_usdt"`
	GrossUSDT             float64         `json:"gross_usdt"`
	RequiredUSDT          float64         `json:"required_usdt"`
	USDTRate              float64         `json:"usdt_rate"`
	PaymentAddress        string          `json:"payment_address"`
	PaymentNetwork        string          `json:"payment_network"`
	Status                M2MIntentStatus `json:"status"`
	DepositTx             *string         `json:"deposit_tx,omitempty"`
	DepositAmountUSDT     *float64        `json:"deposit_amount_usdt,omitempty"`
	EfiEndToEndID         *string         `json:"efi_end_to_end_id,omitempty"`
	EfiStatus             *string         `json:"efi_status,omitempty"`
	SettlementReceiptURL  string          `json:"settlement_receipt_url,omitempty"`
	SettlementReceiptNote string          `json:"settlement_receipt_note,omitempty"`
	ErrorMessage          *string         `json:"error_message,omitempty"`
	Attempts              int             `json:"attempts"`
	RequestHash           string          `json:"request_hash"`
	ExpiresAt             time.Time       `json:"expires_at"`
	SettledAt             *time.Time      `json:"settled_at,omitempty"`
	CreatedAt             time.Time       `json:"created_at"`
	UpdatedAt             time.Time       `json:"updated_at"`
}

// M2MCreateInput contains the validated fields for creating a new intent.
type M2MCreateInput struct {
	ID              string
	IdempotencyKey  string
	AgentWallet     string
	PaymentType     M2MPaymentType
	PixKey          string
	PaymentLink     string
	Barcode         string
	BeneficiaryName string
	DueDate         string
	AmountBRL       float64
	FeeBps          int
	FeeUSDT         float64
	GrossUSDT       float64
	RequiredUSDT    float64
	USDTRate        float64
	PaymentAddress  string
	PaymentNetwork  string
	RequestHash     string
	ExpiresAt       time.Time
}

// CanonicalRequestHash returns a deterministic hex SHA-256 for audit purposes.
func CanonicalRequestHash(parts ...string) string {
	joined := strings.Join(parts, "|")
	sum := sha256.Sum256([]byte(joined))
	return hex.EncodeToString(sum[:])
}

// CreateAgentPaymentIntent inserts a new intent and audit log entry atomically.
// If the idempotency_key already exists, it returns the existing intent without error.
func (db *DB) CreateAgentPaymentIntent(ctx context.Context, in M2MCreateInput) (*AgentPaymentIntent, bool, error) {
	tx, err := db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, fmt.Errorf("m2m: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Idempotency check — return existing intent if key already used.
	existing, err := txGetIntentByIdempotencyKey(ctx, tx, in.IdempotencyKey)
	if err != nil {
		return nil, false, fmt.Errorf("m2m: idempotency check: %w", err)
	}
	if existing != nil {
		_ = tx.Rollback()
		return existing, true, nil
	}

	pixKey := sql.NullString{String: in.PixKey, Valid: in.PixKey != ""}
	paymentLink := sql.NullString{String: in.PaymentLink, Valid: in.PaymentLink != ""}
	barcode := sql.NullString{String: in.Barcode, Valid: in.Barcode != ""}
	beneficiaryName := sql.NullString{String: in.BeneficiaryName, Valid: in.BeneficiaryName != ""}
	dueDate := sql.NullString{String: in.DueDate, Valid: in.DueDate != ""}

	const q = `
INSERT INTO agent_payment_intents
    (id, idempotency_key, agent_wallet, payment_type, pix_key, payment_link, barcode, beneficiary_name, due_date,
     amount_brl, fee_bps, fee_usdt, gross_usdt, required_usdt, usdt_rate,
     payment_address, payment_network, status, request_hash, expires_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,'pending_deposit',$18,$19)
RETURNING id, idempotency_key, agent_wallet, payment_type, pix_key, payment_link, barcode, beneficiary_name, due_date,
          amount_brl, fee_bps, fee_usdt, gross_usdt, required_usdt, usdt_rate,
          payment_address, payment_network, status, request_hash, expires_at, attempts, created_at, updated_at`

	row := tx.QueryRowContext(ctx, q,
		in.ID, in.IdempotencyKey, in.AgentWallet, string(in.PaymentType), pixKey,
		paymentLink, barcode, beneficiaryName, dueDate,
		in.AmountBRL, in.FeeBps, in.FeeUSDT, in.GrossUSDT, in.RequiredUSDT, in.USDTRate,
		in.PaymentAddress, strings.ToUpper(strings.TrimSpace(firstNonEmptyDB(in.PaymentNetwork, "BSC"))), in.RequestHash, in.ExpiresAt,
	)
	intent, err := scanIntent(row)
	if err != nil {
		return nil, false, fmt.Errorf("m2m: insert intent: %w", err)
	}

	if err := txAppendAuditLog(ctx, tx, intent.ID, "created", map[string]any{
		"payment_type":    intent.PaymentType,
		"amount_brl":      intent.AmountBRL,
		"required_usdt":   intent.RequiredUSDT,
		"fee_bps":         intent.FeeBps,
		"agent_wallet":    intent.AgentWallet,
		"payment_address": intent.PaymentAddress,
		"payment_network": intent.PaymentNetwork,
		"payment_link":    intent.PaymentLink,
		"barcode":         intent.Barcode,
		"beneficiary":     intent.BeneficiaryName,
		"due_date":        intent.DueDate,
		"expires_at":      intent.ExpiresAt,
	}); err != nil {
		return nil, false, fmt.Errorf("m2m: audit log insert: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("m2m: commit: %w", err)
	}
	return intent, false, nil
}

// GetAgentPaymentIntent fetches an intent by ID and auto-transitions
// pending_deposit intents past their expiry to 'expired'.
func (db *DB) GetAgentPaymentIntent(ctx context.Context, id string) (*AgentPaymentIntent, error) {
	const q = `
SELECT id, idempotency_key, agent_wallet, payment_type, pix_key, payment_link, barcode, beneficiary_name, due_date,
       amount_brl, fee_bps, fee_usdt, gross_usdt, required_usdt, usdt_rate,
       payment_address, payment_network, status, deposit_tx, deposit_amount_usdt,
       efi_end_to_end_id, efi_status, settlement_receipt_url, settlement_receipt_note, error_message, attempts,
       request_hash, expires_at, settled_at, created_at, updated_at
FROM agent_payment_intents
WHERE id = $1`

	row := db.SQL.QueryRowContext(ctx, q, id)
	intent, err := scanIntentFull(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("m2m: get intent: %w", err)
	}

	// Lazy expiry: if still pending and past TTL, mark expired.
	if intent.Status == M2MStatusPendingDeposit && time.Now().After(intent.ExpiresAt) {
		_, _ = db.SQL.ExecContext(ctx,
			`UPDATE agent_payment_intents SET status='expired', updated_at=NOW()
			 WHERE id=$1 AND status='pending_deposit'`, id)
		intent.Status = M2MStatusExpired
	}
	return intent, nil
}

// M2MDepositMatch holds a candidate intent that matches a blockchain deposit.
type M2MDepositMatch struct {
	IntentID     string
	AgentWallet  string
	RequiredUSDT float64
}

// PickAvailableM2MDepositAddress returns a configured deposit address for a new
// intent. It prefers the least-loaded address, but does not block reuse: the
// on-chain matcher reconciles shared addresses by deposit amount and fails
// closed only when the amount is ambiguous.
func (db *DB) PickAvailableM2MDepositAddress(ctx context.Context, candidates []string, fallback string) (string, error) {
	seen := make(map[string]struct{}, len(candidates)+1)
	var addresses []string
	for _, a := range candidates {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" {
			continue
		}
		if _, ok := seen[a]; ok {
			continue
		}
		seen[a] = struct{}{}
		addresses = append(addresses, a)
	}
	fallback = strings.ToLower(strings.TrimSpace(fallback))
	if fallback != "" {
		if _, ok := seen[fallback]; !ok {
			addresses = append(addresses, fallback)
		}
	}
	if len(addresses) == 0 {
		return "", fmt.Errorf("m2m: no deposit addresses configured")
	}

	const q = `
SELECT COUNT(*)
FROM agent_payment_intents
WHERE LOWER(payment_address) = $1
  AND status = 'pending_deposit'
  AND expires_at > NOW()`
	bestAddress := addresses[0]
	bestCount := int(^uint(0) >> 1)
	for _, address := range addresses {
		var pendingCount int
		if err := db.SQL.QueryRowContext(ctx, q, address).Scan(&pendingCount); err != nil {
			return "", fmt.Errorf("m2m: check deposit address: %w", err)
		}
		if pendingCount < bestCount {
			bestAddress = address
			bestCount = pendingCount
		}
	}
	return bestAddress, nil
}

// ListPendingM2MDepositAddresses returns all active M2M payment addresses the
// on-chain worker must monitor.
func (db *DB) ListPendingM2MDepositAddresses(ctx context.Context) ([]string, error) {
	const q = `
SELECT DISTINCT LOWER(payment_address)
FROM   agent_payment_intents
WHERE  status = 'pending_deposit'
  AND  expires_at > NOW()`
	rows, err := db.SQL.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("m2m: list pending deposit addresses: %w", err)
	}
	defer rows.Close()

	var addresses []string
	for rows.Next() {
		var address string
		if err := rows.Scan(&address); err != nil {
			return nil, err
		}
		addresses = append(addresses, address)
	}
	return addresses, rows.Err()
}

// FindPendingIntentsByDepositAddress returns non-expired pending intents for a
// given deposit address. Shared deposit addresses are supported; reconciliation
// selects a unique candidate by amount in the on-chain worker.
func (db *DB) FindPendingIntentsByDepositAddress(ctx context.Context, address string, network string) ([]M2MDepositMatch, error) {
	const q = `
SELECT id, agent_wallet, required_usdt
FROM   agent_payment_intents
WHERE  payment_address = $1
  AND  UPPER(COALESCE(payment_network, 'BSC')) = UPPER($2)
  AND  status = 'pending_deposit'
  AND  expires_at > NOW()
ORDER BY created_at`

	rows, err := db.SQL.QueryContext(ctx, q, strings.ToLower(address), strings.ToUpper(strings.TrimSpace(network)))
	if err != nil {
		return nil, fmt.Errorf("m2m: find pending intents: %w", err)
	}
	defer rows.Close()

	var matches []M2MDepositMatch
	for rows.Next() {
		var m M2MDepositMatch
		if err := rows.Scan(&m.IntentID, &m.AgentWallet, &m.RequiredUSDT); err != nil {
			return nil, err
		}
		matches = append(matches, m)
	}
	return matches, rows.Err()
}

// ConfirmM2MDeposit atomically claims a pending intent for the given on-chain
// deposit. Returns (true, nil) if the claim succeeded, (false, nil) if the
// intent was already claimed or in a different state.
func (db *DB) ConfirmM2MDeposit(ctx context.Context, intentID, txHash string, depositUSDT float64) (bool, error) {
	const q = `
UPDATE agent_payment_intents
SET    status             = 'paid_crypto',
       deposit_tx         = $2,
       deposit_amount_usdt = $3,
       updated_at         = NOW()
WHERE  id = $1
  AND  status = 'pending_deposit'
  AND  deposit_tx IS NULL`

	res, err := db.SQL.ExecContext(ctx, q, intentID, txHash, depositUSDT)
	if err != nil {
		return false, fmt.Errorf("m2m: confirm deposit: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return false, nil
	}

	_ = db.appendAuditLog(ctx, intentID, "deposit_confirmed", map[string]any{
		"deposit_tx":   txHash,
		"deposit_usdt": depositUSDT,
	})
	return true, nil
}

// AcquireM2MSettlementLock uses a PostgreSQL advisory lock (per-intent) inside
// a transaction to prevent duplicate settlement across multiple replicas.
// The caller MUST call the returned release func when done processing.
// Returns (false, nil, nil) when another replica already holds the lock.
func (db *DB) AcquireM2MSettlementLock(ctx context.Context, intentID string) (bool, *sql.Tx, error) {
	tx, err := db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return false, nil, fmt.Errorf("m2m: begin lock tx: %w", err)
	}

	var locked bool
	// pg_try_advisory_xact_lock is released automatically when tx commits/rolls back.
	if err := tx.QueryRowContext(ctx,
		`SELECT pg_try_advisory_xact_lock(hashtext($1))`, intentID,
	).Scan(&locked); err != nil {
		_ = tx.Rollback()
		return false, nil, fmt.Errorf("m2m: advisory lock: %w", err)
	}
	if !locked {
		_ = tx.Rollback()
		return false, nil, nil
	}
	return true, tx, nil
}

// MarkM2MSettling transitions paid_crypto → settling inside the provided
// locked transaction (so the advisory lock and the status update are atomic).
func (db *DB) MarkM2MSettling(ctx context.Context, tx *sql.Tx, intentID string) error {
	res, err := tx.ExecContext(ctx,
		`UPDATE agent_payment_intents SET status='settling', attempts=attempts+1, updated_at=NOW()
		 WHERE id=$1 AND status='paid_crypto'`, intentID)
	if err != nil {
		return fmt.Errorf("m2m: mark settling: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("m2m: intent %s not in paid_crypto state", intentID)
	}
	return nil
}

// MarkM2MSettled records successful settlement and commits the lock transaction.
func (db *DB) MarkM2MSettled(ctx context.Context, tx *sql.Tx, intentID, efiEndToEndID, efiStatus string) error {
	return db.MarkM2MSettledWithReceipt(ctx, tx, intentID, efiEndToEndID, efiStatus, "", "")
}

// MarkM2MSettledWithReceipt records successful settlement and stores an optional receipt for the agent.
func (db *DB) MarkM2MSettledWithReceipt(ctx context.Context, tx *sql.Tx, intentID, efiEndToEndID, efiStatus, receiptURL, receiptNote string) error {
	receiptURLValue := sql.NullString{String: receiptURL, Valid: receiptURL != ""}
	receiptNoteValue := sql.NullString{String: receiptNote, Valid: receiptNote != ""}
	_, err := tx.ExecContext(ctx,
		`UPDATE agent_payment_intents
		 SET status='settled', efi_end_to_end_id=$2, efi_status=$3,
		     settlement_receipt_url=$4, settlement_receipt_note=$5,
		     settled_at=NOW(), updated_at=NOW()
		 WHERE id=$1`, intentID, efiEndToEndID, efiStatus, receiptURLValue, receiptNoteValue)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("m2m: mark settled: %w", err)
	}
	_ = txAppendAuditLog(ctx, tx, intentID, "settlement_succeeded", map[string]any{
		"efi_end_to_end_id": efiEndToEndID,
		"efi_status":        efiStatus,
		"receipt_url":       receiptURL,
		"receipt_note":      receiptNote,
	})
	return tx.Commit()
}

// MarkM2MFailed records a settlement failure and commits the lock transaction.
func (db *DB) MarkM2MFailed(ctx context.Context, tx *sql.Tx, intentID, errMsg string, permanent bool) error {
	nextStatus := "paid_crypto" // allow retry
	if permanent {
		nextStatus = "failed"
	}
	_, err := tx.ExecContext(ctx,
		`UPDATE agent_payment_intents
		 SET status=$2, error_message=$3, updated_at=NOW()
		 WHERE id=$1`, intentID, nextStatus, errMsg)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("m2m: mark failed: %w", err)
	}
	_ = txAppendAuditLog(ctx, tx, intentID, "settlement_failed", map[string]any{
		"error":     errMsg,
		"permanent": permanent,
		"next":      nextStatus,
	})
	return tx.Commit()
}

// ListAgentPaymentIntentsByWallet returns recent payment intents for a given agent wallet.
func (db *DB) ListAgentPaymentIntentsByWallet(ctx context.Context, wallet, statusFilter string, limit int) ([]AgentPaymentIntent, error) {
	wallet = strings.ToLower(strings.TrimSpace(wallet))
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	args := []any{wallet, limit}
	where := "lower(agent_wallet) = $1"
	if s := strings.TrimSpace(statusFilter); s != "" {
		args = []any{wallet, s, limit}
		where = "lower(agent_wallet) = $1 AND status = $2"
	}
	const qBase = `
SELECT id, idempotency_key, agent_wallet, payment_type, pix_key, payment_link, barcode, beneficiary_name, due_date,
       amount_brl, fee_bps, fee_usdt, gross_usdt, required_usdt, usdt_rate,
       payment_address, payment_network, status, deposit_tx, deposit_amount_usdt,
       efi_end_to_end_id, efi_status, settlement_receipt_url, settlement_receipt_note, error_message, attempts,
       request_hash, expires_at, settled_at, created_at, updated_at
FROM   agent_payment_intents
WHERE  `
	limitIdx := len(args)
	rows, err := db.SQL.QueryContext(ctx, qBase+where+fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", limitIdx), args...)
	if err != nil {
		return nil, fmt.Errorf("m2m: list by wallet: %w", err)
	}
	defer rows.Close()
	var out []AgentPaymentIntent
	for rows.Next() {
		intent, err := scanIntentFullRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *intent)
	}
	return out, rows.Err()
}

// GetPaidCryptoIntents returns intents ready for fiat settlement (up to 50 at a time).
func (db *DB) GetPaidCryptoIntents(ctx context.Context) ([]AgentPaymentIntent, error) {
	const q = `
SELECT id, idempotency_key, agent_wallet, payment_type, pix_key, payment_link, barcode, beneficiary_name, due_date,
       amount_brl, fee_bps, fee_usdt, gross_usdt, required_usdt, usdt_rate,
       payment_address, payment_network, status, deposit_tx, deposit_amount_usdt,
       efi_end_to_end_id, efi_status, settlement_receipt_url, settlement_receipt_note, error_message, attempts,
       request_hash, expires_at, settled_at, created_at, updated_at
FROM   agent_payment_intents
WHERE  status = 'paid_crypto'
  AND  attempts < 4
ORDER BY created_at
LIMIT  50`

	rows, err := db.SQL.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("m2m: get paid_crypto intents: %w", err)
	}
	defer rows.Close()

	var out []AgentPaymentIntent
	for rows.Next() {
		intent, err := scanIntentFullRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *intent)
	}
	return out, rows.Err()
}

// M2MDailyOutflowBRL returns the total amount_brl settled in the last 24 h.
func (db *DB) M2MDailyOutflowBRL(ctx context.Context) (float64, error) {
	var total float64
	err := db.SQL.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(amount_brl),0) FROM agent_payment_intents
		 WHERE status='settled' AND settled_at > NOW() - INTERVAL '24 hours'`,
	).Scan(&total)
	return total, err
}

// appendAuditLog writes an immutable audit entry (non-transactional version).
func (db *DB) appendAuditLog(ctx context.Context, intentID, event string, payload map[string]any) error {
	raw, _ := json.Marshal(payload)
	_, err := db.SQL.ExecContext(ctx,
		`INSERT INTO agent_payment_audit_log (intent_id, event, payload) VALUES ($1,$2,$3)`,
		intentID, event, raw)
	return err
}

// ─── Internal helpers ────────────────────────────────────────────────────────

func txGetIntentByIdempotencyKey(ctx context.Context, tx *sql.Tx, key string) (*AgentPaymentIntent, error) {
	const q = `
SELECT id, idempotency_key, agent_wallet, payment_type, pix_key, payment_link, barcode, beneficiary_name, due_date,
       amount_brl, fee_bps, fee_usdt, gross_usdt, required_usdt, usdt_rate,
       payment_address, payment_network, status, deposit_tx, deposit_amount_usdt,
       efi_end_to_end_id, efi_status, settlement_receipt_url, settlement_receipt_note, error_message, attempts,
       request_hash, expires_at, settled_at, created_at, updated_at
FROM   agent_payment_intents WHERE idempotency_key = $1`
	row := tx.QueryRowContext(ctx, q, key)
	intent, err := scanIntentFull(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return intent, err
}

func txAppendAuditLog(ctx context.Context, tx *sql.Tx, intentID, event string, payload map[string]any) error {
	raw, _ := json.Marshal(payload)
	_, err := tx.ExecContext(ctx,
		`INSERT INTO agent_payment_audit_log (intent_id, event, payload) VALUES ($1,$2,$3)`,
		intentID, event, raw)
	return err
}

// scanIntent scans the minimal set of columns returned by INSERT … RETURNING.
func scanIntent(row *sql.Row) (*AgentPaymentIntent, error) {
	var i AgentPaymentIntent
	var pixKey, paymentLink, barcode, beneficiaryName, dueDate sql.NullString
	err := row.Scan(
		&i.ID, &i.IdempotencyKey, &i.AgentWallet, &i.PaymentType, &pixKey, &paymentLink, &barcode, &beneficiaryName, &dueDate,
		&i.AmountBRL, &i.FeeBps, &i.FeeUSDT, &i.GrossUSDT, &i.RequiredUSDT, &i.USDTRate,
		&i.PaymentAddress, &i.PaymentNetwork, &i.Status, &i.RequestHash, &i.ExpiresAt,
		&i.Attempts, &i.CreatedAt, &i.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if pixKey.Valid {
		i.PixKey = pixKey.String
	}
	applyM2MDestinationFields(&i, paymentLink, barcode, beneficiaryName, dueDate)
	return &i, nil
}

func scanIntentFull(row rowScanner) (*AgentPaymentIntent, error) {
	var i AgentPaymentIntent
	var pixKey, paymentLink, barcode, beneficiaryName, dueDate, depositTx, efiID, efiStatus, receiptURL, receiptNote, errMsg sql.NullString
	var depositUSDT sql.NullFloat64
	var settledAt sql.NullTime
	err := row.Scan(
		&i.ID, &i.IdempotencyKey, &i.AgentWallet, &i.PaymentType, &pixKey, &paymentLink, &barcode, &beneficiaryName, &dueDate,
		&i.AmountBRL, &i.FeeBps, &i.FeeUSDT, &i.GrossUSDT, &i.RequiredUSDT, &i.USDTRate,
		&i.PaymentAddress, &i.PaymentNetwork, &i.Status, &depositTx, &depositUSDT,
		&efiID, &efiStatus, &receiptURL, &receiptNote, &errMsg, &i.Attempts,
		&i.RequestHash, &i.ExpiresAt, &settledAt, &i.CreatedAt, &i.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if pixKey.Valid {
		i.PixKey = pixKey.String
	}
	applyM2MDestinationFields(&i, paymentLink, barcode, beneficiaryName, dueDate)
	if depositTx.Valid {
		i.DepositTx = &depositTx.String
	}
	if depositUSDT.Valid {
		i.DepositAmountUSDT = &depositUSDT.Float64
	}
	if efiID.Valid {
		i.EfiEndToEndID = &efiID.String
	}
	if efiStatus.Valid {
		i.EfiStatus = &efiStatus.String
	}
	applyM2MReceiptFields(&i, receiptURL, receiptNote)
	if errMsg.Valid {
		i.ErrorMessage = &errMsg.String
	}
	if settledAt.Valid {
		i.SettledAt = &settledAt.Time
	}
	return &i, nil
}

func scanIntentFullRow(rows *sql.Rows) (*AgentPaymentIntent, error) {
	var i AgentPaymentIntent
	var pixKey, paymentLink, barcode, beneficiaryName, dueDate, depositTx, efiID, efiStatus, receiptURL, receiptNote, errMsg sql.NullString
	var depositUSDT sql.NullFloat64
	var settledAt sql.NullTime
	err := rows.Scan(
		&i.ID, &i.IdempotencyKey, &i.AgentWallet, &i.PaymentType, &pixKey, &paymentLink, &barcode, &beneficiaryName, &dueDate,
		&i.AmountBRL, &i.FeeBps, &i.FeeUSDT, &i.GrossUSDT, &i.RequiredUSDT, &i.USDTRate,
		&i.PaymentAddress, &i.PaymentNetwork, &i.Status, &depositTx, &depositUSDT,
		&efiID, &efiStatus, &receiptURL, &receiptNote, &errMsg, &i.Attempts,
		&i.RequestHash, &i.ExpiresAt, &settledAt, &i.CreatedAt, &i.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if pixKey.Valid {
		i.PixKey = pixKey.String
	}
	applyM2MDestinationFields(&i, paymentLink, barcode, beneficiaryName, dueDate)
	if depositTx.Valid {
		i.DepositTx = &depositTx.String
	}
	if depositUSDT.Valid {
		i.DepositAmountUSDT = &depositUSDT.Float64
	}
	if efiID.Valid {
		i.EfiEndToEndID = &efiID.String
	}
	if efiStatus.Valid {
		i.EfiStatus = &efiStatus.String
	}
	applyM2MReceiptFields(&i, receiptURL, receiptNote)
	if errMsg.Valid {
		i.ErrorMessage = &errMsg.String
	}
	if settledAt.Valid {
		i.SettledAt = &settledAt.Time
	}
	return &i, nil
}

func applyM2MDestinationFields(i *AgentPaymentIntent, paymentLink, barcode, beneficiaryName, dueDate sql.NullString) {
	if paymentLink.Valid {
		i.PaymentLink = paymentLink.String
	}
	if barcode.Valid {
		i.Barcode = barcode.String
	}
	if beneficiaryName.Valid {
		i.BeneficiaryName = beneficiaryName.String
	}
	if dueDate.Valid {
		i.DueDate = dueDate.String
	}
}

func applyM2MReceiptFields(i *AgentPaymentIntent, receiptURL, receiptNote sql.NullString) {
	if receiptURL.Valid {
		i.SettlementReceiptURL = receiptURL.String
	}
	if receiptNote.Valid {
		i.SettlementReceiptNote = receiptNote.String
	}
}
