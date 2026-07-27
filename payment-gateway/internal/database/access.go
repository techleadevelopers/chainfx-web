package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"payment-gateway/internal/privacy"

	"github.com/lib/pq"
)

const (
	AccessStatusPending   = "pending"
	AccessStatusConfirmed = "confirmed"
	AccessStatusGranted   = "granted"
	AccessStatusExpired   = "expired"
	GrantStatusActive     = "active"
)

type APIProduct struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Description     string    `json:"description"`
	Unit            string    `json:"unit"`
	QuotaUnits      int       `json:"quota"`
	PriceUSDT       float64   `json:"priceUsdt"`
	DurationSeconds int       `json:"durationSeconds"`
	ProviderName    string    `json:"providerName"`
	ProviderURL     string    `json:"providerUrl,omitempty"`
	Active          bool      `json:"active"`
	CreatedAt       time.Time `json:"createdAt"`
}

type APIPayment struct {
	ID                 string     `json:"id"`
	ProductID          string     `json:"productId"`
	BuyerWallet        string     `json:"buyerWallet"`
	AmountUSDT         float64    `json:"amountUsdt"`
	ChainFXFeeUSDT     float64    `json:"chainfxFeeUsdt"`
	ProviderAmountUSDT float64    `json:"providerAmountUsdt"`
	Asset              string     `json:"asset"`
	Network            string     `json:"network"`
	PaymentAddress     string     `json:"paymentAddress"`
	Memo               string     `json:"memo"`
	Nonce              string     `json:"nonce"`
	RequestHash        string     `json:"requestHash"`
	Status             string     `json:"status"`
	TxHash             *string    `json:"txHash,omitempty"`
	IdempotencyKey     *string    `json:"idempotencyKey,omitempty"`
	QuoteExpiresAt     time.Time  `json:"quoteExpiresAt"`
	ConfirmedAt        *time.Time `json:"confirmedAt,omitempty"`
	CreatedAt          time.Time  `json:"createdAt"`
}

type APIAccessGrant struct {
	ID             string    `json:"id"`
	PaymentID      string    `json:"paymentId"`
	ProductID      string    `json:"productId"`
	BuyerWallet    string    `json:"buyerWallet"`
	QuotaTotal     int       `json:"quotaTotal"`
	QuotaRemaining int       `json:"quotaRemaining"`
	ExpiresAt      time.Time `json:"expiresAt"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"createdAt"`
}

type AccessQuoteInput struct {
	ProductID      string
	BuyerWallet    string
	PaymentAddress string
	Nonce          string
	RequestHash    string
	ChainFXFeeBps  int
	QuoteTTL       time.Duration
	IdempotencyKey string
}

type AccessGrantResult struct {
	Payment     *APIPayment     `json:"payment"`
	Grant       *APIAccessGrant `json:"grant"`
	AccessToken string          `json:"accessToken,omitempty"`
	Product     *APIProduct     `json:"product,omitempty"`
}

