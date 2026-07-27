package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type AgentPolicy struct {
	AgentID             string          `json:"agentId"`
	Environment         string          `json:"environment"`
	AgentType           string          `json:"agentType"`
	WalletMode          string          `json:"walletMode"`
	DailyLimitUSDT      string          `json:"dailyLimitUsdt"`
	MonthlyLimitUSDT    string          `json:"monthlyLimitUsdt"`
	MaxTransactionUSDT  string          `json:"maxTransactionUsdt"`
	AllowedAssets       json.RawMessage `json:"allowedAssets"`
	AllowedCapabilities json.RawMessage `json:"allowedCapabilities"`
	AllowedProviders    json.RawMessage `json:"allowedProviders"`
	Permissions         json.RawMessage `json:"permissions"`
	RequireRealProvider bool            `json:"requireRealProvider"`
	MockFallback        bool            `json:"mockFallback"`
	Status              string          `json:"status"`
	CreatedAt           time.Time       `json:"createdAt"`
	UpdatedAt           time.Time       `json:"updatedAt"`
}

type AgentPolicyInput struct {
	Environment         string   `json:"environment"`
	AgentType           string   `json:"agentType"`
	WalletMode          string   `json:"walletMode"`
	DailyLimitUSDT      string   `json:"dailyLimitUsdt"`
	MonthlyLimitUSDT    string   `json:"monthlyLimitUsdt"`
	MaxTransactionUSDT  string   `json:"maxTransactionUsdt"`
	AllowedAssets       []string `json:"allowedAssets"`
	AllowedCapabilities []string `json:"allowedCapabilities"`
	AllowedProviders    []string `json:"allowedProviders"`
	Permissions         []string `json:"permissions"`
	RequireRealProvider bool     `json:"requireRealProvider"`
	MockFallback        *bool    `json:"mockFallback"`
	Status              string   `json:"status"`
}

