package mobile

// db_phase5.go — database queries for Phase 5 features (mobile-only).
// All queries use mobileQueries (which wraps *sql.DB directly) so existing
// server-side tables and queries are untouched.

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/lib/pq"

	"payment-gateway/internal/models"
)

// ─── Assets ───────────────────────────────────────────────────────────────────

func (q *mobileQueries) ListAssets(ctx context.Context, onlyActive bool) ([]models.Asset, error) {
	query := `SELECT symbol,name,network,contract_address,decimals,
	                 min_amount,max_amount,daily_limit,monthly_limit,fee_bps,active,created_at
	          FROM assets`
	if onlyActive {
		query += " WHERE active=true"
	}
	query += " ORDER BY symbol"
	rows, err := q.sql.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Asset
	for rows.Next() {
		a := models.Asset{}
		if err := rows.Scan(&a.Symbol, &a.Name, &a.Network, &a.ContractAddress,
			&a.Decimals, &a.MinAmount, &a.MaxAmount, &a.DailyLimit, &a.MonthlyLimit,
			&a.FeeBPS, &a.Active, &a.CreatedAt); err == nil {
			out = append(out, a)
		}
	}
	return out, rows.Err()
}

func (q *mobileQueries) GetAsset(ctx context.Context, symbol string) (*models.Asset, error) {
	a := &models.Asset{}
	err := q.sql.QueryRowContext(ctx, `
		SELECT symbol,name,network,contract_address,decimals,
		       min_amount,max_amount,daily_limit,monthly_limit,fee_bps,active,created_at
		FROM assets WHERE symbol=$1`, strings.ToUpper(symbol)).Scan(
		&a.Symbol, &a.Name, &a.Network, &a.ContractAddress,
		&a.Decimals, &a.MinAmount, &a.MaxAmount, &a.DailyLimit, &a.MonthlyLimit,
		&a.FeeBPS, &a.Active, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return a, err
}

// ─── Countries ────────────────────────────────────────────────────────────────

func (q *mobileQueries) ListCountries(ctx context.Context) ([]models.Country, error) {
	rows, err := q.sql.QueryContext(ctx, `
		SELECT code,name,currency,language,active,created_at
		FROM countries WHERE active=true ORDER BY code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Country
	for rows.Next() {
		c := models.Country{}
		if err := rows.Scan(&c.Code, &c.Name, &c.Currency, &c.Language, &c.Active, &c.CreatedAt); err == nil {
			out = append(out, c)
		}
	}
	return out, rows.Err()
}

func (q *mobileQueries) GetCountry(ctx context.Context, code string) (*models.Country, error) {
	c := &models.Country{}
	err := q.sql.QueryRowContext(ctx, `
		SELECT code,name,currency,language,active,created_at
		FROM countries WHERE code=$1`, strings.ToUpper(code)).Scan(
		&c.Code, &c.Name, &c.Currency, &c.Language, &c.Active, &c.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

func (q *mobileQueries) ListRailsByCountry(ctx context.Context, countryCode string) ([]models.PaymentRail, error) {
	rows, err := q.sql.QueryContext(ctx, `
		SELECT id,country_code,name,currency,active,metadata,created_at
		FROM payment_rails WHERE country_code=$1 AND active=true ORDER BY id`,
		strings.ToUpper(countryCode))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.PaymentRail
	for rows.Next() {
		r := models.PaymentRail{}
		if err := rows.Scan(&r.ID, &r.CountryCode, &r.Name, &r.Currency, &r.Active, &r.Metadata, &r.CreatedAt); err == nil {
			out = append(out, r)
		}
	}
	return out, rows.Err()
}

// ─── KYC ──────────────────────────────────────────────────────────────────────

func (q *mobileQueries) CreateKYCRequest(ctx context.Context, userID string, level models.KYCLevel,
	docType, docURL, docBackURL, selfieURL, facialVideoURL, proofAddrURL, proofIncURL *string) (*models.KYCRequest, error) {
	if err := q.ensureMobileMediaSchema(ctx); err != nil {
		return nil, err
	}
	var id string
	err := q.sql.QueryRowContext(ctx, `
		INSERT INTO kyc_requests
		  (user_id, level, status, document_type, document_url,
		   document_back_url, selfie_url, facial_video_url, proof_of_address_url, proof_of_income_url)
		VALUES ($1,$2,'pending',$3,$4,$5,$6,$7,$8,$9)
		RETURNING id`,
		userID, int(level), docType, docURL, docBackURL, selfieURL, facialVideoURL, proofAddrURL, proofIncURL).Scan(&id)
	if err != nil {
		return nil, err
	}
	return q.GetKYCRequest(ctx, id)
}

func (q *mobileQueries) GetKYCRequest(ctx context.Context, id string) (*models.KYCRequest, error) {
	_ = q.ensureMobileMediaSchema(ctx)
	k := &models.KYCRequest{}
	err := q.sql.QueryRowContext(ctx, `
		SELECT id,user_id,level,status,document_type,document_url,selfie_url,
		       document_back_url,facial_video_url,proof_of_address_url,proof_of_income_url,reviewer_notes,
		       submitted_at,reviewed_at,created_at,updated_at
		FROM kyc_requests WHERE id=$1`, id).Scan(
		&k.ID, &k.UserID, &k.Level, &k.Status, &k.DocumentType, &k.DocumentURL,
		&k.SelfieURL, &k.DocumentBackURL, &k.FacialVideoURL, &k.ProofOfAddressURL, &k.ProofOfIncomeURL, &k.ReviewerNotes,
		&k.SubmittedAt, &k.ReviewedAt, &k.CreatedAt, &k.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return k, err
}

func (q *mobileQueries) GetLatestKYCByUser(ctx context.Context, userID string) (*models.KYCRequest, error) {
	_ = q.ensureMobileMediaSchema(ctx)
	k := &models.KYCRequest{}
	err := q.sql.QueryRowContext(ctx, `
		SELECT id,user_id,level,status,document_type,document_url,selfie_url,
		       document_back_url,facial_video_url,proof_of_address_url,proof_of_income_url,reviewer_notes,
		       submitted_at,reviewed_at,created_at,updated_at
		FROM kyc_requests WHERE user_id=$1
		ORDER BY created_at DESC LIMIT 1`, userID).Scan(
		&k.ID, &k.UserID, &k.Level, &k.Status, &k.DocumentType, &k.DocumentURL,
		&k.SelfieURL, &k.DocumentBackURL, &k.FacialVideoURL, &k.ProofOfAddressURL, &k.ProofOfIncomeURL, &k.ReviewerNotes,
		&k.SubmittedAt, &k.ReviewedAt, &k.CreatedAt, &k.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return k, err
}

func (q *mobileQueries) GetApprovedKYCLevel(ctx context.Context, userID string) (models.KYCLevel, error) {
	var level int
	err := q.sql.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(level), 0)
		FROM kyc_requests
		WHERE user_id=$1 AND status='approved'`, userID).Scan(&level)
	return models.KYCLevel(level), err
}

func (q *mobileQueries) ListKYCByUser(ctx context.Context, userID string, limit int) ([]models.KYCRequest, error) {
	_ = q.ensureMobileMediaSchema(ctx)
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := q.sql.QueryContext(ctx, `
		SELECT id,user_id,level,status,document_type,document_url,selfie_url,
		       document_back_url,facial_video_url,proof_of_address_url,proof_of_income_url,reviewer_notes,
		       submitted_at,reviewed_at,created_at,updated_at
		FROM kyc_requests WHERE user_id=$1 ORDER BY created_at DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.KYCRequest
	for rows.Next() {
		k := models.KYCRequest{}
		_ = rows.Scan(&k.ID, &k.UserID, &k.Level, &k.Status, &k.DocumentType, &k.DocumentURL,
			&k.SelfieURL, &k.DocumentBackURL, &k.FacialVideoURL, &k.ProofOfAddressURL, &k.ProofOfIncomeURL, &k.ReviewerNotes,
			&k.SubmittedAt, &k.ReviewedAt, &k.CreatedAt, &k.UpdatedAt)
		out = append(out, k)
	}
	return out, rows.Err()
}

// ─── Swaps ────────────────────────────────────────────────────────────────────

func (q *mobileQueries) CreateSwap(ctx context.Context, userID, fromAsset, toAsset string,
	fromAmount, slippage float64, feeBPS int) (*models.Swap, error) {
	s := &models.Swap{}
	err := q.sql.QueryRowContext(ctx, `
		INSERT INTO swaps (user_id, from_asset, to_asset, from_amount, fee_bps, slippage_tolerance)
		VALUES ($1,$2,$3,$4,$5,$6)
		RETURNING id,user_id,from_asset,to_asset,from_amount,to_amount,rate,
		          fee_bps,slippage_tolerance,status,tx_hash,error,created_at,updated_at`,
		userID, fromAsset, toAsset, fromAmount, feeBPS, slippage).Scan(
		&s.ID, &s.UserID, &s.FromAsset, &s.ToAsset, &s.FromAmount, &s.ToAmount,
		&s.Rate, &s.FeeBPS, &s.SlippageTolerance, &s.Status, &s.TxHash, &s.Error,
		&s.CreatedAt, &s.UpdatedAt)
	return s, err
}

func (q *mobileQueries) GetSwap(ctx context.Context, id string) (*models.Swap, error) {
	s := &models.Swap{}
	err := q.sql.QueryRowContext(ctx, `
		SELECT id,user_id,from_asset,to_asset,from_amount,to_amount,rate,
		       fee_bps,slippage_tolerance,status,tx_hash,error,created_at,updated_at
		FROM swaps WHERE id=$1`, id).Scan(
		&s.ID, &s.UserID, &s.FromAsset, &s.ToAsset, &s.FromAmount, &s.ToAmount,
		&s.Rate, &s.FeeBPS, &s.SlippageTolerance, &s.Status, &s.TxHash, &s.Error,
		&s.CreatedAt, &s.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return s, err
}

func (q *mobileQueries) ListSwapsByUser(ctx context.Context, userID string, limit int) ([]models.Swap, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := q.sql.QueryContext(ctx, `
		SELECT id,user_id,from_asset,to_asset,from_amount,to_amount,rate,
		       fee_bps,slippage_tolerance,status,tx_hash,error,created_at,updated_at
		FROM swaps WHERE user_id=$1 ORDER BY created_at DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Swap
	for rows.Next() {
		s := models.Swap{}
		_ = rows.Scan(&s.ID, &s.UserID, &s.FromAsset, &s.ToAsset, &s.FromAmount, &s.ToAmount,
			&s.Rate, &s.FeeBPS, &s.SlippageTolerance, &s.Status, &s.TxHash, &s.Error,
			&s.CreatedAt, &s.UpdatedAt)
		out = append(out, s)
	}
	return out, rows.Err()
}

func (q *mobileQueries) UpdateSwapStatus(ctx context.Context, id string, status models.SwapStatus, toAmount *float64, rate *float64, txHash *string, swapErr *string) error {
	_, err := q.sql.ExecContext(ctx, `
		UPDATE swaps SET status=$2, to_amount=COALESCE($3,to_amount),
		                 rate=COALESCE($4,rate), tx_hash=COALESCE($5,tx_hash),
		                 error=COALESCE($6,error), updated_at=NOW()
		WHERE id=$1`, id, string(status), toAmount, rate, txHash, swapErr)
	return err
}

// ─── Webhooks ─────────────────────────────────────────────────────────────────

func (q *mobileQueries) CreateWebhookSubscription(ctx context.Context, userID, targetURL, secret string, events []string) (*models.WebhookSubscription, error) {
	var id string
	err := q.sql.QueryRowContext(ctx, `
		INSERT INTO webhook_subscriptions (user_id, target_url, secret, events)
		VALUES (NULLIF($1,'')::uuid, $2, $3, $4::text[])
		RETURNING id`, userID, targetURL, secret, pq.Array(events)).Scan(&id)
	if err != nil {
		return nil, err
	}
	return q.GetWebhookSubscription(ctx, id)
}

func (q *mobileQueries) GetWebhookSubscription(ctx context.Context, id string) (*models.WebhookSubscription, error) {
	ws := &models.WebhookSubscription{}
	var events pq.StringArray
	err := q.sql.QueryRowContext(ctx, `
		SELECT id, user_id::text, target_url, secret, events, active, created_at, updated_at
		FROM webhook_subscriptions WHERE id=$1`, id).Scan(
		&ws.ID, &ws.UserID, &ws.TargetURL, &ws.Secret, &events,
		&ws.Active, &ws.CreatedAt, &ws.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ws.Events = []string(events)
	return ws, nil
}

func (q *mobileQueries) ListWebhooksByUser(ctx context.Context, userID string) ([]models.WebhookSubscription, error) {
	rows, err := q.sql.QueryContext(ctx, `
		SELECT id, user_id::text, target_url, secret, events, active, created_at, updated_at
		FROM webhook_subscriptions WHERE user_id=$1::uuid ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.WebhookSubscription
	for rows.Next() {
		ws := models.WebhookSubscription{}
		var events pq.StringArray
		_ = rows.Scan(&ws.ID, &ws.UserID, &ws.TargetURL, &ws.Secret, &events,
			&ws.Active, &ws.CreatedAt, &ws.UpdatedAt)
		ws.Events = []string(events)
		out = append(out, ws)
	}
	return out, rows.Err()
}

func (q *mobileQueries) DeleteWebhookSubscription(ctx context.Context, id, userID string) error {
	res, err := q.sql.ExecContext(ctx,
		"DELETE FROM webhook_subscriptions WHERE id=$1 AND user_id=$2::uuid", id, userID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errors.New("webhook não encontrado")
	}
	return nil
}

func (q *mobileQueries) ToggleWebhookSubscription(ctx context.Context, id, userID string, active bool) error {
	_, err := q.sql.ExecContext(ctx,
		"UPDATE webhook_subscriptions SET active=$1, updated_at=NOW() WHERE id=$2 AND user_id=$3::uuid",
		active, id, userID)
	return err
}

// ─── Webhook deliveries (used by WebhookDeliveryWorker) ──────────────────────

func (q *mobileQueries) EnqueueWebhookDelivery(ctx context.Context, subscriptionID, eventType string, payload []byte) error {
	_, err := q.sql.ExecContext(ctx, `
		INSERT INTO webhook_deliveries (subscription_id, event_type, payload, status)
		VALUES ($1, $2, $3::jsonb, 'pending')`,
		subscriptionID, eventType, string(payload))
	return err
}

func (q *mobileQueries) FetchPendingDeliveries(ctx context.Context, limit int) ([]models.WebhookDelivery, error) {
	rows, err := q.sql.QueryContext(ctx, `
		SELECT id, subscription_id, event_type, payload::text, status, attempts,
		       next_retry_at, response_status, response_body, last_error, created_at, updated_at
		FROM webhook_deliveries
		WHERE status IN ('pending','retrying') AND (next_retry_at IS NULL OR next_retry_at <= NOW())
		ORDER BY created_at ASC LIMIT $1
		FOR UPDATE SKIP LOCKED`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.WebhookDelivery
	for rows.Next() {
		d := models.WebhookDelivery{}
		_ = rows.Scan(&d.ID, &d.SubscriptionID, &d.EventType, &d.Payload, &d.Status,
			&d.Attempts, &d.NextRetryAt, &d.ResponseStatus, &d.ResponseBody, &d.LastError,
			&d.CreatedAt, &d.UpdatedAt)
		out = append(out, d)
	}
	return out, rows.Err()
}

func (q *mobileQueries) MarkDeliveryResult(ctx context.Context, id string, status models.WebhookDeliveryStatus, respStatus int, respBody, errMsg string, nextRetry *time.Time) error {
	_, err := q.sql.ExecContext(ctx, `
		UPDATE webhook_deliveries
		SET status=$2, attempts=attempts+1,
		    response_status=$3, response_body=NULLIF($4,''),
		    last_error=NULLIF($5,''), next_retry_at=$6, updated_at=NOW()
		WHERE id=$1`, id, string(status), respStatus, respBody, errMsg, nextRetry)
	return err
}

// ─── Subscriptions for event dispatch ─────────────────────────────────────────

// SubscriptionsForEvent returns all active webhook subscriptions listening to a given event.
func (q *mobileQueries) SubscriptionsForEvent(ctx context.Context, eventType string) ([]models.WebhookSubscription, error) {
	rows, err := q.sql.QueryContext(ctx, `
		SELECT id, user_id::text, target_url, secret, events, active, created_at, updated_at
		FROM webhook_subscriptions
		WHERE active=true AND ($1 = ANY(events) OR '*' = ANY(events))`,
		eventType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.WebhookSubscription
	for rows.Next() {
		ws := models.WebhookSubscription{}
		var events pq.StringArray
		_ = rows.Scan(&ws.ID, &ws.UserID, &ws.TargetURL, &ws.Secret, &events,
			&ws.Active, &ws.CreatedAt, &ws.UpdatedAt)
		ws.Events = []string(events)
		out = append(out, ws)
	}
	return out, rows.Err()
}
