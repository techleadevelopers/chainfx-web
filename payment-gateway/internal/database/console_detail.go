package database

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// ─── Agent Detail ─────────────────────────────────────────────────────────────

// AgentDetail is a full view of one agent: identity, policy, recent activity.
type AgentDetail struct {
	AgentID        string                   `json:"agentId"`
	Name           string                   `json:"name"`
	Status         string                   `json:"status"`
	Wallet         string                   `json:"wallet"`
	CapabilityList []string                 `json:"capabilities"`
	SpendUSDT      string                   `json:"spendUsdt"`
	QuotaRemaining int                      `json:"quotaRemaining"`
	LastActivityAt *time.Time               `json:"lastActivityAt,omitempty"`
	CreatedAt      time.Time                `json:"createdAt"`
	Policy         *AgentPolicy             `json:"policy,omitempty"`
	Purchases      []*AgentPurchaseSummary  `json:"recentPurchases"`
	Executions     []*AgentExecutionSummary `json:"recentExecutions"`
}

// AgentPurchaseSummary is a lightweight purchase row for the agent detail view.
type AgentPurchaseSummary struct {
	ID          string    `json:"id"`
	ProductID   string    `json:"productId"`
	Status      string    `json:"status"`
	GrossAmount string    `json:"grossAmount"`
	Asset       string    `json:"asset"`
	Network     string    `json:"network"`
	TakeRateBps int       `json:"takeRateBps"`
	ChainFXAmt  string    `json:"chainfxAmount"`
	ProviderAmt string    `json:"providerAmount"`
	CreatedAt   time.Time `json:"createdAt"`
}