type AgentPolicyDecision struct {
	Allowed bool   `json:"allowed"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

func DefaultAgentPolicyInput() AgentPolicyInput {
	mockFallback := true
	return AgentPolicyInput{
		Environment:         "sandbox",
		AgentType:           "autonomous",
		WalletMode:          "existing",
		DailyLimitUSDT:      "500",
		MonthlyLimitUSDT:    "5000",
		MaxTransactionUSDT:  "100",
		AllowedAssets:       []string{"USDT", "USDC"},
		AllowedCapabilities: []string{},
		AllowedProviders:    []string{},
		Permissions:         []string{"capabilities:read", "capabilities:purchase", "capabilities:execute", "trades:create", "payments:create", "settlements:read", "webhooks:write"},
		RequireRealProvider: false,
		MockFallback:        &mockFallback,
		Status:              "active",
	}
}

func (db *DB) UpsertAgentPolicy(ctx context.Context, agentID string, in AgentPolicyInput) (*AgentPolicy, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return nil, fmt.Errorf("agentId is required")
	}
	in = normalizeAgentPolicyInput(in)
	allowedAssets, _ := json.Marshal(cleanStringList(in.AllowedAssets))
	allowedCapabilities, _ := json.Marshal(cleanStringList(in.AllowedCapabilities))
	allowedProviders, _ := json.Marshal(cleanStringList(in.AllowedProviders))
	permissions, _ := json.Marshal(cleanStringList(in.Permissions))
	row := db.SQL.QueryRowContext(ctx, `
		INSERT INTO marketplace_agent_policies (
		  agent_id, environment, agent_type, wallet_mode, daily_limit_usdt, monthly_limit_usdt,
		  max_transaction_usdt, allowed_assets_json, allowed_capabilities_json,
		  allowed_providers_json, permissions_json, require_real_provider, mock_fallback, status
		) VALUES ($1,$2,$3,$4,$5::numeric,$6::numeric,$7::numeric,$8,$9,$10,$11,$12,$13,$14)
		ON CONFLICT (agent_id) DO UPDATE SET
		  environment = EXCLUDED.environment,
		  agent_type = EXCLUDED.agent_type,
		  wallet_mode = EXCLUDED.wallet_mode,
		  daily_limit_usdt = EXCLUDED.daily_limit_usdt,
		  monthly_limit_usdt = EXCLUDED.monthly_limit_usdt,
		  max_transaction_usdt = EXCLUDED.max_transaction_usdt,
		  allowed_assets_json = EXCLUDED.allowed_assets_json,
		  allowed_capabilities_json = EXCLUDED.allowed_capabilities_json,
		  allowed_providers_json = EXCLUDED.allowed_providers_json,
		  permissions_json = EXCLUDED.permissions_json,
		  require_real_provider = EXCLUDED.require_real_provider,
		  mock_fallback = EXCLUDED.mock_fallback,
		  status = EXCLUDED.status,
		  updated_at = now()
		RETURNING `+agentPolicyColumns(),
		agentID, in.Environment, in.AgentType, in.WalletMode, in.DailyLimitUSDT, in.MonthlyLimitUSDT,
		in.MaxTransactionUSDT, json.RawMessage(allowedAssets), json.RawMessage(allowedCapabilities),
		json.RawMessage(allowedProviders), json.RawMessage(permissions), in.RequireRealProvider, *in.MockFallback, in.Status)
	return scanAgentPolicy(row)
}

func (db *DB) GetAgentPolicy(ctx context.Context, agentID string) (*AgentPolicy, error) {
	row := db.SQL.QueryRowContext(ctx, `SELECT `+agentPolicyColumns()+` FROM marketplace_agent_policies WHERE agent_id = $1`, strings.TrimSpace(agentID))
	policy, err := scanAgentPolicy(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return policy, err
}

func (db *DB) GetAgentPolicyByWallet(ctx context.Context, wallet string) (*AgentPolicy, error) {
	row := db.SQL.QueryRowContext(ctx, `
		SELECT `+agentPolicyColumnsWithAlias("p")+`
		FROM marketplace_agent_policies p
		JOIN marketplace_agent_identities a ON a.agent_id = p.agent_id
		WHERE lower(a.wallet) = lower($1)
		ORDER BY p.updated_at DESC
		LIMIT 1`, strings.TrimSpace(wallet))
	policy, err := scanAgentPolicy(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return policy, err
}

func (db *DB) GetAgentPolicyByAccessToken(ctx context.Context, token string) (*AgentPolicy, error) {
	tokenHash := db.accessTokenHash(strings.TrimSpace(token))
	row := db.SQL.QueryRowContext(ctx, `
		SELECT `+agentPolicyColumnsWithAlias("p")+`
		FROM marketplace_agent_policies p
		JOIN marketplace_agent_identities a ON a.agent_id = p.agent_id
		JOIN api_access_grants g ON lower(g.buyer_wallet) = lower(a.wallet)
		WHERE g.access_token_hash = $1
		ORDER BY p.updated_at DESC
		LIMIT 1`, tokenHash)
	policy, err := scanAgentPolicy(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return policy, err
}

func (db *DB) ValidateAgentPurchasePolicy(ctx context.Context, wallet, capability, asset, grossAmount string) (*AgentPolicy, AgentPolicyDecision, error) {
	policy, err := db.GetAgentPolicyByWallet(ctx, wallet)
	if err != nil {
		return policy, AgentPolicyDecision{Allowed: false, Code: "AGENT_POLICY_LOOKUP_FAILED", Message: "Agent policy lookup failed."}, err
	}
	if policy == nil {
		return nil, AgentPolicyDecision{Allowed: false, Code: "AGENT_POLICY_REQUIRED", Message: "Agent policy is required for this operation."}, nil
	}
	if policy.Status != "active" {
		return policy, AgentPolicyDecision{Allowed: false, Code: "AGENT_POLICY_INACTIVE", Message: "Agent policy is not active."}, nil
	}
	if !jsonListContains(policy.Permissions, "capabilities:purchase") {
		return policy, AgentPolicyDecision{Allowed: false, Code: "AGENT_PERMISSION_DENIED", Message: "Agent cannot purchase capabilities."}, nil
	}
	if !jsonListEmpty(policy.AllowedAssets) && !jsonListContains(policy.AllowedAssets, strings.ToUpper(asset)) {
		return policy, AgentPolicyDecision{Allowed: false, Code: "ASSET_NOT_ALLOWED", Message: "Payment asset is not allowed for this agent."}, nil
	}
	if !jsonListEmpty(policy.AllowedCapabilities) && !jsonListContains(policy.AllowedCapabilities, capability) {
		return policy, AgentPolicyDecision{Allowed: false, Code: "CAPABILITY_NOT_ALLOWED", Message: "Capability is not allowed for this agent."}, nil
	}
	if limitExceeded(grossAmount, policy.MaxTransactionUSDT) {
		return policy, AgentPolicyDecision{Allowed: false, Code: "MAX_TRANSACTION_EXCEEDED", Message: "Purchase exceeds the agent maximum transaction policy."}, nil
	}
	if decision := db.enforceAgentSpendLimits(ctx, wallet, grossAmount, policy); !decision.Allowed {
		return policy, decision, nil
	}
	return policy, AgentPolicyDecision{Allowed: true}, nil
}

func (db *DB) ValidateAgentPaymentPolicy(ctx context.Context, wallet, asset, grossAmount string) (*AgentPolicy, AgentPolicyDecision, error) {
	policy, err := db.GetAgentPolicyByWallet(ctx, wallet)
	if err != nil {
		return policy, AgentPolicyDecision{Allowed: false, Code: "AGENT_POLICY_LOOKUP_FAILED", Message: "Agent policy lookup failed."}, err
	}
	if policy == nil {
		return nil, AgentPolicyDecision{Allowed: false, Code: "AGENT_POLICY_REQUIRED", Message: "Agent policy is required for this operation."}, nil
	}
	if policy.Status != "active" {
		return policy, AgentPolicyDecision{Allowed: false, Code: "AGENT_POLICY_INACTIVE", Message: "Agent policy is not active."}, nil
	}
	if !jsonListContains(policy.Permissions, "payments:create") {
		return policy, AgentPolicyDecision{Allowed: false, Code: "AGENT_PERMISSION_DENIED", Message: "Agent cannot create payment intents."}, nil
	}
	if !jsonListEmpty(policy.AllowedAssets) && !jsonListContains(policy.AllowedAssets, strings.ToUpper(asset)) {
		return policy, AgentPolicyDecision{Allowed: false, Code: "ASSET_NOT_ALLOWED", Message: "Payment asset is not allowed for this agent."}, nil
	}
	if limitExceeded(grossAmount, policy.MaxTransactionUSDT) {
		return policy, AgentPolicyDecision{Allowed: false, Code: "MAX_TRANSACTION_EXCEEDED", Message: "Payment exceeds the agent maximum transaction policy."}, nil
	}
	if decision := db.enforceAgentSpendLimits(ctx, wallet, grossAmount, policy); !decision.Allowed {
		return policy, decision, nil
	}
	return policy, AgentPolicyDecision{Allowed: true}, nil
}

func (db *DB) ValidateAgentExecutionPolicy(ctx context.Context, token, capability, provider string, requireReal bool) (*AgentPolicy, AgentPolicyDecision, error) {
	policy, err := db.GetAgentPolicyByAccessToken(ctx, token)
	if err != nil {
		return policy, AgentPolicyDecision{Allowed: false, Code: "AGENT_POLICY_LOOKUP_FAILED", Message: "Agent policy lookup failed."}, err
	}
	if policy == nil {
		return nil, AgentPolicyDecision{Allowed: false, Code: "AGENT_POLICY_REQUIRED", Message: "Agent policy is required for this operation."}, nil
	}
	if policy.Status != "active" {
		return policy, AgentPolicyDecision{Allowed: false, Code: "AGENT_POLICY_INACTIVE", Message: "Agent policy is not active."}, nil
	}
	if !jsonListContains(policy.Permissions, "capabilities:execute") {
		return policy, AgentPolicyDecision{Allowed: false, Code: "AGENT_PERMISSION_DENIED", Message: "Agent cannot execute capabilities."}, nil
	}
	if !jsonListEmpty(policy.AllowedCapabilities) && !jsonListContains(policy.AllowedCapabilities, capability) {
		return policy, AgentPolicyDecision{Allowed: false, Code: "CAPABILITY_NOT_ALLOWED", Message: "Capability is not allowed for this agent."}, nil
	}
	if provider != "" && !jsonListEmpty(policy.AllowedProviders) && !jsonListContains(policy.AllowedProviders, provider) {
		return policy, AgentPolicyDecision{Allowed: false, Code: "PROVIDER_NOT_ALLOWED", Message: "Provider is not allowed for this agent."}, nil
	}
	if policy.RequireRealProvider && !requireReal {
		return policy, AgentPolicyDecision{Allowed: false, Code: "REAL_PROVIDER_REQUIRED", Message: "This agent policy requires a real provider."}, nil
	}
	return policy, AgentPolicyDecision{Allowed: true}, nil
}

func normalizeAgentPolicyInput(in AgentPolicyInput) AgentPolicyInput {
	defaults := DefaultAgentPolicyInput()
	if strings.TrimSpace(in.Environment) == "" {
		in.Environment = defaults.Environment
	}
	in.Environment = normalizeDeveloperEnvironment(in.Environment)
	if strings.TrimSpace(in.AgentType) == "" {
		in.AgentType = defaults.AgentType
	}
	if strings.TrimSpace(in.WalletMode) == "" {
		in.WalletMode = defaults.WalletMode
	}
	if strings.TrimSpace(in.DailyLimitUSDT) == "" {
		in.DailyLimitUSDT = defaults.DailyLimitUSDT
	}
	if strings.TrimSpace(in.MonthlyLimitUSDT) == "" {
		in.MonthlyLimitUSDT = defaults.MonthlyLimitUSDT
	}
	if strings.TrimSpace(in.MaxTransactionUSDT) == "" {
		in.MaxTransactionUSDT = defaults.MaxTransactionUSDT
	}
	if len(in.AllowedAssets) == 0 {
		in.AllowedAssets = defaults.AllowedAssets
	}
	if len(in.Permissions) == 0 {
		in.Permissions = defaults.Permissions
	}
	if in.MockFallback == nil {
		in.MockFallback = defaults.MockFallback
	}
	if strings.TrimSpace(in.Status) == "" {
		in.Status = defaults.Status
	}
	in.Status = strings.ToLower(strings.TrimSpace(in.Status))
	if in.Status != "active" && in.Status != "paused" && in.Status != "disabled" {
		in.Status = "active"
	}
	return in
}

func scanAgentPolicy(row rowScanner) (*AgentPolicy, error) {
	item := &AgentPolicy{}
	if err := row.Scan(&item.AgentID, &item.Environment, &item.AgentType, &item.WalletMode,
		&item.DailyLimitUSDT, &item.MonthlyLimitUSDT, &item.MaxTransactionUSDT,
		&item.AllowedAssets, &item.AllowedCapabilities, &item.AllowedProviders, &item.Permissions,
		&item.RequireRealProvider, &item.MockFallback, &item.Status, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return nil, err
	}
	return item, nil
}

func agentPolicyColumns() string {
	return "agent_id, environment, agent_type, wallet_mode, daily_limit_usdt::text, monthly_limit_usdt::text, max_transaction_usdt::text, allowed_assets_json, allowed_capabilities_json, allowed_providers_json, permissions_json, require_real_provider, mock_fallback, status, created_at, updated_at"
}

func agentPolicyColumnsWithAlias(alias string) string {
	cols := strings.Split(agentPolicyColumns(), ", ")
	for i, col := range cols {
		if strings.Contains(col, "::") {
			parts := strings.Split(col, "::")
			cols[i] = alias + "." + parts[0] + "::" + parts[1]
			continue
		}
		cols[i] = alias + "." + col
	}
	return strings.Join(cols, ", ")
}

func jsonListEmpty(raw json.RawMessage) bool {
	var values []string
	_ = json.Unmarshal(raw, &values)
	return len(values) == 0
}

func jsonListContains(raw json.RawMessage, value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	var values []string
	_ = json.Unmarshal(raw, &values)
	for _, item := range values {
		if strings.ToLower(strings.TrimSpace(item)) == value {
			return true
		}
	}
	return false
}

func limitExceeded(amount, limit string) bool {
	amountValue, _ := strconv.ParseFloat(strings.TrimSpace(amount), 64)
	limitValue, _ := strconv.ParseFloat(strings.TrimSpace(limit), 64)
	return limitValue > 0 && amountValue > limitValue
}

func (db *DB) enforceAgentSpendLimits(ctx context.Context, wallet, amount string, policy *AgentPolicy) AgentPolicyDecision {
	amountValue, _ := strconv.ParseFloat(strings.TrimSpace(amount), 64)
	if amountValue <= 0 || policy == nil {
		return AgentPolicyDecision{Allowed: true}
	}
	dailyLimit, _ := strconv.ParseFloat(strings.TrimSpace(policy.DailyLimitUSDT), 64)
	monthlyLimit, _ := strconv.ParseFloat(strings.TrimSpace(policy.MonthlyLimitUSDT), 64)
	if dailyLimit <= 0 && monthlyLimit <= 0 {
		return AgentPolicyDecision{Allowed: true}
	}
	daily, monthly, err := db.AgentPolicySpendUSDT(ctx, wallet)
	if err != nil {
		return AgentPolicyDecision{Allowed: false, Code: "AGENT_POLICY_SPEND_UNAVAILABLE", Message: "Agent spend could not be verified."}
	}
	if dailyLimit > 0 && daily+amountValue > dailyLimit {
		return AgentPolicyDecision{Allowed: false, Code: "DAILY_LIMIT_EXCEEDED", Message: "Payment exceeds the agent daily spend policy."}
	}
	if monthlyLimit > 0 && monthly+amountValue > monthlyLimit {
		return AgentPolicyDecision{Allowed: false, Code: "MONTHLY_LIMIT_EXCEEDED", Message: "Payment exceeds the agent monthly spend policy."}
	}
	return AgentPolicyDecision{Allowed: true}
}

func (db *DB) AgentPolicySpendUSDT(ctx context.Context, wallet string) (daily float64, monthly float64, err error) {
	wallet = strings.ToLower(strings.TrimSpace(wallet))
	if wallet == "" {
		return 0, 0, nil
	}
	const q = `
WITH spend AS (
  SELECT gross_amount::numeric AS amount, created_at
    FROM marketplace_purchases
   WHERE lower(agent_wallet) = lower($1)
     AND status NOT IN ('expired','payment_invalid','grant_failed')
  UNION ALL
  SELECT pay_amount::numeric AS amount, created_at
    FROM agent_trade_intents
   WHERE lower(agent_wallet) = lower($1)
     AND status NOT IN ('expired','failed')
  UNION ALL
  SELECT required_usdt::numeric AS amount, created_at
    FROM agent_payment_intents
   WHERE lower(agent_wallet) = lower($1)
     AND status NOT IN ('expired','failed')
)
SELECT
  COALESCE(SUM(amount) FILTER (WHERE created_at >= date_trunc('day', now() AT TIME ZONE 'UTC')), 0)::float8,
  COALESCE(SUM(amount) FILTER (WHERE created_at >= date_trunc('month', now() AT TIME ZONE 'UTC')), 0)::float8
FROM spend`
	err = db.SQL.QueryRowContext(ctx, q, wallet).Scan(&daily, &monthly)
	return daily, monthly, err
}
