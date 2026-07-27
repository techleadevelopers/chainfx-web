package webhooks

import (
	"context"
	"fmt"
	"strings"

	"payment-gateway/internal/database"
)

// Registry manages outbound automation subscriptions (create/list/delete)
// on top of the database, enforcing the SSRF and event-name rules shared by
// the REST API, the MCP tools and any future caller.
type Registry struct {
	db *database.DB
}

// NewRegistry builds a Registry bound to db.
func NewRegistry(db *database.DB) *Registry {
	return &Registry{db: db}
}

// Create validates and persists a new webhook subscription.
func (reg *Registry) Create(ctx context.Context, provider, targetURL, secret, description string, events []string) (*database.WebhookSubscription, error) {
	targetURL = strings.TrimSpace(targetURL)
	if err := ValidateTargetURL(targetURL); err != nil {
		return nil, err
	}
	if provider == "" {
		provider = ProviderGeneric
	}
	var validEvents []string
	for _, e := range events {
		if IsKnownEvent(e) {
			validEvents = append(validEvents, e)
		}
	}
	if len(validEvents) == 0 {
		return nil, fmt.Errorf("events deve conter pelo menos um evento válido: %v", AllEvents())
	}
	// Web/admin callers have no MCP API key — pass empty hash, "web" origin.
	return reg.db.CreateWebhookSubscription(ctx, provider, targetURL, secret, description, "", "web", validEvents)
}

// Get returns a single subscription by id.
func (reg *Registry) Get(ctx context.Context, id string) (*database.WebhookSubscription, error) {
	return reg.db.GetWebhookSubscription(ctx, id)
}

// List returns every configured subscription.
func (reg *Registry) List(ctx context.Context) ([]*database.WebhookSubscription, error) {
	return reg.db.ListWebhookSubscriptions(ctx)
}

// ActiveForEvent returns active subscriptions listening to event.
func (reg *Registry) ActiveForEvent(ctx context.Context, event string) ([]*database.WebhookSubscription, error) {
	return reg.db.ListActiveWebhookSubscriptionsForEvent(ctx, event)
}

// Delete removes a subscription.
func (reg *Registry) Delete(ctx context.Context, id string) error {
	return reg.db.DeleteWebhookSubscription(ctx, id)
}

// SetActive enables or disables a subscription without deleting it.
func (reg *Registry) SetActive(ctx context.Context, id string, active bool) error {
	return reg.db.SetWebhookSubscriptionActive(ctx, id, active)
}
