package database

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type APIRequestLogInput struct {
	RequestID    string
	Method       string
	Path         string
	RouteClass   string
	StatusCode   int
	DurationMS   int64
	APIKeyHash   string
	APIKeyScope  string
	AuthMode     string
	ClientIP     string
	UserAgent    string
	AgentID      string
	AgentSigHash string
}

type APIRequestLog struct {
	ID           string    `json:"id"`
	RequestID    string    `json:"requestId"`
	Method       string    `json:"method"`
	Path         string    `json:"path"`
	RouteClass   string    `json:"routeClass"`
	StatusCode   int       `json:"statusCode"`
	DurationMS   int64     `json:"durationMs"`
	APIKeyHash   string    `json:"apiKeyHash,omitempty"`
	APIKeyScope  string    `json:"apiKeyScope,omitempty"`
	AuthMode     string    `json:"authMode,omitempty"`
	ClientIP     string    `json:"clientIp,omitempty"`
	UserAgent    string    `json:"userAgent,omitempty"`
	AgentID      string    `json:"agentId,omitempty"`
	AgentSigHash string    `json:"agentSignatureHash,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
}

type MCPToolLogInput struct {
	RequestID    string
	ToolName     string
	Status       string
	ErrorMessage string
	DurationMS   int64
	APIKeyHash   string
	AuthMode     string
	AgentID      string
	AgentSigHash string
}

type MCPToolLog struct {
	ID           string    `json:"id"`
	RequestID    string    `json:"requestId,omitempty"`
	ToolName     string    `json:"toolName"`
	Status       string    `json:"status"`
	ErrorMessage string    `json:"errorMessage,omitempty"`
	DurationMS   int64     `json:"durationMs"`
	APIKeyHash   string    `json:"apiKeyHash,omitempty"`
	AuthMode     string    `json:"authMode,omitempty"`
	AgentID      string    `json:"agentId,omitempty"`
	AgentSigHash string    `json:"agentSignatureHash,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
}

