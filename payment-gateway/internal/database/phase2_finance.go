package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrQuoteNotFound         = errors.New("quote not found")
	ErrQuoteExpired          = errors.New("quote expired")
	ErrQuoteConsumed         = errors.New("quote already consumed")
	ErrQuoteMismatch         = errors.New("quote does not match request")
	ErrIdempotencyInProgress = errors.New("idempotency key in progress")
	ErrIdempotencyPayload    = errors.New("idempotency key payload mismatch")
)

type Quote struct {
	ID                string     `json:"id"`
	Side              string     `json:"side"`
	Asset             string     `json:"asset"`
	Network           string     `json:"network"`
	FiatCurrency      string     `json:"fiatCurrency"`
	PaymentMethod     string     `json:"paymentMethod"`
	AmountMinor       int64      `json:"amountMinor"`
	CryptoAmountUnits string     `json:"cryptoAmountUnits"`
	Rate              float64    `json:"rate"`
	MarketRate        float64    `json:"marketRate"`
	FeeMinor          int64      `json:"feeMinor"`
	ExpiresAt         time.Time  `json:"expiresAt"`
	ConsumedAt        *time.Time `json:"consumedAt,omitempty"`
	APIKeyHash        string     `json:"apiKeyHash,omitempty"`
	BodyHash          string     `json:"bodyHash"`
	CreatedAt         time.Time  `json:"createdAt"`
}

type QuoteInput struct {
	ID                string
	Side              string
	Asset             string
	Network           string
	FiatCurrency      string
	PaymentMethod     string
	AmountMinor       int64
	CryptoAmountUnits string
	Rate              float64
	MarketRate        float64
	FeeMinor          int64
	ExpiresAt         time.Time
	APIKeyHash        string
	BodyHash          string
}

type QuoteConsumeInput struct {
	ID            string
	Side          string
	Asset         string
	Network       string
	FiatCurrency  string
	PaymentMethod string
	AmountMinor   int64
	APIKeyHash    string
}

type IdempotencyRecord struct {
	Key            string          `json:"key"`
	Operation      string          `json:"operation"`
	APIKeyHash     string          `json:"apiKeyHash,omitempty"`
	BodyHash       string          `json:"bodyHash"`
	Status         string          `json:"status"`
	ResultType     *string         `json:"resultType,omitempty"`
	ResultID       *string         `json:"resultId,omitempty"`
	ResponseStatus *int            `json:"responseStatus,omitempty"`
	ResponseJSON   json.RawMessage `json:"response,omitempty"`
	CreatedAt      time.Time       `json:"createdAt"`
	UpdatedAt      time.Time       `json:"updatedAt"`
}

type IdempotencyStart struct {
	Record   *IdempotencyRecord
	Replay   bool
	Conflict bool
}

func (db *DB) CreateQuote(ctx context.Context, in QuoteInput) (*Quote, error) {
	if strings.TrimSpace(in.ID) == "" {
		in.ID = "qt_" + strings.ReplaceAll(NewID(), "-", "")
	}
	if in.ExpiresAt.IsZero() {
		in.ExpiresAt = time.Now().UTC().Add(5 * time.Minute)
	}
	_, err := db.SQL.ExecContext(ctx, `
		INSERT INTO quotes (
		  id, side, asset, network, fiat_currency, payment_method, amount_minor,
		  crypto_amount_units, rate, market_rate, fee_minor, expires_at,
		  api_key_hash, body_hash
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		in.ID, normalizeDBLower(in.Side), strings.ToUpper(strings.TrimSpace(in.Asset)),
		strings.ToUpper(strings.TrimSpace(in.Network)),
		strings.ToUpper(strings.TrimSpace(in.FiatCurrency)), normalizeDBLower(in.PaymentMethod),
		in.AmountMinor, in.CryptoAmountUnits, in.Rate, in.MarketRate, in.FeeMinor,
		in.ExpiresAt.UTC(), strings.TrimSpace(in.APIKeyHash), in.BodyHash)
	if err != nil {
		return nil, err
	}
	return db.GetQuote(ctx, in.ID)
}

func (db *DB) GetQuote(ctx context.Context, id string) (*Quote, error) {
	return scanQuote(db.SQL.QueryRowContext(ctx, quoteSelectSQL()+` WHERE id = $1`, strings.TrimSpace(id)))
}

func (db *DB) ConsumeQuote(ctx context.Context, in QuoteConsumeInput) (*Quote, error) {
	tx, err := db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	q, err := scanQuote(tx.QueryRowContext(ctx, quoteSelectSQL()+` WHERE id = $1 FOR UPDATE`, strings.TrimSpace(in.ID)))
	if err == sql.ErrNoRows || q == nil {
		return nil, ErrQuoteNotFound
	}
	if err != nil {
		return nil, err
	}
	if q.ConsumedAt != nil {
		return nil, ErrQuoteConsumed
	}
	if time.Now().UTC().After(q.ExpiresAt) {
		return nil, ErrQuoteExpired
	}
	if !quoteMatches(q, in) {
		return nil, ErrQuoteMismatch
	}
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE quotes SET consumed_at = $2 WHERE id = $1`, q.ID, now); err != nil {
		return nil, err
	}
	q.ConsumedAt = &now
	return q, tx.Commit()
}

