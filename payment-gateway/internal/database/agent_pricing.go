package database

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// AgentPricingPolicy holds per-agent fee overrides for M2M rails and capability
// take-rate. NULL values (represented as pointer zero) mean "use global default".
type AgentPricingPolicy struct {
	ID                string     `json:"id"`
	AgentWallet       string     `json:"agentWallet"`
	Environment       string     `json:"environment"`
	PixFeeBps         *int       `json:"pixFeeBps,omitempty"`         // nil → global M2M_PIX_FEE_BPS
	CreditCardFeeBps  *int       `json:"creditCardFeeBps,omitempty"`  // nil → global M2M_CREDIT_FEE_BPS
	CapabilityTakeBps *int       `json:"capabilityTakeBps,omitempty"` // nil → plan take_rate_bps
	DailyMaxBRL       *float64   `json:"dailyMaxBrl,omitempty"`       // nil → global M2M_MAX_DAILY_OUTFLOW_BRL
	MonthlyMaxBRL     *float64   `json:"monthlyMaxBrl,omitempty"`
	Notes             string     `json:"notes,omitempty"`
	Status            string     `json:"status"`
	CreatedAt         time.Time  `json:"createdAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
}

// GetAgentPricingPolicy returns the per-agent pricing override for wallet+environment,
// or nil if none exists. Callers should fall back to env-var globals when nil.
func (db *DB) GetAgentPricingPolicy(ctx context.Context, wallet, environment string) (*AgentPricingPolicy, error) {
	wallet = strings.ToLower(strings.TrimSpace(wallet))
	if environment == "" {
		environment = "sandbox"
	}
	p := &AgentPricingPolicy{}
	var pixBps, ccBps, takeBps sql.NullInt64
	var dailyMax, monthlyMax sql.NullFloat64
	err := db.SQL.QueryRowContext(ctx, `
		SELECT id, agent_wallet, environment,
		       pix_fee_bps, credit_card_fee_bps, capability_take_bps,
		       daily_max_brl, monthly_max_brl,
		       COALESCE(notes, ''), status, created_at, updated_at
		FROM agent_pricing_policies
		WHERE lower(agent_wallet) = $1 AND environment = $2 AND status = 'active'
		ORDER BY updated_at DESC
		LIMIT 1`, wallet, environment).Scan(
		&p.ID, &p.AgentWallet, &p.Environment,
		&pixBps, &ccBps, &takeBps,
		&dailyMax, &monthlyMax,
		&p.Notes, &p.Status, &p.CreatedAt, &p.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if pixBps.Valid {
		v := int(pixBps.Int64)
		p.PixFeeBps = &v
	}
	if ccBps.Valid {
		v := int(ccBps.Int64)
		p.CreditCardFeeBps = &v
	}
	if takeBps.Valid {
		v := int(takeBps.Int64)
		p.CapabilityTakeBps = &v
	}
	if dailyMax.Valid {
		p.DailyMaxBRL = &dailyMax.Float64
	}
	if monthlyMax.Valid {
		p.MonthlyMaxBRL = &monthlyMax.Float64
	}
	return p, nil
}

// UpsertAgentPricingPolicy inserts or updates a per-agent pricing override.
func (db *DB) UpsertAgentPricingPolicy(ctx context.Context, p AgentPricingPolicy) (*AgentPricingPolicy, error) {
	wallet := strings.ToLower(strings.TrimSpace(p.AgentWallet))
	if wallet == "" {
		return nil, nil
	}
	env := p.Environment
	if env == "" {
		env = "sandbox"
	}
	status := p.Status
	if status == "" {
		status = "active"
	}
	_, err := db.SQL.ExecContext(ctx, `
		INSERT INTO agent_pricing_policies (
		  agent_wallet, environment, pix_fee_bps, credit_card_fee_bps, capability_take_bps,
		  daily_max_brl, monthly_max_brl, notes, status
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (lower(agent_wallet), environment) DO UPDATE SET
		  pix_fee_bps         = EXCLUDED.pix_fee_bps,
		  credit_card_fee_bps = EXCLUDED.credit_card_fee_bps,
		  capability_take_bps = EXCLUDED.capability_take_bps,
		  daily_max_brl       = EXCLUDED.daily_max_brl,
		  monthly_max_brl     = EXCLUDED.monthly_max_brl,
		  notes               = EXCLUDED.notes,
		  status              = EXCLUDED.status,
		  updated_at          = now()`,
		wallet, env, p.PixFeeBps, p.CreditCardFeeBps, p.CapabilityTakeBps,
		p.DailyMaxBRL, p.MonthlyMaxBRL, p.Notes, status)
	if err != nil {
		return nil, err
	}
	return db.GetAgentPricingPolicy(ctx, wallet, env)
}

// ResolveM2MFeeBps returns the effective PIX or credit-card fee bps for a wallet,
// falling back to the provided global defaults when no override exists.
func (db *DB) ResolveM2MFeeBps(ctx context.Context, wallet, paymentType, environment string, globalPixBps, globalCreditBps int) (int, error) {
	policy, err := db.GetAgentPricingPolicy(ctx, wallet, environment)
	if err != nil || policy == nil {
		// No override — use global
		if strings.EqualFold(paymentType, "pix") {
			return globalPixBps, err
		}
		return globalCreditBps, err
	}
	switch strings.ToLower(paymentType) {
	case "pix":
		if policy.PixFeeBps != nil {
			return *policy.PixFeeBps, nil
		}
		return globalPixBps, nil
	default: // credit_card
		if policy.CreditCardFeeBps != nil {
			return *policy.CreditCardFeeBps, nil
		}
		return globalCreditBps, nil
	}
}

// AgentGrantSummary is a lightweight summary of an active access grant.
type AgentGrantSummary struct {
	ID             string    `json:"id"`
	ProductID      string    `json:"productId"`
	BuyerWallet    string    `json:"buyerWallet"`
	QuotaTotal     int       `json:"quotaTotal"`
	QuotaRemaining int       `json:"quotaRemaining"`
	QuotaUsed      int       `json:"quotaUsed"`
	Status         string    `json:"status"`
	ExpiresAt      time.Time `json:"expiresAt"`
	CreatedAt      time.Time `json:"createdAt"`
}

// ListAgentActiveGrants returns active (non-exhausted, non-expired) access grants
// for a given agent wallet address.
func (db *DB) ListAgentActiveGrants(ctx context.Context, wallet string) ([]*AgentGrantSummary, error) {
	wallet = strings.ToLower(strings.TrimSpace(wallet))
	if wallet == "" {
		return nil, nil
	}
	rows, err := db.SQL.QueryContext(ctx, `
		SELECT g.id, g.product_id, g.buyer_wallet,
		       g.quota_total, g.quota_remaining, COALESCE(g.quota_used,0),
		       g.status, g.expires_at, g.created_at
		FROM api_access_grants g
		WHERE lower(g.buyer_wallet) = $1
		  AND g.status = 'active'
		  AND g.expires_at > now()
		ORDER BY g.expires_at ASC
		LIMIT 50`, wallet)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*AgentGrantSummary
	for rows.Next() {
		g := &AgentGrantSummary{}
		if err := rows.Scan(&g.ID, &g.ProductID, &g.BuyerWallet,
			&g.QuotaTotal, &g.QuotaRemaining, &g.QuotaUsed,
			&g.Status, &g.ExpiresAt, &g.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// RiskDashboardStats holds the aggregated risk and settlement data for the operator.
type RiskDashboardStats struct {
	PendingIntents       int     `json:"pendingIntents"`
	PendingDepositBRL    float64 `json:"pendingDepositBrl"`
	SettledToday         int     `json:"settledToday"`
	SettledTodayBRL      float64 `json:"settledTodayBrl"`
	FailedToday          int     `json:"failedToday"`
	DailyOutflowBRL      float64 `json:"dailyOutflowBrl"`
	EfiPendingCount      int     `json:"efiPendingCount"`
	ExpiredToday         int     `json:"expiredToday"`
	// Overpayment tracking: deposits where agent sent more USDT than required.
	// Excess stays in TREASURY_HOT and requires manual reconciliation.
	OverpaidIntents      int     `json:"overpaidIntents"`
	OverpaymentUSDT      float64 `json:"overpaymentUsdt"`
}

// GetRiskDashboardStats returns the aggregated M2M risk/settlement stats.
func (db *DB) GetRiskDashboardStats(ctx context.Context) (*RiskDashboardStats, error) {
	s := &RiskDashboardStats{}
	err := db.SQL.QueryRowContext(ctx, `
		SELECT
		  COUNT(*) FILTER (WHERE status = 'pending_deposit')                          AS pending_intents,
		  COALESCE(SUM(amount_brl) FILTER (WHERE status = 'pending_deposit'), 0)      AS pending_brl,
		  COUNT(*) FILTER (WHERE status = 'settled'
		                     AND settled_at >= date_trunc('day', now() AT TIME ZONE 'UTC')) AS settled_today,
		  COALESCE(SUM(amount_brl) FILTER (
		      WHERE status = 'settled'
		        AND settled_at >= date_trunc('day', now() AT TIME ZONE 'UTC')), 0)    AS settled_brl_today,
		  COUNT(*) FILTER (WHERE status = 'failed'
		                     AND updated_at >= date_trunc('day', now() AT TIME ZONE 'UTC')) AS failed_today,
		  COALESCE(SUM(amount_brl) FILTER (
		      WHERE status IN ('settled','settling','paid_crypto')), 0)                AS daily_outflow_brl,
		  COUNT(*) FILTER (WHERE status = 'settling')                                 AS efi_pending,
		  COUNT(*) FILTER (WHERE status = 'expired'
		                     AND updated_at >= date_trunc('day', now() AT TIME ZONE 'UTC')) AS expired_today,
		  -- Overpayment: intents where agent sent more than required (deposit_amount_usdt > required_usdt)
		  COUNT(*) FILTER (WHERE deposit_amount_usdt IS NOT NULL
		                     AND deposit_amount_usdt > required_usdt + 0.001) AS overpaid_intents,
		  COALESCE(SUM(
		    GREATEST(deposit_amount_usdt - required_usdt, 0)
		  ) FILTER (WHERE deposit_amount_usdt IS NOT NULL
		              AND deposit_amount_usdt > required_usdt + 0.001), 0) AS overpayment_usdt
		FROM agent_payment_intents
		WHERE created_at >= date_trunc('day', now() AT TIME ZONE 'UTC') - INTERVAL '1 day'
	`).Scan(
		&s.PendingIntents, &s.PendingDepositBRL,
		&s.SettledToday, &s.SettledTodayBRL,
		&s.FailedToday, &s.DailyOutflowBRL,
		&s.EfiPendingCount, &s.ExpiredToday,
		&s.OverpaidIntents, &s.OverpaymentUSDT,
	)
	if err != nil {
		return nil, err
	}
	return s, nil
}