type MarketplacePurchaseSummary struct {
	ID             string    `json:"id"`
	ProductID      string    `json:"productId"`
	PlanID         string    `json:"planId"`
	AgentWallet    string    `json:"agentWallet"`
	PaymentAsset   string    `json:"paymentAsset"`
	Network        string    `json:"network"`
	GrossAmount    string    `json:"grossAmount"`
	ProviderAmount string    `json:"providerAmount"`
	Status         string    `json:"status"`
	TxHash         string    `json:"txHash,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
}

type MarketplaceUsageSummary struct {
	ID             string    `json:"id"`
	CapabilityID   string    `json:"capability"`
	ProviderSlug   string    `json:"provider"`
	RoutingMode    string    `json:"routingMode"`
	Operation      string    `json:"operation"`
	Status         string    `json:"status"`
	UnitsConsumed  int       `json:"unitsConsumed"`
	QuotaRemaining int       `json:"quotaRemaining"`
	LatencyMS      int       `json:"latencyMs"`
	RequestID      string    `json:"requestId"`
	CreatedAt      time.Time `json:"createdAt"`
}

type AgentDiscoveryAnalytics struct {
	Window                string                     `json:"window"`
	GeneratedAt           time.Time                  `json:"generatedAt"`
	DiscoveryRequests     int                        `json:"discoveryRequests"`
	EstimatedUniqueScouts int                        `json:"estimatedUniqueScouts"`
	AuthenticatedCallers  int                        `json:"authenticatedCallers"`
	MCPToolCalls          int                        `json:"mcpToolCalls"`
	MCPToolErrors         int                        `json:"mcpToolErrors"`
	A2ACalls              int                        `json:"a2aCalls"`
	ConnectedAgents       int                        `json:"connectedAgents"`
	PaymentIntents        int                        `json:"paymentIntents"`
	PayingAgentWallets    int                        `json:"payingAgentWallets"`
	MarketplacePurchases  int                        `json:"marketplacePurchases"`
	CapabilityExecutions  int                        `json:"capabilityExecutions"`
	FailedPaymentIntents  int                        `json:"failedPaymentIntents"`
	PaymentRetryAttempts  int                        `json:"paymentRetryAttempts"`
	DiscoveryToActionRate string                     `json:"discoveryToActionRate"`
	TopDiscoveryEndpoints []AgentEndpointStat        `json:"topDiscoveryEndpoints"`
	TopAgentUserAgents    []AgentUserAgentStat       `json:"topAgentUserAgents"`
	TopMCPTools           []AgentMCPToolStat         `json:"topMcpTools"`
	CapabilityConversions []CapabilityConversionStat `json:"capabilityConversions"`
	Funnel                map[string]int             `json:"funnel"`
	Notes                 []string                   `json:"notes"`
}

type AgentEndpointStat struct {
	Path     string    `json:"path"`
	Count    int       `json:"count"`
	Unique   int       `json:"unique"`
	LastSeen time.Time `json:"lastSeen"`
}

type AgentUserAgentStat struct {
	UserAgent string    `json:"userAgent"`
	Count     int       `json:"count"`
	UniqueIPs int       `json:"uniqueIps"`
	LastSeen  time.Time `json:"lastSeen"`
}

type AgentMCPToolStat struct {
	ToolName string    `json:"toolName"`
	Count    int       `json:"count"`
	Errors   int       `json:"errors"`
	AvgMS    int       `json:"avgMs"`
	LastSeen time.Time `json:"lastSeen"`
}

type CapabilityConversionStat struct {
	CapabilityID         string    `json:"capabilityId"`
	Explorations         int       `json:"explorations"`
	Purchases            int       `json:"purchases"`
	Executions           int       `json:"executions"`
	ExecutionFailures    int       `json:"executionFailures"`
	AvgExecutionMS       int       `json:"avgExecutionMs"`
	LastSeen             time.Time `json:"lastSeen"`
	ExplorationToBuyRate string    `json:"explorationToBuyRate"`
}

type DeveloperDashboardSummary struct {
	GeneratedAt time.Time                     `json:"generatedAt"`
	Counts      map[string]int                `json:"counts"`
	Health      map[string]any                `json:"health"`
	APILogs     []*APIRequestLog              `json:"apiLogs"`
	MCPLogs     []*MCPToolLog                 `json:"mcpLogs"`
	Purchases   []*MarketplacePurchaseSummary `json:"purchases"`
	Usage       []*MarketplaceUsageSummary    `json:"usage"`
	Webhooks    *WebhookDeliveryStats         `json:"webhooks,omitempty"`
	AgentFunnel *AgentDiscoveryAnalytics      `json:"agentFunnel,omitempty"`
}

func (db *DB) RecordAPIRequestLog(ctx context.Context, in APIRequestLogInput) error {
	if db == nil || db.SQL == nil {
		return nil
	}
	_, err := db.SQL.ExecContext(ctx, `
		INSERT INTO api_request_logs (
		  id, request_id, method, path, route_class, status_code, duration_ms,
		  api_key_hash, api_key_scope, auth_mode, client_ip, user_agent, agent_id, agent_signature_hash
		) VALUES ($1,$2,$3,$4,$5,$6,$7,NULLIF($8,''),NULLIF($9,''),NULLIF($10,''),NULLIF($11,''),NULLIF($12,''),NULLIF($13,''),NULLIF($14,''))`,
		NewID(), strings.TrimSpace(in.RequestID), strings.ToUpper(strings.TrimSpace(in.Method)),
		strings.TrimSpace(in.Path), strings.TrimSpace(in.RouteClass), in.StatusCode, in.DurationMS,
		strings.TrimSpace(in.APIKeyHash), strings.TrimSpace(in.APIKeyScope), strings.TrimSpace(in.AuthMode),
		strings.TrimSpace(in.ClientIP), strings.TrimSpace(in.UserAgent), strings.TrimSpace(in.AgentID), strings.TrimSpace(in.AgentSigHash))
	return err
}

func (db *DB) RecordMCPToolLog(ctx context.Context, in MCPToolLogInput) error {
	if db == nil || db.SQL == nil {
		return nil
	}
	status := strings.TrimSpace(in.Status)
	if status == "" {
		status = "ok"
	}
	_, err := db.SQL.ExecContext(ctx, `
		INSERT INTO mcp_tool_logs (
		  id, request_id, tool_name, status, error_message, duration_ms, api_key_hash, auth_mode, agent_id, agent_signature_hash
		) VALUES ($1,NULLIF($2,''),$3,$4,NULLIF($5,''),$6,NULLIF($7,''),NULLIF($8,''),NULLIF($9,''),NULLIF($10,''))`,
		NewID(), strings.TrimSpace(in.RequestID), strings.TrimSpace(in.ToolName), status,
		strings.TrimSpace(in.ErrorMessage), in.DurationMS, strings.TrimSpace(in.APIKeyHash), strings.TrimSpace(in.AuthMode),
		strings.TrimSpace(in.AgentID), strings.TrimSpace(in.AgentSigHash))
	return err
}

func (db *DB) DeveloperDashboard(ctx context.Context, limit int) (*DeveloperDashboardSummary, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	apiLogs, err := db.ListAPIRequestLogs(ctx, limit)
	if err != nil {
		return nil, err
	}
	mcpLogs, err := db.ListMCPToolLogs(ctx, limit)
	if err != nil {
		return nil, err
	}
	purchases, err := db.ListMarketplacePurchaseSummaries(ctx, limit)
	if err != nil {
		return nil, err
	}
	usage, err := db.ListMarketplaceUsageSummaries(ctx, limit)
	if err != nil {
		return nil, err
	}
	webhookStats, _ := db.WebhookDashboardStats(ctx)
	agentFunnel, _ := db.AgentDiscoveryAnalytics(ctx)
	counts, err := db.developerDashboardCounts(ctx)
	if err != nil {
		return nil, err
	}
	return &DeveloperDashboardSummary{
		GeneratedAt: time.Now().UTC(),
		Counts:      counts,
		Health: map[string]any{
			"apiRequestLogging": true,
			"mcpToolLogging":    true,
			"payloadStorage":    "redacted",
		},
		APILogs:     apiLogs,
		MCPLogs:     mcpLogs,
		Purchases:   purchases,
		Usage:       usage,
		Webhooks:    webhookStats,
		AgentFunnel: agentFunnel,
	}, nil
}

func (db *DB) AgentDiscoveryAnalytics(ctx context.Context) (*AgentDiscoveryAnalytics, error) {
	if db == nil || db.SQL == nil {
		return nil, nil
	}
	const window = "24h"
	out := &AgentDiscoveryAnalytics{
		Window:      window,
		GeneratedAt: time.Now().UTC(),
		Notes: []string{
			"estimatedUniqueScouts deduplicates by X-Agent-ID when present, then API key hash, otherwise by client IP plus User-Agent.",
			"Authenticated callers and connected/paying wallets are stronger signals of real agents than anonymous discovery hits.",
			"Browser, curl and generic synthetic clients can still appear in discovery counts until callers send an explicit agent identity header.",
		},
	}
	scalarQueries := map[*int]string{
		&out.DiscoveryRequests: `SELECT COUNT(*) FROM api_request_logs WHERE created_at > now() - interval '24 hours' AND (
			route_class IN ('public_discovery','discovery') OR path IN ('/agent-pay.json','/mcp/capabilities.json') OR path LIKE '/.well-known/%'
		)`,
		&out.EstimatedUniqueScouts: `SELECT COUNT(DISTINCT COALESCE(NULLIF(agent_id,''), NULLIF(api_key_hash,''), 'anon:' || COALESCE(client_ip,'') || ':' || COALESCE(user_agent,''))) FROM api_request_logs WHERE created_at > now() - interval '24 hours' AND (
			route_class IN ('public_discovery','discovery') OR path IN ('/agent-pay.json','/mcp/capabilities.json') OR path LIKE '/.well-known/%'
		)`,
		&out.AuthenticatedCallers: `SELECT COUNT(DISTINCT api_key_hash) FROM api_request_logs WHERE created_at > now() - interval '24 hours' AND COALESCE(api_key_hash,'') <> '' AND (
			path LIKE '/mcp/%' OR path LIKE '/agent/%' OR path LIKE '/a2a%' OR path LIKE '/marketplace/%' OR path LIKE '/x402/%'
		)`,
		&out.MCPToolCalls:         `SELECT COUNT(*) FROM mcp_tool_logs WHERE created_at > now() - interval '24 hours'`,
		&out.MCPToolErrors:        `SELECT COUNT(*) FROM mcp_tool_logs WHERE created_at > now() - interval '24 hours' AND status <> 'ok'`,
		&out.A2ACalls:             `SELECT COUNT(*) FROM api_request_logs WHERE created_at > now() - interval '24 hours' AND path LIKE '/a2a%'`,
		&out.ConnectedAgents:      `SELECT COUNT(*) FROM marketplace_agent_identities WHERE created_at > now() - interval '24 hours'`,
		&out.PaymentIntents:       `SELECT COUNT(*) FROM agent_payment_intents WHERE created_at > now() - interval '24 hours'`,
		&out.PayingAgentWallets:   `SELECT COUNT(DISTINCT lower(agent_wallet)) FROM agent_payment_intents WHERE created_at > now() - interval '24 hours'`,
		&out.MarketplacePurchases: `SELECT COUNT(*) FROM marketplace_purchases WHERE created_at > now() - interval '24 hours'`,
		&out.CapabilityExecutions: `SELECT COUNT(*) FROM marketplace_execution_events WHERE created_at > now() - interval '24 hours'`,
		&out.FailedPaymentIntents: `SELECT COUNT(*) FROM agent_payment_intents WHERE created_at > now() - interval '24 hours' AND status IN ('failed','expired')`,
		&out.PaymentRetryAttempts: `SELECT COALESCE(SUM(attempts), 0)::int FROM agent_payment_intents WHERE created_at > now() - interval '24 hours'`,
	}
	for dest, query := range scalarQueries {
		if err := db.SQL.QueryRowContext(ctx, query).Scan(dest); err != nil {
			return nil, err
		}
	}
	actions := out.A2ACalls + out.PaymentIntents + out.MarketplacePurchases + out.CapabilityExecutions
	if out.EstimatedUniqueScouts > 0 {
		out.DiscoveryToActionRate = formatPercent(float64(actions) / float64(out.EstimatedUniqueScouts))
	} else {
		out.DiscoveryToActionRate = "0.00%"
	}
	out.Funnel = map[string]int{
		"discoveryRequests":     out.DiscoveryRequests,
		"estimatedUniqueScouts": out.EstimatedUniqueScouts,
		"authenticatedCallers":  out.AuthenticatedCallers,
		"connectedAgents":       out.ConnectedAgents,
		"mcpToolCalls":          out.MCPToolCalls,
		"a2aCalls":              out.A2ACalls,
		"paymentIntents":        out.PaymentIntents,
		"failedPaymentIntents":  out.FailedPaymentIntents,
		"paymentRetryAttempts":  out.PaymentRetryAttempts,
		"marketplacePurchases":  out.MarketplacePurchases,
		"capabilityExecutions":  out.CapabilityExecutions,
	}
	endpoints, err := db.ListTopAgentDiscoveryEndpoints(ctx, 10)
	if err != nil {
		return nil, err
	}
	out.TopDiscoveryEndpoints = endpoints
	userAgents, err := db.ListTopAgentUserAgents(ctx, 10)
	if err != nil {
		return nil, err
	}
	out.TopAgentUserAgents = userAgents
	tools, err := db.ListTopAgentMCPTools(ctx, 10)
	if err != nil {
		return nil, err
	}
	out.TopMCPTools = tools
	conversions, err := db.ListCapabilityConversions(ctx, 20)
	if err != nil {
		return nil, err
	}
	out.CapabilityConversions = conversions
	return out, nil
}

func (db *DB) ListTopAgentDiscoveryEndpoints(ctx context.Context, limit int) ([]AgentEndpointStat, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	rows, err := db.SQL.QueryContext(ctx, `
		SELECT path, COUNT(*)::int,
		       COUNT(DISTINCT COALESCE(NULLIF(agent_id,''), NULLIF(api_key_hash,''), 'anon:' || COALESCE(client_ip,'') || ':' || COALESCE(user_agent,'')))::int,
		       MAX(created_at)
		FROM api_request_logs
		WHERE created_at > now() - interval '24 hours'
		  AND (route_class IN ('public_discovery','discovery') OR path IN ('/agent-pay.json','/mcp/capabilities.json') OR path LIKE '/.well-known/%')
		GROUP BY path
		ORDER BY COUNT(*) DESC, MAX(created_at) DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgentEndpointStat{}
	for rows.Next() {
		var item AgentEndpointStat
		if err := rows.Scan(&item.Path, &item.Count, &item.Unique, &item.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (db *DB) ListTopAgentUserAgents(ctx context.Context, limit int) ([]AgentUserAgentStat, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	rows, err := db.SQL.QueryContext(ctx, `
		SELECT COALESCE(NULLIF(user_agent,''), 'unknown') AS user_agent,
		       COUNT(*)::int,
		       COUNT(DISTINCT COALESCE(client_ip,''))::int,
		       MAX(created_at)
		FROM api_request_logs
		WHERE created_at > now() - interval '24 hours'
		  AND (path LIKE '/mcp/%' OR path LIKE '/agent/%' OR path LIKE '/a2a%' OR path LIKE '/.well-known/%' OR path = '/agent-pay.json')
		GROUP BY COALESCE(NULLIF(user_agent,''), 'unknown')
		ORDER BY COUNT(*) DESC, MAX(created_at) DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgentUserAgentStat{}
	for rows.Next() {
		var item AgentUserAgentStat
		if err := rows.Scan(&item.UserAgent, &item.Count, &item.UniqueIPs, &item.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (db *DB) ListTopAgentMCPTools(ctx context.Context, limit int) ([]AgentMCPToolStat, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	rows, err := db.SQL.QueryContext(ctx, `
		SELECT tool_name,
		       COUNT(*)::int,
		       COUNT(*) FILTER (WHERE status <> 'ok')::int,
		       COALESCE(ROUND(AVG(duration_ms)), 0)::int,
		       MAX(created_at)
		FROM mcp_tool_logs
		WHERE created_at > now() - interval '24 hours'
		GROUP BY tool_name
		ORDER BY COUNT(*) DESC, MAX(created_at) DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgentMCPToolStat{}
	for rows.Next() {
		var item AgentMCPToolStat
		if err := rows.Scan(&item.ToolName, &item.Count, &item.Errors, &item.AvgMS, &item.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (db *DB) ListCapabilityConversions(ctx context.Context, limit int) ([]CapabilityConversionStat, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	rows, err := db.SQL.QueryContext(ctx, `
		WITH raw_explored AS (
		  SELECT
		    CASE
		      WHEN path LIKE '/marketplace/capabilities/%' THEN trim(leading '/' FROM replace(path, '/marketplace/capabilities', ''))
		      WHEN path LIKE '/agent/v1/capabilities/%' THEN trim(leading '/' FROM replace(path, '/agent/v1/capabilities', ''))
		      ELSE ''
		    END AS raw_capability,
		    created_at
		  FROM api_request_logs
		  WHERE created_at > now() - interval '24 hours'
		    AND (
		      path LIKE '/marketplace/capabilities/%'
		      OR path LIKE '/agent/v1/capabilities/%'
		    )
		    AND path NOT LIKE '%/execute'
		    AND path NOT LIKE '%/route'
		),
		explored AS (
		  SELECT raw_capability, COUNT(*)::int AS explorations, MAX(created_at) AS last_seen
		  FROM raw_explored
		  GROUP BY raw_capability
		),
		explored_clean AS (
		  SELECT
		    NULLIF(split_part(raw_capability, '/', 1), '') AS capability_id,
		    SUM(explorations)::int AS explorations,
		    MAX(last_seen) AS last_seen
		  FROM explored
		  GROUP BY NULLIF(split_part(raw_capability, '/', 1), '')
		),
		purchased AS (
		  SELECT product_id AS capability_id, COUNT(*)::int AS purchases, MAX(created_at) AS last_seen
		  FROM marketplace_purchases
		  WHERE created_at > now() - interval '24 hours'
		  GROUP BY product_id
		),
		executed AS (
		  SELECT capability_id,
		         COUNT(*)::int AS executions,
		         COUNT(*) FILTER (WHERE status <> 'completed' OR COALESCE(error_code,'') <> '')::int AS failures,
		         COALESCE(ROUND(AVG(latency_ms)), 0)::int AS avg_ms,
		         MAX(created_at) AS last_seen
		  FROM marketplace_execution_events
		  WHERE created_at > now() - interval '24 hours'
		  GROUP BY capability_id
		),
		joined AS (
		  SELECT
		    COALESCE(e.capability_id, p.capability_id, x.capability_id) AS capability_id,
		    COALESCE(e.explorations, 0)::int AS explorations,
		    COALESCE(p.purchases, 0)::int AS purchases,
		    COALESCE(x.executions, 0)::int AS executions,
		    COALESCE(x.failures, 0)::int AS failures,
		    COALESCE(x.avg_ms, 0)::int AS avg_ms,
		    GREATEST(COALESCE(e.last_seen, 'epoch'::timestamptz), COALESCE(p.last_seen, 'epoch'::timestamptz), COALESCE(x.last_seen, 'epoch'::timestamptz)) AS last_seen
		  FROM explored_clean e
		  FULL JOIN purchased p ON p.capability_id = e.capability_id
		  FULL JOIN executed x ON x.capability_id = COALESCE(e.capability_id, p.capability_id)
		)
		SELECT capability_id, explorations, purchases, executions, failures, avg_ms, last_seen
		FROM joined
		WHERE COALESCE(capability_id, '') <> ''
		ORDER BY explorations DESC, purchases DESC, executions DESC, last_seen DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CapabilityConversionStat{}
	for rows.Next() {
		var item CapabilityConversionStat
		if err := rows.Scan(&item.CapabilityID, &item.Explorations, &item.Purchases, &item.Executions, &item.ExecutionFailures, &item.AvgExecutionMS, &item.LastSeen); err != nil {
			return nil, err
		}
		if item.Explorations > 0 {
			item.ExplorationToBuyRate = formatPercent(float64(item.Purchases) / float64(item.Explorations))
		} else {
			item.ExplorationToBuyRate = "0.00%"
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func formatPercent(value float64) string {
	return fmt.Sprintf("%.2f%%", value*100)
}

func (db *DB) developerDashboardCounts(ctx context.Context) (map[string]int, error) {
	counts := map[string]int{}
	queries := map[string]string{
		"apiLogs24h":         `SELECT COUNT(*) FROM api_request_logs WHERE created_at > now() - interval '24 hours'`,
		"mcpCalls24h":        `SELECT COUNT(*) FROM mcp_tool_logs WHERE created_at > now() - interval '24 hours'`,
		"mcpErrors24h":       `SELECT COUNT(*) FROM mcp_tool_logs WHERE created_at > now() - interval '24 hours' AND status <> 'ok'`,
		"purchases24h":       `SELECT COUNT(*) FROM marketplace_purchases WHERE created_at > now() - interval '24 hours'`,
		"activePurchases":    `SELECT COUNT(*) FROM marketplace_purchases WHERE status = 'active'`,
		"capabilityUsage24h": `SELECT COUNT(*) FROM marketplace_execution_events WHERE created_at > now() - interval '24 hours'`,
	}
	for key, query := range queries {
		var n int
		if err := db.SQL.QueryRowContext(ctx, query).Scan(&n); err != nil {
			return nil, err
		}
		counts[key] = n
	}
	return counts, nil
}

func (db *DB) ListAPIRequestLogs(ctx context.Context, limit int) ([]*APIRequestLog, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := db.SQL.QueryContext(ctx, `
		SELECT id::text, request_id, method, path, route_class, status_code, duration_ms,
		       COALESCE(api_key_hash,''), COALESCE(api_key_scope,''), COALESCE(auth_mode,''),
		       COALESCE(client_ip,''), COALESCE(user_agent,''), COALESCE(agent_id,''), COALESCE(agent_signature_hash,''), created_at
		FROM api_request_logs
		ORDER BY created_at DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*APIRequestLog{}
	for rows.Next() {
		item := &APIRequestLog{}
		if err := rows.Scan(&item.ID, &item.RequestID, &item.Method, &item.Path, &item.RouteClass,
			&item.StatusCode, &item.DurationMS, &item.APIKeyHash, &item.APIKeyScope, &item.AuthMode,
			&item.ClientIP, &item.UserAgent, &item.AgentID, &item.AgentSigHash, &item.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (db *DB) ListMCPToolLogs(ctx context.Context, limit int) ([]*MCPToolLog, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := db.SQL.QueryContext(ctx, `
		SELECT id::text, COALESCE(request_id,''), tool_name, status, COALESCE(error_message,''),
		       duration_ms, COALESCE(api_key_hash,''), COALESCE(auth_mode,''), COALESCE(agent_id,''), COALESCE(agent_signature_hash,''), created_at
		FROM mcp_tool_logs
		ORDER BY created_at DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*MCPToolLog{}
	for rows.Next() {
		item := &MCPToolLog{}
		if err := rows.Scan(&item.ID, &item.RequestID, &item.ToolName, &item.Status, &item.ErrorMessage,
			&item.DurationMS, &item.APIKeyHash, &item.AuthMode, &item.AgentID, &item.AgentSigHash, &item.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (db *DB) ListMarketplacePurchaseSummaries(ctx context.Context, limit int) ([]*MarketplacePurchaseSummary, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := db.SQL.QueryContext(ctx, `
		SELECT id, product_id, plan_id, agent_wallet, payment_asset, network,
		       gross_amount::text, provider_amount::text, status, COALESCE(tx_hash,''), created_at
		FROM marketplace_purchases
		ORDER BY created_at DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*MarketplacePurchaseSummary{}
	for rows.Next() {
		item := &MarketplacePurchaseSummary{}
		if err := rows.Scan(&item.ID, &item.ProductID, &item.PlanID, &item.AgentWallet, &item.PaymentAsset,
			&item.Network, &item.GrossAmount, &item.ProviderAmount, &item.Status, &item.TxHash, &item.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (db *DB) ListMarketplaceUsageSummaries(ctx context.Context, limit int) ([]*MarketplaceUsageSummary, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := db.SQL.QueryContext(ctx, `
		SELECT id, capability_id, provider_slug, routing_mode, operation, status,
		       units_consumed, quota_remaining, COALESCE(latency_ms,0), request_id, created_at
		FROM marketplace_execution_events
		ORDER BY created_at DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*MarketplaceUsageSummary{}
	for rows.Next() {
		item := &MarketplaceUsageSummary{}
		if err := rows.Scan(&item.ID, &item.CapabilityID, &item.ProviderSlug, &item.RoutingMode,
			&item.Operation, &item.Status, &item.UnitsConsumed, &item.QuotaRemaining,
			&item.LatencyMS, &item.RequestID, &item.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func ScanNullString(value sql.NullString) string {
	if value.Valid {
		return value.String
	}
	return ""
}