func (db *DB) BeginIdempotency(ctx context.Context, key, operation, apiKeyHash, bodyHash string) (*IdempotencyStart, error) {
	key = strings.TrimSpace(key)
	operation = strings.TrimSpace(operation)
	if key == "" || operation == "" {
		return nil, fmt.Errorf("idempotency key and operation are required")
	}
	tx, err := db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rec, err := scanIdempotency(tx.QueryRowContext(ctx, idempotencySelectSQL()+`
		WHERE key = $1 AND operation = $2 AND COALESCE(api_key_hash, '') = COALESCE(NULLIF($3,''), '')
		FOR UPDATE`, key, operation, apiKeyHash))
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if rec != nil {
		if rec.BodyHash != bodyHash {
			return &IdempotencyStart{Record: rec, Conflict: true}, tx.Commit()
		}
		if rec.Status == "completed" || rec.Status == "failed" {
			return &IdempotencyStart{Record: rec, Replay: true}, tx.Commit()
		}
		return &IdempotencyStart{Record: rec}, ErrIdempotencyInProgress
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO idempotency_keys (key, operation, api_key_hash, body_hash, status)
		VALUES ($1,$2,$3,$4,'started')`,
		key, operation, strings.TrimSpace(apiKeyHash), bodyHash)
	if err != nil {
		return nil, err
	}
	rec = &IdempotencyRecord{Key: key, Operation: operation, APIKeyHash: apiKeyHash, BodyHash: bodyHash, Status: "started", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	return &IdempotencyStart{Record: rec}, tx.Commit()
}

func (db *DB) CompleteIdempotency(ctx context.Context, key, operation, apiKeyHash, resultType, resultID string, responseStatus int, response any) error {
	raw, err := json.Marshal(response)
	if err != nil {
		return err
	}
	_, err = db.SQL.ExecContext(ctx, `
		UPDATE idempotency_keys
		SET status = 'completed', result_type = $4, result_id = $5,
		    response_status = $6, response_json = $7, updated_at = now()
		WHERE key = $1 AND operation = $2 AND COALESCE(api_key_hash, '') = COALESCE(NULLIF($3,''), '')`,
		strings.TrimSpace(key), strings.TrimSpace(operation), apiKeyHash, nullableString(resultType),
		nullableString(resultID), responseStatus, json.RawMessage(raw))
	return err
}

func (db *DB) FailIdempotency(ctx context.Context, key, operation, apiKeyHash, resultType, resultID string, responseStatus int, response any) error {
	raw, err := json.Marshal(response)
	if err != nil {
		return err
	}
	_, err = db.SQL.ExecContext(ctx, `
		UPDATE idempotency_keys
		SET status = 'failed', result_type = $4, result_id = $5,
		    response_status = $6, response_json = $7, updated_at = now()
		WHERE key = $1 AND operation = $2 AND COALESCE(api_key_hash, '') = COALESCE(NULLIF($3,''), '')`,
		strings.TrimSpace(key), strings.TrimSpace(operation), apiKeyHash, nullableString(resultType),
		nullableString(resultID), responseStatus, json.RawMessage(raw))
	return err
}

func (db *DB) UpdateBuyOrderPayment(ctx context.Context, id, status, providerPaymentID string, pixPayload any) (*BuyOrder, error) {
	rawPayload, err := json.Marshal(pixPayload)
	if err != nil {
		return nil, err
	}
	_, err = db.SQL.ExecContext(ctx, `
		UPDATE buy_orders
		SET status = $2,
		    provider_payment_id = COALESCE(NULLIF($3,''), provider_payment_id),
		    pix_payload = $4,
		    error = NULL,
		    updated_at = now()
		WHERE id = $1`,
		id, status, nullableString(providerPaymentID), json.RawMessage(rawPayload))
	if err != nil {
		return nil, err
	}
	_ = db.AddBuyEvent(ctx, id, "buy.payment_created", map[string]any{"providerPaymentId": providerPaymentID})
	return db.GetBuyOrder(ctx, id)
}

func quoteSelectSQL() string {
	return `SELECT id, side, asset, COALESCE(network, ''), fiat_currency, payment_method, amount_minor, crypto_amount_units,
	       rate::float8, COALESCE(market_rate, 0)::float8, fee_minor, expires_at, consumed_at,
	       COALESCE(api_key_hash, ''), body_hash, created_at
	FROM quotes`
}

func scanQuote(row rowScanner) (*Quote, error) {
	var q Quote
	var consumed sql.NullTime
	if err := row.Scan(&q.ID, &q.Side, &q.Asset, &q.Network, &q.FiatCurrency, &q.PaymentMethod, &q.AmountMinor,
		&q.CryptoAmountUnits, &q.Rate, &q.MarketRate, &q.FeeMinor, &q.ExpiresAt, &consumed,
		&q.APIKeyHash, &q.BodyHash, &q.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if consumed.Valid {
		q.ConsumedAt = &consumed.Time
	}
	return &q, nil
}

func idempotencySelectSQL() string {
	return `SELECT key, operation, COALESCE(api_key_hash, ''), body_hash, status,
	       result_type, result_id, response_status, COALESCE(response_json, '{}'::jsonb),
	       created_at, updated_at
	FROM idempotency_keys`
}

func scanIdempotency(row rowScanner) (*IdempotencyRecord, error) {
	var rec IdempotencyRecord
	var resultType, resultID sql.NullString
	var responseStatus sql.NullInt64
	if err := row.Scan(&rec.Key, &rec.Operation, &rec.APIKeyHash, &rec.BodyHash, &rec.Status,
		&resultType, &resultID, &responseStatus, &rec.ResponseJSON, &rec.CreatedAt, &rec.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if resultType.Valid {
		rec.ResultType = &resultType.String
	}
	if resultID.Valid {
		rec.ResultID = &resultID.String
	}
	if responseStatus.Valid {
		v := int(responseStatus.Int64)
		rec.ResponseStatus = &v
	}
	return &rec, nil
}

func quoteMatches(q *Quote, in QuoteConsumeInput) bool {
	if q == nil {
		return false
	}
	return strings.EqualFold(q.Side, in.Side) &&
		strings.EqualFold(q.Asset, in.Asset) &&
		(strings.TrimSpace(in.Network) == "" || strings.EqualFold(q.Network, in.Network)) &&
		strings.EqualFold(q.FiatCurrency, in.FiatCurrency) &&
		(strings.TrimSpace(in.PaymentMethod) == "" || strings.EqualFold(q.PaymentMethod, in.PaymentMethod)) &&
		q.AmountMinor == in.AmountMinor &&
		(strings.TrimSpace(q.APIKeyHash) == "" || strings.TrimSpace(q.APIKeyHash) == strings.TrimSpace(in.APIKeyHash))
}

func normalizeDBLower(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
