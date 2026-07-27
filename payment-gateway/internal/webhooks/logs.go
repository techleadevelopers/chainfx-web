package webhooks

import (
	"context"

	"payment-gateway/internal/database"
)

// Logs exposes read access to persisted webhook delivery attempts, backing
// the /api/webhooks/subscriptions/{id}/logs endpoint and the dashboard.
type Logs struct {
	db *database.DB
}

// NewLogs builds a Logs reader bound to db.
func NewLogs(db *database.DB) *Logs {
	return &Logs{db: db}
}

// ForSubscription returns the most recent delivery attempts for a
// subscription, newest first.
func (l *Logs) ForSubscription(ctx context.Context, subscriptionID string, limit int) ([]*database.WebhookDelivery, error) {
	return l.db.ListWebhookDeliveries(ctx, subscriptionID, limit)
}
