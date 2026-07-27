package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/lib/pq"
)

// WebhookSubscription is an outbound automation subscription (n8n, Zapier,
// Make.com or any generic HTTP receiver) that gets notified when platform
// events happen.
type WebhookSubscription struct {
	ID              string     `json:"id"`
	Provider        string     `json:"provider"`
	TargetURL       string     `json:"targetUrl"`
	Secret          string     `json:"-"`
	HasSecret       bool       `json:"hasSecret"`
	Events          []string   `json:"events"`
	Active          bool       `json:"active"`
	Description     string     `json:"description,omitempty"`
	// AgentKeyHash is the full SHA-256 hex of the MCP API key that created this
	// subscription. Empty for web/mobile origins. Used to enforce IDOR isolation:
	// MCP callers only see subscriptions matching their own key hash.
	AgentKeyHash string `json:"-"`
	// CreatedBy is "mcp" | "web" | "mobile". Populated after migration 004.
	CreatedBy       string     `json:"createdBy,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
	LastTriggeredAt *time.Time `json:"lastTriggeredAt,omitempty"`
	LastStatusCode  *int       `json:"lastStatusCode,omitempty"`
	LastError       *string    `json:"lastError,omitempty"`
	FailureCount    int        `json:"failureCount"`
}

// CreateWebhookSubscription inserts a new outbound automation subscription.
// CreateWebhookSubscription inserts a new outbound automation subscription.
// agentKeyHash: full SHA-256 hex of the MCP API key (pass "" for web/mobile).
// createdBy: "mcp" | "web" | "mobile" (defaults to "web" when empty).
func (db *DB) CreateWebhookSubscription(ctx context.Context, provider, targetURL, secret, description, agentKeyHash, createdBy string, events []string) (*WebhookSubscription, error) {
	id := NewID()
	if createdBy == "" {
		createdBy = "web"
	}
	var agentHash sql.NullString
	if agentKeyHash != "" {
		agentHash = sql.NullString{String: agentKeyHash, Valid: true}
	}
	_, err := db.SQL.ExecContext(ctx, `
		INSERT INTO webhook_subscriptions (id, provider, target_url, secret, events, description, agent_api_key_hash, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		id, provider, targetURL, secret, pq.Array(events), description, agentHash, createdBy)
	if err != nil {
		return nil, err
	}
	return db.GetWebhookSubscription(ctx, id)
}

// GetWebhookSubscription fetches a single subscription by id.
func (db *DB) GetWebhookSubscription(ctx context.Context, id string) (*WebhookSubscription, error) {
	row := db.SQL.QueryRowContext(ctx, `
		SELECT id, provider, target_url, secret, events, active, COALESCE(description, ''),
		       created_at, updated_at, last_triggered_at, last_status_code, last_error, failure_count,
		       COALESCE(agent_api_key_hash, ''), COALESCE(created_by, 'web')
		FROM webhook_subscriptions WHERE id = $1`, id)
	return scanWebhookSubscriptionFull(row)
}

