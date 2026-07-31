package database

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"payment-gateway/internal/config"
	"payment-gateway/internal/models"
	"payment-gateway/internal/privacy"

	_ "github.com/lib/pq"
	"golang.org/x/crypto/bcrypt"
)

type DB struct {
	SQL     *sql.DB
	privacy *privacy.Codec
	cfg     *config.Config
}

type SellPayoutExecution struct {
	ID                     string
	OrderID                string
	Provider               string
	ProviderIDempotencyKey string
	ProviderIDEnvio        string
	AmountBRLMinor         int64
	RecipientReference     string
	Status                 string
	AttemptCount           int
	SubmitStartedAt        *time.Time
	SubmitCompletedAt      *time.Time
	SubmitOutcome          string
	FirstAmbiguousAt       *time.Time
	LastReconciledAt       *time.Time
	ConsecutiveNotFound    int
	NextAttemptAt          time.Time
	ProviderReference      string
	ProviderE2EID          string
	LastError              string
	CreatedAt              time.Time
	UpdatedAt              time.Time
	CompletedAt            *time.Time
}

type OrderInput struct {
	ID                string
	AccessToken       string
	Status            string
	AmountBRL         float64
	AmountUSDT        float64
	FeeBRL            float64
	PayoutBRL         float64
	PixKey            string
	Address           string
	Asset             string
	Network           string
	RateLocked        float64
	RateLockExpiresAt time.Time
	RequestID         string
	PixCpf            string
	PixPhone          string
	Email             string
	DerivationIndex   *int
}

type BTCSellFundingInput struct {
	OrderID         string
	UserID          string
	WalletAddressID string
	BTCAddress      string
	BTCNetwork      string
	ExpectedSats    int64
	QuoteID         string
}

type BTCSellFundingMatch struct {
	OrderID       string
	Status        string
	ExpectedSats  int64
	ReceivedSats  int64
	Confirmations int
	DepositKey    string
	Ready         bool
	Reason        string
}

type BTCWalletAddressLookup struct {
	ID      string
	UserID  string
	Address string
	Network string
}

type BuyOrder struct {
	ID                string          `json:"id"`
	AccessToken       string          `json:"accessToken,omitempty"`
	Status            string          `json:"status"`
	AmountBRL         float64         `json:"amount_brl"`
	AmountFiat        float64         `json:"amount_fiat"`
	FiatCurrency      string          `json:"fiat_currency"`
	PaymentMethod     string          `json:"payment_method"`
	ProviderPaymentID *string         `json:"provider_payment_id,omitempty"`
	FeeBRL            float64         `json:"fee_brl"`
	PayoutBRL         float64         `json:"payout_brl"`
	CryptoAmount      float64         `json:"crypto_amount"`
	Asset             string          `json:"asset"`
	Network           string          `json:"network"`
	DestAddress       string          `json:"dest_address"`
	RateLocked        float64         `json:"rate_locked"`
	RateLockExpiresAt time.Time       `json:"rate_lock_expires_at"`
	PixPayload        json.RawMessage `json:"pix_payload,omitempty"`
	TxHashOut         *string         `json:"tx_hash_out,omitempty"`
	Error             *string         `json:"error,omitempty"`
	RequestID         *string         `json:"request_id,omitempty"`
	PaidAt            *time.Time      `json:"paid_at,omitempty"`
	SettledAt         *time.Time      `json:"settled_at,omitempty"`
	DeliveredAt       *time.Time      `json:"delivered_at,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
}

type BuyOrderInput struct {
	ID                string
	AccessToken       string
	Status            string
	AmountBRL         float64
	AmountFiat        float64
	FiatCurrency      string
	PaymentMethod     string
	ProviderPaymentID string
	RequestID         string
	FeeBRL            float64
	PayoutBRL         float64
	CryptoAmount      float64
	Asset             string
	Network           string
	DestAddress       string
	RateLocked        float64
	RateLockExpiresAt time.Time
	PixPayload        any
	CustomerEmail     string
}

type PixStats struct {
	Count int
	Total float64
}

type DeveloperEvent struct {
	ID        string          `json:"id"`
	Source    string          `json:"source"`
	OrderID   string          `json:"orderId"`
	RequestID *string         `json:"requestId,omitempty"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"createdAt"`
}

type LiquidityQuoteRecord struct {
	Provider           string
	ProviderType       string
	ExternalQuoteID    string
	Asset              string
	Network            string
	TokenContract      string
	TokenDecimals      int
	FiatCostBRL        float64
	ProviderFeeBRL     float64
	NetworkFeeBRL      float64
	SpreadBRL          float64
	TotalCostBRL       float64
	CryptoAmount       float64
	DeliverySLASeconds int
	ReliabilityBps     int
	DirectDelivery     bool
	Selected           bool
	ExpiresAt          time.Time
	Payload            any
}

type LiquidityExecutionRecord struct {
	QuoteID         string
	Provider        string
	Status          string
	ExternalOrderID string
	TxHash          string
	Error           string
	Payload         any
}

type AdminTransaction struct {
	Source            string     `json:"source"`
	ID                string     `json:"id"`
	Status            string     `json:"status"`
	Product           string     `json:"product,omitempty"`
	ProductType       string     `json:"productType,omitempty"`
	AmountBRL         float64    `json:"amountBRL"`
	AmountFiat        float64    `json:"amountFiat"`
	FiatCurrency      string     `json:"fiatCurrency"`
	PaymentMethod     string     `json:"paymentMethod"`
	FundingMethod     string     `json:"fundingMethod,omitempty"`
	FeeBRL            float64    `json:"feeBRL"`
	PayoutBRL         float64    `json:"payoutBRL"`
	CryptoAmount      float64    `json:"cryptoAmount"`
	Asset             string     `json:"asset"`
	Address           string     `json:"address"`
	Network           string     `json:"network"`
	RateLocked        float64    `json:"rateLocked"`
	ProviderPaymentID *string    `json:"providerPaymentId,omitempty"`
	TxHash            *string    `json:"txHash,omitempty"`
	DepositTx         *string    `json:"depositTx,omitempty"`
	DepositAmount     *float64   `json:"depositAmount,omitempty"`
	PixCode           string     `json:"pixCode,omitempty"`
	PixExpiresAt      *time.Time `json:"pixExpiresAt,omitempty"`
	PixKey            string     `json:"pixKey,omitempty"`
	PixCpf            string     `json:"pixCpf,omitempty"`
	PixPhone          string     `json:"pixPhone,omitempty"`
	Email             string     `json:"email,omitempty"`
	Error             *string    `json:"error,omitempty"`
	RequestID         *string    `json:"requestId,omitempty"`
	CreatedAt         time.Time  `json:"createdAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
}

type AdminUser struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"createdAt"`
}

type Sweep struct {
	ID         string
	ChildIndex int
	FromAddr   string
	ToAddr     string
	Amount     float64
	Status     string
	OrderID    *string
}

func ConnectPostgres(cfg *config.Config) (*DB, error) {
	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL não configurado")
	}

	db, err := sql.Open("postgres", cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(50)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(30 * time.Minute)

	log.Println("Conectando ao banco de dados PostgreSQL...")
	pingCtx, pingCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer pingCancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, err
	}
	wrapped := &DB{SQL: db}
	codec, err := privacy.New(cfg.LGPDSecret)
	if err == nil {
		wrapped.privacy = codec
	}
	wrapped.cfg = cfg
	schemaCtx, schemaCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer schemaCancel()
	if err := wrapped.InitSchema(schemaCtx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := wrapped.EnsureBootstrapAdmin(schemaCtx); err != nil {
		_ = db.Close()
		return nil, err
	}
	log.Println("Conexão com PostgreSQL estabelecida com sucesso!")
	return wrapped, nil
}

func (db *DB) Close() {
	if db.SQL != nil {
		_ = db.SQL.Close()
	}
}

func (db *DB) Ping(ctx context.Context) error {
	return db.SQL.PingContext(ctx)
}

func (db *DB) InitSchema(ctx context.Context) error {
	_, err := db.SQL.ExecContext(ctx, schemaSQL)
	return err
}

func (db *DB) CreateOrder(ctx context.Context, order OrderInput) (*models.Order, error) {
	if order.ID == "" {
		order.ID = NewID()
	}
	if order.AccessToken == "" {
		order.AccessToken = NewAccessToken()
	}
	if (order.PixCpf != "" || order.PixPhone != "" || order.Email != "") && db.privacy == nil {
		return nil, fmt.Errorf("LGPD_SECRET nao configurado para salvar dados pessoais")
	}
	pixCpfHash := privacy.Hash(order.PixCpf, db.cfg.LGPDSecret)
	pixPhoneHash := privacy.Hash(order.PixPhone, db.cfg.LGPDSecret)

	tx, err := db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("CreateOrder: begin tx: %w", err)
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, `
                INSERT INTO orders (id, access_token, request_id, status, amount_brl, btc_amount, fee_brl, payout_brl, address, asset, network, rate_locked, rate_lock_expires_at, created_at, pix_cpf_hash, pix_phone_hash, derivation_index)
                VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,now(),$14,$15,$16)`,
		order.ID, order.AccessToken, nullableString(order.RequestID), order.Status, order.AmountBRL, order.AmountUSDT, order.FeeBRL, order.PayoutBRL, order.Address,
		order.Asset, order.Network, order.RateLocked, order.RateLockExpiresAt, nullableString(pixCpfHash), nullableString(pixPhoneHash), order.DerivationIndex,
	)
	if err != nil {
		return nil, err
	}
	if order.PixCpf != "" || order.PixPhone != "" || order.Email != "" {
		pixCpfEnc, err := db.privacy.Encrypt(order.PixCpf)
		if err != nil {
			return nil, err
		}
		pixPhoneEnc, err := db.privacy.Encrypt(order.PixPhone)
		if err != nil {
			return nil, err
		}
		emailEnc, err := db.privacy.Encrypt(strings.ToLower(strings.TrimSpace(order.Email)))
		if err != nil {
			return nil, err
		}
		_, err = tx.ExecContext(ctx, `
                        INSERT INTO order_private (order_id, pix_cpf_enc, pix_phone_enc, email_enc)
                        VALUES ($1,$2,$3,$4)
                        ON CONFLICT (order_id) DO UPDATE SET pix_cpf_enc = EXCLUDED.pix_cpf_enc, pix_phone_enc = EXCLUDED.pix_phone_enc, email_enc = COALESCE(EXCLUDED.email_enc, order_private.email_enc)`,
			order.ID, nullableString(pixCpfEnc), nullableString(pixPhoneEnc), nullableString(emailEnc))
		if err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("CreateOrder: commit: %w", err)
	}
	_ = db.AddEvent(ctx, order.ID, "order.created", map[string]any{"requestId": order.RequestID, "amountBRL": order.AmountBRL, "amountUSDT": order.AmountUSDT})
	return db.GetOrder(ctx, order.ID)
}

// ClaimOrderForPayout atomically transitions an order from 'pago' → 'processando_payout'.
// Returns true if this caller successfully claimed the order; false if another goroutine/
// replica already claimed it. This prevents double-payout races.
func (db *DB) ClaimOrderForPayout(ctx context.Context, orderID string) (bool, error) {
	var claimed string
	err := db.SQL.QueryRowContext(ctx, `
                UPDATE orders
                   SET status = 'processando_payout', updated_at = now()
                 WHERE id = $1 AND status = 'pago'
                RETURNING id`, orderID).Scan(&claimed)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (db *DB) EnsureSellPayoutExecution(ctx context.Context, orderID, provider, providerIDEnvio string, amountBRLMinor int64, recipientReference string) (*SellPayoutExecution, error) {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		provider = "efi"
	}
	providerIDEnvio = strings.TrimSpace(providerIDEnvio)
	providerIDKey := "sell-payout-" + providerIDEnvio
	tx, err := db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
                INSERT INTO sell_payout_executions
                  (order_id, provider, provider_idempotency_key, provider_id_envio,
                   amount_brl_minor, recipient_reference, status, submit_outcome)
                VALUES ($1,$2,$3,$4,$5,$6,'pending','not_submitted')
                ON CONFLICT (order_id) DO NOTHING`,
		orderID, provider, providerIDKey, providerIDEnvio, amountBRLMinor, recipientReference); err != nil {
		return nil, err
	}
	exec, err := scanSellPayoutExecution(tx.QueryRowContext(ctx, sellPayoutExecutionSelectSQL()+` WHERE order_id=$1 FOR UPDATE`, orderID))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return exec, nil
}

