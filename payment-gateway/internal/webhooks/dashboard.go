package webhooks

import (
	"context"

	"payment-gateway/internal/database"
)

// DashboardSummary aggregates subscription and delivery health for the
// operator-facing webhook dashboard (GET /api/webhooks/dashboard).
type DashboardSummary struct {
	Stats         *database.WebhookDeliveryStats  `json:"stats"`
	Subscriptions []*database.WebhookSubscription `json:"subscriptions"`
}

// Dashboard exposes an aggregated view over the registry and logs, so a
// single call renders a full webhook health dashboard.
type Dashboard struct {
	db *database.DB
}

// NewDashboard builds a Dashboard reader bound to db.
func NewDashboard(db *database.DB) *Dashboard {
	return &Dashboard{db: db}
}

// Summary returns subscription health stats plus the current subscription
// list (including each one's last delivery status).
func (d *Dashboard) Summary(ctx context.Context) (*DashboardSummary, error) {
	stats, err := d.db.WebhookDashboardStats(ctx)
	if err != nil {
		return nil, err
	}
	subs, err := d.db.ListWebhookSubscriptions(ctx)
	if err != nil {
		return nil, err
	}
	return &DashboardSummary{Stats: stats, Subscriptions: subs}, nil
}