// ListWebhookSubscriptions returns every configured subscription.
func (db *DB) ListWebhookSubscriptions(ctx context.Context) ([]*WebhookSubscription, error) {
	rows, err := db.SQL.QueryContext(ctx, `
		SELECT id, provider, target_url, secret, events, active, COALESCE(description, ''),
		       created_at, updated_at, last_triggered_at, last_status_code, last_error, failure_count
		FROM webhook_subscriptions ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*WebhookSubscription
	for rows.Next() {
		sub, err := scanWebhookSubscription(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

// ListWebhookSubscriptionsByAgent returns subscriptions owned by a specific MCP agent
// (filtered by agent_api_key_hash). If agentKeyHash is empty, returns all subscriptions
// (for admin/dashboard use). This is the IDOR-safe variant for MCP callers.
func (db *DB) ListWebhookSubscriptionsByAgent(ctx context.Context, agentKeyHash string) ([]*WebhookSubscription, error) {
	if agentKeyHash == "" {
		return db.ListWebhookSubscriptions(ctx)
	}
	rows, err := db.SQL.QueryContext(ctx, `
SELECT id, provider, target_url, secret, events, active, COALESCE(description, ''),
       created_at, updated_at, last_triggered_at, last_status_code, last_error, failure_count,
       COALESCE(agent_api_key_hash, ''), COALESCE(created_by, 'web')
FROM webhook_subscriptions
WHERE agent_api_key_hash = $1
ORDER BY created_at DESC`, agentKeyHash)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*WebhookSubscription
	for rows.Next() {
		sub, err := scanWebhookSubscriptionFull(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

// ListActiveWebhookSubscriptionsForEvent returns active subscriptions listening to eventType.
func (db *DB) ListActiveWebhookSubscriptionsForEvent(ctx context.Context, eventType string) ([]*WebhookSubscription, error) {
	rows, err := db.SQL.QueryContext(ctx, `
		SELECT id, provider, target_url, secret, events, active, COALESCE(description, ''),
		       created_at, updated_at, last_triggered_at, last_status_code, last_error, failure_count
		FROM webhook_subscriptions
		WHERE active = true AND ($1 = ANY(events) OR '*' = ANY(events))`, eventType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*WebhookSubscription
	for rows.Next() {
		sub, err := scanWebhookSubscription(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

// DeleteWebhookSubscription removes a subscription.
func (db *DB) DeleteWebhookSubscription(ctx context.Context, id string) error {
	_, err := db.SQL.ExecContext(ctx, `DELETE FROM webhook_subscriptions WHERE id = $1`, id)
	return err
}

// SetWebhookSubscriptionActive toggles a subscription on/off.
func (db *DB) SetWebhookSubscriptionActive(ctx context.Context, id string, active bool) error {
	_, err := db.SQL.ExecContext(ctx, `UPDATE webhook_subscriptions SET active = $2, updated_at = now() WHERE id = $1`, id, active)
	return err
}

// RecordWebhookDelivery stores a delivery attempt and updates subscription health fields.
func (db *DB) RecordWebhookDelivery(ctx context.Context, subscriptionID, event string, payload map[string]any, statusCode int, ok bool, deliveryErr string, attempt int) error {
	raw, _ := json.Marshal(payload)
	id := NewID()
	if _, err := db.SQL.ExecContext(ctx, `
		INSERT INTO webhook_deliveries (id, subscription_id, event, payload, status_code, ok, error, attempt)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		id, subscriptionID, event, raw, statusCode, ok, nullableString(deliveryErr), attempt); err != nil {
		return err
	}
	failureIncrement := 0
	if !ok {
		failureIncrement = 1
	}
	_, err := db.SQL.ExecContext(ctx, `
		UPDATE webhook_subscriptions
		SET last_triggered_at = now(),
		    last_status_code = $2,
		    last_error = $3,
		    failure_count = CASE WHEN $4 THEN 0 ELSE failure_count + $5 END,
		    updated_at = now()
		WHERE id = $1`, subscriptionID, statusCode, nullableString(deliveryErr), ok, failureIncrement)
	return err
}

// WebhookDelivery is a single logged delivery attempt for a subscription.
type WebhookDelivery struct {
	ID             string          `json:"id"`
	SubscriptionID string          `json:"subscriptionId"`
	Event          string          `json:"event"`
	Payload        json.RawMessage `json:"payload"`
	StatusCode     int             `json:"statusCode"`
	OK             bool            `json:"ok"`
	Error          *string         `json:"error,omitempty"`
	Attempt        int             `json:"attempt"`
	CreatedAt      time.Time       `json:"createdAt"`
}