func (db *DB) ListAPIProducts(ctx context.Context) ([]*APIProduct, error) {
	rows, err := db.SQL.QueryContext(ctx, `
		SELECT id, name, description, unit, quota_units, price_usdt::float8, duration_seconds,
		       provider_name, COALESCE(provider_url, ''), active, created_at
		FROM api_products
		WHERE active = true
		ORDER BY price_usdt ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*APIProduct
	for rows.Next() {
		p, err := scanAPIProduct(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (db *DB) GetAPIProduct(ctx context.Context, id string) (*APIProduct, error) {
	row := db.SQL.QueryRowContext(ctx, `
		SELECT id, name, description, unit, quota_units, price_usdt::float8, duration_seconds,
		       provider_name, COALESCE(provider_url, ''), active, created_at
		FROM api_products
		WHERE id = $1 AND active = true`, id)
	p, err := scanAPIProduct(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return p, err
}

func (db *DB) CreateAccessQuote(ctx context.Context, in AccessQuoteInput) (*APIPayment, *APIProduct, error) {
	product, err := db.GetAPIProduct(ctx, in.ProductID)
	if err != nil {
		return nil, nil, err
	}
	if product == nil {
		return nil, nil, fmt.Errorf("produto nao encontrado")
	}
	_, _ = db.SQL.ExecContext(ctx, `
		INSERT INTO agent_wallets (address, first_seen_at, last_seen_at)
		VALUES ($1, now(), now())
		ON CONFLICT (address) DO UPDATE SET last_seen_at = now()`,
		strings.ToLower(strings.TrimSpace(in.BuyerWallet)))
	fee := roundUSDT(product.PriceUSDT * float64(in.ChainFXFeeBps) / 10000)
	if fee <= 0 {
		fee = 0.01
	}
	providerAmount := roundUSDT(product.PriceUSDT - fee)
	payment := &APIPayment{
		ID:                 NewID(),
		ProductID:          product.ID,
		BuyerWallet:        strings.ToLower(strings.TrimSpace(in.BuyerWallet)),
		AmountUSDT:         product.PriceUSDT,
		ChainFXFeeUSDT:     fee,
		ProviderAmountUSDT: providerAmount,
		Asset:              "USDT",
		Network:            "BSC",
		PaymentAddress:     strings.ToLower(strings.TrimSpace(in.PaymentAddress)),
		Memo:               "api_access_" + strings.ReplaceAll(NewID(), "-", ""),
		Nonce:              in.Nonce,
		RequestHash:        in.RequestHash,
		Status:             AccessStatusPending,
		QuoteExpiresAt:     time.Now().UTC().Add(in.QuoteTTL),
	}
	var idempotency any
	if strings.TrimSpace(in.IdempotencyKey) != "" {
		idempotency = strings.TrimSpace(in.IdempotencyKey)
	}
	_, err = db.SQL.ExecContext(ctx, `
		INSERT INTO api_payments (
		  id, product_id, buyer_wallet, amount_usdt, chainfx_fee_usdt, provider_amount_usdt,
		  asset, network, payment_address, memo, nonce, request_hash, status, quote_expires_at, idempotency_key
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		payment.ID, payment.ProductID, payment.BuyerWallet, payment.AmountUSDT, payment.ChainFXFeeUSDT,
		payment.ProviderAmountUSDT, payment.Asset, payment.Network, payment.PaymentAddress, payment.Memo,
		payment.Nonce, payment.RequestHash, payment.Status, payment.QuoteExpiresAt, idempotency)
	if err != nil {
		if pqErr, ok := err.(*pq.Error); ok && pqErr.Code == "23505" && idempotency != nil {
			existing, getErr := db.GetAccessPaymentByIdempotency(ctx, strings.TrimSpace(in.IdempotencyKey))
			return existing, product, getErr
		}
		return nil, nil, err
	}
	return payment, product, nil
}

func (db *DB) GetAccessPayment(ctx context.Context, id string) (*APIPayment, error) {
	row := db.SQL.QueryRowContext(ctx, `
		SELECT id, product_id, buyer_wallet, amount_usdt::float8, chainfx_fee_usdt::float8, provider_amount_usdt::float8,
		       asset, network, payment_address, memo, nonce, request_hash, status, tx_hash, idempotency_key,
		       quote_expires_at, confirmed_at, created_at
		FROM api_payments WHERE id = $1`, id)
	p, err := scanAPIPayment(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return p, err
}

func (db *DB) GetAccessPaymentByIdempotency(ctx context.Context, key string) (*APIPayment, error) {
	row := db.SQL.QueryRowContext(ctx, `
		SELECT id, product_id, buyer_wallet, amount_usdt::float8, chainfx_fee_usdt::float8, provider_amount_usdt::float8,
		       asset, network, payment_address, memo, nonce, request_hash, status, tx_hash, idempotency_key,
		       quote_expires_at, confirmed_at, created_at
		FROM api_payments WHERE idempotency_key = $1`, key)
	p, err := scanAPIPayment(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return p, err
}

func (db *DB) ConfirmAccessPaymentAndGrant(ctx context.Context, paymentID, txHash, idempotencyKey string) (*AccessGrantResult, error) {
	tx, err := db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	payment, err := scanAPIPayment(tx.QueryRowContext(ctx, `
		SELECT id, product_id, buyer_wallet, amount_usdt::float8, chainfx_fee_usdt::float8, provider_amount_usdt::float8,
		       asset, network, payment_address, memo, nonce, request_hash, status, tx_hash, idempotency_key,
		       quote_expires_at, confirmed_at, created_at
		FROM api_payments WHERE id = $1 FOR UPDATE`, paymentID))
	if err != nil {
		return nil, err
	}
	if time.Now().UTC().After(payment.QuoteExpiresAt) && payment.Status == AccessStatusPending {
		_, _ = tx.ExecContext(ctx, `UPDATE api_payments SET status = $2, updated_at = now() WHERE id = $1`, payment.ID, AccessStatusExpired)
		return nil, fmt.Errorf("quote expirado")
	}

	product, err := scanAPIProduct(tx.QueryRowContext(ctx, `
		SELECT id, name, description, unit, quota_units, price_usdt::float8, duration_seconds,
		       provider_name, COALESCE(provider_url, ''), active, created_at
		FROM api_products WHERE id = $1 FOR SHARE`, payment.ProductID))
	if err != nil {
		return nil, err
	}

	if payment.Status == AccessStatusGranted {
		grant, err := db.getGrantByPaymentTx(ctx, tx, payment.ID)
		if err != nil {
			return nil, err
		}
		return &AccessGrantResult{Payment: payment, Grant: grant, Product: product}, tx.Commit()
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE api_payments
		SET status = $2, tx_hash = $3, idempotency_key = COALESCE(idempotency_key, NULLIF($4,'')),
		    confirmed_at = COALESCE(confirmed_at, now()), updated_at = now()
		WHERE id = $1`,
		payment.ID, AccessStatusConfirmed, strings.ToLower(strings.TrimSpace(txHash)), strings.TrimSpace(idempotencyKey))
	if err != nil {
		return nil, err
	}

	token := "ak_live_agent_" + NewAccessToken()
	tokenHash := db.accessTokenHash(token)
	grant := &APIAccessGrant{
		ID:             NewID(),
		PaymentID:      payment.ID,
		ProductID:      payment.ProductID,
		BuyerWallet:    payment.BuyerWallet,
		QuotaTotal:     product.QuotaUnits,
		QuotaRemaining: product.QuotaUnits,
		ExpiresAt:      time.Now().UTC().Add(time.Duration(product.DurationSeconds) * time.Second),
		Status:         GrantStatusActive,
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO api_access_grants (
		  id, payment_id, product_id, buyer_wallet, access_token_hash, quota_total, quota_remaining, expires_at, status
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (payment_id) DO NOTHING`,
		grant.ID, grant.PaymentID, grant.ProductID, grant.BuyerWallet, tokenHash, grant.QuotaTotal, grant.QuotaRemaining, grant.ExpiresAt, grant.Status)
	if err != nil {
		return nil, err
	}
	if existing, err := db.getGrantByPaymentTx(ctx, tx, payment.ID); err == nil && existing != nil {
		grant = existing
	}
	_, err = tx.ExecContext(ctx, `UPDATE api_payments SET status = $2, updated_at = now() WHERE id = $1`, payment.ID, AccessStatusGranted)
	if err != nil {
		return nil, err
	}
	_, _ = tx.ExecContext(ctx, `
		INSERT INTO agent_wallets (address, first_seen_at, last_seen_at, total_spent_usdt)
		VALUES ($1, now(), now(), $2)
		ON CONFLICT (address) DO UPDATE
		SET last_seen_at = now(),
		    total_spent_usdt = agent_wallets.total_spent_usdt + EXCLUDED.total_spent_usdt`,
		payment.BuyerWallet, payment.AmountUSDT)
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	payment.Status = AccessStatusGranted
	txHashValue := strings.ToLower(strings.TrimSpace(txHash))
	payment.TxHash = &txHashValue
	return &AccessGrantResult{Payment: payment, Grant: grant, AccessToken: token, Product: product}, nil
}

func (db *DB) GetAccessGrant(ctx context.Context, id string) (*APIAccessGrant, error) {
	row := db.SQL.QueryRowContext(ctx, `
		SELECT id, payment_id, product_id, buyer_wallet, quota_total, quota_remaining, expires_at, status, created_at
		FROM api_access_grants WHERE id = $1`, id)
	grant, err := scanAccessGrant(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return grant, err
}

func (db *DB) ConsumeAccessUsage(ctx context.Context, token string, units int, requestHash, idempotencyKey string, metadata map[string]any) (*APIAccessGrant, bool, error) {
	if units <= 0 {
		units = 1
	}
	if strings.TrimSpace(idempotencyKey) == "" {
		return nil, false, fmt.Errorf("idempotencyKey obrigatorio")
	}
	tokenHash := db.accessTokenHash(token)
	tx, err := db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	grant, err := scanAccessGrant(tx.QueryRowContext(ctx, `
		SELECT id, payment_id, product_id, buyer_wallet, quota_total, quota_remaining, expires_at, status, created_at
		FROM api_access_grants
		WHERE access_token_hash = $1 FOR UPDATE`, tokenHash))
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, false, fmt.Errorf("access token invalido")
		}
		return nil, false, err
	}
	if grant.Status != GrantStatusActive || time.Now().UTC().After(grant.ExpiresAt) {
		return nil, false, fmt.Errorf("grant expirado ou inativo")
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM api_usage_events WHERE grant_id = $1 AND idempotency_key = $2)`, grant.ID, idempotencyKey).Scan(&exists); err != nil {
		return nil, false, err
	}
	if exists {
		return grant, true, tx.Commit()
	}
	if grant.QuotaRemaining < units {
		return nil, false, fmt.Errorf("quota insuficiente")
	}
	raw, _ := json.Marshal(metadata)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO api_usage_events (id, grant_id, product_id, units, request_hash, idempotency_key, metadata)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		NewID(), grant.ID, grant.ProductID, units, requestHash, idempotencyKey, raw)
	if err != nil {
		return nil, false, err
	}
	grant.QuotaRemaining -= units
	_, err = tx.ExecContext(ctx, `UPDATE api_access_grants SET quota_remaining = $2, updated_at = now() WHERE id = $1`, grant.ID, grant.QuotaRemaining)
	if err != nil {
		return nil, false, err
	}
	return grant, false, tx.Commit()
}

func (db *DB) getGrantByPaymentTx(ctx context.Context, tx *sql.Tx, paymentID string) (*APIAccessGrant, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT id, payment_id, product_id, buyer_wallet, quota_total, quota_remaining, expires_at, status, created_at
		FROM api_access_grants WHERE payment_id = $1`, paymentID)
	grant, err := scanAccessGrant(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return grant, err
}

func (db *DB) accessTokenHash(token string) string {
	secret := ""
	if db.cfg != nil {
		secret = db.cfg.LGPDSecret
	}
	hash := privacy.Hash(strings.TrimSpace(token), secret)
	if hash == "" {
		hash = strings.TrimSpace(token)
	}
	return hash
}

func scanAPIProduct(row rowScanner) (*APIProduct, error) {
	var p APIProduct
	if err := row.Scan(&p.ID, &p.Name, &p.Description, &p.Unit, &p.QuotaUnits, &p.PriceUSDT, &p.DurationSeconds, &p.ProviderName, &p.ProviderURL, &p.Active, &p.CreatedAt); err != nil {
		return nil, err
	}
	return &p, nil
}

func scanAPIPayment(row rowScanner) (*APIPayment, error) {
	var p APIPayment
	var txHash, idem sql.NullString
	var confirmed sql.NullTime
	if err := row.Scan(&p.ID, &p.ProductID, &p.BuyerWallet, &p.AmountUSDT, &p.ChainFXFeeUSDT, &p.ProviderAmountUSDT, &p.Asset, &p.Network, &p.PaymentAddress, &p.Memo, &p.Nonce, &p.RequestHash, &p.Status, &txHash, &idem, &p.QuoteExpiresAt, &confirmed, &p.CreatedAt); err != nil {
		return nil, err
	}
	if txHash.Valid {
		p.TxHash = &txHash.String
	}
	if idem.Valid {
		p.IdempotencyKey = &idem.String
	}
	if confirmed.Valid {
		p.ConfirmedAt = &confirmed.Time
	}
	return &p, nil
}

func scanAccessGrant(row rowScanner) (*APIAccessGrant, error) {
	var g APIAccessGrant
	if err := row.Scan(&g.ID, &g.PaymentID, &g.ProductID, &g.BuyerWallet, &g.QuotaTotal, &g.QuotaRemaining, &g.ExpiresAt, &g.Status, &g.CreatedAt); err != nil {
		return nil, err
	}
	return &g, nil
}

func roundUSDT(value float64) float64 {
	if value < 0 {
		return 0
	}
	return float64(int64(value*1_000_000+0.5)) / 1_000_000
}