func (db *DB) GetSellPayoutExecutionByOrder(ctx context.Context, orderID string) (*SellPayoutExecution, error) {
	exec, err := scanSellPayoutExecution(db.SQL.QueryRowContext(ctx, sellPayoutExecutionSelectSQL()+` WHERE order_id=$1`, orderID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return exec, err
}

func (db *DB) ListDueSellPayoutExecutions(ctx context.Context, limit int) ([]SellPayoutExecution, error) {
	if limit <= 0 {
		limit = 50
	}
	tx, err := db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `
                WITH due AS (
                  SELECT id, next_attempt_at AS due_next_attempt_at, created_at AS due_created_at
                    FROM sell_payout_executions
                   WHERE status IN ('pending','submit_started','submitted','provider_pending','provider_unknown')
                     AND next_attempt_at <= now()
                   ORDER BY next_attempt_at ASC, created_at ASC, id ASC
                   LIMIT $1
                   FOR UPDATE SKIP LOCKED
                ),
                leased AS (
                  UPDATE sell_payout_executions e
                     SET next_attempt_at = now() + interval '30 seconds',
                         updated_at = now()
                    FROM due
                   WHERE e.id = due.id
                   RETURNING e.id
                )
                SELECT e.id::text, e.order_id::text, e.provider, e.provider_idempotency_key, e.provider_id_envio,
                       e.amount_brl_minor, e.recipient_reference, e.status, e.attempt_count,
                       e.submit_started_at, e.submit_completed_at, e.submit_outcome,
                       e.first_ambiguous_at, e.last_reconciled_at, e.consecutive_not_found,
                       e.next_attempt_at, COALESCE(e.provider_reference,''), COALESCE(e.provider_e2e_id,''),
                       COALESCE(e.last_error,''), e.created_at, e.updated_at, e.completed_at
                  FROM leased
                  JOIN due ON due.id = leased.id
                  JOIN sell_payout_executions e ON e.id = leased.id
                 ORDER BY due_next_attempt_at ASC, due_created_at ASC, id ASC`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SellPayoutExecution
	for rows.Next() {
		exec, err := scanSellPayoutExecution(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *exec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func (db *DB) MarkSellPayoutSubmitStarted(ctx context.Context, executionID string) error {
	_, err := db.SQL.ExecContext(ctx, `
                UPDATE sell_payout_executions
                   SET status = 'submit_started',
                       attempt_count = attempt_count + 1,
                       submit_started_at = COALESCE(submit_started_at, now()),
                       submit_outcome = 'started',
                       updated_at = now()
                 WHERE id = $1
                   AND status IN ('pending','submit_started')`, executionID)
	return err
}

func (db *DB) MarkSellPayoutSubmitted(ctx context.Context, executionID, providerReference, providerE2EID, providerStatus string) error {
	_, err := db.SQL.ExecContext(ctx, `
                UPDATE sell_payout_executions
                   SET status = 'provider_pending',
                       submit_completed_at = now(),
                       submit_outcome = 'confirmed',
                       provider_reference = COALESCE(NULLIF($2,''), provider_reference),
                       provider_e2e_id = COALESCE(NULLIF($3,''), provider_e2e_id),
                       last_error = NULLIF($4,''),
                       consecutive_not_found = 0,
                       next_attempt_at = now() + interval '30 seconds',
                       updated_at = now()
                 WHERE id = $1
                   AND status NOT IN ('completed','failed','manual_review')`, executionID, providerReference, providerE2EID, providerStatus)
	return err
}

func (db *DB) MarkSellPayoutProviderUnknown(ctx context.Context, executionID, errMsg string) error {
	_, err := db.SQL.ExecContext(ctx, `
                UPDATE sell_payout_executions
                   SET status = 'provider_unknown',
                       submit_outcome = 'ambiguous',
                       first_ambiguous_at = COALESCE(first_ambiguous_at, now()),
                       last_error = NULLIF($2,''),
                       next_attempt_at = now() + interval '30 seconds',
                       updated_at = now()
                 WHERE id = $1
                   AND status NOT IN ('completed','failed','manual_review')`, executionID, errMsg)
	return err
}

func (db *DB) MarkSellPayoutFailed(ctx context.Context, executionID, orderID, errMsg string, manual bool) error {
	status := "failed"
	orderStatus := "erro"
	if manual {
		status = "manual_review"
		orderStatus = string(models.StatusIncidenteValidacao)
	}
	tx, err := db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
                UPDATE sell_payout_executions
                   SET status = $2,
                       submit_outcome = CASE WHEN submit_outcome = 'not_submitted' THEN submit_outcome ELSE 'rejected' END,
                       last_error = NULLIF($3,''),
                       updated_at = now()
                 WHERE id = $1
                   AND status NOT IN ('completed')`, executionID, status, errMsg); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
                UPDATE orders
                   SET status = $2,
                       error = COALESCE(NULLIF($3,''), error),
                       updated_at = now()
                 WHERE id = $1
                   AND status <> 'concluida'`, orderID, orderStatus, errMsg); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return db.AddEvent(ctx, orderID, "order."+orderStatus, map[string]any{"error": errMsg, "payout_execution_id": executionID})
}

func (db *DB) MarkSellPayoutReconcileNotFound(ctx context.Context, executionID, errMsg string, retryAfter time.Duration, minAttempts int, grace time.Duration) error {
	if retryAfter <= 0 {
		retryAfter = 30 * time.Second
	}
	tx, err := db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	exec, err := scanSellPayoutExecution(tx.QueryRowContext(ctx, sellPayoutExecutionSelectSQL()+` WHERE id=$1 FOR UPDATE`, executionID))
	if err != nil {
		return err
	}
	nextStatus := exec.Status
	nextErr := errMsg
	if exec.FirstAmbiguousAt != nil && time.Since(*exec.FirstAmbiguousAt) >= grace && exec.ConsecutiveNotFound+1 >= minAttempts {
		nextStatus = "manual_review"
		nextErr = "sell payout provider lookup inconclusive after ambiguous grace: " + errMsg
	}
	if _, err := tx.ExecContext(ctx, `
                UPDATE sell_payout_executions
                   SET status = $2,
                       last_error = NULLIF($3,''),
                       consecutive_not_found = consecutive_not_found + 1,
                       last_reconciled_at = now(),
                       next_attempt_at = now() + make_interval(secs => $4),
                       updated_at = now()
                 WHERE id = $1
                   AND status NOT IN ('completed','failed')`,
		executionID, nextStatus, nextErr, int(retryAfter.Seconds())); err != nil {
		return err
	}
	if nextStatus == "manual_review" {
		if _, err := tx.ExecContext(ctx, `
                        UPDATE orders
                           SET status = 'incidente_validacao',
                               error = COALESCE(NULLIF($2,''), error),
                               updated_at = now()
                         WHERE id = $1
                           AND status <> 'concluida'`, exec.OrderID, nextErr); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if nextStatus == "manual_review" {
		return db.AddEvent(ctx, exec.OrderID, "order.incidente_validacao", map[string]any{"error": nextErr, "payout_execution_id": executionID})
	}
	return nil
}

func (db *DB) ApplySellPayoutProviderEvent(ctx context.Context, providerIDEnvio, providerReference, providerE2EID, providerStatus string, payload map[string]any) (bool, *SellPayoutExecution, error) {
	tx, err := db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return false, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	exec, err := scanSellPayoutExecution(tx.QueryRowContext(ctx, sellPayoutExecutionSelectSQL()+` WHERE provider_id_envio=$1 FOR UPDATE`, providerIDEnvio))
	if err != nil {
		return false, nil, err
	}
	terminalBefore := exec.Status == "completed" || exec.Status == "failed" || exec.Status == "manual_review"
	nextStatus, terminalOK, terminalFail := sellPayoutStatusFromProvider(providerStatus)
	if terminalBefore && exec.Status == "completed" {
		return true, exec, tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `
                UPDATE sell_payout_executions
                   SET status = $2,
                       provider_reference = COALESCE(NULLIF($3,''), provider_reference),
                       provider_e2e_id = COALESCE(NULLIF($4,''), provider_e2e_id),
                       submit_completed_at = CASE WHEN $5 THEN COALESCE(submit_completed_at, now()) ELSE submit_completed_at END,
                       completed_at = CASE WHEN $5 THEN COALESCE(completed_at, now()) ELSE completed_at END,
                       submit_outcome = CASE WHEN $5 THEN 'confirmed' WHEN $6 THEN 'rejected' ELSE submit_outcome END,
                       last_error = CASE WHEN $6 THEN NULLIF($7,'') ELSE NULL END,
                       consecutive_not_found = 0,
                       last_reconciled_at = now(),
                       next_attempt_at = now() + interval '30 seconds',
                       updated_at = now()
                 WHERE id = $1
                   AND status NOT IN ('completed')`,
		exec.ID, nextStatus, firstNonEmpty(providerReference, providerIDEnvio), providerE2EID, terminalOK, terminalFail, providerStatus); err != nil {
		return false, nil, err
	}
	if terminalOK {
		if _, err := tx.ExecContext(ctx, `
                        UPDATE orders
                           SET status = 'concluida',
                               tx_hash = COALESCE(NULLIF($2,''), tx_hash),
                               updated_at = now()
                         WHERE id = $1
                           AND status IN ('processando_payout','pago')`, exec.OrderID, firstNonEmpty(providerE2EID, providerReference, providerIDEnvio)); err != nil {
			return false, nil, err
		}
	}
	if terminalFail {
		if _, err := tx.ExecContext(ctx, `
                        UPDATE orders
                           SET status = 'incidente_validacao',
                               error = COALESCE(NULLIF($2,''), error),
                               updated_at = now()
                         WHERE id = $1
                           AND status <> 'concluida'`, exec.OrderID, "sell payout rejected by provider: "+providerStatus); err != nil {
			return false, nil, err
		}
	}
	updated, err := scanSellPayoutExecution(tx.QueryRowContext(ctx, sellPayoutExecutionSelectSQL()+` WHERE id=$1`, exec.ID))
	if err != nil {
		return false, nil, err
	}
	if err := tx.Commit(); err != nil {
		return false, nil, err
	}
	if terminalOK {
		_ = db.AddEvent(ctx, exec.OrderID, "order.concluida", map[string]any{"payout_execution_id": exec.ID, "provider_status": providerStatus, "provider_payload": payload})
	}
	if terminalFail {
		_ = db.AddEvent(ctx, exec.OrderID, "order.incidente_validacao", map[string]any{"payout_execution_id": exec.ID, "provider_status": providerStatus, "provider_payload": payload})
	}
	return terminalBefore, updated, nil
}

// ClaimOrderForManualPayout atomically moves a confirmed sell deposit into the
// admin PIX queue. It prevents duplicated worker events from creating multiple
// manual payout tasks for the same order.
func (db *DB) ClaimOrderForManualPayout(ctx context.Context, orderID string, extra map[string]any) (bool, error) {
	var claimed string
	err := db.SQL.QueryRowContext(ctx, `
                UPDATE orders
                   SET status = 'aguardando_pix_manual', updated_at = now()
                 WHERE id = $1 AND status = 'pago'
                RETURNING id`, orderID).Scan(&claimed)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, db.AddEvent(ctx, orderID, "order.aguardando_pix_manual", extra)
}

func (db *DB) MarkManualPixPaid(ctx context.Context, orderID, providerID, adminEmail, note string) (bool, error) {
	if strings.TrimSpace(providerID) == "" {
		providerID = "pix-manual-" + orderID
	}
	tx, err := db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var currentStatus string
	var depositTx sql.NullString
	var payoutBRL float64
	err = tx.QueryRowContext(ctx, `
                SELECT status, deposit_tx, COALESCE(payout_brl, 0)::float8
                  FROM orders
                 WHERE id = $1
                 FOR UPDATE`, orderID).Scan(&currentStatus, &depositTx, &payoutBRL)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if currentStatus == string(models.StatusConcluida) {
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return true, nil
	}
	if currentStatus != string(models.StatusAguardandoPixManual) || !depositTx.Valid || strings.TrimSpace(depositTx.String) == "" {
		return false, tx.Commit()
	}
	confirmedAt := time.Now().UTC()
	var claimed string
	err = tx.QueryRowContext(ctx, `
                UPDATE orders
                   SET status = 'concluida',
                       tx_hash = COALESCE(NULLIF($2,''), tx_hash),
                       updated_at = now()
                 WHERE id = $1
                   AND status = 'aguardando_pix_manual'
                RETURNING id`, orderID, providerID).Scan(&claimed)
	if err == sql.ErrNoRows {
		return false, tx.Commit()
	}
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, db.AddEvent(ctx, orderID, "order.pix_manual_paid", map[string]any{
		"providerId":      providerID,
		"adminEmail":      adminEmail,
		"note":            note,
		"payoutMethod":    "manual_pix",
		"payoutAmountBRL": payoutBRL,
		"confirmedBy":     adminEmail,
		"confirmedAt":     confirmedAt.Format(time.RFC3339Nano),
	})
}

// ClaimBuyOrderForSend atomically transitions a buy order from 'pago_fiat'/'pago_pix' → 'enviando'.
// Returns true if this caller successfully claimed the order; false if another goroutine/
// replica already claimed it. This prevents double-send races across replicas.
func (db *DB) ClaimBuyOrderForSend(ctx context.Context, orderID string) (bool, error) {
	var claimed string
	err := db.SQL.QueryRowContext(ctx, `
                UPDATE buy_orders
                   SET status = 'enviando', updated_at = now()
                 WHERE id = $1
                   AND (
                        status IN ('pago_fiat', 'pago_pix')
                        OR (status = 'enviando' AND updated_at < now() - interval '60 seconds')
                   )
                RETURNING id`, orderID).Scan(&claimed)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (db *DB) GetOrder(ctx context.Context, id string) (*models.Order, error) {
	row := db.SQL.QueryRowContext(ctx, `
                SELECT o.id, COALESCE(o.access_token, ''), o.status, o.amount_brl, o.btc_amount, COALESCE(o.fee_brl,0), COALESCE(o.payout_brl,0), o.address, o.asset, o.network,
                       o.rate_locked, o.rate_lock_expires_at, o.created_at, COALESCE(o.updated_at, o.created_at), o.tx_hash, o.error,
                       o.deposit_tx, o.deposit_amount, op.pix_cpf_enc, op.pix_phone_enc, o.derivation_index
                FROM orders o
                LEFT JOIN order_private op ON op.order_id = o.id
                WHERE o.id = $1`, id)
	return db.scanOrder(row)
}

func (db *DB) GetPendingOrders(ctx context.Context) ([]models.Order, error) {
	rows, err := db.SQL.QueryContext(ctx, `
                SELECT o.id, COALESCE(o.access_token, ''), o.status, o.amount_brl, o.btc_amount, COALESCE(o.fee_brl,0), COALESCE(o.payout_brl,0), o.address, o.asset, o.network,
                       o.rate_locked, o.rate_lock_expires_at, o.created_at, COALESCE(o.updated_at, o.created_at), o.tx_hash, o.error,
                       o.deposit_tx, o.deposit_amount, op.pix_cpf_enc, op.pix_phone_enc, o.derivation_index
                FROM orders o
                LEFT JOIN order_private op ON op.order_id = o.id
                WHERE o.status IN ('aguardando_deposito','aguardando_validacao')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Order
	for rows.Next() {
		o, err := db.scanOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *o)
	}
	return out, rows.Err()
}

func (db *DB) GetPendingOrdersByNetwork(ctx context.Context, network string) ([]models.Order, error) {
	rows, err := db.SQL.QueryContext(ctx, `
                SELECT o.id, COALESCE(o.access_token, ''), o.status, o.amount_brl, o.btc_amount, COALESCE(o.fee_brl,0), COALESCE(o.payout_brl,0), o.address, o.asset, o.network,
                       o.rate_locked, o.rate_lock_expires_at, o.created_at, COALESCE(o.updated_at, o.created_at), o.tx_hash, o.error,
                       o.deposit_tx, o.deposit_amount, op.pix_cpf_enc, op.pix_phone_enc, o.derivation_index
                FROM orders o
                LEFT JOIN order_private op ON op.order_id = o.id
                WHERE o.status IN ('aguardando_deposito','aguardando_validacao')
                  AND upper(o.network) = upper($1)`, strings.TrimSpace(network))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Order
	for rows.Next() {
		o, err := db.scanOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *o)
	}
	return out, rows.Err()
}

func (db *DB) UpdateOrderStatus(ctx context.Context, id, status string, extra map[string]any) error {
	txHash, _ := extra["txHash"].(string)
	errMsg, _ := extra["error"].(string)
	depositTx, _ := extra["depositTx"].(string)
	var depositAmount any
	if v, ok := extra["depositAmount"]; ok {
		depositAmount = v
	}
	_, err := db.SQL.ExecContext(ctx, `
                UPDATE orders SET status = $2,
                        tx_hash = COALESCE(NULLIF($3,''), tx_hash),
                        error = COALESCE(NULLIF($4,''), error),
                        deposit_tx = COALESCE(NULLIF($5,''), deposit_tx),
                        deposit_amount = COALESCE($6, deposit_amount),
                        updated_at = now()
                WHERE id = $1`, id, status, txHash, errMsg, depositTx, depositAmount)
	if err != nil {
		return err
	}
	return db.AddEvent(ctx, id, "order."+status, extra)
}

// ConfirmSellDepositForPayout atomically accepts a sell funding event only while
// the economic rate lock is still valid. The boundary is strict: expires_at <=
// NOW() is expired and must not move to payout.
func (db *DB) ConfirmSellDepositForPayout(ctx context.Context, id, txHash string, depositAmount float64, extra map[string]any) (bool, error) {
	res, err := db.SQL.ExecContext(ctx, `
                UPDATE orders
                   SET status = 'pago',
                       deposit_tx = COALESCE(NULLIF($2,''), deposit_tx),
                       deposit_amount = COALESCE($3, deposit_amount),
                       updated_at = now()
                 WHERE id = $1
                   AND status IN ('aguardando_deposito','aguardando_validacao')
                   AND rate_lock_expires_at > now()`,
		id, txHash, depositAmount)
	if err != nil {
		return false, err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if rows == 0 {
		return false, nil
	}
	return true, db.AddEvent(ctx, id, "order.pago", extra)
}

func (db *DB) CreateBTCSellFunding(ctx context.Context, in BTCSellFundingInput) error {
	if strings.TrimSpace(in.OrderID) == "" || strings.TrimSpace(in.UserID) == "" || strings.TrimSpace(in.BTCAddress) == "" || in.ExpectedSats <= 0 {
		return fmt.Errorf("CreateBTCSellFunding: dados obrigatorios ausentes")
	}
	network := strings.ToLower(strings.TrimSpace(in.BTCNetwork))
	if network == "" {
		network = "testnet"
	}
	_, err := db.SQL.ExecContext(ctx, `
INSERT INTO btc_sell_fundings
  (order_id, user_id, wallet_address_id, btc_address, btc_network, expected_sats, quote_id, status)
VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, NULLIF($7,''), 'awaiting_deposit')
ON CONFLICT (order_id) DO NOTHING`,
		in.OrderID, in.UserID, in.WalletAddressID, in.BTCAddress, network, in.ExpectedSats, in.QuoteID)
	if err != nil {
		return fmt.Errorf("CreateBTCSellFunding: %w", err)
	}
	return nil
}

func (db *DB) FindBTCWalletAddressByAddress(ctx context.Context, network, address string) (*BTCWalletAddressLookup, error) {
	network = strings.ToLower(strings.TrimSpace(network))
	address = strings.TrimSpace(address)
	if network == "" || address == "" {
		return nil, nil
	}
	var out BTCWalletAddressLookup
	err := db.SQL.QueryRowContext(ctx, `
SELECT id, user_id::text, address, network
  FROM btc_wallet_addresses
 WHERE network=$1
   AND address=$2
   AND status='active'
 LIMIT 1`, network, address).Scan(&out.ID, &out.UserID, &out.Address, &out.Network)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (db *DB) ActiveBTCSellFundingOrderForQuote(ctx context.Context, quoteID, userID string) (string, error) {
	quoteID = strings.TrimSpace(quoteID)
	userID = strings.TrimSpace(userID)
	if quoteID == "" || userID == "" {
		return "", nil
	}
	var orderID string
	err := db.SQL.QueryRowContext(ctx, `
SELECT order_id::text
  FROM btc_sell_fundings
 WHERE quote_id=$1 AND user_id=$2::uuid
 ORDER BY created_at DESC
 LIMIT 1`, quoteID, userID).Scan(&orderID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return orderID, err
}

func (db *DB) ApplyBTCSellFundingEvent(ctx context.Context, btcNetwork, userID, address, txid string, vout uint32, amountSats int64, confirmations, minConfirmations int) (*BTCSellFundingMatch, error) {
	btcNetwork = strings.ToLower(strings.TrimSpace(btcNetwork))
	userID = strings.TrimSpace(userID)
	address = strings.TrimSpace(address)
	txid = strings.TrimSpace(txid)
	if btcNetwork == "" || userID == "" || address == "" || txid == "" || amountSats <= 0 {
		return nil, fmt.Errorf("ApplyBTCSellFundingEvent: evento BTC incompleto")
	}
	depositKey := fmt.Sprintf("%s:%d", txid, vout)
	tx, err := db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var exactMatches int
	if err := tx.QueryRowContext(ctx, `
SELECT count(*)
  FROM btc_sell_fundings
 WHERE btc_network=$1
   AND user_id=$2::uuid
   AND btc_address=$3
   AND expected_sats=$4
   AND status IN ('awaiting_deposit','detected','pending_confirmations')`,
		btcNetwork, userID, address, amountSats).Scan(&exactMatches); err != nil {
		return nil, err
	}
	if exactMatches > 1 {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}

	var match BTCSellFundingMatch
	var orderStatus string
	if exactMatches == 1 {
		err = tx.QueryRowContext(ctx, `
SELECT f.order_id::text, f.status, f.expected_sats, o.status
  FROM btc_sell_fundings f
  JOIN orders o ON o.id = f.order_id
 WHERE f.btc_network=$1
   AND f.user_id=$2::uuid
   AND f.btc_address=$3
   AND f.expected_sats=$4
   AND f.status IN ('awaiting_deposit','detected','pending_confirmations')
 ORDER BY f.created_at ASC
 LIMIT 1
 FOR UPDATE OF f, o`,
			btcNetwork, userID, address, amountSats).Scan(&match.OrderID, &match.Status, &match.ExpectedSats, &orderStatus)
	} else {
		var activeMatches int
		if err := tx.QueryRowContext(ctx, `
SELECT count(*)
  FROM btc_sell_fundings
 WHERE btc_network=$1
   AND user_id=$2::uuid
   AND btc_address=$3
   AND status IN ('awaiting_deposit','detected','pending_confirmations')`,
			btcNetwork, userID, address).Scan(&activeMatches); err != nil {
			return nil, err
		}
		if activeMatches != 1 {
			if err := tx.Commit(); err != nil {
				return nil, err
			}
			return nil, nil
		}
		err = tx.QueryRowContext(ctx, `
SELECT f.order_id::text, f.status, f.expected_sats, o.status
  FROM btc_sell_fundings f
  JOIN orders o ON o.id = f.order_id
 WHERE f.btc_network=$1
   AND f.user_id=$2::uuid
   AND f.btc_address=$3
   AND f.status IN ('awaiting_deposit','detected','pending_confirmations')
 ORDER BY f.created_at ASC
 LIMIT 1
 FOR UPDATE OF f, o`,
			btcNetwork, userID, address).Scan(&match.OrderID, &match.Status, &match.ExpectedSats, &orderStatus)
	}
	if err == sql.ErrNoRows {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	match.ReceivedSats = amountSats
	match.Confirmations = confirmations
	match.DepositKey = depositKey

	if amountSats != match.ExpectedSats {
		reason := "btc funding amount diverge do snapshot"
		nextStatus := "manual_review"
		if amountSats < match.ExpectedSats {
			reason = "btc underpayment"
		} else if amountSats > match.ExpectedSats {
			reason = "btc overpayment"
		}
		_, err = tx.ExecContext(ctx, `
UPDATE btc_sell_fundings
   SET txid=$2, vout=$3, received_sats=$4, confirmations=$5, status=$6, error=$7,
       detected_at=COALESCE(detected_at, now()), updated_at=now()
 WHERE order_id=$1::uuid`, match.OrderID, txid, int(vout), amountSats, confirmations, nextStatus, reason)
		if err != nil {
			return nil, err
		}
		_, err = tx.ExecContext(ctx, `
UPDATE orders
   SET status='aguardando_validacao',
       deposit_tx=COALESCE(NULLIF($2,''), deposit_tx),
       deposit_amount=COALESCE($3, deposit_amount),
       error=COALESCE(NULLIF($4,''), error),
       updated_at=now()
 WHERE id=$1::uuid
   AND status IN ('aguardando_deposito','aguardando_validacao')`,
			match.OrderID, depositKey, float64(amountSats)/100000000, reason)
		if err != nil {
			return nil, err
		}
		match.Status = nextStatus
		match.Reason = reason
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		_ = db.AddEvent(ctx, match.OrderID, "order.btc_funding_manual_review", map[string]any{
			"txid": txid, "vout": vout, "amountSats": amountSats, "expectedSats": match.ExpectedSats, "reason": reason,
		})
		return &match, nil
	}

	if confirmations < minConfirmations {
		_, err = tx.ExecContext(ctx, `
UPDATE btc_sell_fundings
   SET txid=$2, vout=$3, received_sats=$4, confirmations=$5, status='pending_confirmations',
       detected_at=COALESCE(detected_at, now()), updated_at=now()
 WHERE order_id=$1::uuid`, match.OrderID, txid, int(vout), amountSats, confirmations)
		if err != nil {
			return nil, err
		}
		match.Status = "pending_confirmations"
		match.Reason = "below_min_confirmations"
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		_ = db.AddEvent(ctx, match.OrderID, "order.btc_funding_pending_confirmations", map[string]any{
			"txid": txid, "vout": vout, "amountSats": amountSats, "confirmations": confirmations, "minConfirmations": minConfirmations,
		})
		return &match, nil
	}

	var rateExpired bool
	if err := tx.QueryRowContext(ctx, `
SELECT rate_lock_expires_at <= now()
  FROM orders
 WHERE id=$1::uuid
 FOR UPDATE`, match.OrderID).Scan(&rateExpired); err != nil {
		return nil, err
	}
	if rateExpired {
		reason := "funding recebido apos expiracao do rate lock; payout manual-ready bloqueado"
		if _, err := tx.ExecContext(ctx, `
UPDATE btc_sell_fundings
   SET txid=$2, vout=$3, received_sats=$4, confirmations=$5, status='manual_review',
       error=$6, detected_at=COALESCE(detected_at, now()), updated_at=now()
 WHERE order_id=$1::uuid`,
			match.OrderID, txid, int(vout), amountSats, confirmations, reason); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE orders
   SET status='incidente_validacao',
       deposit_tx=COALESCE(NULLIF($2,''), deposit_tx),
       deposit_amount=COALESCE($3, deposit_amount),
       error=COALESCE(NULLIF($4,''), error),
       updated_at=now()
 WHERE id=$1::uuid
   AND status IN ('aguardando_deposito','aguardando_validacao','expirada','incidente_validacao')`,
			match.OrderID, depositKey, float64(amountSats)/100000000, reason); err != nil {
			return nil, err
		}
		match.Status = "manual_review"
		match.Reason = "funding_expired_quote"
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		_ = db.AddEvent(ctx, match.OrderID, "order.btc_funding_expired_quote", map[string]any{
			"txid": txid, "vout": vout, "amountSats": amountSats, "confirmations": confirmations, "reason": reason,
		})
		return &match, nil
	}

	if _, err := tx.ExecContext(ctx, `
UPDATE btc_sell_fundings
   SET txid=$2, vout=$3, received_sats=$4, confirmations=$5, status='confirmed',
       detected_at=COALESCE(detected_at, now()), confirmed_at=COALESCE(confirmed_at, now()), updated_at=now()
 WHERE order_id=$1::uuid`, match.OrderID, txid, int(vout), amountSats, confirmations); err != nil {
		return nil, err
	}
	match.Status = "confirmed"
	match.Ready = true
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &match, nil
}

func (db *DB) RecordExpiredSellFunding(ctx context.Context, id, txHash string, depositAmount float64, extra map[string]any) error {
	errMsg, _ := extra["error"].(string)
	if strings.TrimSpace(errMsg) == "" {
		errMsg = "funding recebido apos expiracao do rate lock; payout automatico bloqueado"
	}
	_, err := db.SQL.ExecContext(ctx, `
                UPDATE orders
                   SET status = 'incidente_validacao',
                       deposit_tx = COALESCE(NULLIF($2,''), deposit_tx),
                       deposit_amount = COALESCE($3, deposit_amount),
                       error = COALESCE(NULLIF($4,''), error),
                       updated_at = now()
                 WHERE id = $1
                   AND status IN ('aguardando_deposito','aguardando_validacao','expirada','incidente_validacao')`,
		id, txHash, depositAmount, errMsg)
	if err != nil {
		return err
	}
	return db.AddEvent(ctx, id, "order.funding_expired_quote", extra)
}

func (db *DB) HasPendingOrderForAddress(ctx context.Context, address string) (bool, error) {
	var exists bool
	err := db.SQL.QueryRowContext(ctx, `
                SELECT EXISTS(
                        SELECT 1 FROM orders
                        WHERE lower(address) = lower($1)
                          AND status IN ('aguardando_deposito','aguardando_validacao','pago','processando_payout')
                )`, address).Scan(&exists)
	return exists, err
}

func (db *DB) HasPendingOrderForAddressNetwork(ctx context.Context, address, network string) (bool, error) {
	var exists bool
	err := db.SQL.QueryRowContext(ctx, `
                SELECT EXISTS(
                        SELECT 1 FROM orders
                        WHERE lower(address) = lower($1)
                          AND upper(network) = upper($2)
                          AND status IN ('aguardando_deposito','aguardando_validacao','pago','processando_payout')
                )`, address, strings.TrimSpace(network)).Scan(&exists)
	return exists, err
}

func (db *DB) HasDepositTxForOtherOrder(ctx context.Context, orderID, txHash string) (bool, error) {
	if txHash == "" {
		return false, nil
	}
	var exists bool
	err := db.SQL.QueryRowContext(ctx, `
                SELECT EXISTS(
                        SELECT 1 FROM orders
                        WHERE deposit_tx = $1
                          AND id::text <> $2
                )`, txHash, orderID).Scan(&exists)
	return exists, err
}

func (db *DB) ExpireStaleSellOrders(ctx context.Context) (int, error) {
	rows, err := db.SQL.QueryContext(ctx, `
		UPDATE orders
		   SET status = 'expirada',
		       error = 'deposito nao identificado dentro da janela de pagamento; ordem expirada sem estorno automatico',
		       updated_at = now()
		 WHERE status = 'aguardando_deposito'
		   AND deposit_tx IS NULL
		   AND rate_lock_expires_at <= now()
		RETURNING id`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, id := range ids {
		_ = db.AddEvent(ctx, id, "order.expirada", map[string]any{
			"reason": "deposito nao identificado dentro da janela de pagamento",
			"policy": "no_automatic_refund",
		})
	}
	return len(ids), nil
}

func (db *DB) ExpireStaleBuyOrders(ctx context.Context) (int, error) {
	rows, err := db.SQL.QueryContext(ctx, `
		UPDATE buy_orders
		   SET status = 'expirado',
		       error = 'pagamento Pix nao identificado dentro da janela de pagamento',
		       updated_at = now()
		 WHERE status IN ('aguardando_pix','payment_provider_pending')
		   AND paid_at IS NULL
		   AND rate_lock_expires_at <= now()
		RETURNING id`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, id := range ids {
		_ = db.AddBuyEvent(ctx, id, "buy.expirado", map[string]any{
			"reason": "pagamento Pix nao identificado dentro da janela de pagamento",
		})
	}
	return len(ids), nil
}

func (db *DB) AddEvent(ctx context.Context, orderID, eventType string, payload any) error {
	raw, _ := json.Marshal(payload)
	requestID := requestIDFromPayload(payload)
	_, err := db.SQL.ExecContext(ctx,
		`INSERT INTO order_events (id, order_id, request_id, type, payload) VALUES ($1,$2,$3,$4,$5)`,
		NewID(), orderID, nullableString(requestID), eventType, raw)
	return err
}

func (db *DB) HasEvent(ctx context.Context, orderID, eventType, field, value string) (bool, error) {
	var exists bool
	err := db.SQL.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM order_events WHERE order_id = $1 AND type = $2 AND payload ->> $3 = $4)`,
		orderID, eventType, field, value).Scan(&exists)
	return exists, err
}

func (db *DB) StatsPixLast24h(ctx context.Context, pixCpf, pixPhone string) (PixStats, error) {
	if pixCpf == "" && pixPhone == "" {
		return PixStats{}, nil
	}
	var count int
	var total float64
	pixCpfHash := privacy.Hash(pixCpf, db.cfg.LGPDSecret)
	pixPhoneHash := privacy.Hash(pixPhone, db.cfg.LGPDSecret)
	err := db.SQL.QueryRowContext(ctx, `
                SELECT COUNT(*)::int, COALESCE(SUM(amount_brl),0)::float8
                FROM orders
                WHERE created_at >= now() - interval '24 hours'
                  AND status NOT IN ('expirada','erro','incidente_validacao')
                  AND (($1 <> '' AND pix_cpf_hash = $1) OR ($2 <> '' AND pix_phone_hash = $2))`,
		pixCpfHash, pixPhoneHash).Scan(&count, &total)
	return PixStats{Count: count, Total: total}, err
}

func (db *DB) CountCompletedOrdersForPix(ctx context.Context, pixCpf, pixPhone string) (int, error) {
	var count int
	pixCpfHash := privacy.Hash(pixCpf, db.cfg.LGPDSecret)
	pixPhoneHash := privacy.Hash(pixPhone, db.cfg.LGPDSecret)
	err := db.SQL.QueryRowContext(ctx, `
                SELECT COUNT(*)::int FROM orders
                WHERE status IN ('concluida','concluída')
                  AND (($1 <> '' AND pix_cpf_hash = $1) OR ($2 <> '' AND pix_phone_hash = $2))`,
		pixCpfHash, pixPhoneHash).Scan(&count)
	return count, err
}
func (db *DB) NextDerivationIndex(ctx context.Context) (int, error) {
	var idx int
	err := db.SQL.QueryRowContext(ctx, `SELECT COALESCE(MAX(derivation_index), -1) + 1 FROM orders`).Scan(&idx)
	return idx, err
}

func (db *DB) GetCursor(ctx context.Context, network string) (int64, bool, error) {
	var block int64
	err := db.SQL.QueryRowContext(ctx, `SELECT last_block FROM onchain_cursor WHERE network = $1`, network).Scan(&block)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	return block, err == nil, err
}

func (db *DB) SaveCursor(ctx context.Context, network string, lastBlock int64) error {
	_, err := db.SQL.ExecContext(ctx, `
                INSERT INTO onchain_cursor (network, last_block) VALUES ($1,$2)
                ON CONFLICT (network) DO UPDATE SET last_block = EXCLUDED.last_block, updated_at = now()`,
		network, lastBlock)
	return err
}

func (db *DB) CreateSweep(ctx context.Context, childIndex int, fromAddr, toAddr string, amount float64, orderID *string) (*Sweep, error) {
	id := NewID()
	_, err := db.SQL.ExecContext(ctx, `
                INSERT INTO sweeps (id, child_index, from_addr, to_addr, amount, status, order_id)
                VALUES ($1,$2,$3,$4,$5,'pending',$6)`,
		id, childIndex, fromAddr, toAddr, amount, orderID)
	if err != nil {
		return nil, err
	}
	return &Sweep{ID: id, ChildIndex: childIndex, FromAddr: fromAddr, ToAddr: toAddr, Amount: amount, Status: "pending", OrderID: orderID}, nil
}

func (db *DB) ListPendingSweeps(ctx context.Context) ([]Sweep, error) {
	rows, err := db.SQL.QueryContext(ctx, `SELECT id, child_index, from_addr, to_addr, amount::float8, status, order_id FROM sweeps WHERE status = 'pending'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Sweep
	for rows.Next() {
		var s Sweep
		var orderID sql.NullString
		if err := rows.Scan(&s.ID, &s.ChildIndex, &s.FromAddr, &s.ToAddr, &s.Amount, &s.Status, &orderID); err != nil {
			return nil, err
		}
		if orderID.Valid {
			s.OrderID = &orderID.String
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (db *DB) MarkSweep(ctx context.Context, id, status, txHash string) error {
	_, err := db.SQL.ExecContext(ctx, `UPDATE sweeps SET status = $2, tx_hash = COALESCE(NULLIF($3,''), tx_hash), updated_at = now() WHERE id = $1`, id, status, txHash)
	return err
}

func (db *DB) OrdersToSweep(ctx context.Context) ([]models.Order, error) {
	rows, err := db.SQL.QueryContext(ctx, `
                SELECT o.id, COALESCE(o.access_token, ''), o.status, o.amount_brl, o.btc_amount, COALESCE(o.fee_brl,0), COALESCE(o.payout_brl,0), o.address, o.asset, o.network,
                       o.rate_locked, o.rate_lock_expires_at, o.created_at, COALESCE(o.updated_at, o.created_at), o.tx_hash, o.error,
                       o.deposit_tx, o.deposit_amount, op.pix_cpf_enc, op.pix_phone_enc, o.derivation_index
                FROM orders o
                LEFT JOIN order_private op ON op.order_id = o.id
                WHERE o.status = 'pago'
                  AND o.derivation_index IS NOT NULL
                  AND NOT EXISTS (SELECT 1 FROM sweeps s WHERE s.order_id = o.id AND s.status IN ('pending','sent','confirmed'))`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Order
	for rows.Next() {
		o, err := db.scanOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *o)
	}
	return out, rows.Err()
}

func (db *DB) CreateBuyOrder(ctx context.Context, buy BuyOrderInput) (*BuyOrder, error) {
	id := buy.ID
	if id == "" {
		id = NewID()
	}
	rawPayload, err := json.Marshal(buy.PixPayload)
	if err != nil {
		return nil, err
	}
	if buy.AccessToken == "" {
		buy.AccessToken = NewAccessToken()
	}
	network := strings.ToUpper(strings.TrimSpace(buy.Network))
	if network == "" {
		network = "BSC"
	}
	_, err = db.SQL.ExecContext(ctx, `
                INSERT INTO buy_orders (id, access_token, request_id, status, amount_brl, amount_fiat, fiat_currency, payment_method, provider_payment_id, fee_brl, payout_brl, crypto_amount, asset, network, dest_address, rate_locked, rate_lock_expires_at, pix_payload)
                VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`,
		id, buy.AccessToken, nullableString(buy.RequestID), buy.Status, buy.AmountBRL, buy.AmountFiat, buy.FiatCurrency, buy.PaymentMethod, nullableString(buy.ProviderPaymentID),
		buy.FeeBRL, buy.PayoutBRL, buy.CryptoAmount, buy.Asset, network, buy.DestAddress, buy.RateLocked, buy.RateLockExpiresAt, rawPayload)
	if err != nil {
		return nil, err
	}
	_ = db.AddBuyEvent(ctx, id, "buy.created", map[string]any{"amountFiat": buy.AmountFiat, "fiatCurrency": buy.FiatCurrency, "paymentMethod": buy.PaymentMethod, "cryptoAmount": buy.CryptoAmount})
	if strings.TrimSpace(buy.CustomerEmail) != "" {
		_ = db.SaveBuyOrderEmail(ctx, id, buy.CustomerEmail)
	}
	return db.GetBuyOrder(ctx, id)
}

func (db *DB) SaveBuyOrderEmail(ctx context.Context, id, email string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return nil
	}
	if db.privacy == nil {
		return fmt.Errorf("LGPD_SECRET nao configurado para salvar email")
	}
	emailEnc, err := db.privacy.Encrypt(email)
	if err != nil {
		return err
	}
	_, err = db.SQL.ExecContext(ctx, `
                INSERT INTO buy_order_private (buy_order_id, email_enc)
                VALUES ($1,$2)
                ON CONFLICT (buy_order_id) DO UPDATE SET email_enc = EXCLUDED.email_enc`,
		id, nullableString(emailEnc))
	return err
}

func (db *DB) GetBuyOrderEmail(ctx context.Context, id string) (string, error) {
	var emailEnc sql.NullString
	err := db.SQL.QueryRowContext(ctx, `SELECT email_enc FROM buy_order_private WHERE buy_order_id = $1`, id).Scan(&emailEnc)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil || !emailEnc.Valid || emailEnc.String == "" {
		return "", err
	}
	if db.privacy == nil {
		return "", fmt.Errorf("LGPD_SECRET nao configurado para ler email")
	}
	return db.privacy.Decrypt(emailEnc.String)
}

func (db *DB) GetOrderEmail(ctx context.Context, id string) (string, error) {
	var emailEnc sql.NullString
	err := db.SQL.QueryRowContext(ctx, `SELECT email_enc FROM order_private WHERE order_id = $1`, id).Scan(&emailEnc)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil || !emailEnc.Valid || emailEnc.String == "" {
		return "", err
	}
	if db.privacy == nil {
		return "", fmt.Errorf("LGPD_SECRET nao configurado para ler email")
	}
	return db.privacy.Decrypt(emailEnc.String)
}

func (db *DB) UpsertMarketingContact(ctx context.Context, email, source string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return nil
	}
	_, err := db.SQL.ExecContext(ctx, `
                INSERT INTO marketing_contacts (email, source, subscribed_at, unsubscribed_at, updated_at)
                VALUES ($1,$2,now(),NULL,now())
                ON CONFLICT (email) DO UPDATE SET source = COALESCE(NULLIF(EXCLUDED.source,''), marketing_contacts.source), unsubscribed_at = NULL, updated_at = now()`,
		email, strings.TrimSpace(source))
	return err
}

func (db *DB) GetBuyOrder(ctx context.Context, id string) (*BuyOrder, error) {
	row := db.SQL.QueryRowContext(ctx, `
                SELECT id, COALESCE(access_token, ''), request_id, status, amount_brl::float8, COALESCE(amount_fiat, amount_brl)::float8,
                       COALESCE(fiat_currency, 'BRL'), COALESCE(payment_method, 'pix'), provider_payment_id,
                       COALESCE(fee_brl,0)::float8, COALESCE(payout_brl,0)::float8,
                       crypto_amount::float8, asset, COALESCE(network, 'BSC'), dest_address, rate_locked::float8, rate_lock_expires_at,
                       COALESCE(pix_payload, '{}'::jsonb), tx_hash_out, error, paid_at, settled_at, delivered_at, created_at, updated_at
                FROM buy_orders WHERE id = $1`, id)
	var buy BuyOrder
	var requestID, providerPaymentID, txHashOut, errMsg sql.NullString
	var paidAt, settledAt, deliveredAt sql.NullTime
	if err := row.Scan(&buy.ID, &buy.AccessToken, &requestID, &buy.Status, &buy.AmountBRL, &buy.AmountFiat, &buy.FiatCurrency, &buy.PaymentMethod, &providerPaymentID,
		&buy.FeeBRL, &buy.PayoutBRL, &buy.CryptoAmount, &buy.Asset,
		&buy.Network, &buy.DestAddress, &buy.RateLocked, &buy.RateLockExpiresAt, &buy.PixPayload, &txHashOut, &errMsg, &paidAt, &settledAt, &deliveredAt, &buy.CreatedAt, &buy.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if txHashOut.Valid {
		buy.TxHashOut = &txHashOut.String
	}
	if requestID.Valid {
		buy.RequestID = &requestID.String
	}
	if providerPaymentID.Valid {
		buy.ProviderPaymentID = &providerPaymentID.String
	}
	if errMsg.Valid {
		buy.Error = &errMsg.String
	}
	if paidAt.Valid {
		buy.PaidAt = &paidAt.Time
	}
	if settledAt.Valid {
		buy.SettledAt = &settledAt.Time
	}
	if deliveredAt.Valid {
		buy.DeliveredAt = &deliveredAt.Time
	}
	return &buy, nil
}

func (db *DB) UpdateBuyOrderStatus(ctx context.Context, id, status string, extra map[string]any) error {
	txHashOut, _ := extra["txHashOut"].(string)
	providerPaymentID, _ := extra["providerPaymentId"].(string)
	errMsg, _ := extra["error"].(string)
	_, err := db.SQL.ExecContext(ctx, `
                UPDATE buy_orders SET status = $2,
                        tx_hash_out = COALESCE(NULLIF($3,''), tx_hash_out),
                        provider_payment_id = COALESCE(NULLIF($4,''), provider_payment_id),
                        error = COALESCE(NULLIF($5,''), error),
                        paid_at = CASE WHEN $2 IN ('pago_fiat','pago_pix') AND paid_at IS NULL THEN now() ELSE paid_at END,
                        settled_at = CASE WHEN $2 IN ('pago_fiat','pago_pix') AND settled_at IS NULL THEN now() ELSE settled_at END,
                        delivered_at = CASE WHEN $2 IN ('enviado','delivered','confirmado') AND delivered_at IS NULL THEN now() ELSE delivered_at END,
                        updated_at = now()
                WHERE id = $1
                  AND NOT (status IN ('enviado','delivered','confirmado') AND $2 = 'erro')`, id, status, txHashOut, providerPaymentID, errMsg)
	if err != nil {
		return err
	}
	return db.AddBuyEvent(ctx, id, "buy."+status, extra)
}

func (db *DB) ApplyBuyProviderWebhook(ctx context.Context, buyOrderID, providerID, providerStatus, status string, extra map[string]any) (bool, error) {
	tx, err := db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	if extra == nil {
		extra = map[string]any{}
	}
	requestID := requestIDFromPayload(extra)
	providerPayload := map[string]any{"requestId": requestID, "providerId": providerID, "status": providerStatus}
	rawProvider, _ := json.Marshal(providerPayload)
	if providerID != "" {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO buy_order_events (id, buy_order_id, request_id, type, payload)
                         VALUES ($1,$2,$3,'webhook.provider',$4)
                         ON CONFLICT DO NOTHING`,
			NewID(), buyOrderID, nullableString(requestID), rawProvider)
		if err != nil {
			return false, err
		}
		rows, err := res.RowsAffected()
		if err != nil {
			return false, err
		}
		if rows == 0 {
			return true, nil
		}
	}

	var currentStatus string
	_ = tx.QueryRowContext(ctx, `SELECT status FROM buy_orders WHERE id = $1 FOR UPDATE`, buyOrderID).Scan(&currentStatus)
	txHashOut, _ := extra["txHashOut"].(string)
	providerPaymentID, _ := extra["providerPaymentId"].(string)
	errMsg, _ := extra["error"].(string)
	if providerPaymentID == "" {
		providerPaymentID = providerID
	}
	statusToApply := status
	latePaidWebhook := isBuyPaidStatus(status) && isTerminalUnpaidBuyStatus(currentStatus)
	if latePaidWebhook {
		statusToApply = "incidente_validacao"
		errMsg = "Pix recebido apos expiracao/cancelamento; revisar manualmente antes de entregar cripto ou devolver"
		extra["error"] = errMsg
		extra["latePaidWebhook"] = true
		extra["previousStatus"] = currentStatus
		extra["requestedStatus"] = status
		extra["action"] = "manual_review_required_no_auto_delivery"
	}
	res, err := tx.ExecContext(ctx, `
                UPDATE buy_orders SET status = $2,
                        tx_hash_out = COALESCE(NULLIF($3,''), tx_hash_out),
                        provider_payment_id = COALESCE(NULLIF($4,''), provider_payment_id),
                        error = COALESCE(NULLIF($5,''), error),
                        paid_at = CASE WHEN $2 IN ('pago_fiat','pago_pix','incidente_validacao') AND paid_at IS NULL THEN now() ELSE paid_at END,
                        settled_at = CASE WHEN $2 IN ('pago_fiat','pago_pix') AND settled_at IS NULL THEN now() ELSE settled_at END,
                        delivered_at = CASE WHEN $2 IN ('enviado','delivered','confirmado') AND delivered_at IS NULL THEN now() ELSE delivered_at END,
                        updated_at = now()
                WHERE id = $1
                  AND NOT (status IN ('pago_fiat','pago_pix','enviando','pendente_confirmacao','enviado','delivered','confirmado') AND $2 LIKE 'aguardando_%')
                  AND NOT (status IN ('enviando','pendente_confirmacao','enviado','delivered','confirmado') AND $2 IN ('pago_fiat','pago_pix','erro'))`,
		buyOrderID, statusToApply, txHashOut, providerPaymentID, errMsg)
	if err != nil {
		return false, err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if rows == 0 {
		return true, tx.Commit()
	}
	rawStatus, _ := json.Marshal(extra)
	_, err = tx.ExecContext(ctx,
		`INSERT INTO buy_order_events (id, buy_order_id, request_id, type, payload) VALUES ($1,$2,$3,$4,$5)`,
		NewID(), buyOrderID, nullableString(requestID), "buy."+statusToApply, rawStatus)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return latePaidWebhook, nil
}

func isBuyPaidStatus(status string) bool {
	return status == "pago_fiat" || status == "pago_pix"
}

func isTerminalUnpaidBuyStatus(status string) bool {
	switch status {
	case "expirado", "expirada", "cancelado", "cancelada", "erro":
		return true
	default:
		return false
	}
}

func (db *DB) AddBuyEvent(ctx context.Context, buyOrderID, eventType string, payload any) error {
	raw, _ := json.Marshal(payload)
	requestID := requestIDFromPayload(payload)
	_, err := db.SQL.ExecContext(ctx,
		`INSERT INTO buy_order_events (id, buy_order_id, request_id, type, payload) VALUES ($1,$2,$3,$4,$5)`,
		NewID(), buyOrderID, nullableString(requestID), eventType, raw)
	return err
}

func (db *DB) RecordLiquidityQuotes(ctx context.Context, buyOrderID string, quotes []LiquidityQuoteRecord) (map[string]string, error) {
	ids := make(map[string]string, len(quotes))
	if len(quotes) == 0 {
		return ids, nil
	}
	tx, err := db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for _, quote := range quotes {
		id := NewID()
		raw, _ := json.Marshal(quote.Payload)
		if len(raw) == 0 || string(raw) == "null" {
			raw, _ = json.Marshal(map[string]any{})
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO buy_liquidity_quotes
			  (id, buy_order_id, provider, provider_type, external_quote_id, asset, network,
			   token_contract, token_decimals, fiat_cost_brl, provider_fee_brl, network_fee_brl, spread_brl, total_cost_brl,
			   crypto_amount, delivery_sla_seconds, reliability_bps, direct_delivery, selected, expires_at, payload)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)
			ON CONFLICT (buy_order_id, provider, external_quote_id) DO UPDATE SET
			  selected=EXCLUDED.selected,
			  total_cost_brl=EXCLUDED.total_cost_brl,
			  token_contract=EXCLUDED.token_contract,
			  token_decimals=EXCLUDED.token_decimals,
			  payload=EXCLUDED.payload,
			  created_at=buy_liquidity_quotes.created_at
		`, id, buyOrderID, quote.Provider, quote.ProviderType, nullableString(quote.ExternalQuoteID),
			quote.Asset, quote.Network, nullableString(quote.TokenContract), quote.TokenDecimals, quote.FiatCostBRL, quote.ProviderFeeBRL, quote.NetworkFeeBRL,
			quote.SpreadBRL, quote.TotalCostBRL, quote.CryptoAmount, quote.DeliverySLASeconds,
			quote.ReliabilityBps, quote.DirectDelivery, quote.Selected, quote.ExpiresAt, raw)
		if err != nil {
			return nil, err
		}
		ids[quote.Provider] = id
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return ids, nil
}

func (db *DB) RecordLiquidityExecution(ctx context.Context, buyOrderID string, exec LiquidityExecutionRecord) error {
	raw, _ := json.Marshal(exec.Payload)
	if len(raw) == 0 || string(raw) == "null" {
		raw, _ = json.Marshal(map[string]any{})
	}
	_, err := db.SQL.ExecContext(ctx, `
		INSERT INTO buy_liquidity_executions
		  (id, buy_order_id, quote_id, provider, status, external_order_id, tx_hash, error, payload)
		VALUES ($1,$2,NULLIF($3,'')::uuid,$4,$5,NULLIF($6,''),NULLIF($7,''),NULLIF($8,''),$9)
	`, NewID(), buyOrderID, exec.QuoteID, exec.Provider, exec.Status, exec.ExternalOrderID, exec.TxHash, exec.Error, raw)
	if err != nil {
		return err
	}
	return db.AddBuyEvent(ctx, buyOrderID, "buy.liquidity."+strings.ToLower(strings.TrimSpace(exec.Status)), map[string]any{
		"provider":        exec.Provider,
		"externalOrderId": exec.ExternalOrderID,
		"txHash":          exec.TxHash,
		"error":           exec.Error,
	})
}

func (db *DB) HasBuyEvent(ctx context.Context, buyOrderID, eventType, field, value string) (bool, error) {
	var exists bool
	err := db.SQL.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM buy_order_events WHERE buy_order_id = $1 AND type = $2 AND payload ->> $3 = $4)`,
		buyOrderID, eventType, field, value).Scan(&exists)
	return exists, err
}

func (db *DB) ListDeveloperEvents(ctx context.Context, limit int) ([]DeveloperEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := db.SQL.QueryContext(ctx, `
                SELECT id::text, source, order_id::text, request_id, type, payload, created_at
                FROM (
                        SELECT id, 'sell' AS source, order_id, request_id, type, payload, created_at FROM order_events
                        UNION ALL
                        SELECT id, 'buy' AS source, buy_order_id AS order_id, request_id, type, payload, created_at FROM buy_order_events
                ) events
                ORDER BY created_at DESC
                LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeveloperEvent
	for rows.Next() {
		var event DeveloperEvent
		var requestID sql.NullString
		if err := rows.Scan(&event.ID, &event.Source, &event.OrderID, &requestID, &event.Type, &event.Payload, &event.CreatedAt); err != nil {
			return nil, err
		}
		if requestID.Valid {
			event.RequestID = &requestID.String
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

func (db *DB) ListAdminTransactions(ctx context.Context, limit int) ([]AdminTransaction, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	query := `
                SELECT source, id, status, product, product_type,
                       amount_brl::float8, amount_fiat::float8,
                       fiat_currency, payment_method, funding_method,
                       fee_brl::float8, payout_brl::float8,
                       crypto_amount::float8, asset, address, network, rate_locked::float8,
                       provider_payment_id, tx_hash, deposit_tx, deposit_amount,
                       pix_code, pix_expires_at, pix_cpf_enc, pix_phone_enc, email_enc, error, request_id,
                       created_at, updated_at
                FROM (
                        SELECT 'buy' AS source, bo.id::text AS id, status,
                               'BUY crypto'::text AS product,
                               'buy'::text AS product_type,
                               amount_brl,
                               COALESCE(amount_fiat, amount_brl) AS amount_fiat,
                               COALESCE(fiat_currency, 'BRL') AS fiat_currency,
                               COALESCE(payment_method, 'pix') AS payment_method,
                               COALESCE(payment_method, 'pix') AS funding_method,
                               COALESCE(fee_brl, 0) AS fee_brl,
                               COALESCE(payout_brl, 0) AS payout_brl,
                               crypto_amount,
                               asset,
                               dest_address AS address,
                               COALESCE(network, 'BSC') AS network,
                               rate_locked,
                               provider_payment_id,
                               tx_hash_out AS tx_hash,
                               NULL::text AS deposit_tx,
                               NULL::numeric AS deposit_amount,
                               ''::text AS pix_code,
                               NULL::timestamptz AS pix_expires_at,
                               NULL::text AS pix_cpf_enc,
                               NULL::text AS pix_phone_enc,
                               bop.email_enc,
                               error,
                               request_id,
                               bo.created_at,
                               COALESCE(bo.updated_at, bo.created_at) AS updated_at
                        FROM buy_orders bo
                        LEFT JOIN buy_order_private bop ON bop.buy_order_id = bo.id
                        UNION ALL
                        SELECT 'sell' AS source, o.id::text AS id, status,
                               'SELL Pix payout'::text AS product,
                               'sell'::text AS product_type,
                               amount_brl,
                               amount_brl AS amount_fiat,
                               'BRL' AS fiat_currency,
                               'pix' AS payment_method,
                               'onchain'::text AS funding_method,
                               COALESCE(fee_brl, 0) AS fee_brl,
                               COALESCE(payout_brl, 0) AS payout_brl,
                               btc_amount AS crypto_amount,
                               asset,
                               address,
                               network,
                               rate_locked,
                               NULL::text AS provider_payment_id,
                               tx_hash,
                               deposit_tx,
                               deposit_amount,
                               ''::text AS pix_code,
                               NULL::timestamptz AS pix_expires_at,
                               op.pix_cpf_enc,
                               op.pix_phone_enc,
                               op.email_enc,
                               error,
                               request_id,
                               o.created_at,
                               COALESCE(o.updated_at, o.created_at) AS updated_at
                        FROM orders o
                        LEFT JOIN order_private op ON op.order_id = o.id
`
	if ok, err := db.adminCommerceReady(ctx); err != nil {
		return nil, err
	} else if ok {
		query += `
                        UNION ALL
                        SELECT 'commerce' AS source, o.id::text AS id, o.status,
                               COALESCE(pp.title, o.product_id)::text AS product,
                               COALESCE(pp.product_type, 'gift_card')::text AS product_type,
                               o.amount_brl,
                               o.amount_brl AS amount_fiat,
                               'BRL'::text AS fiat_currency,
                               'commerce'::text AS payment_method,
                               COALESCE(o.funding_method, 'internal_usdt') AS funding_method,
                               COALESCE(o.fee_brl, 0) AS fee_brl,
                               0::numeric AS payout_brl,
                               (o.required_usdt_micro::float8 / 1000000.0)::numeric AS crypto_amount,
                               CASE WHEN COALESCE(o.funding_method, '') = 'pix' THEN 'BRL' ELSE 'USDT' END AS asset,
                               o.wallet_address AS address,
                               'BSC'::text AS network,
                               o.usdt_rate AS rate_locked,
                               NULLIF(o.provider_reference, '') AS provider_payment_id,
                               NULL::text AS tx_hash,
                               NULL::text AS deposit_tx,
                               NULL::numeric AS deposit_amount,
                               COALESCE(o.pix_code, '') AS pix_code,
                               o.pix_expires_at,
                               NULL::text AS pix_cpf_enc,
                               NULL::text AS pix_phone_enc,
                               NULL::text AS email_enc,
                               NULLIF(o.error_message, '') AS error,
                               o.idempotency_key AS request_id,
                               o.created_at,
                               COALESCE(o.updated_at, o.created_at) AS updated_at
                        FROM mobile_gift_card_orders o
                        LEFT JOIN gift_card_provider_products pp ON pp.product_id = o.product_id
`
	}
	query += `
                ) txs
                ORDER BY created_at DESC
                LIMIT $1`
	rows, err := db.SQL.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AdminTransaction
	for rows.Next() {
		var tx AdminTransaction
		var providerPaymentID, txHash, depositTx, pixCode, pixCpfEnc, pixPhoneEnc, emailEnc, errMsg, requestID sql.NullString
		var depositAmount sql.NullFloat64
		var pixExpiresAt sql.NullTime
		if err := rows.Scan(&tx.Source, &tx.ID, &tx.Status, &tx.Product, &tx.ProductType, &tx.AmountBRL, &tx.AmountFiat,
			&tx.FiatCurrency, &tx.PaymentMethod, &tx.FundingMethod, &tx.FeeBRL, &tx.PayoutBRL,
			&tx.CryptoAmount, &tx.Asset, &tx.Address, &tx.Network, &tx.RateLocked,
			&providerPaymentID, &txHash, &depositTx, &depositAmount, &pixCode, &pixExpiresAt, &pixCpfEnc, &pixPhoneEnc, &emailEnc, &errMsg, &requestID,
			&tx.CreatedAt, &tx.UpdatedAt); err != nil {
			return nil, err
		}
		if providerPaymentID.Valid {
			tx.ProviderPaymentID = &providerPaymentID.String
		}
		if txHash.Valid {
			tx.TxHash = &txHash.String
		}
		if depositTx.Valid {
			tx.DepositTx = &depositTx.String
		}
		if depositAmount.Valid {
			tx.DepositAmount = &depositAmount.Float64
		}
		if pixCode.Valid {
			tx.PixCode = pixCode.String
		}
		if pixExpiresAt.Valid {
			tx.PixExpiresAt = &pixExpiresAt.Time
		}
		if db.privacy != nil {
			if pixCpfEnc.Valid && pixCpfEnc.String != "" {
				if plain, err := db.privacy.Decrypt(pixCpfEnc.String); err == nil {
					tx.PixCpf = plain
				}
			}
			if pixPhoneEnc.Valid && pixPhoneEnc.String != "" {
				if plain, err := db.privacy.Decrypt(pixPhoneEnc.String); err == nil {
					tx.PixPhone = plain
					tx.PixKey = plain
				}
			}
			if emailEnc.Valid && emailEnc.String != "" {
				if plain, err := db.privacy.Decrypt(emailEnc.String); err == nil {
					tx.Email = plain
				}
			}
		}
		if tx.PixKey == "" {
			tx.PixKey = firstNonEmpty(tx.PixPhone, tx.PixCpf)
		}
		if errMsg.Valid {
			tx.Error = &errMsg.String
		}
		if requestID.Valid {
			tx.RequestID = &requestID.String
		}
		out = append(out, tx)
	}
	return out, rows.Err()
}

func (db *DB) adminCommerceReady(ctx context.Context) (bool, error) {
	var ready bool
	err := db.SQL.QueryRowContext(ctx, `
                SELECT to_regclass('public.mobile_gift_card_orders') IS NOT NULL
                   AND to_regclass('public.gift_card_provider_products') IS NOT NULL
                   AND EXISTS (
                         SELECT 1
                           FROM information_schema.columns
                          WHERE table_schema = 'public'
                            AND table_name = 'mobile_gift_card_orders'
                            AND column_name = 'funding_method'
                   )
                   AND EXISTS (
                         SELECT 1
                           FROM information_schema.columns
                          WHERE table_schema = 'public'
                            AND table_name = 'mobile_gift_card_orders'
                            AND column_name = 'pix_code'
                   )
                   AND EXISTS (
                         SELECT 1
                           FROM information_schema.columns
                          WHERE table_schema = 'public'
                            AND table_name = 'mobile_gift_card_orders'
                            AND column_name = 'pix_expires_at'
                   )`).Scan(&ready)
	return ready, err
}

func (db *DB) EnsureBootstrapAdmin(ctx context.Context) error {
	email := strings.TrimSpace(strings.ToLower(db.cfg.AdminBootstrapEmail))
	password := db.cfg.AdminBootstrapPassword
	if email == "" || password == "" {
		return nil
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if err := db.ensureBootstrapMobileUser(ctx, email, string(hash)); err != nil {
		return err
	}
	var exists bool
	if err := db.SQL.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM admin_users WHERE lower(email) = lower($1))`, email).Scan(&exists); err != nil {
		return err
	}
	if exists {
		if _, err := db.SQL.ExecContext(ctx, `
                        UPDATE admin_users
                        SET password_hash = $1,
                            role = COALESCE(NULLIF(role, ''), 'owner'),
                            disabled_at = NULL,
                            updated_at = now()
                        WHERE lower(email) = lower($2)`,
			string(hash), email); err != nil {
			return err
		}
		_, err := db.SQL.ExecContext(ctx, `
                        UPDATE admin_sessions
                        SET revoked_at = now()
                        WHERE revoked_at IS NULL
                          AND admin_user_id IN (SELECT id FROM admin_users WHERE lower(email) = lower($1))`,
			email)
		return err
	}
	_, err = db.SQL.ExecContext(ctx, `
                INSERT INTO admin_users (id, email, password_hash, role, created_at, updated_at)
                VALUES ($1, $2, $3, 'owner', now(), now())`,
		NewID(), email, string(hash))
	return err
}

func (db *DB) ensureBootstrapMobileUser(ctx context.Context, email, passwordHash string) error {
	if email == "" || passwordHash == "" {
		return nil
	}
	var exists bool
	if err := db.SQL.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'users')`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return nil
	}
	_, err := db.SQL.ExecContext(ctx, `
                INSERT INTO users (email, password_hash, full_name, kyc_status, deleted_at, created_at, updated_at)
                VALUES ($1, $2, 'Paulo ChainFX', 'approved', NULL, now(), now())
                ON CONFLICT (email) DO UPDATE
                SET password_hash = EXCLUDED.password_hash,
                    full_name = COALESCE(NULLIF(users.full_name, ''), EXCLUDED.full_name),
                    kyc_status = CASE WHEN users.kyc_status = '' THEN EXCLUDED.kyc_status ELSE users.kyc_status END,
                    deleted_at = NULL,
                    updated_at = now()`,
		email, passwordHash)
	return err
}

func (db *DB) AuthenticateAdmin(ctx context.Context, email, password string) (*AdminUser, string, error) {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" || password == "" {
		return nil, "", fmt.Errorf("email e senha obrigatorios")
	}
	var user AdminUser
	var hash string
	err := db.SQL.QueryRowContext(ctx, `
                SELECT id::text, email, password_hash, role, created_at
                FROM admin_users
                WHERE lower(email) = lower($1) AND disabled_at IS NULL`, email).
		Scan(&user.ID, &user.Email, &hash, &user.Role, &user.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, "", fmt.Errorf("credenciais invalidas")
	}
	if err != nil {
		return nil, "", err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return nil, "", fmt.Errorf("credenciais invalidas")
	}
	token := NewAccessToken()
	tokenHash := privacy.Hash(token, db.cfg.LGPDSecret)
	if tokenHash == "" {
		tokenHash = token
	}
	_, err = db.SQL.ExecContext(ctx, `
                INSERT INTO admin_sessions (id, admin_user_id, token_hash, expires_at, created_at)
                VALUES ($1, $2, $3, now() + interval '12 hours', now())`,
		NewID(), user.ID, tokenHash)
	if err != nil {
		return nil, "", err
	}
	return &user, token, nil
}

func (db *DB) ValidateAdminSession(ctx context.Context, token string) (*AdminUser, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, nil
	}
	tokenHash := privacy.Hash(token, db.cfg.LGPDSecret)
	if tokenHash == "" {
		tokenHash = token
	}
	var user AdminUser
	err := db.SQL.QueryRowContext(ctx, `
                SELECT u.id::text, u.email, u.role, u.created_at
                FROM admin_sessions s
                JOIN admin_users u ON u.id = s.admin_user_id
                WHERE s.token_hash = $1
                  AND s.revoked_at IS NULL
                  AND s.expires_at > now()
                  AND u.disabled_at IS NULL`, tokenHash).
		Scan(&user.ID, &user.Email, &user.Role, &user.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (db *DB) ListPendingBuys(ctx context.Context) ([]BuyOrder, error) {
	rows, err := db.SQL.QueryContext(ctx, `
                SELECT id, COALESCE(access_token, ''), request_id, status, amount_brl::float8, COALESCE(amount_fiat, amount_brl)::float8,
                       COALESCE(fiat_currency, 'BRL'), COALESCE(payment_method, 'pix'), provider_payment_id,
                       COALESCE(fee_brl,0)::float8, COALESCE(payout_brl,0)::float8,
                       crypto_amount::float8, asset, COALESCE(network, 'BSC'), dest_address, rate_locked::float8, rate_lock_expires_at,
                       COALESCE(pix_payload, '{}'::jsonb), tx_hash_out, error, paid_at, settled_at, delivered_at, created_at, updated_at
                FROM buy_orders
               WHERE status IN ('pago_fiat','pago_pix')
                  OR (status = 'enviando' AND updated_at < now() - interval '60 seconds')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BuyOrder
	for rows.Next() {
		var buy BuyOrder
		var requestID, providerPaymentID, txHashOut, errMsg sql.NullString
		var paidAt, settledAt, deliveredAt sql.NullTime
		if err := rows.Scan(&buy.ID, &buy.AccessToken, &requestID, &buy.Status, &buy.AmountBRL, &buy.AmountFiat, &buy.FiatCurrency, &buy.PaymentMethod, &providerPaymentID,
			&buy.FeeBRL, &buy.PayoutBRL, &buy.CryptoAmount, &buy.Asset,
			&buy.Network, &buy.DestAddress, &buy.RateLocked, &buy.RateLockExpiresAt, &buy.PixPayload, &txHashOut, &errMsg, &paidAt, &settledAt, &deliveredAt, &buy.CreatedAt, &buy.UpdatedAt); err != nil {
			return nil, err
		}
		if requestID.Valid {
			buy.RequestID = &requestID.String
		}
		if txHashOut.Valid {
			buy.TxHashOut = &txHashOut.String
		}
		if providerPaymentID.Valid {
			buy.ProviderPaymentID = &providerPaymentID.String
		}
		if errMsg.Valid {
			buy.Error = &errMsg.String
		}
		if paidAt.Valid {
			buy.PaidAt = &paidAt.Time
		}
		if settledAt.Valid {
			buy.SettledAt = &settledAt.Time
		}
		if deliveredAt.Valid {
			buy.DeliveredAt = &deliveredAt.Time
		}
		out = append(out, buy)
	}
	return out, rows.Err()
}

func NewID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s", hex.EncodeToString(b[0:4]), hex.EncodeToString(b[4:6]), hex.EncodeToString(b[6:8]), hex.EncodeToString(b[8:10]), hex.EncodeToString(b[10:16]))
}

func NewAccessToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return NewID() + NewID()
	}
	return hex.EncodeToString(b)
}

func (db *DB) ValidateOrderAccess(ctx context.Context, id, token string) (bool, error) {
	if token == "" {
		return false, nil
	}
	var ok bool
	err := db.SQL.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM orders WHERE id = $1 AND access_token = $2)`, id, token).Scan(&ok)
	return ok, err
}

func (db *DB) ValidateBuyAccess(ctx context.Context, id, token string) (bool, error) {
	if token == "" {
		return false, nil
	}
	var ok bool
	err := db.SQL.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM buy_orders WHERE id = $1 AND access_token = $2)`, id, token).Scan(&ok)
	return ok, err
}

type scanner interface {
	Scan(dest ...any) error
}

func (db *DB) scanOrder(row scanner) (*models.Order, error) {
	var o models.Order
	var status string
	var txHash, errMsg, depositTx, pixCpf, pixPhone sql.NullString
	var depositAmount sql.NullFloat64
	var derivationIndex sql.NullInt64
	err := row.Scan(&o.ID, &o.AccessToken, &status, &o.AmountBRL, &o.AmountUSDT, &o.FeeBRL, &o.PayoutBRL, &o.Address, &o.Asset, &o.Network,
		&o.RateLocked, &o.RateLockExpiresAt, &o.CreatedAt, &o.UpdatedAt, &txHash, &errMsg, &depositTx, &depositAmount, &pixCpf, &pixPhone, &derivationIndex)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	o.Status = models.OrderStatus(status)
	o.BSCAddress = o.Address
	if pixCpf.Valid && pixCpf.String != "" && db.privacy != nil {
		if plain, err := db.privacy.Decrypt(pixCpf.String); err == nil {
			o.PixCpf = plain
		}
	}
	if pixPhone.Valid && pixPhone.String != "" && db.privacy != nil {
		if plain, err := db.privacy.Decrypt(pixPhone.String); err == nil {
			o.PixPhone = plain
		}
	}
	o.PixKey = firstNonEmpty(o.PixPhone, o.PixCpf)
	if o.PixPhone != "" {
		o.PixType = "pix"
	} else if o.PixCpf != "" {
		o.PixType = "cpf"
	}
	if txHash.Valid {
		o.TxHash = &txHash.String
	}
	if errMsg.Valid {
		o.Error = &errMsg.String
	}
	if depositTx.Valid {
		o.DepositTx = &depositTx.String
	}
	if depositAmount.Valid {
		o.DepositAmount = &depositAmount.Float64
	}
	if derivationIndex.Valid {
		i := int(derivationIndex.Int64)
		o.DerivationIndex = &i
	}
	return &o, nil
}

func sellPayoutExecutionSelectSQL() string {
	return `SELECT id::text, order_id::text, provider, provider_idempotency_key, provider_id_envio,
                       amount_brl_minor, recipient_reference, status, attempt_count,
                       submit_started_at, submit_completed_at, submit_outcome,
                       first_ambiguous_at, last_reconciled_at, consecutive_not_found,
                       next_attempt_at, COALESCE(provider_reference,''), COALESCE(provider_e2e_id,''),
                       COALESCE(last_error,''), created_at, updated_at, completed_at
                  FROM sell_payout_executions`
}

func scanSellPayoutExecution(row interface {
	Scan(dest ...any) error
}) (*SellPayoutExecution, error) {
	var exec SellPayoutExecution
	var submitStarted, submitCompleted, firstAmbiguous, lastReconciled, completed sql.NullTime
	if err := row.Scan(
		&exec.ID, &exec.OrderID, &exec.Provider, &exec.ProviderIDempotencyKey, &exec.ProviderIDEnvio,
		&exec.AmountBRLMinor, &exec.RecipientReference, &exec.Status, &exec.AttemptCount,
		&submitStarted, &submitCompleted, &exec.SubmitOutcome,
		&firstAmbiguous, &lastReconciled, &exec.ConsecutiveNotFound,
		&exec.NextAttemptAt, &exec.ProviderReference, &exec.ProviderE2EID,
		&exec.LastError, &exec.CreatedAt, &exec.UpdatedAt, &completed,
	); err != nil {
		return nil, err
	}
	if submitStarted.Valid {
		exec.SubmitStartedAt = &submitStarted.Time
	}
	if submitCompleted.Valid {
		exec.SubmitCompletedAt = &submitCompleted.Time
	}
	if firstAmbiguous.Valid {
		exec.FirstAmbiguousAt = &firstAmbiguous.Time
	}
	if lastReconciled.Valid {
		exec.LastReconciledAt = &lastReconciled.Time
	}
	if completed.Valid {
		exec.CompletedAt = &completed.Time
	}
	return &exec, nil
}

func sellPayoutStatusFromProvider(status string) (next string, completed bool, failed bool) {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "REALIZADO", "CONCLUIDA", "CONCLUÍDA", "COMPLETED", "CONFIRMED", "SETTLED", "SUCCESS", "SUCCEEDED":
		return "completed", true, false
	case "REJEITADO", "REJECTED", "FAILED", "CANCELLED", "CANCELED", "DENIED", "DEVOLVIDO":
		return "failed", false, true
	default:
		return "provider_pending", false, false
	}
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func requestIDFromPayload(payload any) string {
	if payload == nil {
		return ""
	}
	if data, ok := payload.(map[string]any); ok {
		if value, ok := data["requestId"].(string); ok {
			return value
		}
		if value, ok := data["request_id"].(string); ok {
			return value
		}
	}
	return ""
}

const schemaSQL = `
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS admin_users (
  id UUID PRIMARY KEY,
  email TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  role TEXT NOT NULL DEFAULT 'owner',
  disabled_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS admin_sessions (
  id UUID PRIMARY KEY,
  admin_user_id UUID NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
  token_hash TEXT NOT NULL UNIQUE,
  expires_at TIMESTAMPTZ NOT NULL,
  revoked_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_admin_sessions_token_hash ON admin_sessions(token_hash);
CREATE INDEX IF NOT EXISTS idx_admin_sessions_active ON admin_sessions(admin_user_id, expires_at) WHERE revoked_at IS NULL;

CREATE TABLE IF NOT EXISTS orders (
  id UUID PRIMARY KEY,
  access_token TEXT,
  request_id TEXT,
  status VARCHAR(32) NOT NULL,
  amount_brl NUMERIC(18,2) NOT NULL,
  btc_amount NUMERIC(28,8) NOT NULL,
  fee_brl NUMERIC(18,2),
  payout_brl NUMERIC(18,2),
  address TEXT NOT NULL,
  asset VARCHAR(16) NOT NULL DEFAULT 'USDT',
  network VARCHAR(32) NOT NULL DEFAULT 'BSC',
  rate_locked NUMERIC(28,8) NOT NULL,
  rate_lock_expires_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  tx_hash TEXT,
  error TEXT,
  deposit_tx TEXT,
  deposit_amount NUMERIC(28,8),
  pix_cpf TEXT,
  pix_phone TEXT,
  pix_cpf_hash TEXT,
  pix_phone_hash TEXT,
  derivation_index INT
);

ALTER TABLE orders ADD COLUMN IF NOT EXISTS access_token TEXT;
UPDATE orders SET access_token = encode(gen_random_bytes(32), 'hex') WHERE access_token IS NULL OR access_token = '';
CREATE UNIQUE INDEX IF NOT EXISTS idx_orders_access_token ON orders (access_token);

CREATE TABLE IF NOT EXISTS order_private (
  order_id UUID PRIMARY KEY REFERENCES orders(id) ON DELETE CASCADE,
  pix_cpf_enc TEXT,
  pix_phone_enc TEXT,
  email_enc TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE order_private ADD COLUMN IF NOT EXISTS email_enc TEXT;

ALTER TABLE orders ADD COLUMN IF NOT EXISTS request_id TEXT;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS pix_cpf_hash TEXT;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS pix_phone_hash TEXT;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS user_id UUID REFERENCES users(id);
CREATE INDEX IF NOT EXISTS idx_orders_user_created ON orders(user_id, created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_orders_deposit_tx_unique ON orders (deposit_tx) WHERE deposit_tx IS NOT NULL AND deposit_tx <> '';

CREATE TABLE IF NOT EXISTS btc_sell_fundings (
  order_id UUID PRIMARY KEY REFERENCES orders(id) ON DELETE CASCADE,
  user_id UUID NOT NULL REFERENCES users(id),
  wallet_address_id TEXT NOT NULL,
  btc_address TEXT NOT NULL,
  btc_network TEXT NOT NULL CHECK (btc_network IN ('mainnet','testnet','signet','regtest')),
  expected_sats BIGINT NOT NULL CHECK (expected_sats > 0),
  received_sats BIGINT,
  txid TEXT,
  vout INTEGER,
  confirmations INTEGER NOT NULL DEFAULT 0,
  quote_id TEXT,
  status TEXT NOT NULL DEFAULT 'awaiting_deposit'
    CHECK (status IN ('awaiting_deposit','detected','pending_confirmations','confirmed','manual_review','expired','orphaned')),
  error TEXT,
  detected_at TIMESTAMPTZ,
  confirmed_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_btc_sell_fundings_lookup
  ON btc_sell_fundings(btc_network, user_id, btc_address, status, created_at);
CREATE UNIQUE INDEX IF NOT EXISTS uq_btc_sell_fundings_txid_vout
  ON btc_sell_fundings(btc_network, txid, vout)
  WHERE txid IS NOT NULL AND txid <> '' AND vout IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_btc_sell_fundings_quote
  ON btc_sell_fundings(quote_id)
  WHERE quote_id IS NOT NULL AND quote_id <> '';

CREATE TABLE IF NOT EXISTS order_events (
  id UUID PRIMARY KEY,
  order_id UUID REFERENCES orders(id),
  request_id TEXT,
  type VARCHAR(64) NOT NULL,
  payload JSONB,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE order_events ADD COLUMN IF NOT EXISTS request_id TEXT;

CREATE TABLE IF NOT EXISTS order_incidents (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  order_id UUID NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
  incident_type TEXT NOT NULL,
  severity TEXT NOT NULL DEFAULT 'high' CHECK (severity IN ('low','medium','high','critical')),
  reason TEXT NOT NULL,
  payload JSONB NOT NULL DEFAULT '{}'::jsonb,
  status TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','under_review','resolved','rejected')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_order_incidents_open_unique
  ON order_incidents(order_id, incident_type)
  WHERE status = 'open';
CREATE INDEX IF NOT EXISTS idx_order_incidents_status_created_at
  ON order_incidents(status, created_at DESC);

CREATE TABLE IF NOT EXISTS payouts (
  id UUID PRIMARY KEY,
  order_id UUID REFERENCES orders(id),
  pix_cpf TEXT,
  pix_key TEXT,
  status VARCHAR(32) NOT NULL,
  provider_response JSONB,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS onchain_cursor (
  id SERIAL PRIMARY KEY,
  network VARCHAR(32) NOT NULL UNIQUE,
  last_block BIGINT NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS sweeps (
  id UUID PRIMARY KEY,
  child_index INT NOT NULL,
  from_addr TEXT NOT NULL,
  to_addr TEXT NOT NULL,
  amount NUMERIC(28,8) NOT NULL,
  tx_hash TEXT,
  status VARCHAR(32) NOT NULL DEFAULT 'pending',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  idempotency_key TEXT,
  amount_BNB_fee NUMERIC(28,8),
  order_id UUID REFERENCES orders(id)
);

CREATE TABLE IF NOT EXISTS buy_orders (
  id UUID PRIMARY KEY,
  access_token TEXT,
  request_id TEXT,
  status VARCHAR(32) NOT NULL,
  amount_brl NUMERIC(18,2) NOT NULL,
  amount_fiat NUMERIC(18,2),
  fiat_currency VARCHAR(8) NOT NULL DEFAULT 'BRL',
  payment_method VARCHAR(32) NOT NULL DEFAULT 'pix',
  provider_payment_id TEXT,
  fee_brl NUMERIC(18,2),
  payout_brl NUMERIC(18,2),
  crypto_amount NUMERIC(28,8) NOT NULL,
  asset VARCHAR(16) NOT NULL DEFAULT 'USDT',
  network TEXT NOT NULL DEFAULT 'BSC',
  dest_address TEXT NOT NULL,
  rate_locked NUMERIC(28,8) NOT NULL,
  rate_lock_expires_at TIMESTAMPTZ NOT NULL,
  pix_payload JSONB,
  tx_hash_out TEXT,
  error TEXT,
  paid_at TIMESTAMPTZ,
  settled_at TIMESTAMPTZ,
  delivered_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE buy_orders ADD COLUMN IF NOT EXISTS access_token TEXT;
ALTER TABLE buy_orders ADD COLUMN IF NOT EXISTS amount_fiat NUMERIC(18,2);
ALTER TABLE buy_orders ADD COLUMN IF NOT EXISTS request_id TEXT;
ALTER TABLE buy_orders ADD COLUMN IF NOT EXISTS fiat_currency VARCHAR(8) NOT NULL DEFAULT 'BRL';
ALTER TABLE buy_orders ADD COLUMN IF NOT EXISTS payment_method VARCHAR(32) NOT NULL DEFAULT 'pix';
ALTER TABLE buy_orders ADD COLUMN IF NOT EXISTS provider_payment_id TEXT;
ALTER TABLE buy_orders ADD COLUMN IF NOT EXISTS paid_at TIMESTAMPTZ;
ALTER TABLE buy_orders ADD COLUMN IF NOT EXISTS settled_at TIMESTAMPTZ;
ALTER TABLE buy_orders ADD COLUMN IF NOT EXISTS delivered_at TIMESTAMPTZ;
ALTER TABLE buy_orders ADD COLUMN IF NOT EXISTS user_id UUID REFERENCES users(id);
ALTER TABLE buy_orders ADD COLUMN IF NOT EXISTS network TEXT NOT NULL DEFAULT 'BSC';
UPDATE buy_orders SET amount_fiat = amount_brl WHERE amount_fiat IS NULL;
UPDATE buy_orders SET access_token = encode(gen_random_bytes(32), 'hex') WHERE access_token IS NULL OR access_token = '';
CREATE UNIQUE INDEX IF NOT EXISTS idx_buy_orders_access_token ON buy_orders (access_token);
CREATE INDEX IF NOT EXISTS idx_buy_orders_user_created ON buy_orders(user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS buy_order_private (
  buy_order_id UUID PRIMARY KEY REFERENCES buy_orders(id) ON DELETE CASCADE,
  email_enc TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS marketing_contacts (
  email TEXT PRIMARY KEY,
  source TEXT,
  subscribed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  unsubscribed_at TIMESTAMPTZ,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS buy_order_events (
  id UUID PRIMARY KEY,
  buy_order_id UUID REFERENCES buy_orders(id),
  request_id TEXT,
  type VARCHAR(64) NOT NULL,
  payload JSONB,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE buy_order_events ADD COLUMN IF NOT EXISTS request_id TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS idx_buy_webhook_provider_once ON buy_order_events (buy_order_id, (payload ->> 'providerId')) WHERE type = 'webhook.provider' AND payload ? 'providerId';
CREATE UNIQUE INDEX IF NOT EXISTS idx_order_idempotency_once ON order_events (order_id, (payload ->> 'key')) WHERE type = 'idempotency' AND payload ? 'key';

CREATE TABLE IF NOT EXISTS buy_liquidity_quotes (
  id UUID PRIMARY KEY,
  buy_order_id UUID NOT NULL REFERENCES buy_orders(id) ON DELETE CASCADE,
  provider TEXT NOT NULL,
  provider_type TEXT NOT NULL DEFAULT 'liquidity_provider',
  external_quote_id TEXT,
  asset TEXT NOT NULL,
  network TEXT NOT NULL,
  token_contract TEXT,
  token_decimals INTEGER NOT NULL DEFAULT 18,
  fiat_cost_brl NUMERIC(18,2) NOT NULL DEFAULT 0,
  provider_fee_brl NUMERIC(18,2) NOT NULL DEFAULT 0,
  network_fee_brl NUMERIC(18,2) NOT NULL DEFAULT 0,
  spread_brl NUMERIC(18,2) NOT NULL DEFAULT 0,
  total_cost_brl NUMERIC(18,2) NOT NULL DEFAULT 0,
  crypto_amount NUMERIC(28,8) NOT NULL DEFAULT 0,
  delivery_sla_seconds INTEGER NOT NULL DEFAULT 300,
  reliability_bps INTEGER NOT NULL DEFAULT 9000,
  direct_delivery BOOLEAN NOT NULL DEFAULT false,
  selected BOOLEAN NOT NULL DEFAULT false,
  expires_at TIMESTAMPTZ,
  payload JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (buy_order_id, provider, external_quote_id)
);
CREATE INDEX IF NOT EXISTS idx_buy_liquidity_quotes_order_selected ON buy_liquidity_quotes(buy_order_id, selected, created_at DESC);

CREATE TABLE IF NOT EXISTS buy_liquidity_executions (
  id UUID PRIMARY KEY,
  buy_order_id UUID NOT NULL REFERENCES buy_orders(id) ON DELETE CASCADE,
  quote_id UUID REFERENCES buy_liquidity_quotes(id) ON DELETE SET NULL,
  provider TEXT NOT NULL,
  status TEXT NOT NULL,
  external_order_id TEXT,
  tx_hash TEXT,
  error TEXT,
  payload JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_buy_liquidity_executions_order_created ON buy_liquidity_executions(buy_order_id, created_at DESC);

CREATE TABLE IF NOT EXISTS settlement_instructions (
  id UUID PRIMARY KEY,
  operation_id BYTEA NOT NULL UNIQUE,
  settlement_intent_id TEXT NOT NULL,
  order_id TEXT NOT NULL,
  side TEXT,
  source_channel TEXT,
  network TEXT NOT NULL,
  chain_id BIGINT NOT NULL,
  vault_address TEXT NOT NULL,
  token_address TEXT NOT NULL,
  recipient_address TEXT NOT NULL,
  amount_raw NUMERIC(78, 0) NOT NULL,
  policy_version TEXT NOT NULL,
  network_policy_version TEXT,
  risk_policy_version TEXT,
  contract_version TEXT,
  authorization_status TEXT NOT NULL,
  execution_status TEXT NOT NULL,
  authorized_at TIMESTAMPTZ,
  expires_at TIMESTAMPTZ,
  consumed_at TIMESTAMPTZ,
  tx_hash TEXT,
  block_number BIGINT,
  confirmations INTEGER,
  failure_code TEXT,
  failure_reason TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_settlement_instructions_order ON settlement_instructions(order_id);
CREATE INDEX IF NOT EXISTS idx_settlement_instructions_status ON settlement_instructions(authorization_status, execution_status);
CREATE INDEX IF NOT EXISTS idx_settlement_instructions_network ON settlement_instructions(network, chain_id, token_address);

CREATE TABLE IF NOT EXISTS settlement_receipts (
  id UUID PRIMARY KEY,
  operation_id BYTEA NOT NULL UNIQUE,
  tx_hash TEXT NOT NULL UNIQUE,
  chain_id BIGINT NOT NULL,
  block_number BIGINT,
  block_hash TEXT,
  transaction_index INTEGER,
  receipt_status BIGINT,
  confirmations BIGINT NOT NULL DEFAULT 0,
  vault_event_log_index INTEGER,
  transfer_event_log_index INTEGER,
  receipt_verified BOOLEAN NOT NULL DEFAULT FALSE,
  vault_event_verified BOOLEAN NOT NULL DEFAULT FALSE,
  transfer_event_verified BOOLEAN NOT NULL DEFAULT FALSE,
  confirmations_verified BOOLEAN NOT NULL DEFAULT FALSE,
  reconciliation_status TEXT NOT NULL DEFAULT 'PENDING',
  failure_code TEXT,
  failure_field TEXT,
  failure_details JSONB,
  first_seen_at TIMESTAMPTZ,
  confirmed_at TIMESTAMPTZ,
  reconciled_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_settlement_receipts_status ON settlement_receipts(reconciliation_status, updated_at);
CREATE INDEX IF NOT EXISTS idx_settlement_receipts_chain_block ON settlement_receipts(chain_id, block_number);

CREATE TABLE IF NOT EXISTS settlement_reconciliation_events (
  id UUID PRIMARY KEY,
  operation_id BYTEA NOT NULL,
  tx_hash TEXT,
  event_type TEXT NOT NULL,
  previous_status TEXT,
  new_status TEXT,
  expected JSONB,
  observed JSONB,
  failure_code TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_settlement_reconciliation_events_operation ON settlement_reconciliation_events(operation_id, created_at);
CREATE INDEX IF NOT EXISTS idx_settlement_reconciliation_events_type ON settlement_reconciliation_events(event_type, created_at);

CREATE TABLE IF NOT EXISTS quotes (
  id TEXT PRIMARY KEY,
  side VARCHAR(8) NOT NULL CHECK (side IN ('buy','sell')),
  asset VARCHAR(16) NOT NULL,
  network VARCHAR(32) NOT NULL DEFAULT '',
  fiat_currency VARCHAR(8) NOT NULL,
  payment_method VARCHAR(32) NOT NULL DEFAULT 'pix',
  amount_minor BIGINT NOT NULL,
  crypto_amount_units TEXT NOT NULL,
  rate NUMERIC(28,8) NOT NULL,
  market_rate NUMERIC(28,8) NOT NULL DEFAULT 0,
  fee_minor BIGINT NOT NULL DEFAULT 0,
  expires_at TIMESTAMPTZ NOT NULL,
  consumed_at TIMESTAMPTZ,
  api_key_hash TEXT NOT NULL DEFAULT '',
  body_hash TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE quotes ADD COLUMN IF NOT EXISTS network VARCHAR(32) NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_quotes_api_key_expires ON quotes(api_key_hash, expires_at);
CREATE INDEX IF NOT EXISTS idx_quotes_consumable ON quotes(expires_at) WHERE consumed_at IS NULL;

CREATE TABLE IF NOT EXISTS idempotency_keys (
  key TEXT NOT NULL,
  operation TEXT NOT NULL,
  api_key_hash TEXT,
  body_hash TEXT NOT NULL,
  status VARCHAR(16) NOT NULL DEFAULT 'started',
  result_type TEXT,
  result_id TEXT,
  response_status INT,
  response_json JSONB,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (key, operation, api_key_hash)
);
ALTER TABLE idempotency_keys ALTER COLUMN api_key_hash SET DEFAULT '';
UPDATE idempotency_keys SET api_key_hash = '' WHERE api_key_hash IS NULL;
ALTER TABLE idempotency_keys ALTER COLUMN api_key_hash SET NOT NULL;
CREATE INDEX IF NOT EXISTS idx_idempotency_result ON idempotency_keys(result_type, result_id);

CREATE TABLE IF NOT EXISTS api_request_logs (
  id UUID PRIMARY KEY,
  request_id TEXT NOT NULL,
  method VARCHAR(12) NOT NULL,
  path TEXT NOT NULL,
  route_class VARCHAR(32) NOT NULL,
  status_code INT NOT NULL,
  duration_ms BIGINT NOT NULL,
  api_key_hash TEXT,
  api_key_scope VARCHAR(32),
  auth_mode VARCHAR(32),
  client_ip TEXT,
  user_agent TEXT,
  agent_id TEXT,
  agent_signature_hash TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE api_request_logs ADD COLUMN IF NOT EXISTS agent_id TEXT;
ALTER TABLE api_request_logs ADD COLUMN IF NOT EXISTS agent_signature_hash TEXT;
CREATE INDEX IF NOT EXISTS idx_api_request_logs_created ON api_request_logs(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_api_request_logs_route ON api_request_logs(route_class, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_api_request_logs_key ON api_request_logs(api_key_hash, created_at DESC) WHERE api_key_hash IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_api_request_logs_agent ON api_request_logs(agent_id, created_at DESC) WHERE agent_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS mcp_tool_logs (
  id UUID PRIMARY KEY,
  request_id TEXT,
  tool_name TEXT NOT NULL,
  status VARCHAR(32) NOT NULL,
  error_message TEXT,
  duration_ms BIGINT NOT NULL DEFAULT 0,
  api_key_hash TEXT,
  auth_mode VARCHAR(32),
  agent_id TEXT,
  agent_signature_hash TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE mcp_tool_logs ADD COLUMN IF NOT EXISTS agent_id TEXT;
ALTER TABLE mcp_tool_logs ADD COLUMN IF NOT EXISTS agent_signature_hash TEXT;
CREATE INDEX IF NOT EXISTS idx_mcp_tool_logs_created ON mcp_tool_logs(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_mcp_tool_logs_tool ON mcp_tool_logs(tool_name, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_mcp_tool_logs_key ON mcp_tool_logs(api_key_hash, created_at DESC) WHERE api_key_hash IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_mcp_tool_logs_agent ON mcp_tool_logs(agent_id, created_at DESC) WHERE agent_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS developer_projects (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  environment TEXT NOT NULL DEFAULT 'sandbox' CHECK (environment IN ('sandbox','production')),
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','archived','disabled')),
  spending_limit_usdt NUMERIC(28,8) NOT NULL DEFAULT 0,
  metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_developer_projects_environment ON developer_projects(environment, status);

CREATE TABLE IF NOT EXISTS developer_api_keys (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES developer_projects(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  environment TEXT NOT NULL DEFAULT 'sandbox' CHECK (environment IN ('sandbox','production')),
  public_key TEXT NOT NULL UNIQUE,
  secret_key_hash TEXT NOT NULL UNIQUE,
  log_hash TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled','revoked')),
  scopes_json JSONB NOT NULL DEFAULT '[]'::jsonb,
  ip_restrictions_json JSONB NOT NULL DEFAULT '[]'::jsonb,
  rate_limit_per_minute INT NOT NULL DEFAULT 600,
  spending_limit_usdt NUMERIC(28,8) NOT NULL DEFAULT 0,
  expires_at TIMESTAMPTZ,
  last_used_at TIMESTAMPTZ,
  rotated_at TIMESTAMPTZ,
  revoked_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_developer_api_keys_project ON developer_api_keys(project_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_developer_api_keys_secret_hash ON developer_api_keys(secret_key_hash);
CREATE INDEX IF NOT EXISTS idx_developer_api_keys_log_hash ON developer_api_keys(log_hash);

CREATE TABLE IF NOT EXISTS webhook_subscriptions (
  id UUID PRIMARY KEY,
  provider VARCHAR(32) NOT NULL DEFAULT 'generic',
  target_url TEXT NOT NULL,
  secret TEXT,
  events TEXT[] NOT NULL DEFAULT '{}',
  active BOOLEAN NOT NULL DEFAULT true,
  description TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_triggered_at TIMESTAMPTZ,
  last_status_code INT,
  last_error TEXT,
  failure_count INT NOT NULL DEFAULT 0
);

ALTER TABLE webhook_subscriptions ADD COLUMN IF NOT EXISTS provider VARCHAR(32) NOT NULL DEFAULT 'generic';
ALTER TABLE webhook_subscriptions ADD COLUMN IF NOT EXISTS secret TEXT;
ALTER TABLE webhook_subscriptions ADD COLUMN IF NOT EXISTS active BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE webhook_subscriptions ADD COLUMN IF NOT EXISTS description TEXT;
ALTER TABLE webhook_subscriptions ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now();
ALTER TABLE webhook_subscriptions ADD COLUMN IF NOT EXISTS last_triggered_at TIMESTAMPTZ;
ALTER TABLE webhook_subscriptions ADD COLUMN IF NOT EXISTS last_status_code INT;
ALTER TABLE webhook_subscriptions ADD COLUMN IF NOT EXISTS last_error TEXT;
ALTER TABLE webhook_subscriptions ADD COLUMN IF NOT EXISTS failure_count INT NOT NULL DEFAULT 0;

DO $$
BEGIN
  IF EXISTS (
    SELECT 1
    FROM information_schema.columns
    WHERE table_name = 'webhook_subscriptions'
      AND column_name = 'events'
      AND data_type = 'jsonb'
  ) THEN
    ALTER TABLE webhook_subscriptions ALTER COLUMN events DROP DEFAULT;
    ALTER TABLE webhook_subscriptions
      ALTER COLUMN events TYPE TEXT[]
      USING COALESCE(ARRAY(SELECT jsonb_array_elements_text(events)), ARRAY[]::TEXT[]);
    ALTER TABLE webhook_subscriptions ALTER COLUMN events SET DEFAULT '{}';
    ALTER TABLE webhook_subscriptions ALTER COLUMN events SET NOT NULL;
  END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_webhook_subs_active ON webhook_subscriptions(active) WHERE active = true;
CREATE INDEX IF NOT EXISTS idx_webhook_subs_events ON webhook_subscriptions USING gin(events);

CREATE TABLE IF NOT EXISTS webhook_deliveries (
  id UUID PRIMARY KEY,
  subscription_id UUID NOT NULL REFERENCES webhook_subscriptions(id) ON DELETE CASCADE,
  event VARCHAR(64) NOT NULL,
  payload JSONB NOT NULL,
  status_code INT NOT NULL DEFAULT 0,
  ok BOOLEAN NOT NULL DEFAULT false,
  error TEXT,
  attempt INT NOT NULL DEFAULT 1,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE webhook_deliveries ADD COLUMN IF NOT EXISTS event VARCHAR(64);
ALTER TABLE webhook_deliveries ADD COLUMN IF NOT EXISTS status_code INT NOT NULL DEFAULT 0;
ALTER TABLE webhook_deliveries ADD COLUMN IF NOT EXISTS ok BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE webhook_deliveries ADD COLUMN IF NOT EXISTS error TEXT;
ALTER TABLE webhook_deliveries ADD COLUMN IF NOT EXISTS attempt INT NOT NULL DEFAULT 1;
CREATE INDEX IF NOT EXISTS idx_webhook_del_sub ON webhook_deliveries(subscription_id, created_at DESC);

CREATE TABLE IF NOT EXISTS api_products (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  description TEXT NOT NULL,
  unit TEXT NOT NULL DEFAULT 'request',
  quota_units INT NOT NULL,
  price_usdt NUMERIC(28,8) NOT NULL,
  duration_seconds INT NOT NULL,
  provider_name TEXT NOT NULL DEFAULT 'ChainFX',
  provider_url TEXT,
  active BOOLEAN NOT NULL DEFAULT true,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO api_products (id, name, description, unit, quota_units, price_usdt, duration_seconds, provider_name, provider_url)
VALUES
  ('chainfx-mcp-basic', 'ChainFX MCP Basic', 'Autonomous agent access to ChainFX MCP tools, rates, prompts and automation hooks.', 'tool_call', 10000, 10.00, 2592000, 'ChainFX', 'https://www.chainfx.store'),
  ('api-credit-basic', 'API Credit Basic', 'General API access credits for machine-to-machine stablecoin-paid usage.', 'request', 10000, 10.00, 2592000, 'ChainFX', 'https://www.chainfx.store')
ON CONFLICT (id) DO NOTHING;

CREATE TABLE IF NOT EXISTS agent_wallets (
  address TEXT PRIMARY KEY,
  first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  reputation_tier TEXT NOT NULL DEFAULT 'tier0',
  total_spent_usdt NUMERIC(28,8) NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS api_payments (
  id UUID PRIMARY KEY,
  product_id TEXT NOT NULL REFERENCES api_products(id),
  buyer_wallet TEXT NOT NULL,
  amount_usdt NUMERIC(28,8) NOT NULL,
  chainfx_fee_usdt NUMERIC(28,8) NOT NULL,
  provider_amount_usdt NUMERIC(28,8) NOT NULL,
  asset TEXT NOT NULL DEFAULT 'USDT',
  network TEXT NOT NULL DEFAULT 'BSC',
  payment_address TEXT NOT NULL,
  memo TEXT NOT NULL UNIQUE,
  nonce TEXT NOT NULL,
  request_hash TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending',
  tx_hash TEXT UNIQUE,
  idempotency_key TEXT UNIQUE,
  quote_expires_at TIMESTAMPTZ NOT NULL,
  confirmed_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_api_payments_wallet ON api_payments(buyer_wallet, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_api_payments_status ON api_payments(status, quote_expires_at);

CREATE TABLE IF NOT EXISTS api_access_grants (
  id UUID PRIMARY KEY,
  payment_id UUID NOT NULL UNIQUE REFERENCES api_payments(id),
  product_id TEXT NOT NULL REFERENCES api_products(id),
  buyer_wallet TEXT NOT NULL,
  access_token_hash TEXT NOT NULL UNIQUE,
  quota_total INT NOT NULL,
  quota_remaining INT NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  status TEXT NOT NULL DEFAULT 'active',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_api_access_grants_wallet ON api_access_grants(buyer_wallet, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_api_access_grants_status ON api_access_grants(status, expires_at);

CREATE TABLE IF NOT EXISTS api_usage_events (
  id UUID PRIMARY KEY,
  grant_id UUID NOT NULL REFERENCES api_access_grants(id),
  product_id TEXT NOT NULL REFERENCES api_products(id),
  units INT NOT NULL,
  request_hash TEXT,
  idempotency_key TEXT NOT NULL,
  metadata JSONB,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (grant_id, idempotency_key)
);

CREATE INDEX IF NOT EXISTS idx_api_usage_events_grant ON api_usage_events(grant_id, created_at DESC);

CREATE TABLE IF NOT EXISTS agent_supported_assets (
  symbol TEXT NOT NULL,
  network TEXT NOT NULL DEFAULT 'BSC',
  contract_address TEXT NOT NULL,
  decimals INT NOT NULL DEFAULT 18,
  fee_bps INT NOT NULL DEFAULT 600,
  min_amount NUMERIC(28,8) NOT NULL DEFAULT 5,
  status TEXT NOT NULL DEFAULT 'active',
  enabled BOOLEAN NOT NULL DEFAULT true,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (symbol, network)
);

ALTER TABLE agent_supported_assets ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'active';

INSERT INTO agent_supported_assets (symbol, network, contract_address, decimals, fee_bps, min_amount, status, enabled)
VALUES
  ('USDT', 'BSC', '0x55d398326f99059fF775485246999027B3197955', 18, 600, 5.00, 'active', true),
  ('USDC', 'BSC', '0x8ac76a51cc950d9822d68b83fe1ad97b32cd580d', 18, 600, 5.00, 'active', true),
  ('USDT', 'POLYGON', '0xc2132D05D31c914a87C6611C10748AEb04B58e8F', 6, 600, 5.00, 'active', true),
  ('USDC', 'POLYGON', '0x2791Bca1f2de4661ED88A30C99A7a9449Aa84174', 6, 600, 5.00, 'active', true),
  ('BUSD', 'BSC', '0xe9e7cea3dedca5984780bafc599b69add087d56', 18, 600, 5.00, 'legacy', false)
ON CONFLICT (symbol, network) DO NOTHING;

UPDATE agent_supported_assets SET status = 'legacy', enabled = false WHERE symbol = 'BUSD' AND network = 'BSC';

CREATE TABLE IF NOT EXISTS agent_trade_intents (
  id UUID PRIMARY KEY,
  agent_wallet TEXT NOT NULL,
  pay_asset TEXT NOT NULL,
  receive_asset TEXT NOT NULL,
  pay_amount NUMERIC(28,8) NOT NULL,
  receive_amount NUMERIC(28,8) NOT NULL,
  chainfx_fee_amount NUMERIC(28,8) NOT NULL,
  fee_bps INT NOT NULL,
  network TEXT NOT NULL DEFAULT 'BSC',
  payment_address TEXT NOT NULL,
  destination_wallet TEXT NOT NULL,
  pay_token_contract TEXT NOT NULL,
  receive_token_contract TEXT NOT NULL,
  nonce TEXT NOT NULL,
  request_hash TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending',
  tx_hash TEXT,
  chain_id BIGINT,
  log_index INT,
  block_number BIGINT,
  block_hash TEXT,
  transfer_from TEXT,
  transfer_to TEXT,
  transfer_amount_raw TEXT,
  overpayment_amount NUMERIC(28,8) NOT NULL DEFAULT 0,
  settlement_tx_hash TEXT,
  idempotency_key TEXT UNIQUE,
  expires_at TIMESTAMPTZ NOT NULL,
  paid_at TIMESTAMPTZ,
  settled_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE agent_trade_intents DROP CONSTRAINT IF EXISTS agent_trade_intents_tx_hash_key;
ALTER TABLE agent_trade_intents ADD COLUMN IF NOT EXISTS chain_id BIGINT;
ALTER TABLE agent_trade_intents ADD COLUMN IF NOT EXISTS log_index INT;
ALTER TABLE agent_trade_intents ADD COLUMN IF NOT EXISTS block_number BIGINT;
ALTER TABLE agent_trade_intents ADD COLUMN IF NOT EXISTS block_hash TEXT;
ALTER TABLE agent_trade_intents ADD COLUMN IF NOT EXISTS transfer_from TEXT;
ALTER TABLE agent_trade_intents ADD COLUMN IF NOT EXISTS transfer_to TEXT;
ALTER TABLE agent_trade_intents ADD COLUMN IF NOT EXISTS transfer_amount_raw TEXT;
ALTER TABLE agent_trade_intents ADD COLUMN IF NOT EXISTS overpayment_amount NUMERIC(28,8) NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_agent_trade_wallet ON agent_trade_intents(agent_wallet, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_agent_trade_status ON agent_trade_intents(status, expires_at);
CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_trade_payment_log ON agent_trade_intents(chain_id, tx_hash, log_index) WHERE tx_hash IS NOT NULL AND log_index IS NOT NULL;

CREATE TABLE IF NOT EXISTS agent_bill_payments (
  id TEXT PRIMARY KEY,
  agent_wallet TEXT NOT NULL,
  rail TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'quoted',
  fiat_currency TEXT NOT NULL DEFAULT 'BRL',
  amount_brl NUMERIC(28,8) NOT NULL,
  markup_bps INT NOT NULL,
  fee_brl NUMERIC(28,8) NOT NULL,
  total_brl NUMERIC(28,8) NOT NULL,
  usdt_brl_rate NUMERIC(28,8) NOT NULL,
  usdt_amount NUMERIC(28,8) NOT NULL,
  payment_asset TEXT NOT NULL DEFAULT 'USDT',
  payment_network TEXT NOT NULL DEFAULT 'BSC',
  payment_contract TEXT NOT NULL,
  payment_address TEXT NOT NULL,
  pix_key_hash TEXT,
  pix_key_enc TEXT,
  beneficiary_name TEXT,
  description TEXT,
  external_reference TEXT,
  request_hash TEXT NOT NULL UNIQUE,
  idempotency_key TEXT UNIQUE,
  expires_at TIMESTAMPTZ NOT NULL,
  paid_at TIMESTAMPTZ,
  executed_at TIMESTAMPTZ,
  failed_at TIMESTAMPTZ,
  tx_hash TEXT,
  chain_id BIGINT,
  tx_log_index INT,
  block_number BIGINT,
  block_hash TEXT,
  transfer_from TEXT,
  transfer_to TEXT,
  transfer_amount_raw TEXT,
  overpayment_amount NUMERIC(28,8) NOT NULL DEFAULT 0,
  provider_id TEXT,
  provider_status TEXT,
  failure_code TEXT,
  failure_message TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE agent_bill_payments ADD COLUMN IF NOT EXISTS chain_id BIGINT;
ALTER TABLE agent_bill_payments ADD COLUMN IF NOT EXISTS block_number BIGINT;
ALTER TABLE agent_bill_payments ADD COLUMN IF NOT EXISTS block_hash TEXT;
ALTER TABLE agent_bill_payments ADD COLUMN IF NOT EXISTS transfer_from TEXT;
ALTER TABLE agent_bill_payments ADD COLUMN IF NOT EXISTS transfer_to TEXT;
ALTER TABLE agent_bill_payments ADD COLUMN IF NOT EXISTS transfer_amount_raw TEXT;
ALTER TABLE agent_bill_payments ADD COLUMN IF NOT EXISTS overpayment_amount NUMERIC(28,8) NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_agent_bill_payments_wallet ON agent_bill_payments(agent_wallet, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_agent_bill_payments_status ON agent_bill_payments(status, expires_at);
CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_bill_payments_payment_log ON agent_bill_payments(chain_id, tx_hash, tx_log_index) WHERE tx_hash IS NOT NULL AND tx_log_index IS NOT NULL;

CREATE TABLE IF NOT EXISTS marketplace_providers (
  id TEXT PRIMARY KEY,
  slug TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  description TEXT NOT NULL,
  website_url TEXT,
  settlement_wallet TEXT NOT NULL,
  settlement_asset TEXT NOT NULL DEFAULT 'USDT',
  settlement_network TEXT NOT NULL DEFAULT 'BSC',
  status TEXT NOT NULL DEFAULT 'pending',
  metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_marketplace_providers_settlement_wallet_evm') THEN
    ALTER TABLE marketplace_providers
      ADD CONSTRAINT chk_marketplace_providers_settlement_wallet_evm
      CHECK (settlement_wallet ~* '^0x[0-9a-f]{40}$');
  END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_mp_providers_status ON marketplace_providers(status);

CREATE TABLE IF NOT EXISTS marketplace_capabilities (
  id TEXT PRIMARY KEY,
  slug TEXT NOT NULL UNIQUE,
  display_name TEXT NOT NULL,
  description TEXT NOT NULL,
  category TEXT NOT NULL,
  routing_mode TEXT NOT NULL DEFAULT 'best_available',
  status TEXT NOT NULL DEFAULT 'active',
  operations_json JSONB NOT NULL DEFAULT '[]'::jsonb,
  metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_mp_capabilities_category_status ON marketplace_capabilities(category, status);
CREATE INDEX IF NOT EXISTS idx_mp_capabilities_routing ON marketplace_capabilities(routing_mode, status);

CREATE TABLE IF NOT EXISTS marketplace_capability_contracts (
  id TEXT PRIMARY KEY,
  capability_id TEXT NOT NULL REFERENCES marketplace_capabilities(id),
  version TEXT NOT NULL DEFAULT 'v1',
  status TEXT NOT NULL DEFAULT 'active',
  input_schema_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  output_schema_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  examples_json JSONB NOT NULL DEFAULT '[]'::jsonb,
  metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(capability_id, version)
);

CREATE INDEX IF NOT EXISTS idx_mp_capability_contracts_lookup ON marketplace_capability_contracts(capability_id, status, version);

CREATE TABLE IF NOT EXISTS marketplace_capability_providers (
  id TEXT PRIMARY KEY,
  capability_id TEXT NOT NULL REFERENCES marketplace_capabilities(id),
  provider_id TEXT REFERENCES marketplace_providers(id),
  provider_slug TEXT NOT NULL,
  provider_name TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'planned',
  routing_priority INT NOT NULL DEFAULT 100,
  metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(capability_id, provider_slug)
);

CREATE INDEX IF NOT EXISTS idx_mp_capability_providers_capability_status ON marketplace_capability_providers(capability_id, status);

CREATE TABLE IF NOT EXISTS marketplace_routes (
  id TEXT PRIMARY KEY,
  capability_id TEXT NOT NULL REFERENCES marketplace_capabilities(id),
  route_name TEXT NOT NULL,
  routing_mode TEXT NOT NULL DEFAULT 'best_available',
  status TEXT NOT NULL DEFAULT 'active',
  fallback_enabled BOOLEAN NOT NULL DEFAULT true,
  metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(capability_id, route_name)
);

CREATE INDEX IF NOT EXISTS idx_mp_routes_capability_status ON marketplace_routes(capability_id, status);

CREATE TABLE IF NOT EXISTS marketplace_provider_policies (
  id TEXT PRIMARY KEY,
  capability_id TEXT NOT NULL REFERENCES marketplace_capabilities(id),
  provider_slug TEXT NOT NULL,
  priority INT NOT NULL DEFAULT 100,
  cost_score INT NOT NULL DEFAULT 100,
  latency_ms INT NOT NULL DEFAULT 1000,
  quality_score INT NOT NULL DEFAULT 50,
  success_rate_bps INT NOT NULL DEFAULT 10000,
  region TEXT NOT NULL DEFAULT 'global',
  fallback_order INT NOT NULL DEFAULT 100,
  max_units_per_request INT,
  status TEXT NOT NULL DEFAULT 'active',
  policy_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(capability_id, provider_slug)
);

CREATE INDEX IF NOT EXISTS idx_mp_provider_policies_capability_status ON marketplace_provider_policies(capability_id, status);
CREATE INDEX IF NOT EXISTS idx_mp_provider_policies_routing ON marketplace_provider_policies(capability_id, status, priority, cost_score, latency_ms, quality_score);

ALTER TABLE marketplace_provider_policies ADD COLUMN IF NOT EXISTS cost_score INT NOT NULL DEFAULT 100;
ALTER TABLE marketplace_provider_policies ADD COLUMN IF NOT EXISTS latency_ms INT NOT NULL DEFAULT 1000;
ALTER TABLE marketplace_provider_policies ADD COLUMN IF NOT EXISTS quality_score INT NOT NULL DEFAULT 50;
ALTER TABLE marketplace_provider_policies ADD COLUMN IF NOT EXISTS success_rate_bps INT NOT NULL DEFAULT 10000;
ALTER TABLE marketplace_provider_policies ADD COLUMN IF NOT EXISTS region TEXT NOT NULL DEFAULT 'global';
ALTER TABLE marketplace_provider_policies ADD COLUMN IF NOT EXISTS fallback_order INT NOT NULL DEFAULT 100;

CREATE TABLE IF NOT EXISTS marketplace_products (
  id TEXT PRIMARY KEY,
  provider_id TEXT NOT NULL REFERENCES marketplace_providers(id),
  slug TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  description TEXT NOT NULL,
  category TEXT NOT NULL,
  delivery_type TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'draft',
  capability TEXT NOT NULL,
  documentation_url TEXT,
  endpoint_base_url TEXT,
  metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE marketplace_products ADD COLUMN IF NOT EXISTS capability_id TEXT REFERENCES marketplace_capabilities(id);
CREATE INDEX IF NOT EXISTS idx_mp_products_lookup ON marketplace_products(category, status);
CREATE INDEX IF NOT EXISTS idx_mp_products_provider_status ON marketplace_products(provider_id, status);
CREATE INDEX IF NOT EXISTS idx_mp_products_capability_status ON marketplace_products(capability_id, status);

CREATE TABLE IF NOT EXISTS marketplace_plans (
  id TEXT PRIMARY KEY,
  product_id TEXT NOT NULL REFERENCES marketplace_products(id),
  slug TEXT NOT NULL,
  name TEXT NOT NULL,
  price_amount NUMERIC(28,6) NOT NULL,
  payment_asset TEXT NOT NULL,
  network TEXT NOT NULL DEFAULT 'BSC',
  take_rate_bps INT NOT NULL DEFAULT 2000,
  quota INT NOT NULL,
  validity_seconds INT NOT NULL,
  status TEXT NOT NULL DEFAULT 'active',
  metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(product_id, slug)
);

CREATE INDEX IF NOT EXISTS idx_mp_plans_product_status ON marketplace_plans(product_id, status);
CREATE INDEX IF NOT EXISTS idx_mp_plans_asset_network_status ON marketplace_plans(payment_asset, network, status);

CREATE TABLE IF NOT EXISTS marketplace_purchases (
  id TEXT PRIMARY KEY,
  provider_id TEXT NOT NULL REFERENCES marketplace_providers(id),
  product_id TEXT NOT NULL REFERENCES marketplace_products(id),
  plan_id TEXT NOT NULL REFERENCES marketplace_plans(id),
  agent_wallet TEXT NOT NULL,
  payer_wallet TEXT NOT NULL,
  payment_address TEXT NOT NULL,
  payment_asset TEXT NOT NULL,
  payment_contract TEXT NOT NULL,
  network TEXT NOT NULL DEFAULT 'BSC',
  chain_id BIGINT NOT NULL DEFAULT 56,
  gross_amount NUMERIC(28,6) NOT NULL,
  chainfx_amount NUMERIC(28,6) NOT NULL,
  provider_amount NUMERIC(28,6) NOT NULL,
  take_rate_bps INT NOT NULL,
  request_hash TEXT NOT NULL UNIQUE,
  nonce TEXT NOT NULL,
  idempotency_key TEXT UNIQUE,
  expires_at TIMESTAMPTZ NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending_payment',
  tx_hash TEXT,
  tx_log_index INT,
  tx_block_number BIGINT,
  tx_block_hash TEXT,
  transfer_from TEXT,
  transfer_to TEXT,
  transfer_amount_raw TEXT,
  overpayment_amount NUMERIC(28,6) NOT NULL DEFAULT 0,
  paid_at TIMESTAMPTZ,
  granted_at TIMESTAMPTZ,
  failed_at TIMESTAMPTZ,
  failure_code TEXT,
  failure_message TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_marketplace_purchase_nonce ON marketplace_purchases(nonce);
CREATE UNIQUE INDEX IF NOT EXISTS idx_marketplace_purchase_payment_log ON marketplace_purchases(chain_id, tx_hash, tx_log_index) WHERE tx_hash IS NOT NULL AND tx_log_index IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_marketplace_purchases_wallet ON marketplace_purchases(agent_wallet, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_marketplace_purchases_status ON marketplace_purchases(status, expires_at);

CREATE TABLE IF NOT EXISTS marketplace_provider_settlements (
  id TEXT PRIMARY KEY,
  provider_id TEXT NOT NULL REFERENCES marketplace_providers(id),
  purchase_id TEXT NOT NULL UNIQUE REFERENCES marketplace_purchases(id),
  asset TEXT NOT NULL,
  network TEXT NOT NULL,
  gross_amount NUMERIC(28,6) NOT NULL,
  chainfx_amount NUMERIC(28,6) NOT NULL,
  provider_amount NUMERIC(28,6) NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending',
  settlement_wallet TEXT NOT NULL,
  tx_hash TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  settled_at TIMESTAMPTZ,
  failed_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS marketplace_agent_identities (
  agent_id TEXT PRIMARY KEY,
  wallet TEXT,
  name TEXT,
  api_key_hash TEXT NOT NULL UNIQUE,
  capabilities_json JSONB NOT NULL DEFAULT '[]'::jsonb,
  status TEXT NOT NULL DEFAULT 'active',
  metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_mp_agent_identities_wallet ON marketplace_agent_identities(wallet) WHERE wallet IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_mp_agent_identities_status ON marketplace_agent_identities(status);

CREATE TABLE IF NOT EXISTS marketplace_agent_policies (
  agent_id TEXT PRIMARY KEY REFERENCES marketplace_agent_identities(agent_id) ON DELETE CASCADE,
  environment TEXT NOT NULL DEFAULT 'sandbox' CHECK (environment IN ('sandbox','production')),
  agent_type TEXT NOT NULL DEFAULT 'autonomous',
  wallet_mode TEXT NOT NULL DEFAULT 'existing',
  daily_limit_usdt NUMERIC(28,8) NOT NULL DEFAULT 500,
  monthly_limit_usdt NUMERIC(28,8) NOT NULL DEFAULT 5000,
  max_transaction_usdt NUMERIC(28,8) NOT NULL DEFAULT 100,
  allowed_assets_json JSONB NOT NULL DEFAULT '["USDT","USDC"]'::jsonb,
  allowed_capabilities_json JSONB NOT NULL DEFAULT '[]'::jsonb,
  allowed_providers_json JSONB NOT NULL DEFAULT '[]'::jsonb,
  permissions_json JSONB NOT NULL DEFAULT '["capabilities:read","capabilities:purchase","capabilities:execute","trades:create","settlements:read","webhooks:write"]'::jsonb,
  require_real_provider BOOLEAN NOT NULL DEFAULT false,
  mock_fallback BOOLEAN NOT NULL DEFAULT true,
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','paused','disabled')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_mp_agent_policies_environment ON marketplace_agent_policies(environment, status);

CREATE TABLE IF NOT EXISTS developer_project_agents (
  project_id TEXT NOT NULL REFERENCES developer_projects(id) ON DELETE CASCADE,
  agent_id TEXT NOT NULL REFERENCES marketplace_agent_identities(agent_id) ON DELETE CASCADE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (project_id, agent_id)
);

CREATE TABLE IF NOT EXISTS marketplace_execution_events (
  id TEXT PRIMARY KEY,
  capability_id TEXT NOT NULL REFERENCES marketplace_capabilities(id),
  grant_id UUID NOT NULL REFERENCES api_access_grants(id),
  product_id TEXT NOT NULL,
  provider_slug TEXT NOT NULL,
  provider_name TEXT NOT NULL,
  route_name TEXT NOT NULL,
  routing_mode TEXT NOT NULL,
  operation TEXT NOT NULL,
  request_id TEXT NOT NULL,
  idempotency_key TEXT NOT NULL UNIQUE,
  units_consumed INT NOT NULL,
  quota_remaining INT NOT NULL,
  status TEXT NOT NULL,
  input_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  output_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  latency_ms INT NOT NULL DEFAULT 0,
  error_code TEXT,
  error_message TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_mp_execution_events_capability ON marketplace_execution_events(capability_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_mp_execution_events_grant ON marketplace_execution_events(grant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_mp_execution_events_provider ON marketplace_execution_events(provider_slug, created_at DESC);

ALTER TABLE marketplace_execution_events ADD COLUMN IF NOT EXISTS latency_ms INT NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS agent_capability_credit_accounts (
  id TEXT PRIMARY KEY,
  agent_wallet TEXT NOT NULL,
  capability_id TEXT NOT NULL REFERENCES marketplace_capabilities(id),
  asset TEXT NOT NULL DEFAULT 'USDT',
  network TEXT NOT NULL DEFAULT 'BSC',
  credit_limit_micro BIGINT NOT NULL DEFAULT 5000000,
  credit_used_micro BIGINT NOT NULL DEFAULT 0,
  min_top_up_micro BIGINT NOT NULL DEFAULT 20000000,
  expires_at TIMESTAMPTZ NOT NULL DEFAULT (now() + interval '7 days'),
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','payment_required','disabled','expired')),
  last_payment_required_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(agent_wallet, capability_id, asset, network),
  CHECK (credit_limit_micro >= 0),
  CHECK (credit_used_micro >= 0),
  CHECK (min_top_up_micro >= 0)
);

CREATE INDEX IF NOT EXISTS idx_agent_cap_credit_wallet ON agent_capability_credit_accounts(agent_wallet, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_agent_cap_credit_capability ON agent_capability_credit_accounts(capability_id, status);

CREATE TABLE IF NOT EXISTS marketplace_memory_entries (
  id TEXT PRIMARY KEY,
  namespace TEXT NOT NULL,
  memory_key TEXT NOT NULL,
  content TEXT NOT NULL,
  metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  status TEXT NOT NULL DEFAULT 'active',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(namespace, memory_key)
);

CREATE INDEX IF NOT EXISTS idx_mp_memory_entries_namespace_status ON marketplace_memory_entries(namespace, status, updated_at DESC);

ALTER TABLE api_access_grants DROP CONSTRAINT IF EXISTS api_access_grants_payment_id_fkey;
ALTER TABLE api_access_grants DROP CONSTRAINT IF EXISTS api_access_grants_product_id_fkey;
ALTER TABLE api_access_grants ALTER COLUMN payment_id DROP NOT NULL;
ALTER TABLE api_access_grants ADD COLUMN IF NOT EXISTS purchase_id TEXT UNIQUE;
ALTER TABLE api_access_grants ADD COLUMN IF NOT EXISTS plan_id TEXT;
ALTER TABLE api_access_grants ADD COLUMN IF NOT EXISTS quota_used INT NOT NULL DEFAULT 0;
ALTER TABLE api_access_grants ADD COLUMN IF NOT EXISTS valid_from TIMESTAMPTZ NOT NULL DEFAULT now();
ALTER TABLE api_usage_events DROP CONSTRAINT IF EXISTS api_usage_events_product_id_fkey;

INSERT INTO marketplace_providers (id, slug, name, description, website_url, settlement_wallet, settlement_asset, settlement_network, status)
VALUES
  ('provider_chainfx_demo', 'chainfx-demo', 'ChainFX Demo Provider', 'Demo provider for premium agent marketplace capabilities.', 'https://www.chainfx.store', '0x000000000000000000000000000000000000dEaD', 'USDT', 'BSC', 'active')
ON CONFLICT (id) DO NOTHING;

INSERT INTO marketplace_capabilities (id, slug, display_name, description, category, routing_mode, status, operations_json)
VALUES
  ('semantic_memory', 'semantic-memory', 'Memory', 'Persistent and semantic memory primitives for AI agents.', 'ai', 'best_available', 'active', '["save_memory","get_memory","semantic_search","knowledge_lookup"]'::jsonb),
  ('llm_chat', 'llm-chat', 'Chat LLM', 'Provider-routed text generation, chat, summarization and classification.', 'ai', 'best_available', 'active', '["generate_text","chat","summarize","classify"]'::jsonb),
  ('document_ocr', 'document-ocr', 'Document OCR', 'Extract and structure text from documents and invoices.', 'ai', 'best_available', 'active', '["extract_text","parse_invoice","parse_document"]'::jsonb),
  ('payments_fx', 'payments-fx', 'Payments / FX', 'Agent payment, FX quote, wallet and settlement capabilities.', 'finance', 'best_available', 'active', '["create_payment","quote_fx","settle_provider","wallet_balance"]'::jsonb),
  ('capability_discovery', 'capability-discovery', 'Discovery', 'Capability search, route estimation and provider choice for agents.', 'data', 'best_available', 'active', '["search_capability","list_providers","estimate_cost","choose_route"]'::jsonb),
  ('aml_screening', 'aml-screening', 'AML Screening', 'Compliance screening capability for wallet and payment workflows.', 'security', 'best_available', 'active', '["screen_wallet","screen_counterparty","check_sanctions"]'::jsonb)
ON CONFLICT (id) DO UPDATE SET
  display_name = EXCLUDED.display_name,
  description = EXCLUDED.description,
  category = EXCLUDED.category,
  routing_mode = EXCLUDED.routing_mode,
  status = EXCLUDED.status,
  operations_json = EXCLUDED.operations_json,
  updated_at = now();

INSERT INTO marketplace_capability_contracts (id, capability_id, version, status, input_schema_json, output_schema_json, examples_json, metadata_json)
VALUES
  ('mcc_semantic_memory_v1', 'semantic_memory', 'v1', 'active',
   '{"type":"object","properties":{"operation":{"type":"string","enum":["save_memory","get_memory","semantic_search","knowledge_lookup"]},"namespace":{"type":"string"},"key":{"type":"string"},"content":{"type":"string"},"query":{"type":"string"}},"additionalProperties":true}'::jsonb,
   '{"type":"object","properties":{"mode":{"type":"string"},"provider":{"type":"string"},"operation":{"type":"string"},"memoryId":{"type":"string"},"saved":{"type":"boolean"},"found":{"type":"boolean"},"content":{"type":"string"},"results":{"type":"array"}},"additionalProperties":true}'::jsonb,
   '[{"operation":"save_memory","input":{"namespace":"agent","key":"deal-1","content":"Client prefers USDT settlement."}},{"operation":"semantic_search","input":{"namespace":"agent","query":"settlement"}}]'::jsonb,
   '{"positioning":"native ChainFX memory for agents"}'::jsonb),
  ('mcc_llm_chat_v1', 'llm_chat', 'v1', 'active',
   '{"type":"object","properties":{"operation":{"type":"string","enum":["generate_text","chat","summarize","classify"]},"prompt":{"type":"string"},"messages":{"type":"array"},"text":{"type":"string"}},"additionalProperties":true}'::jsonb,
   '{"type":"object","properties":{"mode":{"type":"string"},"provider":{"type":"string"},"operation":{"type":"string"},"text":{"type":"string"}},"required":["text"],"additionalProperties":true}'::jsonb,
   '[{"operation":"summarize","input":{"text":"Long document text"}}]'::jsonb,
   '{"providerClass":"openai_compatible"}'::jsonb),
  ('mcc_document_ocr_v1', 'document_ocr', 'v1', 'active',
   '{"type":"object","properties":{"operation":{"type":"string","enum":["extract_text","parse_invoice","parse_document"]},"fileUrl":{"type":"string"},"fileBase64":{"type":"string"},"mimeType":{"type":"string"}},"additionalProperties":true}'::jsonb,
   '{"type":"object","properties":{"mode":{"type":"string"},"provider":{"type":"string"},"operation":{"type":"string"},"text":{"type":"string"},"pages":{"type":"integer"},"fields":{"type":"object"}},"additionalProperties":true}'::jsonb,
   '[{"operation":"extract_text","input":{"fileUrl":"https://example.com/invoice.pdf"}}]'::jsonb,
   '{"providerClass":"http_adapter"}'::jsonb),
  ('mcc_payments_fx_v1', 'payments_fx', 'v1', 'active',
   '{"type":"object","properties":{"operation":{"type":"string","enum":["create_payment","quote_fx","settle_provider","wallet_balance"]},"asset":{"type":"string"},"amount":{"type":"string"},"wallet":{"type":"string"}},"additionalProperties":true}'::jsonb,
   '{"type":"object","properties":{"mode":{"type":"string"},"provider":{"type":"string"},"message":{"type":"string"},"status":{"type":"string"}},"additionalProperties":true}'::jsonb,
   '[{"operation":"quote_fx","input":{"asset":"USDT","amount":"100.000000"}}]'::jsonb,
   '{"realSettlement":"agent_rail"}'::jsonb),
  ('mcc_capability_discovery_v1', 'capability_discovery', 'v1', 'active',
   '{"type":"object","properties":{"operation":{"type":"string","enum":["search_capability","list_providers","estimate_cost","choose_route"]},"query":{"type":"string"},"capability":{"type":"string"}},"additionalProperties":true}'::jsonb,
   '{"type":"object","properties":{"mode":{"type":"string"},"results":{"type":"array"},"providers":{"type":"array"},"selected":{"type":"object"}},"additionalProperties":true}'::jsonb,
   '[{"operation":"search_capability","input":{"query":"market data"}}]'::jsonb,
   '{"discovery":"capability_network"}'::jsonb),
  ('mcc_aml_screening_v1', 'aml_screening', 'v1', 'active',
   '{"type":"object","properties":{"operation":{"type":"string","enum":["screen_wallet","screen_counterparty","check_sanctions"]},"wallet":{"type":"string"},"counterparty":{"type":"string"}},"additionalProperties":true}'::jsonb,
   '{"type":"object","properties":{"mode":{"type":"string"},"risk":{"type":"string"},"matches":{"type":"array"},"status":{"type":"string"}},"additionalProperties":true}'::jsonb,
   '[{"operation":"screen_wallet","input":{"wallet":"0x0000000000000000000000000000000000000000"}}]'::jsonb,
   '{"compliance":"demo"}'::jsonb)
ON CONFLICT (capability_id, version) DO UPDATE SET
  status = EXCLUDED.status,
  input_schema_json = EXCLUDED.input_schema_json,
  output_schema_json = EXCLUDED.output_schema_json,
  examples_json = EXCLUDED.examples_json,
  metadata_json = EXCLUDED.metadata_json,
  updated_at = now();

INSERT INTO marketplace_capability_providers (id, capability_id, provider_id, provider_slug, provider_name, status, routing_priority)
VALUES
  ('mcp_llm_openai', 'llm_chat', NULL, 'openai', 'OpenAI', 'active', 10),
  ('mcp_llm_anthropic', 'llm_chat', NULL, 'anthropic', 'Anthropic', 'planned', 20),
  ('mcp_llm_gemini', 'llm_chat', NULL, 'gemini', 'Gemini', 'planned', 30),
  ('mcp_ocr_http', 'document_ocr', 'provider_chainfx_demo', 'chainfx-ocr-http', 'ChainFX OCR HTTP Adapter', 'active', 5),
  ('mcp_ocr_google', 'document_ocr', NULL, 'google-vision', 'Google Vision', 'planned', 10),
  ('mcp_ocr_azure', 'document_ocr', NULL, 'azure-vision', 'Azure Vision', 'planned', 20),
  ('mcp_ocr_aws', 'document_ocr', NULL, 'aws-textract', 'AWS Textract', 'planned', 30),
  ('mcp_memory_chainfx', 'semantic_memory', 'provider_chainfx_demo', 'chainfx-memory', 'ChainFX Memory', 'active', 10),
  ('mcp_payments_chainfx', 'payments_fx', 'provider_chainfx_demo', 'chainfx-rail', 'ChainFX Agent Rail', 'active', 10),
  ('mcp_discovery_chainfx', 'capability_discovery', 'provider_chainfx_demo', 'chainfx-discovery', 'ChainFX Discovery', 'active', 10),
  ('mcp_aml_chainfx', 'aml_screening', 'provider_chainfx_demo', 'chainfx-aml-demo', 'ChainFX AML Demo', 'active', 10)
ON CONFLICT (capability_id, provider_slug) DO UPDATE SET
  provider_name = EXCLUDED.provider_name,
  status = EXCLUDED.status,
  routing_priority = EXCLUDED.routing_priority,
  updated_at = now();

INSERT INTO marketplace_routes (id, capability_id, route_name, routing_mode, status, fallback_enabled)
VALUES
  ('mpr_semantic_memory_default', 'semantic_memory', 'default', 'best_available', 'active', true),
  ('mpr_llm_chat_default', 'llm_chat', 'default', 'best_available', 'active', true),
  ('mpr_document_ocr_default', 'document_ocr', 'default', 'best_available', 'active', true),
  ('mpr_payments_fx_default', 'payments_fx', 'default', 'best_available', 'active', true),
  ('mpr_capability_discovery_default', 'capability_discovery', 'default', 'best_available', 'active', true),
  ('mpr_aml_screening_default', 'aml_screening', 'default', 'best_available', 'active', true)
ON CONFLICT (capability_id, route_name) DO UPDATE SET
  routing_mode = EXCLUDED.routing_mode,
  status = EXCLUDED.status,
  fallback_enabled = EXCLUDED.fallback_enabled,
  updated_at = now();

INSERT INTO marketplace_provider_policies (id, capability_id, provider_slug, priority, cost_score, latency_ms, quality_score, success_rate_bps, region, fallback_order, status, policy_json)
VALUES
  ('mpp_llm_openai', 'llm_chat', 'openai', 10, 35, 650, 92, 9900, 'global', 10, 'active', '{"execution":"openai_compatible","env":"OPENAI_API_KEY","fallback":"mock"}'::jsonb),
  ('mpp_llm_anthropic', 'llm_chat', 'anthropic', 20, 45, 700, 94, 9800, 'global', 20, 'planned', '{"execution":"mock"}'::jsonb),
  ('mpp_llm_gemini', 'llm_chat', 'gemini', 30, 25, 800, 88, 9750, 'global', 30, 'planned', '{"execution":"mock"}'::jsonb),
  ('mpp_ocr_http', 'document_ocr', 'chainfx-ocr-http', 5, 20, 500, 80, 9900, 'global', 5, 'active', '{"execution":"http_adapter","env":"CAPABILITY_OCR_URL","fallback":"mock"}'::jsonb),
  ('mpp_ocr_google', 'document_ocr', 'google-vision', 10, 40, 700, 90, 9850, 'global', 10, 'planned', '{"execution":"mock"}'::jsonb),
  ('mpp_ocr_azure', 'document_ocr', 'azure-vision', 20, 42, 750, 89, 9825, 'global', 20, 'planned', '{"execution":"mock"}'::jsonb),
  ('mpp_ocr_aws', 'document_ocr', 'aws-textract', 30, 38, 800, 91, 9800, 'global', 30, 'planned', '{"execution":"mock"}'::jsonb),
  ('mpp_memory_chainfx', 'semantic_memory', 'chainfx-memory', 10, 5, 40, 75, 9990, 'global', 10, 'active', '{"execution":"native_postgres"}'::jsonb),
  ('mpp_payments_chainfx', 'payments_fx', 'chainfx-rail', 10, 10, 120, 90, 9950, 'global', 10, 'active', '{"execution":"mock","realSettlement":"agent_rail"}'::jsonb),
  ('mpp_discovery_chainfx', 'capability_discovery', 'chainfx-discovery', 10, 5, 50, 85, 9990, 'global', 10, 'active', '{"execution":"mock"}'::jsonb),
  ('mpp_aml_chainfx', 'aml_screening', 'chainfx-aml-demo', 10, 30, 600, 78, 9900, 'global', 10, 'active', '{"execution":"mock"}'::jsonb)
ON CONFLICT (capability_id, provider_slug) DO UPDATE SET
  priority = EXCLUDED.priority,
  cost_score = EXCLUDED.cost_score,
  latency_ms = EXCLUDED.latency_ms,
  quality_score = EXCLUDED.quality_score,
  success_rate_bps = EXCLUDED.success_rate_bps,
  region = EXCLUDED.region,
  fallback_order = EXCLUDED.fallback_order,
  status = EXCLUDED.status,
  policy_json = EXCLUDED.policy_json,
  updated_at = now();

INSERT INTO marketplace_products (id, provider_id, slug, name, description, category, delivery_type, status, capability, documentation_url, endpoint_base_url)
VALUES
  ('prod_fx_enterprise', 'provider_chainfx_demo', 'fx-enterprise-api', 'FX Enterprise API', 'Enterprise FX rates and settlement data for agents.', 'finance', 'api_access', 'active', 'fx_enterprise', 'https://www.chainfx.store/developers', 'https://www.chainfx.store'),
  ('prod_ocr_enterprise', 'provider_chainfx_demo', 'ocr-enterprise', 'OCR Enterprise', 'Document OCR capability for autonomous workflows.', 'ai', 'api_access', 'active', 'document_ocr', 'https://www.chainfx.store/developers', 'https://www.chainfx.store'),
  ('prod_aml_screening', 'provider_chainfx_demo', 'aml-screening', 'AML Screening', 'AML screening capability for payment and wallet workflows.', 'security', 'api_access', 'active', 'aml_screening', 'https://www.chainfx.store/developers', 'https://www.chainfx.store'),
  ('prod_gpt_business', 'provider_chainfx_demo', 'gpt-business-credits', 'GPT Business Credits', 'Business-grade AI credits for agent workloads.', 'ai', 'usage_credits', 'active', 'gpt_business_credits', 'https://www.chainfx.store/developers', 'https://www.chainfx.store')
ON CONFLICT (id) DO NOTHING;

UPDATE marketplace_products SET capability_id = 'payments_fx', capability = 'payments_fx' WHERE id = 'prod_fx_enterprise';
UPDATE marketplace_products SET capability_id = 'document_ocr', capability = 'document_ocr' WHERE id = 'prod_ocr_enterprise';
UPDATE marketplace_products SET capability_id = 'aml_screening', capability = 'aml_screening' WHERE id = 'prod_aml_screening';
UPDATE marketplace_products SET capability_id = 'llm_chat', capability = 'llm_chat' WHERE id = 'prod_gpt_business';

INSERT INTO marketplace_plans (id, product_id, slug, name, price_amount, payment_asset, network, take_rate_bps, quota, validity_seconds, status)
VALUES
  ('plan_fx_400', 'prod_fx_enterprise', 'enterprise-400', 'Enterprise Pack', 400.000000, 'USDT', 'BSC', 2000, 100000, 2592000, 'active'),
  ('plan_ocr_80', 'prod_ocr_enterprise', 'enterprise-80', 'Enterprise Pack', 80.000000, 'USDT', 'BSC', 2000, 1000, 2592000, 'active'),
  ('plan_aml_600', 'prod_aml_screening', 'enterprise-600', 'Enterprise Pack', 600.000000, 'USDT', 'BSC', 2000, 10000, 2592000, 'active'),
  ('plan_gpt_300', 'prod_gpt_business', 'business-300', 'Business Credits', 300.000000, 'USDT', 'BSC', 2000, 100000, 2592000, 'active'),
  ('plan_fx_400_polygon', 'prod_fx_enterprise', 'enterprise-400-polygon', 'Enterprise Pack Polygon', 400.000000, 'USDT', 'POLYGON', 2000, 100000, 2592000, 'active'),
  ('plan_ocr_80_polygon', 'prod_ocr_enterprise', 'enterprise-80-polygon', 'Enterprise Pack Polygon', 80.000000, 'USDT', 'POLYGON', 2000, 1000, 2592000, 'active'),
  ('plan_aml_600_polygon', 'prod_aml_screening', 'enterprise-600-polygon', 'Enterprise Pack Polygon', 600.000000, 'USDT', 'POLYGON', 2000, 10000, 2592000, 'active'),
  ('plan_gpt_300_polygon', 'prod_gpt_business', 'business-300-polygon', 'Business Credits Polygon', 300.000000, 'USDT', 'POLYGON', 2000, 100000, 2592000, 'active')
ON CONFLICT (id) DO NOTHING;

CREATE TABLE IF NOT EXISTS worker_dlq (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  event_type TEXT NOT NULL,
  order_id TEXT,
  attempts INT NOT NULL DEFAULT 0,
  reason TEXT NOT NULL,
  payload JSONB NOT NULL DEFAULT '{}'::jsonb,
  status TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','resolved','ignored')),
  failed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_worker_dlq_status_failed_at ON worker_dlq(status, failed_at DESC);
CREATE INDEX IF NOT EXISTS idx_worker_dlq_order_id ON worker_dlq(order_id) WHERE order_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS gas_relay_requests (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_address TEXT NOT NULL,
  sig_r TEXT NOT NULL,
  sig_s TEXT NOT NULL,
  sig_hash TEXT NOT NULL UNIQUE,
  tx_to TEXT NOT NULL,
  tx_data TEXT NOT NULL DEFAULT '',
  fee_usdt NUMERIC(20,8) NOT NULL DEFAULT 0,
  gas_price_gwei NUMERIC(20,8),
  gas_limit BIGINT,
  status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','processing','sent','failed','dlq')),
  tx_hash TEXT,
  attempts INT NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  next_retry_at TIMESTAMPTZ,
  dlq_at TIMESTAMPTZ,
  last_error TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_gas_relay_requests_status ON gas_relay_requests(status);
CREATE INDEX IF NOT EXISTS idx_gas_relay_requests_user_address ON gas_relay_requests(LOWER(user_address), created_at DESC);
CREATE INDEX IF NOT EXISTS idx_gas_relay_requests_created_at ON gas_relay_requests(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_gas_relay_requests_retry_eligible ON gas_relay_requests(status, next_retry_at, attempts, created_at) WHERE status IN ('pending','failed');
CREATE INDEX IF NOT EXISTS idx_gas_relay_requests_dlq ON gas_relay_requests(dlq_at DESC) WHERE status = 'dlq';

CREATE TABLE IF NOT EXISTS auto_sweeper_runs (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  network TEXT NOT NULL DEFAULT 'BSC',
  hot_wallet TEXT NOT NULL,
  cold_wallet TEXT NOT NULL,
  balance_usdt NUMERIC(20,8) NOT NULL DEFAULT 0,
  swept_usdt NUMERIC(20,8) NOT NULL DEFAULT 0,
  tx_hash TEXT,
  status TEXT NOT NULL DEFAULT 'ok' CHECK (status IN ('ok','skipped','error','broadcast','broadcast_unknown','confirmed','manual_review','signed')),
  operation_id TEXT NOT NULL DEFAULT '',
  chain_id BIGINT NOT NULL DEFAULT 0,
  token_contract TEXT NOT NULL DEFAULT '',
  amount_raw TEXT NOT NULL DEFAULT '',
  signer_status TEXT NOT NULL DEFAULT '',
  nonce BIGINT NOT NULL DEFAULT 0,
  error_msg TEXT,
  ran_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE auto_sweeper_runs ADD COLUMN IF NOT EXISTS operation_id TEXT NOT NULL DEFAULT '';
ALTER TABLE auto_sweeper_runs ADD COLUMN IF NOT EXISTS chain_id BIGINT NOT NULL DEFAULT 0;
ALTER TABLE auto_sweeper_runs ADD COLUMN IF NOT EXISTS token_contract TEXT NOT NULL DEFAULT '';
ALTER TABLE auto_sweeper_runs ADD COLUMN IF NOT EXISTS amount_raw TEXT NOT NULL DEFAULT '';
ALTER TABLE auto_sweeper_runs ADD COLUMN IF NOT EXISTS signer_status TEXT NOT NULL DEFAULT '';
ALTER TABLE auto_sweeper_runs ADD COLUMN IF NOT EXISTS nonce BIGINT NOT NULL DEFAULT 0;
DO $$
DECLARE c record;
BEGIN
  FOR c IN
    SELECT conname
    FROM pg_constraint
    WHERE conrelid = 'auto_sweeper_runs'::regclass
      AND contype = 'c'
      AND pg_get_constraintdef(oid) ILIKE '%status%'
  LOOP
    EXECUTE format('ALTER TABLE auto_sweeper_runs DROP CONSTRAINT IF EXISTS %I', c.conname);
  END LOOP;
  ALTER TABLE auto_sweeper_runs
    ADD CONSTRAINT auto_sweeper_runs_status_check
    CHECK (status IN ('ok','skipped','error','broadcast','broadcast_unknown','confirmed','manual_review','signed'));
END $$;
CREATE INDEX IF NOT EXISTS idx_auto_sweeper_runs_ran_at ON auto_sweeper_runs(ran_at DESC);
CREATE INDEX IF NOT EXISTS idx_auto_sweeper_runs_status_ran_at ON auto_sweeper_runs(status, ran_at DESC);
CREATE INDEX IF NOT EXISTS idx_auto_sweeper_runs_hot_wallet ON auto_sweeper_runs(LOWER(hot_wallet), ran_at DESC);
CREATE INDEX IF NOT EXISTS idx_auto_sweeper_runs_tx_hash ON auto_sweeper_runs(tx_hash) WHERE tx_hash IS NOT NULL;

CREATE TABLE IF NOT EXISTS paymaster_sig_locks (
  sig_hash TEXT PRIMARY KEY,
  acquired_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_paymaster_sig_locks_expires_at ON paymaster_sig_locks(expires_at);

CREATE TABLE IF NOT EXISTS nfc_tokens (
  token_id TEXT PRIMARY KEY,
  token_hash TEXT NOT NULL UNIQUE,
  wallet_address TEXT NOT NULL,
  device_id TEXT,
  network TEXT NOT NULL DEFAULT 'BSC',
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','revoked','expired')),
  expires_at TIMESTAMPTZ NOT NULL,
  last_used_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_nfc_tokens_wallet ON nfc_tokens(LOWER(wallet_address), created_at DESC);
CREATE INDEX IF NOT EXISTS idx_nfc_tokens_expires ON nfc_tokens(expires_at);

CREATE TABLE IF NOT EXISTS nfc_wallet_balances (
  wallet_address TEXT NOT NULL,
  network TEXT NOT NULL DEFAULT 'BSC',
  asset TEXT NOT NULL DEFAULT 'USDT',
  available_usdt_micro BIGINT NOT NULL DEFAULT 0 CHECK (available_usdt_micro >= 0),
  locked_usdt_micro BIGINT NOT NULL DEFAULT 0 CHECK (locked_usdt_micro >= 0),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (wallet_address, network, asset)
);

CREATE TABLE IF NOT EXISTS nfc_merchants (
  id TEXT PRIMARY KEY,
  display_name TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
  settlement_pix_key TEXT,
  settlement_document TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS nfc_terminals (
  id TEXT NOT NULL,
  merchant_id TEXT NOT NULL REFERENCES nfc_merchants(id),
  api_key_hash TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
  max_amount_brl_minor BIGINT NOT NULL DEFAULT 0,
  daily_limit_brl_minor BIGINT NOT NULL DEFAULT 0,
  risk_policy_version TEXT NOT NULL DEFAULT 'default',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (merchant_id, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_nfc_terminal_api_key_hash ON nfc_terminals(api_key_hash);
CREATE INDEX IF NOT EXISTS idx_nfc_terminals_status ON nfc_terminals(status, merchant_id);

CREATE TABLE IF NOT EXISTS nfc_authorizations (
  id TEXT PRIMARY KEY,
  idempotency_key TEXT NOT NULL,
  token_id TEXT NOT NULL,
  token_hash TEXT NOT NULL,
  wallet_address TEXT NOT NULL,
  network TEXT NOT NULL DEFAULT 'BSC',
  merchant_id TEXT NOT NULL,
  terminal_id TEXT NOT NULL,
  external_ref TEXT,
  amount_brl_minor BIGINT NOT NULL CHECK (amount_brl_minor > 0),
  fee_brl_minor BIGINT NOT NULL DEFAULT 0 CHECK (fee_brl_minor >= 0),
  total_brl_minor BIGINT NOT NULL DEFAULT 0 CHECK (total_brl_minor >= 0),
  fee_bps INT NOT NULL DEFAULT 0 CHECK (fee_bps >= 0),
  usdt_rate NUMERIC(20,8) NOT NULL,
  required_usdt_micro BIGINT NOT NULL CHECK (required_usdt_micro > 0),
  status TEXT NOT NULL CHECK (status IN ('approved','declined','requires_funding','reversed','captured','expired')),
  response_code TEXT NOT NULL,
  reason TEXT,
  hold_expires_at TIMESTAMPTZ,
  reversed_at TIMESTAMPTZ,
  captured_at TIMESTAMPTZ,
  expired_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE nfc_authorizations DROP CONSTRAINT IF EXISTS nfc_authorizations_status_check;
ALTER TABLE nfc_authorizations ADD CONSTRAINT nfc_authorizations_status_check
  CHECK (status IN ('approved','declined','requires_funding','reversed','captured','expired'));
ALTER TABLE nfc_authorizations ADD COLUMN IF NOT EXISTS expired_at TIMESTAMPTZ;
ALTER TABLE nfc_authorizations ADD COLUMN IF NOT EXISTS fee_brl_minor BIGINT NOT NULL DEFAULT 0;
ALTER TABLE nfc_authorizations ADD COLUMN IF NOT EXISTS total_brl_minor BIGINT NOT NULL DEFAULT 0;
ALTER TABLE nfc_authorizations ADD COLUMN IF NOT EXISTS fee_bps INT NOT NULL DEFAULT 0;
UPDATE nfc_authorizations SET total_brl_minor = amount_brl_minor WHERE total_brl_minor = 0;
ALTER TABLE nfc_authorizations DROP CONSTRAINT IF EXISTS nfc_authorizations_idempotency_key_key;
CREATE UNIQUE INDEX IF NOT EXISTS uq_nfc_auth_terminal_idempotency ON nfc_authorizations(terminal_id, idempotency_key);
CREATE INDEX IF NOT EXISTS idx_nfc_auth_wallet_created ON nfc_authorizations(LOWER(wallet_address), created_at DESC);
CREATE INDEX IF NOT EXISTS idx_nfc_auth_merchant_created ON nfc_authorizations(merchant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_nfc_auth_status_hold ON nfc_authorizations(status, hold_expires_at) WHERE status = 'approved';

CREATE TABLE IF NOT EXISTS merchant_settlements (
  id TEXT PRIMARY KEY,
  merchant_id TEXT NOT NULL REFERENCES nfc_merchants(id),
  terminal_id TEXT NOT NULL,
  authorization_id TEXT NOT NULL UNIQUE REFERENCES nfc_authorizations(id),
  capture_id TEXT NOT NULL,
  amount_brl_minor BIGINT NOT NULL CHECK (amount_brl_minor > 0),
  fee_brl_minor BIGINT NOT NULL DEFAULT 0 CHECK (fee_brl_minor >= 0),
  provider TEXT NOT NULL DEFAULT 'efi',
  rail TEXT NOT NULL DEFAULT 'pix_send',
  status TEXT NOT NULL CHECK (status IN ('MANUAL_REQUIRED','PENDING','PROCESSING','SUBMITTED','SUBMISSION_UNKNOWN','CONFIRMED','REJECTED','RETRYABLE','MANUAL_REVIEW','CANCELED')),
  provider_reference TEXT,
  provider_e2e_id TEXT,
  provider_id_envio TEXT,
  provider_status TEXT,
  txid TEXT,
  idempotency_key TEXT NOT NULL UNIQUE,
  target_pix_key TEXT,
  target_document TEXT,
  retry_count INT NOT NULL DEFAULT 0,
  next_retry_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  claimed_at TIMESTAMPTZ,
  claimed_by TEXT,
  error_message TEXT,
  submitted_at TIMESTAMPTZ,
  submit_started_at TIMESTAMPTZ,
  submit_completed_at TIMESTAMPTZ,
  submit_outcome TEXT NOT NULL DEFAULT 'not_submitted',
  reconciliation_attempt_count INT NOT NULL DEFAULT 0,
  first_ambiguous_at TIMESTAMPTZ,
  last_reconciled_at TIMESTAMPTZ,
  consecutive_not_found INT NOT NULL DEFAULT 0,
  manual_review_reason TEXT,
  confirmed_at TIMESTAMPTZ,
  failed_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_merchant_settlements_status_retry
  ON merchant_settlements(status, next_retry_at, created_at)
  WHERE status IN ('PENDING','RETRYABLE','SUBMITTED','SUBMISSION_UNKNOWN');
CREATE INDEX IF NOT EXISTS idx_merchant_settlements_merchant_created
  ON merchant_settlements(merchant_id, created_at DESC);

ALTER TABLE merchant_settlements ADD COLUMN IF NOT EXISTS provider_e2e_id TEXT;
ALTER TABLE merchant_settlements ADD COLUMN IF NOT EXISTS provider_id_envio TEXT;
ALTER TABLE merchant_settlements ADD COLUMN IF NOT EXISTS claimed_at TIMESTAMPTZ;
ALTER TABLE merchant_settlements ADD COLUMN IF NOT EXISTS claimed_by TEXT;
ALTER TABLE merchant_settlements ADD COLUMN IF NOT EXISTS submit_started_at TIMESTAMPTZ;
ALTER TABLE merchant_settlements ADD COLUMN IF NOT EXISTS submit_completed_at TIMESTAMPTZ;
ALTER TABLE merchant_settlements ADD COLUMN IF NOT EXISTS submit_outcome TEXT NOT NULL DEFAULT 'not_submitted';
ALTER TABLE merchant_settlements ADD COLUMN IF NOT EXISTS reconciliation_attempt_count INT NOT NULL DEFAULT 0;
ALTER TABLE merchant_settlements ADD COLUMN IF NOT EXISTS first_ambiguous_at TIMESTAMPTZ;
ALTER TABLE merchant_settlements ADD COLUMN IF NOT EXISTS last_reconciled_at TIMESTAMPTZ;
ALTER TABLE merchant_settlements ADD COLUMN IF NOT EXISTS consecutive_not_found INT NOT NULL DEFAULT 0;
ALTER TABLE merchant_settlements ADD COLUMN IF NOT EXISTS manual_review_reason TEXT;
ALTER TABLE merchant_settlements DROP CONSTRAINT IF EXISTS merchant_settlements_status_check;
ALTER TABLE merchant_settlements ADD CONSTRAINT merchant_settlements_status_check
  CHECK (status IN ('MANUAL_REQUIRED','PENDING','PROCESSING','SUBMITTED','SUBMISSION_UNKNOWN','CONFIRMED','REJECTED','RETRYABLE','MANUAL_REVIEW','CANCELED'));
ALTER TABLE merchant_settlements DROP CONSTRAINT IF EXISTS merchant_settlements_submit_outcome_check;
ALTER TABLE merchant_settlements ADD CONSTRAINT merchant_settlements_submit_outcome_check
  CHECK (submit_outcome IN ('not_submitted','started','confirmed','rejected','ambiguous'));
CREATE INDEX IF NOT EXISTS idx_merchant_settlements_processing_stale
  ON merchant_settlements(status, submit_started_at, next_retry_at)
  WHERE status = 'PROCESSING';

CREATE TABLE IF NOT EXISTS merchant_settlement_provider_events (
  id BIGSERIAL PRIMARY KEY,
  settlement_id TEXT NOT NULL REFERENCES merchant_settlements(id),
  provider TEXT NOT NULL,
  id_envio TEXT NOT NULL DEFAULT '',
  e2e_id TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  payload JSONB,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (provider, id_envio, e2e_id, status)
);
CREATE INDEX IF NOT EXISTS idx_merchant_settlement_provider_events_settlement
  ON merchant_settlement_provider_events(settlement_id, created_at DESC);

CREATE TABLE IF NOT EXISTS nfc_settlement_reconciliation_anomalies (
  id BIGSERIAL PRIMARY KEY,
  anomaly_key TEXT NOT NULL UNIQUE,
  anomaly_type TEXT NOT NULL,
  severity TEXT NOT NULL,
  authorization_id TEXT,
  settlement_id TEXT,
  merchant_id TEXT,
  details JSONB,
  status TEXT NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN','RESOLVED')),
  first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  resolved_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_nfc_settlement_anomalies_status
  ON nfc_settlement_reconciliation_anomalies(status, last_seen_at DESC);

CREATE TABLE IF NOT EXISTS nfc_treasury_snapshots (
  id BIGSERIAL PRIMARY KEY,
  provider TEXT NOT NULL DEFAULT 'efi',
  available_brl_minor BIGINT NOT NULL DEFAULT 0,
  reserved_brl_minor BIGINT NOT NULL DEFAULT 0,
  projected_outflow_brl_minor BIGINT NOT NULL DEFAULT 0,
  minimum_buffer_brl_minor BIGINT NOT NULL DEFAULT 0,
  effective_available_brl_minor BIGINT NOT NULL DEFAULT 0,
  observed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  source TEXT NOT NULL DEFAULT 'manual_config',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_nfc_treasury_snapshots_provider_observed
  ON nfc_treasury_snapshots(provider, observed_at DESC);

CREATE TABLE IF NOT EXISTS nfc_brl_reservations (
  id TEXT PRIMARY KEY,
  authorization_id TEXT NOT NULL UNIQUE REFERENCES nfc_authorizations(id),
  merchant_id TEXT NOT NULL,
  terminal_id TEXT NOT NULL,
  amount_brl_minor BIGINT NOT NULL CHECK (amount_brl_minor > 0),
  status TEXT NOT NULL CHECK (status IN ('ACTIVE','CONSUMED','RELEASED')),
  source_snapshot_id BIGINT REFERENCES nfc_treasury_snapshots(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  consumed_at TIMESTAMPTZ,
  released_at TIMESTAMPTZ,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_nfc_brl_reservations_status_created
  ON nfc_brl_reservations(status, created_at);
CREATE INDEX IF NOT EXISTS idx_nfc_brl_reservations_merchant_created
  ON nfc_brl_reservations(merchant_id, created_at DESC);

-- M2M Agent Pay supports shared configured deposit addresses. Older hardening
-- deployments created a unique pending-address index that blocks concurrent
-- PIX/card intents when all deposits route to the same treasury address.
DROP INDEX IF EXISTS uq_m2m_pending_payment_address;

`
