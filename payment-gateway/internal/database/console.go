package database

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

type ConsoleAgentSummary struct {
	AgentID            string          `json:"agentId"`
	Name               string          `json:"name"`
	Status             string          `json:"status"`
	Wallet             string          `json:"wallet"`
	Capabilities       json.RawMessage `json:"capabilities,omitempty"`
	CapabilityCount    int             `json:"capabilityCount"`
	SpendUSDT          string          `json:"spendUsdt"`
	QuotaRemaining     int             `json:"quotaRemaining"`
	LastActivityAt     *time.Time      `json:"lastActivityAt,omitempty"`
	CreatedAt          time.Time       `json:"createdAt"`
	DailyLimitUSDT     string          `json:"dailyLimitUsdt"`
	MonthlyLimitUSDT   string          `json:"monthlyLimitUsdt"`
	MaxTransactionUSDT string          `json:"maxTransactionUsdt"`
}

type ConsoleSpendPoint struct {
	Date           string `json:"date"`
	TotalUSDT      string `json:"totalUsdt"`
	ChainFXFeeUSDT string `json:"chainfxFeeUsdt"`
	ProviderUSDT   string `json:"providerUsdt"`
	NetworkUSDT    string `json:"networkUsdt"`
}

type ConsoleSettlementSummary struct {
	ID             string    `json:"id"`
	ProviderID     string    `json:"providerId"`
	PurchaseID     string    `json:"purchaseId"`
	Asset          string    `json:"asset"`
	GrossAmount    string    `json:"grossAmount"`
	ChainFXAmount  string    `json:"chainfxAmount"`
	ProviderAmount string    `json:"providerAmount"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"createdAt"`
}

func (db *DB) ListConsoleAgents(ctx context.Context, limit int) ([]*ConsoleAgentSummary, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := db.SQL.QueryContext(ctx, `
		SELECT
		  a.agent_id,
		  COALESCE(NULLIF(a.name, ''), a.agent_id),
		  a.status,
		  COALESCE(a.wallet, ''),
		  COALESCE(a.capabilities_json, '[]'::jsonb),
		  COALESCE(jsonb_array_length(a.capabilities_json), 0),
		  COALESCE((
		    SELECT SUM(p.gross_amount)::text
		    FROM marketplace_purchases p
		    WHERE a.wallet IS NOT NULL AND lower(p.agent_wallet) = lower(a.wallet)
		  ), '0'),
		  COALESCE((
		    SELECT SUM(g.quota_remaining)
		    FROM api_access_grants g
		    WHERE a.wallet IS NOT NULL AND lower(g.buyer_wallet) = lower(a.wallet)
		  ), 0),
		  (
		    SELECT MAX(last_seen) FROM (
		      SELECT MAX(p.created_at) AS last_seen FROM marketplace_purchases p WHERE a.wallet IS NOT NULL AND lower(p.agent_wallet) = lower(a.wallet)
		      UNION ALL
		      SELECT MAX(e.created_at) AS last_seen
		      FROM marketplace_execution_events e
		      JOIN api_access_grants g ON g.id = e.grant_id
		      WHERE a.wallet IS NOT NULL AND lower(g.buyer_wallet) = lower(a.wallet)
		    ) activity
		  ),
		  a.created_at,
		  COALESCE(p.daily_limit_usdt::text, '500'),
		  COALESCE(p.monthly_limit_usdt::text, '5000'),
		  COALESCE(p.max_transaction_usdt::text, '100')
		FROM marketplace_agent_identities a
		LEFT JOIN marketplace_agent_policies p ON p.agent_id = a.agent_id
		ORDER BY a.created_at DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ConsoleAgentSummary{}
	for rows.Next() {
		item := &ConsoleAgentSummary{}
		var lastActivity sqlNullTime
		if err := rows.Scan(&item.AgentID, &item.Name, &item.Status, &item.Wallet, &item.Capabilities,
			&item.CapabilityCount, &item.SpendUSDT, &item.QuotaRemaining, &lastActivity, &item.CreatedAt,
			&item.DailyLimitUSDT, &item.MonthlyLimitUSDT, &item.MaxTransactionUSDT); err != nil {
			return nil, err
		}
		if lastActivity.Valid {
			item.LastActivityAt = &lastActivity.Time
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (db *DB) ListConsoleSpendSeries(ctx context.Context, days int) ([]ConsoleSpendPoint, error) {
	if days <= 0 || days > 90 {
		days = 14
	}
	rows, err := db.SQL.QueryContext(ctx, `
		SELECT
		  to_char(day, 'YYYY-MM-DD') AS day,
		  COALESCE(SUM(gross_amount), 0)::text,
		  COALESCE(SUM(chainfx_amount), 0)::text,
		  COALESCE(SUM(provider_amount), 0)::text
		FROM generate_series(
		  date_trunc('day', now()) - (($1::int - 1) * interval '1 day'),
		  date_trunc('day', now()),
		  interval '1 day'
		) day
		LEFT JOIN marketplace_purchases p ON date_trunc('day', p.created_at) = day
		GROUP BY day
		ORDER BY day ASC`, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ConsoleSpendPoint{}
	for rows.Next() {
		item := ConsoleSpendPoint{NetworkUSDT: "0"}
		if err := rows.Scan(&item.Date, &item.TotalUSDT, &item.ChainFXFeeUSDT, &item.ProviderUSDT); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (db *DB) ListConsoleSettlements(ctx context.Context, limit int) ([]*ConsoleSettlementSummary, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := db.SQL.QueryContext(ctx, `
		SELECT id, provider_id, purchase_id, asset,
		       gross_amount::text, chainfx_amount::text, provider_amount::text,
		       status, created_at
		FROM marketplace_provider_settlements
		ORDER BY created_at DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ConsoleSettlementSummary{}
	for rows.Next() {
		item := &ConsoleSettlementSummary{}
		if err := rows.Scan(&item.ID, &item.ProviderID, &item.PurchaseID, &item.Asset,
			&item.GrossAmount, &item.ChainFXAmount, &item.ProviderAmount, &item.Status, &item.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func SumDecimalStrings(values ...string) float64 {
	var out float64
	for _, value := range values {
		parsed, _ := strconv.ParseFloat(strings.TrimSpace(value), 64)
		out += parsed
	}
	return out
}

type sqlNullTime struct {
	Time  time.Time
	Valid bool
}

func (nt *sqlNullTime) Scan(value any) error {
	if value == nil {
		nt.Valid = false
		return nil
	}
	switch t := value.(type) {
	case time.Time:
		nt.Time = t
		nt.Valid = true
	}
	return nil
}