// AgentExecutionSummary is a lightweight execution row for the agent detail view.
type AgentExecutionSummary struct {
	ID           string    `json:"id"`
	CapabilityID string    `json:"capability"`
	Operation    string    `json:"operation"`
	ProviderSlug string    `json:"provider"`
	Status       string    `json:"status"`
	Units        int       `json:"units"`
	LatencyMS    int       `json:"latencyMs"`
	ErrorCode    string    `json:"errorCode,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
}

// GetAgentDetail returns a full agent profile by agent_id or wallet address.
func (db *DB) GetAgentDetail(ctx context.Context, agentIDOrWallet string) (*AgentDetail, error) {
	agentIDOrWallet = strings.TrimSpace(agentIDOrWallet)
	row := db.SQL.QueryRowContext(ctx, `
		SELECT
		  a.agent_id,
		  COALESCE(NULLIF(a.name,''), a.agent_id),
		  a.status,
		  COALESCE(a.wallet,''),
		  COALESCE((
		    SELECT SUM(p.gross_amount)::text
		    FROM marketplace_purchases p
		    WHERE a.wallet IS NOT NULL AND lower(p.agent_wallet) = lower(a.wallet)
		  ),'0'),
		  COALESCE((
		    SELECT SUM(g.quota_remaining)
		    FROM api_access_grants g
		    WHERE a.wallet IS NOT NULL AND lower(g.buyer_wallet) = lower(a.wallet)
		  ),0),
		  (SELECT MAX(last_seen) FROM (
		    SELECT MAX(p.created_at) AS last_seen FROM marketplace_purchases p
		    WHERE a.wallet IS NOT NULL AND lower(p.agent_wallet) = lower(a.wallet)
		    UNION ALL
		    SELECT MAX(e.created_at) AS last_seen
		    FROM marketplace_execution_events e
		    JOIN api_access_grants g ON g.id = e.grant_id
		    WHERE a.wallet IS NOT NULL AND lower(g.buyer_wallet) = lower(a.wallet)
		  ) activity),
		  a.created_at
		FROM marketplace_agent_identities a
		WHERE a.agent_id = $1 OR lower(a.wallet) = lower($1)
		LIMIT 1`, agentIDOrWallet)

	detail := &AgentDetail{Purchases: []*AgentPurchaseSummary{}, Executions: []*AgentExecutionSummary{}}
	var lastActivity sqlNullTime
	if err := row.Scan(
		&detail.AgentID, &detail.Name, &detail.Status, &detail.Wallet,
		&detail.SpendUSDT, &detail.QuotaRemaining, &lastActivity, &detail.CreatedAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if lastActivity.Valid {
		detail.LastActivityAt = &lastActivity.Time
	}

	// Capabilities from JSON column
	capRows, err := db.SQL.QueryContext(ctx, `
		SELECT jsonb_array_elements_text(COALESCE(capabilities_json,'[]'::jsonb))
		FROM marketplace_agent_identities WHERE agent_id = $1`, detail.AgentID)
	if err == nil {
		defer capRows.Close()
		for capRows.Next() {
			var cap string
			_ = capRows.Scan(&cap)
			detail.CapabilityList = append(detail.CapabilityList, cap)
		}
	}
	if detail.CapabilityList == nil {
		detail.CapabilityList = []string{}
	}

	// Policy from DB
	detail.Policy, _ = db.GetAgentPolicy(ctx, detail.AgentID)

	// Recent purchases (last 10)
	if detail.Wallet != "" {
		pRows, err := db.SQL.QueryContext(ctx, `
			SELECT id, product_id, status,
			       gross_amount::text, payment_asset, network,
			       take_rate_bps, chainfx_amount::text, provider_amount::text, created_at
			FROM marketplace_purchases
			WHERE lower(agent_wallet) = lower($1)
			ORDER BY created_at DESC LIMIT 10`, detail.Wallet)
		if err == nil {
			defer pRows.Close()
			for pRows.Next() {
				p := &AgentPurchaseSummary{}
				if err := pRows.Scan(&p.ID, &p.ProductID, &p.Status, &p.GrossAmount,
					&p.Asset, &p.Network, &p.TakeRateBps, &p.ChainFXAmt, &p.ProviderAmt, &p.CreatedAt); err == nil {
					detail.Purchases = append(detail.Purchases, p)
				}
			}
		}
	}

	// Recent executions (last 10) via grant buyer_wallet
	if detail.Wallet != "" {
		eRows, err := db.SQL.QueryContext(ctx, `
			SELECT e.id, e.capability_id, COALESCE(e.operation,'execute'),
			       e.provider_slug, e.status, COALESCE(e.units_consumed,1),
			       COALESCE(e.latency_ms,0), COALESCE(e.error_code,''), e.created_at
			FROM marketplace_execution_events e
			JOIN api_access_grants g ON g.id = e.grant_id
			WHERE lower(g.buyer_wallet) = lower($1)
			ORDER BY e.created_at DESC LIMIT 10`, detail.Wallet)
		if err == nil {
			defer eRows.Close()
			for eRows.Next() {
				ex := &AgentExecutionSummary{}
				if err := eRows.Scan(&ex.ID, &ex.CapabilityID, &ex.Operation,
					&ex.ProviderSlug, &ex.Status, &ex.Units, &ex.LatencyMS,
					&ex.ErrorCode, &ex.CreatedAt); err == nil {
					detail.Executions = append(detail.Executions, ex)
				}
			}
		}
	}

	return detail, nil
}

func (db *DB) GetAgentDetailForProject(ctx context.Context, projectID, agentIDOrWallet string) (*AgentDetail, error) {
	projectID = strings.TrimSpace(projectID)
	agentIDOrWallet = strings.TrimSpace(agentIDOrWallet)
	if projectID == "" || agentIDOrWallet == "" {
		return nil, nil
	}
	var owned bool
	if err := db.SQL.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM marketplace_agent_identities a
			JOIN developer_project_agents dpa ON dpa.agent_id = a.agent_id
			WHERE dpa.project_id = $1
			  AND (a.agent_id = $2 OR lower(a.wallet) = lower($2))
		)`, projectID, agentIDOrWallet).Scan(&owned); err != nil {
		return nil, err
	}
	if !owned {
		return nil, nil
	}
	return db.GetAgentDetail(ctx, agentIDOrWallet)
}

// ─── Execution Detail ─────────────────────────────────────────────────────────