// ListWebhookDeliveries returns the most recent delivery attempts for a
// subscription, newest first, capped at limit (defaults to 50).
func (db *DB) ListWebhookDeliveries(ctx context.Context, subscriptionID string, limit int) ([]*WebhookDelivery, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := db.SQL.QueryContext(ctx, `
		SELECT id, subscription_id, event, payload, status_code, ok, error, attempt, created_at
		FROM webhook_deliveries
		WHERE subscription_id = $1
		ORDER BY created_at DESC
		LIMIT $2`, subscriptionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*WebhookDelivery
	for rows.Next() {
		var d WebhookDelivery
		var errMsg sql.NullString
		if err := rows.Scan(&d.ID, &d.SubscriptionID, &d.Event, &d.Payload, &d.StatusCode, &d.OK, &errMsg, &d.Attempt, &d.CreatedAt); err != nil {
			return nil, err
		}
		if errMsg.Valid {
			d.Error = &errMsg.String
		}
		out = append(out, &d)
	}
	return out, rows.Err()
}

// WebhookDeliveryStats summarizes delivery health across all subscriptions
// (or a single one, when subscriptionID is non-empty) over the last 24h.
type WebhookDeliveryStats struct {
	TotalSubscriptions  int `json:"totalSubscriptions"`
	ActiveSubscriptions int `json:"activeSubscriptions"`
	Deliveries24h       int `json:"deliveries24h"`
	Failures24h         int `json:"failures24h"`
}

// WebhookDashboardStats aggregates subscription and delivery health for the
// webhook dashboard.
func (db *DB) WebhookDashboardStats(ctx context.Context) (*WebhookDeliveryStats, error) {
	var stats WebhookDeliveryStats
	if err := db.SQL.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(CASE WHEN active THEN 1 ELSE 0 END), 0)
		FROM webhook_subscriptions`).Scan(&stats.TotalSubscriptions, &stats.ActiveSubscriptions); err != nil {
		return nil, err
	}
	if err := db.SQL.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(CASE WHEN NOT ok THEN 1 ELSE 0 END), 0)
		FROM webhook_deliveries WHERE created_at > now() - interval '24 hours'`).Scan(&stats.Deliveries24h, &stats.Failures24h); err != nil {
		return nil, err
	}
	return &stats, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

// scanWebhookSubscription scans the legacy 13-column result (no agent columns).
// Used by ListWebhookSubscriptions and ListActiveWebhookSubscriptionsForEvent
// which join across all subscriptions without agent ownership context.
func scanWebhookSubscription(row rowScanner) (*WebhookSubscription, error) {
	var sub WebhookSubscription
	var secret sql.NullString
	var lastTriggeredAt sql.NullTime
	var lastStatusCode sql.NullInt64
	var lastError sql.NullString
	if err := row.Scan(
		&sub.ID, &sub.Provider, &sub.TargetURL, &secret, pq.Array(&sub.Events), &sub.Active, &sub.Description,
		&sub.CreatedAt, &sub.UpdatedAt, &lastTriggeredAt, &lastStatusCode, &lastError, &sub.FailureCount,
	); err != nil {
		return nil, err
	}
	sub.Secret = secret.String
	sub.HasSecret = secret.Valid && secret.String != ""
	if lastTriggeredAt.Valid {
		sub.LastTriggeredAt = &lastTriggeredAt.Time
	}
	if lastStatusCode.Valid {
		v := int(lastStatusCode.Int64)
		sub.LastStatusCode = &v
	}
	if lastError.Valid {
		sub.LastError = &lastError.String
	}
	return &sub, nil
}

// scanWebhookSubscriptionFull scans the 15-column result that includes
// agent_api_key_hash and created_by (available after migration 004).
func scanWebhookSubscriptionFull(row rowScanner) (*WebhookSubscription, error) {
	var sub WebhookSubscription
	var secret sql.NullString
	var lastTriggeredAt sql.NullTime
	var lastStatusCode sql.NullInt64
	var lastError sql.NullString
	var agentHash, createdBy string
	if err := row.Scan(
		&sub.ID, &sub.Provider, &sub.TargetURL, &secret, pq.Array(&sub.Events), &sub.Active, &sub.Description,
		&sub.CreatedAt, &sub.UpdatedAt, &lastTriggeredAt, &lastStatusCode, &lastError, &sub.FailureCount,
		&agentHash, &createdBy,
	); err != nil {
		return nil, err
	}
	sub.Secret = secret.String
	sub.HasSecret = secret.Valid && secret.String != ""
	sub.AgentKeyHash = agentHash
	sub.CreatedBy = createdBy
	if lastTriggeredAt.Valid {
		sub.LastTriggeredAt = &lastTriggeredAt.Time
	}
	if lastStatusCode.Valid {
		v := int(lastStatusCode.Int64)
		sub.LastStatusCode = &v
	}
	if lastError.Valid {
		sub.LastError = &lastError.String
	}
	return &sub, nil
}