// GetExecutionDetail returns a full capability execution by ID.
func (db *DB) GetExecutionDetail(ctx context.Context, id string) (*MarketplaceCapabilityExecution, error) {
	id = strings.TrimSpace(id)
	var errorCode, errorMessage sql.NullString
	event := &MarketplaceCapabilityExecution{}
	err := db.SQL.QueryRowContext(ctx, `
		SELECT id, capability_id, provider_slug, provider_name, route_name, routing_mode,
		       operation, request_id, idempotency_key, units_consumed, quota_remaining,
		       status, COALESCE(input_json,'{}'), COALESCE(output_json,'{}'),
		       COALESCE(latency_ms,0), error_code, error_message, created_at
		FROM marketplace_execution_events
		WHERE id = $1`, id).Scan(
		&event.ID, &event.CapabilityID, &event.ProviderSlug, &event.ProviderName,
		&event.RouteName, &event.RoutingMode, &event.Operation, &event.RequestID,
		&event.IdempotencyKey, &event.UnitsConsumed, &event.QuotaRemaining,
		&event.Status, &event.Input, &event.Output,
		&event.LatencyMS, &errorCode, &errorMessage, &event.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if errorCode.Valid {
		event.ErrorCode = errorCode.String
	}
	if errorMessage.Valid {
		event.ErrorMessage = errorMessage.String
	}
	return event, nil
}

func (db *DB) GetExecutionDetailForProject(ctx context.Context, projectID, id string) (*MarketplaceCapabilityExecution, error) {
	projectID = strings.TrimSpace(projectID)
	id = strings.TrimSpace(id)
	if projectID == "" || id == "" {
		return nil, nil
	}
	var owned bool
	if err := db.SQL.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM marketplace_execution_events e
			JOIN api_access_grants g ON g.id = e.grant_id
			JOIN marketplace_agent_identities a ON lower(a.wallet) = lower(g.buyer_wallet)
			JOIN developer_project_agents dpa ON dpa.agent_id = a.agent_id
			WHERE dpa.project_id = $1
			  AND e.id = $2
		)`, projectID, id).Scan(&owned); err != nil {
		return nil, err
	}
	if !owned {
		return nil, nil
	}
	return db.GetExecutionDetail(ctx, id)
}

// ─── Purchase Detail ──────────────────────────────────────────────────────────

// PurchaseDetail is a full view of one purchase including plan + product.
type PurchaseDetail struct {
	*MarketplacePurchase
	ProductName  string `json:"productName,omitempty"`
	PlanName     string `json:"planName,omitempty"`
	ProviderName string `json:"providerName,omitempty"`
	Quota        int    `json:"quota,omitempty"`
	ValiditySecs int    `json:"validitySeconds,omitempty"`
}

// GetPurchaseDetail returns a purchase enriched with plan/product names.
func (db *DB) GetPurchaseDetail(ctx context.Context, id string) (*PurchaseDetail, error) {
	id = strings.TrimSpace(id)
	base, err := db.GetMarketplacePurchase(ctx, id)
	if err != nil || base == nil {
		return nil, err
	}
	detail := &PurchaseDetail{MarketplacePurchase: base}
	_ = db.SQL.QueryRowContext(ctx, `
		SELECT COALESCE(pr.name,''), COALESCE(pl.name,''), COALESCE(prov.name,''),
		       COALESCE(pl.quota,0), COALESCE(pl.validity_seconds,0)
		FROM marketplace_products pr
		LEFT JOIN marketplace_plans pl ON pl.id = $2
		LEFT JOIN marketplace_providers prov ON prov.id = pr.provider_id
		WHERE pr.id = $1`, base.ProductID, base.PlanID).Scan(
		&detail.ProductName, &detail.PlanName, &detail.ProviderName,
		&detail.Quota, &detail.ValiditySecs)
	return detail, nil
}

func (db *DB) GetPurchaseDetailForProject(ctx context.Context, projectID, id string) (*PurchaseDetail, error) {
	projectID = strings.TrimSpace(projectID)
	id = strings.TrimSpace(id)
	if projectID == "" || id == "" {
		return nil, nil
	}
	var owned bool
	if err := db.SQL.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM marketplace_purchases p
			JOIN marketplace_agent_identities a ON lower(a.wallet) = lower(p.agent_wallet)
			JOIN developer_project_agents dpa ON dpa.agent_id = a.agent_id
			WHERE dpa.project_id = $1
			  AND p.id = $2
		)`, projectID, id).Scan(&owned); err != nil {
		return nil, err
	}
	if !owned {
		return nil, nil
	}
	return db.GetPurchaseDetail(ctx, id)
}

// ─── List helpers for agent detail ───────────────────────────────────────────

// ListAgentPolicies returns all agent policies for the agent console.
func (db *DB) ListAgentPolicies(ctx context.Context, limit int) ([]*AgentPolicy, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := db.SQL.QueryContext(ctx,
		`SELECT `+agentPolicyColumns()+` FROM marketplace_agent_policies ORDER BY updated_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*AgentPolicy
	for rows.Next() {
		p, err := scanAgentPolicy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
