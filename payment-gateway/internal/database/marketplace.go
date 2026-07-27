package database

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"payment-gateway/internal/privacy"

	"github.com/lib/pq"
)

const (
	MarketplaceProviderActive = "active"
	MarketplaceProductActive  = "active"
	MarketplacePlanActive     = "active"

	MarketplacePurchaseCreated         = "created"
	MarketplacePurchasePendingPayment  = "pending_payment"
	MarketplacePurchasePaymentDetected = "payment_detected"
	MarketplacePurchaseVerifying       = "verifying"
	MarketplacePurchasePaid            = "paid"
	MarketplacePurchaseGrantingAccess  = "granting_access"
	MarketplacePurchaseActive          = "active"
	MarketplacePurchaseExhausted       = "exhausted"
	MarketplacePurchaseExpired         = "expired"
	MarketplacePurchasePaymentInvalid  = "payment_invalid"
	MarketplacePurchaseGrantFailed     = "grant_failed"
	MarketplacePurchaseManualReview    = "manual_review"

	MarketplaceSettlementPending = "pending"
	MarketplaceGrantActive       = "active"
)

type MarketplaceProvider struct {
	ID                string          `json:"id"`
	Slug              string          `json:"slug"`
	Name              string          `json:"name"`
	Description       string          `json:"description"`
	WebsiteURL        string          `json:"websiteUrl,omitempty"`
	SettlementWallet  string          `json:"settlementWallet"`
	SettlementAsset   string          `json:"settlementAsset"`
	SettlementNetwork string          `json:"settlementNetwork"`
	Status            string          `json:"status"`
	Metadata          json.RawMessage `json:"metadata,omitempty"`
	CreatedAt         time.Time       `json:"createdAt"`
}

type MarketplaceProduct struct {
	ID               string               `json:"id"`
	ProviderID       string               `json:"providerId"`
	Slug             string               `json:"slug"`
	Name             string               `json:"name"`
	Description      string               `json:"description"`
	Category         string               `json:"category"`
	DeliveryType     string               `json:"deliveryType"`
	Status           string               `json:"status"`
	CapabilityID     string               `json:"capabilityId,omitempty"`
	Capability       string               `json:"capability"`
	DocumentationURL string               `json:"documentationUrl,omitempty"`
	EndpointBaseURL  string               `json:"endpointBaseUrl,omitempty"`
	Metadata         json.RawMessage      `json:"metadata,omitempty"`
	Provider         *MarketplaceProvider `json:"provider,omitempty"`
	Plans            []*MarketplacePlan   `json:"plans,omitempty"`
	CreatedAt        time.Time            `json:"createdAt"`
}

type MarketplacePlan struct {
	ID              string          `json:"id"`
	ProductID       string          `json:"productId"`
	Slug            string          `json:"slug"`
	Name            string          `json:"name"`
	PriceAmount     string          `json:"price"`
	PaymentAsset    string          `json:"asset"`
	Network         string          `json:"network"`
	TakeRateBps     int             `json:"takeRateBps"`
	Quota           int             `json:"quota"`
	ValiditySeconds int             `json:"validitySeconds"`
	Status          string          `json:"status"`
	Metadata        json.RawMessage `json:"metadata,omitempty"`
	CreatedAt       time.Time       `json:"createdAt"`
}

type MarketplacePurchase struct {
	ID                string     `json:"id"`
	ProviderID        string     `json:"providerId"`
	ProductID         string     `json:"productId"`
	PlanID            string     `json:"planId"`
	AgentWallet       string     `json:"agentWallet"`
	PayerWallet       string     `json:"payerWallet"`
	PaymentAddress    string     `json:"paymentAddress"`
	PaymentAsset      string     `json:"paymentAsset"`
	PaymentContract   string     `json:"paymentContract"`
	Network           string     `json:"network"`
	ChainID           int64      `json:"chainId"`
	GrossAmount       string     `json:"grossAmount"`
	ChainFXAmount     string     `json:"chainfxAmount"`
	ProviderAmount    string     `json:"providerAmount"`
	TakeRateBps       int        `json:"takeRateBps"`
	RequestHash       string     `json:"requestHash"`
	Nonce             string     `json:"nonce"`
	IdempotencyKey    *string    `json:"idempotencyKey,omitempty"`
	ExpiresAt         time.Time  `json:"expiresAt"`
	Status            string     `json:"status"`
	TxHash            *string    `json:"txHash,omitempty"`
	TxLogIndex        *int       `json:"txLogIndex,omitempty"`
	TxBlockNumber     *uint64    `json:"txBlockNumber,omitempty"`
	TxBlockHash       *string    `json:"txBlockHash,omitempty"`
	TransferFrom      *string    `json:"transferFrom,omitempty"`
	TransferTo        *string    `json:"transferTo,omitempty"`
	TransferAmountRaw *string    `json:"transferAmountRaw,omitempty"`
	OverpaymentAmount string     `json:"overpaymentAmount"`
	PaidAt            *time.Time `json:"paidAt,omitempty"`
	GrantedAt         *time.Time `json:"grantedAt,omitempty"`
	FailedAt          *time.Time `json:"failedAt,omitempty"`
	FailureCode       *string    `json:"failureCode,omitempty"`
	FailureMessage    *string    `json:"failureMessage,omitempty"`
	CreatedAt         time.Time  `json:"createdAt"`
}

type MarketplacePurchaseInput struct {
	PlanID          string
	AgentWallet     string
	PayerWallet     string
	PaymentAddress  string
	PaymentContract string
	Nonce           string
	RequestHash     string
	IdempotencyKey  string
	ExpiresAt       time.Time
}

type MarketplaceActivationResult struct {
	Purchase    *MarketplacePurchase `json:"purchase"`
	AccessToken string               `json:"accessToken,omitempty"`
	Grant       *APIAccessGrant      `json:"grant,omitempty"`
}

type MarketplaceProductFilters struct {
	Category     string
	Provider     string
	Capability   string
	PaymentAsset string
	Status       string
}

type MarketplaceCapability struct {
	ID          string           `json:"id"`
	Slug        string           `json:"slug"`
	DisplayName string           `json:"displayName"`
	Description string           `json:"description"`
	Category    string           `json:"category"`
	RoutingMode string           `json:"routingMode"`
	Status      string           `json:"status"`
	Operations  json.RawMessage  `json:"operations,omitempty"`
	Metadata    json.RawMessage  `json:"metadata,omitempty"`
	Providers   []string         `json:"providers"`
	Plans       []map[string]any `json:"plans,omitempty"`
	CreatedAt   time.Time        `json:"createdAt"`
}

type MarketplaceCapabilityContract struct {
	ID           string          `json:"id"`
	CapabilityID string          `json:"capability"`
	Version      string          `json:"version"`
	Status       string          `json:"status"`
	InputSchema  json.RawMessage `json:"inputSchema"`
	OutputSchema json.RawMessage `json:"outputSchema"`
	Examples     json.RawMessage `json:"examples,omitempty"`
	Metadata     json.RawMessage `json:"metadata,omitempty"`
	CreatedAt    time.Time       `json:"createdAt"`
	UpdatedAt    time.Time       `json:"updatedAt"`
}

type MarketplaceAgentIdentity struct {
	AgentID      string    `json:"agentId"`
	Wallet       string    `json:"wallet"`
	APIKey       string    `json:"apiKey,omitempty"`
	Status       string    `json:"status"`
	Capabilities []string  `json:"capabilities"`
	CreatedAt    time.Time `json:"createdAt"`
}

type MarketplaceCapabilityExecution struct {
	ID             string          `json:"id"`
	CapabilityID   string          `json:"capability"`
	Operation      string          `json:"operation"`
	ProviderSlug   string          `json:"provider"`
	ProviderName   string          `json:"providerName"`
	RouteName      string          `json:"routeName"`
	RoutingMode    string          `json:"routingMode"`
	Status         string          `json:"status"`
	UnitsConsumed  int             `json:"unitsConsumed"`
	QuotaRemaining int             `json:"quotaRemaining"`
	RequestID      string          `json:"requestId"`
	IdempotencyKey string          `json:"idempotencyKey"`
	Input          json.RawMessage `json:"input,omitempty"`
	Output         json.RawMessage `json:"output,omitempty"`
	LatencyMS      int             `json:"latencyMs,omitempty"`
	ErrorCode      string          `json:"errorCode,omitempty"`
	ErrorMessage   string          `json:"errorMessage,omitempty"`
	CreatedAt      time.Time       `json:"createdAt"`
}

type MarketplaceCapabilityExecuteInput struct {
	Token             string
	AgentWallet       string
	CapabilityID      string
	Operation         string
	RequestID         string
	IdempotencyKey    string
	RequestedProvider string
	RoutingMode       string
	Region            string
	MaxLatencyMS      int
	MaxCostScore      int
	RequireReal       bool
	Units             int
	Input             json.RawMessage
}

type MarketplaceCapabilityExecuteResult struct {
	Event     *MarketplaceCapabilityExecution `json:"execution"`
	Grant     *APIAccessGrant                 `json:"grant"`
	Credit    *AgentCapabilityCreditAccount   `json:"credit,omitempty"`
	Duplicate bool                            `json:"duplicate"`
}

type AgentCapabilityCreditAccount struct {
	ID                    string    `json:"id"`
	AgentWallet           string    `json:"agentWallet"`
	CapabilityID          string    `json:"capability"`
	Asset                 string    `json:"asset"`
	Network               string    `json:"network"`
	CreditLimitUSDT       string    `json:"creditLimitUsdt"`
	CreditUsedUSDT        string    `json:"creditUsedUsdt"`
	CreditRemainingUSDT   string    `json:"creditRemainingUsdt"`
	MinTopUpUSDT          string    `json:"minTopUpUsdt"`
	ExpiresAt             time.Time `json:"expiresAt"`
	Status                string    `json:"status"`
	LastPaymentRequiredAt time.Time `json:"lastPaymentRequiredAt,omitempty"`
	creditLimitMicro      int64
	creditUsedMicro       int64
	minTopUpMicro         int64
}

type AgentCreditPaymentRequiredError struct {
	AgentWallet         string `json:"agentWallet"`
	CapabilityID        string `json:"capability"`
	Asset               string `json:"asset"`
	Network             string `json:"network"`
	CreditLimitUSDT     string `json:"creditLimitUsdt"`
	CreditUsedUSDT      string `json:"creditUsedUsdt"`
	CreditRemainingUSDT string `json:"creditRemainingUsdt"`
	RequiredUSDT        string `json:"requiredUsdt"`
	MinTopUpUSDT        string `json:"minTopUpUsdt"`
}

func (e *AgentCreditPaymentRequiredError) Error() string {
	if e == nil {
		return "payment required"
	}
	return "credit limit reached; top-up required"
}

func (e *AgentCreditPaymentRequiredError) Challenge() map[string]any {
	if e == nil {
		return map[string]any{"code": "AGENT_CREDIT_PAYMENT_REQUIRED"}
	}
	return map[string]any{
		"code":                "AGENT_CREDIT_PAYMENT_REQUIRED",
		"message":             e.Error(),
		"agentWallet":         e.AgentWallet,
		"capability":          e.CapabilityID,
		"asset":               e.Asset,
		"network":             e.Network,
		"creditLimitUsdt":     e.CreditLimitUSDT,
		"creditUsedUsdt":      e.CreditUsedUSDT,
		"creditRemainingUsdt": e.CreditRemainingUSDT,
		"requiredUsdt":        e.RequiredUSDT,
		"minTopUpUsdt":        e.MinTopUpUSDT,
		"nextStep":            "create a capability purchase/top-up of at least 20 USDT, pay on-chain, then retry with accessToken",
	}
}

func EnforceCapabilityTopUpMinimum(priceAmount, asset, network string) error {
	priceMicro, err := ParseMicroAmount(priceAmount)
	if err != nil {
		return err
	}
	const minTopUpMicro int64 = 20_000_000
	if priceMicro < minTopUpMicro {
		return &AgentCreditPaymentRequiredError{
			Asset:        firstNonEmptyDB(strings.TrimSpace(asset), "USDT"),
			Network:      firstNonEmptyDB(strings.TrimSpace(network), "BSC"),
			RequiredUSDT: FormatMicroAmount(priceMicro),
			MinTopUpUSDT: FormatMicroAmount(minTopUpMicro),
		}
	}
	return nil
}

type MarketplaceRouteCandidate struct {
	CapabilityID    string          `json:"capability"`
	RouteName       string          `json:"routeName"`
	RoutingMode     string          `json:"routingMode"`
	FallbackEnabled bool            `json:"fallbackEnabled"`
	ProviderSlug    string          `json:"provider"`
	ProviderName    string          `json:"providerName"`
	Status          string          `json:"status"`
	Priority        int             `json:"priority"`
	CostScore       int             `json:"costScore"`
	LatencyMS       int             `json:"latencyMs"`
	QualityScore    int             `json:"qualityScore"`
	SuccessRateBps  int             `json:"successRateBps"`
	Region          string          `json:"region"`
	FallbackOrder   int             `json:"fallbackOrder"`
	Policy          json.RawMessage `json:"policy,omitempty"`
}

func (db *DB) ListMarketplaceProducts(ctx context.Context, f MarketplaceProductFilters) ([]*MarketplaceProduct, error) {
	status := firstNonEmptyDB(strings.TrimSpace(f.Status), MarketplaceProductActive)
	args := []any{status}
	where := []string{"p.status = $1", "pr.status = 'active'", "pl.status = 'active'"}
	if f.Category != "" {
		args = append(args, strings.ToLower(strings.TrimSpace(f.Category)))
		where = append(where, fmt.Sprintf("p.category = $%d", len(args)))
	}
	if f.Provider != "" {
		args = append(args, strings.ToLower(strings.TrimSpace(f.Provider)))
		where = append(where, fmt.Sprintf("(pr.slug = $%d OR pr.id = $%d)", len(args), len(args)))
	}
	if f.Capability != "" {
		args = append(args, strings.TrimSpace(f.Capability))
		where = append(where, fmt.Sprintf("p.capability = $%d", len(args)))
	}
	if f.PaymentAsset != "" {
		args = append(args, strings.ToUpper(strings.TrimSpace(f.PaymentAsset)))
		where = append(where, fmt.Sprintf("pl.payment_asset = $%d", len(args)))
	}
	rows, err := db.SQL.QueryContext(ctx, `
		SELECT p.id, p.provider_id, p.slug, p.name, p.description, p.category, p.delivery_type, p.status,
		       COALESCE(p.capability_id, ''), p.capability, COALESCE(p.documentation_url,''), COALESCE(p.endpoint_base_url,''), COALESCE(p.metadata_json, '{}'::jsonb), p.created_at,
		       pr.id, pr.slug, pr.name, pr.description, COALESCE(pr.website_url,''), pr.settlement_wallet,
		       pr.settlement_asset, pr.settlement_network, pr.status, COALESCE(pr.metadata_json, '{}'::jsonb), pr.created_at,
		       pl.id, pl.product_id, pl.slug, pl.name, pl.price_amount::text, pl.payment_asset, pl.network,
		       pl.take_rate_bps, pl.quota, pl.validity_seconds, pl.status, COALESCE(pl.metadata_json, '{}'::jsonb), pl.created_at
		FROM marketplace_products p
		JOIN marketplace_providers pr ON pr.id = p.provider_id
		JOIN marketplace_plans pl ON pl.product_id = p.id
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY p.category ASC, p.name ASC, pl.price_amount ASC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := map[string]*MarketplaceProduct{}
	order := []string{}
	for rows.Next() {
		product, provider, plan, err := scanMarketplaceProductRow(rows)
		if err != nil {
			return nil, err
		}
		existing := byID[product.ID]
		if existing == nil {
			product.Provider = provider
			product.Plans = []*MarketplacePlan{}
			byID[product.ID] = product
			order = append(order, product.ID)
			existing = product
		}
		existing.Plans = append(existing.Plans, plan)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]*MarketplaceProduct, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out, nil
}

func (db *DB) GetMarketplaceProduct(ctx context.Context, idOrSlug string) (*MarketplaceProduct, error) {
	products, err := db.listMarketplaceProductByWhere(ctx, `p.status = 'active' AND pr.status = 'active' AND pl.status = 'active' AND (p.id = $1 OR p.slug = $1)`, idOrSlug)
	if err != nil || len(products) == 0 {
		return nil, err
	}
	return products[0], nil
}

// EmptyMarketplaceFilters returns a zero-value filter for convenience.
func (db *DB) EmptyMarketplaceFilters() MarketplaceProductFilters {
	return MarketplaceProductFilters{}
}

func (db *DB) ListMarketplaceCapabilities(ctx context.Context, f MarketplaceProductFilters) ([]*MarketplaceCapability, error) {
	args := []any{}
	where := []string{"c.status = 'active'"}
	if f.Category != "" {
		args = append(args, strings.ToLower(strings.TrimSpace(f.Category)))
		where = append(where, fmt.Sprintf("c.category = $%d", len(args)))
	}
	if f.Capability != "" {
		args = append(args, strings.TrimSpace(f.Capability))
		where = append(where, fmt.Sprintf("(c.id = $%d OR c.slug = $%d)", len(args), len(args)))
	}
	if f.PaymentAsset != "" {
		args = append(args, strings.ToUpper(strings.TrimSpace(f.PaymentAsset)))
		where = append(where, fmt.Sprintf(`EXISTS (
			SELECT 1 FROM marketplace_products p
			JOIN marketplace_plans pl ON pl.product_id = p.id
			JOIN marketplace_providers pr ON pr.id = p.provider_id
			WHERE (p.capability_id = c.id OR p.capability = c.id)
			  AND p.status = 'active' AND pl.status = 'active' AND pr.status = 'active'
			  AND pl.payment_asset = $%d
		)`, len(args)))
	}
	rows, err := db.SQL.QueryContext(ctx, `
		SELECT c.id, c.slug, c.display_name, c.description, c.category, c.routing_mode, c.status,
		       COALESCE(c.operations_json, '[]'::jsonb), COALESCE(c.metadata_json, '{}'::jsonb), c.created_at
		FROM marketplace_capabilities c
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY c.category ASC, c.display_name ASC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*MarketplaceCapability{}
	for rows.Next() {
		capability, err := scanMarketplaceCapability(rows)
		if err != nil {
			return nil, err
		}
		if err := db.fillMarketplaceCapabilityDetails(ctx, capability); err != nil {
			return nil, err
		}
		out = append(out, capability)
	}
	return out, rows.Err()
}

func (db *DB) GetMarketplaceCapability(ctx context.Context, idOrSlug string) (*MarketplaceCapability, error) {
	capability, err := scanMarketplaceCapability(db.SQL.QueryRowContext(ctx, `
		SELECT id, slug, display_name, description, category, routing_mode, status,
		       COALESCE(operations_json, '[]'::jsonb), COALESCE(metadata_json, '{}'::jsonb), created_at
		FROM marketplace_capabilities
		WHERE status = 'active' AND (id = $1 OR slug = $1)`, strings.TrimSpace(idOrSlug)))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return capability, db.fillMarketplaceCapabilityDetails(ctx, capability)
}

func (db *DB) GetMarketplaceCapabilityContract(ctx context.Context, idOrSlug, version string) (*MarketplaceCapabilityContract, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		version = "v1"
	}
	contract := &MarketplaceCapabilityContract{}
	err := db.SQL.QueryRowContext(ctx, `
		SELECT cc.id, cc.capability_id, cc.version, cc.status,
		       COALESCE(cc.input_schema_json, '{}'::jsonb),
		       COALESCE(cc.output_schema_json, '{}'::jsonb),
		       COALESCE(cc.examples_json, '[]'::jsonb),
		       COALESCE(cc.metadata_json, '{}'::jsonb),
		       cc.created_at, cc.updated_at
		FROM marketplace_capability_contracts cc
		JOIN marketplace_capabilities c ON c.id = cc.capability_id
		WHERE cc.status = 'active'
		  AND c.status = 'active'
		  AND (c.id = $1 OR c.slug = $1)
		  AND cc.version = $2`, strings.TrimSpace(idOrSlug), version).Scan(
		&contract.ID,
		&contract.CapabilityID,
		&contract.Version,
		&contract.Status,
		&contract.InputSchema,
		&contract.OutputSchema,
		&contract.Examples,
		&contract.Metadata,
		&contract.CreatedAt,
		&contract.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return contract, err
}

func (db *DB) ResolveMarketplaceCapabilityPlan(ctx context.Context, capabilityID, requestedPlanID, paymentAsset, paymentNetwork string) (*MarketplaceProduct, *MarketplacePlan, error) {
	paymentNetwork = strings.ToUpper(strings.TrimSpace(paymentNetwork))
	if strings.TrimSpace(requestedPlanID) != "" {
		product, _, plan, err := db.getMarketplacePlanBundle(ctx, strings.TrimSpace(requestedPlanID))
		if err != nil {
			return nil, nil, err
		}
		if product == nil || plan == nil {
			return nil, nil, fmt.Errorf("plano nao encontrado")
		}
		if !strings.EqualFold(product.CapabilityID, capabilityID) && !strings.EqualFold(product.Capability, capabilityID) {
			return nil, nil, fmt.Errorf("plano nao pertence a capability")
		}
		if strings.TrimSpace(paymentAsset) != "" && !strings.EqualFold(plan.PaymentAsset, paymentAsset) {
			return nil, nil, fmt.Errorf("plano nao aceita asset %s", strings.ToUpper(strings.TrimSpace(paymentAsset)))
		}
		if paymentNetwork != "" && !strings.EqualFold(plan.Network, paymentNetwork) {
			return nil, nil, fmt.Errorf("plano nao aceita network %s", paymentNetwork)
		}
		return product, plan, nil
	}
	args := []any{strings.TrimSpace(capabilityID)}
	assetFilter := ""
	if strings.TrimSpace(paymentAsset) != "" {
		args = append(args, strings.ToUpper(strings.TrimSpace(paymentAsset)))
		assetFilter = fmt.Sprintf(" AND pl.payment_asset = $%d", len(args))
	}
	networkFilter := ""
	if paymentNetwork != "" {
		args = append(args, paymentNetwork)
		networkFilter = fmt.Sprintf(" AND UPPER(pl.network) = $%d", len(args))
	}
	product, _, plan, err := db.getMarketplacePlanBundleScanner(db.SQL.QueryRowContext(ctx, marketplacePlanBundleSelect()+`
		WHERE (p.capability_id = $1 OR p.capability = $1)
		  AND pl.status = 'active' AND p.status = 'active' AND pr.status = 'active'`+assetFilter+networkFilter+`
		ORDER BY pl.price_amount ASC
		LIMIT 1`, args...))
	if err == sql.ErrNoRows {
		return nil, nil, fmt.Errorf("capability sem plano ativo")
	}
	return product, plan, err
}

func (db *DB) GetMarketplacePlan(ctx context.Context, planID string) (*MarketplaceProduct, *MarketplacePlan, error) {
	product, _, plan, err := db.getMarketplacePlanBundle(ctx, strings.TrimSpace(planID))
	return product, plan, err
}

func (db *DB) CreateMarketplaceAgentIdentity(ctx context.Context, wallet, name string) (*MarketplaceAgentIdentity, error) {
	agentID := "agent_" + strings.ReplaceAll(NewID(), "-", "")
	apiKey := "cfx_agent_" + NewAccessToken()
	wallet = strings.ToLower(strings.TrimSpace(wallet))
	capabilities := []string{"identity", "marketplace", "payments", "fx", "usage", "discovery"}
	rawCapabilities, _ := json.Marshal(capabilities)
	_, err := db.SQL.ExecContext(ctx, `
		INSERT INTO marketplace_agent_identities (
		  agent_id, wallet, name, api_key_hash, capabilities_json, status
		) VALUES ($1,$2,$3,$4,$5,'active')`,
		agentID, wallet, strings.TrimSpace(name), db.accessTokenHash(apiKey), json.RawMessage(rawCapabilities))
	if err != nil {
		return nil, err
	}
	if wallet != "" {
		_, _ = db.SQL.ExecContext(ctx, `
			INSERT INTO agent_wallets (address, first_seen_at, last_seen_at)
			VALUES ($1, now(), now())
			ON CONFLICT (address) DO UPDATE SET last_seen_at = now()`, wallet)
	}
	return &MarketplaceAgentIdentity{
		AgentID:      agentID,
		Wallet:       wallet,
		APIKey:       apiKey,
		Status:       "active",
		Capabilities: capabilities,
		CreatedAt:    time.Now().UTC(),
	}, nil
}

func (db *DB) ExecuteMarketplaceCapabilityMock(ctx context.Context, in MarketplaceCapabilityExecuteInput) (*MarketplaceCapabilityExecuteResult, error) {
	if strings.TrimSpace(in.Token) == "" {
		return nil, fmt.Errorf("access token obrigatorio")
	}
	if strings.TrimSpace(in.IdempotencyKey) == "" || strings.TrimSpace(in.RequestID) == "" {
		return nil, fmt.Errorf("requestId e idempotencyKey obrigatorios")
	}
	if in.Units <= 0 {
		in.Units = 1
	}
	in.CapabilityID = strings.TrimSpace(in.CapabilityID)
	in.Operation = firstNonEmptyDB(strings.TrimSpace(in.Operation), "execute")
	tx, err := db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if existing, err := db.getMarketplaceExecutionByIdempotencyTx(ctx, tx, in.IdempotencyKey); err != nil {
		return nil, err
	} else if existing != nil {
		return &MarketplaceCapabilityExecuteResult{Event: existing, Duplicate: true}, tx.Commit()
	}

	route, err := db.selectMarketplaceRouteTx(ctx, tx, in)
	if err != nil {
		return nil, err
	}
	if _, decision, err := db.ValidateAgentExecutionPolicy(ctx, in.Token, route.CapabilityID, route.ProviderSlug, in.RequireReal); err != nil {
		return nil, err
	} else if !decision.Allowed {
		return nil, fmt.Errorf("%s: %s", decision.Code, decision.Message)
	}
	tokenHash := db.accessTokenHash(in.Token)
	grant, err := scanAccessGrant(tx.QueryRowContext(ctx, `
		SELECT id, COALESCE(payment_id, '00000000-0000-0000-0000-000000000000'::uuid), product_id, buyer_wallet,
		       quota_total, quota_remaining, expires_at, status, created_at
		FROM api_access_grants
		WHERE access_token_hash = $1
		  AND EXISTS (
		    SELECT 1 FROM marketplace_products p
		    WHERE p.id = api_access_grants.product_id
		      AND (p.capability_id = $2 OR p.capability = $2)
		  )
		FOR UPDATE`, tokenHash, route.CapabilityID))
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("access token invalido para capability")
		}
		return nil, err
	}
	if grant.Status != GrantStatusActive || time.Now().UTC().After(grant.ExpiresAt) {
		return nil, fmt.Errorf("grant expirado ou inativo")
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE api_access_grants
		SET quota_remaining = quota_remaining - $2,
		    quota_used = quota_used + $2,
		    updated_at = now()
		WHERE id = $1 AND quota_remaining >= $2`, grant.ID, in.Units)
	if err != nil {
		return nil, err
	}
	affected, _ := res.RowsAffected()
	if affected != 1 {
		return nil, fmt.Errorf("quota insuficiente")
	}
	grant.QuotaRemaining -= in.Units
	if grant.QuotaRemaining == 0 {
		_, _ = tx.ExecContext(ctx, `UPDATE api_access_grants SET status = $2, updated_at = now() WHERE id = $1`, grant.ID, MarketplacePurchaseExhausted)
	}
	metadata := json.RawMessage(`{"source":"capability_router","executionMode":"mock"}`)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO api_usage_events (id, grant_id, product_id, units, request_hash, idempotency_key, metadata)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		NewID(), grant.ID, grant.ProductID, in.Units, in.RequestID, in.IdempotencyKey, metadata)
	if err != nil {
		return nil, err
	}
	output := marketplaceMockOutput(route.CapabilityID, in.Operation, route.ProviderSlug)
	event := &MarketplaceCapabilityExecution{
		ID:             "mpe_" + strings.ReplaceAll(NewID(), "-", ""),
		CapabilityID:   route.CapabilityID,
		Operation:      in.Operation,
		ProviderSlug:   route.ProviderSlug,
		ProviderName:   route.ProviderName,
		RouteName:      route.RouteName,
		RoutingMode:    route.RoutingMode,
		Status:         "mock_completed",
		UnitsConsumed:  in.Units,
		QuotaRemaining: grant.QuotaRemaining,
		RequestID:      in.RequestID,
		IdempotencyKey: in.IdempotencyKey,
		Input:          normalizeJSONRaw(in.Input, `{}`),
		Output:         output,
		CreatedAt:      time.Now().UTC(),
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO marketplace_execution_events (
		  id, capability_id, grant_id, product_id, provider_slug, provider_name, route_name,
		  routing_mode, operation, request_id, idempotency_key, units_consumed,
		  quota_remaining, status, input_json, output_json
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		event.ID, event.CapabilityID, grant.ID, grant.ProductID, event.ProviderSlug, event.ProviderName,
		event.RouteName, event.RoutingMode, event.Operation, event.RequestID, event.IdempotencyKey,
		event.UnitsConsumed, event.QuotaRemaining, event.Status, event.Input, event.Output)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &MarketplaceCapabilityExecuteResult{Event: event, Grant: grant}, nil
}

func (db *DB) ExecuteMarketplaceCapabilityWithRiskCredit(ctx context.Context, in MarketplaceCapabilityExecuteInput) (*MarketplaceCapabilityExecuteResult, error) {
	in.AgentWallet = strings.ToLower(strings.TrimSpace(in.AgentWallet))
	if in.AgentWallet == "" {
		return nil, fmt.Errorf("agentWallet obrigatorio para credito de risco")
	}
	if strings.TrimSpace(in.IdempotencyKey) == "" || strings.TrimSpace(in.RequestID) == "" {
		return nil, fmt.Errorf("requestId e idempotencyKey obrigatorios")
	}
	if in.Units <= 0 {
		in.Units = 1
	}
	in.CapabilityID = strings.TrimSpace(in.CapabilityID)
	in.Operation = firstNonEmptyDB(strings.TrimSpace(in.Operation), "execute")

	tx, err := db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if existing, err := db.getMarketplaceExecutionByIdempotencyTx(ctx, tx, in.IdempotencyKey); err != nil {
		return nil, err
	} else if existing != nil {
		return &MarketplaceCapabilityExecuteResult{Event: existing, Duplicate: true}, tx.Commit()
	}

	route, err := db.selectMarketplaceRouteTx(ctx, tx, in)
	if err != nil {
		return nil, err
	}
	product, plan, err := db.cheapestMarketplaceCapabilityPlanTx(ctx, tx, route.CapabilityID)
	if err != nil {
		return nil, err
	}
	priceMicro, err := ParseMicroAmount(plan.PriceAmount)
	if err != nil {
		return nil, err
	}
	unitCostMicro := int64(1)
	if plan.Quota > 0 {
		unitCostMicro = priceMicro / int64(plan.Quota)
		if unitCostMicro <= 0 {
			unitCostMicro = 1
		}
	}
	chargeMicro := unitCostMicro * int64(in.Units)
	const creditLimitMicro int64 = 5_000_000
	const minTopUpMicro int64 = 20_000_000

	_, _ = tx.ExecContext(ctx, `
		INSERT INTO agent_wallets (address, first_seen_at, last_seen_at)
		VALUES ($1, now(), now())
		ON CONFLICT (address) DO UPDATE SET last_seen_at = now()`, in.AgentWallet)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO agent_capability_credit_accounts (
		  id, agent_wallet, capability_id, asset, network, credit_limit_micro,
		  credit_used_micro, min_top_up_micro, expires_at, status
		) VALUES ($1,$2,$3,$4,$5,$6,0,$7,now() + interval '7 days','active')
		ON CONFLICT (agent_wallet, capability_id, asset, network) DO NOTHING`,
		"acc_"+strings.ReplaceAll(NewID(), "-", ""), in.AgentWallet, route.CapabilityID, plan.PaymentAsset, plan.Network, creditLimitMicro, minTopUpMicro)
	if err != nil {
		return nil, err
	}

	account, err := scanAgentCapabilityCreditAccount(tx.QueryRowContext(ctx, `
		SELECT id, agent_wallet, capability_id, asset, network, credit_limit_micro,
		       credit_used_micro, min_top_up_micro, expires_at, status,
		       COALESCE(last_payment_required_at, '0001-01-01T00:00:00Z'::timestamptz)
		FROM agent_capability_credit_accounts
		WHERE agent_wallet = $1 AND capability_id = $2 AND asset = $3 AND network = $4
		FOR UPDATE`, in.AgentWallet, route.CapabilityID, plan.PaymentAsset, plan.Network))
	if err != nil {
		return nil, err
	}
	remainingMicro := account.creditLimitMicro - account.creditUsedMicro
	if account.Status != "active" || time.Now().UTC().After(account.ExpiresAt) || remainingMicro < chargeMicro {
		_, _ = tx.ExecContext(ctx, `
			UPDATE agent_capability_credit_accounts
			SET last_payment_required_at = now(), updated_at = now()
			WHERE id = $1`, account.ID)
		return nil, &AgentCreditPaymentRequiredError{
			AgentWallet:         in.AgentWallet,
			CapabilityID:        route.CapabilityID,
			Asset:               plan.PaymentAsset,
			Network:             plan.Network,
			CreditLimitUSDT:     FormatMicroAmount(account.creditLimitMicro),
			CreditUsedUSDT:      FormatMicroAmount(account.creditUsedMicro),
			CreditRemainingUSDT: FormatMicroAmount(maxInt64(0, remainingMicro)),
			RequiredUSDT:        FormatMicroAmount(chargeMicro),
			MinTopUpUSDT:        FormatMicroAmount(minTopUpMicro),
		}
	}

	grant, err := db.ensureRiskCreditGrantTx(ctx, tx, in.AgentWallet, route.CapabilityID, product.ID, plan.ID, int(creditLimitMicro/unitCostMicro))
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE agent_capability_credit_accounts
		SET credit_used_micro = credit_used_micro + $2, updated_at = now()
		WHERE id = $1`, account.ID, chargeMicro)
	if err != nil {
		return nil, err
	}
	remainingMicro -= chargeMicro
	quotaRemaining := int(remainingMicro / unitCostMicro)
	metadata, _ := json.Marshal(map[string]any{
		"source":           "mcp_risk_credit",
		"chargeUsdt":       FormatMicroAmount(chargeMicro),
		"unitCostUsdt":     FormatMicroAmount(unitCostMicro),
		"creditLimitUsdt":  FormatMicroAmount(creditLimitMicro),
		"minTopUpUsdt":     FormatMicroAmount(minTopUpMicro),
		"creditTtlSeconds": 604800,
	})
	_, err = tx.ExecContext(ctx, `
		INSERT INTO api_usage_events (id, grant_id, product_id, units, request_hash, idempotency_key, metadata)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		NewID(), grant.ID, product.ID, in.Units, in.RequestID, in.IdempotencyKey, json.RawMessage(metadata))
	if err != nil {
		return nil, err
	}
	output := marketplaceMockOutput(route.CapabilityID, in.Operation, route.ProviderSlug)
	event := &MarketplaceCapabilityExecution{
		ID:             "mpe_" + strings.ReplaceAll(NewID(), "-", ""),
		CapabilityID:   route.CapabilityID,
		Operation:      in.Operation,
		ProviderSlug:   route.ProviderSlug,
		ProviderName:   route.ProviderName,
		RouteName:      route.RouteName,
		RoutingMode:    route.RoutingMode,
		Status:         "credit_completed",
		UnitsConsumed:  in.Units,
		QuotaRemaining: quotaRemaining,
		RequestID:      in.RequestID,
		IdempotencyKey: in.IdempotencyKey,
		Input:          normalizeJSONRaw(in.Input, `{}`),
		Output:         output,
		CreatedAt:      time.Now().UTC(),
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO marketplace_execution_events (
		  id, capability_id, grant_id, product_id, provider_slug, provider_name, route_name,
		  routing_mode, operation, request_id, idempotency_key, units_consumed,
		  quota_remaining, status, input_json, output_json
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		event.ID, event.CapabilityID, grant.ID, product.ID, event.ProviderSlug, event.ProviderName,
		event.RouteName, event.RoutingMode, event.Operation, event.RequestID, event.IdempotencyKey,
		event.UnitsConsumed, event.QuotaRemaining, event.Status, event.Input, event.Output)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	account.creditUsedMicro += chargeMicro
	return &MarketplaceCapabilityExecuteResult{Event: event, Grant: grant, Credit: account}, nil
}

func (db *DB) CompleteMarketplaceExecution(ctx context.Context, eventID, status string, output json.RawMessage) error {
	return db.CompleteMarketplaceExecutionMetrics(ctx, eventID, status, output, 0, "", "")
}

func (db *DB) cheapestMarketplaceCapabilityPlanTx(ctx context.Context, tx *sql.Tx, capabilityID string) (*MarketplaceProduct, *MarketplacePlan, error) {
	row := tx.QueryRowContext(ctx, marketplacePlanBundleSelect()+`
		WHERE (p.capability_id = $1 OR p.capability = $1)
		  AND pl.status = 'active' AND p.status = 'active' AND pr.status = 'active'
		ORDER BY pl.price_amount ASC, pl.id ASC
		LIMIT 1`, strings.TrimSpace(capabilityID))
	product, _, plan, err := db.getMarketplacePlanBundleScanner(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil, fmt.Errorf("capability sem plano ativo")
		}
		return nil, nil, err
	}
	return product, plan, nil
}

func (db *DB) ensureRiskCreditGrantTx(ctx context.Context, tx *sql.Tx, wallet, capabilityID, productID, planID string, quota int) (*APIAccessGrant, error) {
	if quota <= 0 {
		quota = 1
	}
	wallet = strings.ToLower(strings.TrimSpace(wallet))
	tokenHash := db.accessTokenHash("risk_credit:" + wallet + ":" + strings.TrimSpace(capabilityID))
	expiresAt := time.Now().UTC().Add(7 * 24 * time.Hour)
	_, err := tx.ExecContext(ctx, `
		INSERT INTO api_access_grants (
		  id, payment_id, purchase_id, plan_id, product_id, buyer_wallet, access_token_hash,
		  quota_total, quota_remaining, quota_used, valid_from, expires_at, status
		) VALUES ($1,NULL,NULL,$2,$3,$4,$5,$6,$6,0,now(),$7,$8)
		ON CONFLICT (access_token_hash) DO UPDATE SET
		  plan_id = EXCLUDED.plan_id,
		  product_id = EXCLUDED.product_id,
		  quota_total = GREATEST(api_access_grants.quota_total, EXCLUDED.quota_total),
		  expires_at = GREATEST(api_access_grants.expires_at, EXCLUDED.expires_at),
		  status = 'active',
		  updated_at = now()`,
		NewID(), planID, productID, wallet, tokenHash, quota, expiresAt, GrantStatusActive)
	if err != nil {
		return nil, err
	}
	grant, err := scanAccessGrant(tx.QueryRowContext(ctx, `
		SELECT id, COALESCE(payment_id, '00000000-0000-0000-0000-000000000000'::uuid), product_id, buyer_wallet,
		       quota_total, quota_remaining, expires_at, status, created_at
		FROM api_access_grants
		WHERE access_token_hash = $1
		FOR UPDATE`, tokenHash))
	if err != nil {
		return nil, err
	}
	return grant, nil
}

func scanAgentCapabilityCreditAccount(row rowScanner) (*AgentCapabilityCreditAccount, error) {
	var a AgentCapabilityCreditAccount
	var lastPaymentRequiredAt time.Time
	if err := row.Scan(&a.ID, &a.AgentWallet, &a.CapabilityID, &a.Asset, &a.Network, &a.creditLimitMicro, &a.creditUsedMicro, &a.minTopUpMicro, &a.ExpiresAt, &a.Status, &lastPaymentRequiredAt); err != nil {
		return nil, err
	}
	a.CreditLimitUSDT = FormatMicroAmount(a.creditLimitMicro)
	a.CreditUsedUSDT = FormatMicroAmount(a.creditUsedMicro)
	a.CreditRemainingUSDT = FormatMicroAmount(maxInt64(0, a.creditLimitMicro-a.creditUsedMicro))
	a.MinTopUpUSDT = FormatMicroAmount(a.minTopUpMicro)
	if !lastPaymentRequiredAt.IsZero() && lastPaymentRequiredAt.Year() > 1 {
		a.LastPaymentRequiredAt = lastPaymentRequiredAt
	}
	return &a, nil
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func (db *DB) CompleteMarketplaceExecutionMetrics(ctx context.Context, eventID, status string, output json.RawMessage, latencyMS int, errorCode, errorMessage string) error {
	status = firstNonEmptyDB(strings.TrimSpace(status), "real_completed")
	output = normalizeJSONRaw(output, `{}`)
	_, err := db.SQL.ExecContext(ctx, `
		UPDATE marketplace_execution_events
		SET status = $2, output_json = $3, latency_ms = $4, error_code = NULLIF($5, ''), error_message = NULLIF($6, '')
		WHERE id = $1`, strings.TrimSpace(eventID), status, output, latencyMS, strings.TrimSpace(errorCode), strings.TrimSpace(errorMessage))
	return err
}

func (db *DB) RecordMarketplaceProviderMetric(ctx context.Context, capabilityID, providerSlug, status string, latencyMS int) error {
	if strings.TrimSpace(capabilityID) == "" || strings.TrimSpace(providerSlug) == "" {
		return nil
	}
	if latencyMS < 0 {
		latencyMS = 0
	}
	success := strings.EqualFold(status, "real_completed")
	successBps := 0
	if success {
		successBps = 10000
	}
	_, err := db.SQL.ExecContext(ctx, `
		UPDATE marketplace_provider_policies
		SET latency_ms = CASE
		      WHEN latency_ms <= 0 OR $3 <= 0 THEN GREATEST(latency_ms, $3)
		      ELSE ((latency_ms * 8) + ($3 * 2)) / 10
		    END,
		    success_rate_bps = ((success_rate_bps * 9) + $4) / 10,
		    updated_at = now()
		WHERE capability_id = $1 AND provider_slug = $2`,
		strings.TrimSpace(capabilityID), strings.TrimSpace(providerSlug), latencyMS, successBps)
	return err
}

func (db *DB) ReassignMarketplaceExecutionProvider(ctx context.Context, eventID string, candidate *MarketplaceRouteCandidate) error {
	if candidate == nil {
		return nil
	}
	_, err := db.SQL.ExecContext(ctx, `
		UPDATE marketplace_execution_events
		SET provider_slug = $2, provider_name = $3, route_name = $4, routing_mode = $5
		WHERE id = $1`,
		strings.TrimSpace(eventID), candidate.ProviderSlug, candidate.ProviderName, candidate.RouteName, candidate.RoutingMode)
	return err
}

func (db *DB) ApplyMarketplaceMemoryOperation(ctx context.Context, event *MarketplaceCapabilityExecution) (json.RawMessage, error) {
	if event == nil {
		return nil, fmt.Errorf("execution event obrigatorio")
	}
	var input map[string]any
	if len(event.Input) > 0 {
		_ = json.Unmarshal(event.Input, &input)
	}
	if input == nil {
		input = map[string]any{}
	}
	namespace := firstNonEmptyDB(stringFromMap(input, "namespace"), event.ProviderSlug, "default")
	key := firstNonEmptyDB(stringFromMap(input, "key"), stringFromMap(input, "memoryId"), "mem_"+strings.ReplaceAll(NewID(), "-", ""))
	content := firstNonEmptyDB(stringFromMap(input, "content"), stringFromMap(input, "text"), stringFromMap(input, "value"))
	query := firstNonEmptyDB(stringFromMap(input, "query"), stringFromMap(input, "text"), content)
	switch strings.ToLower(strings.TrimSpace(event.Operation)) {
	case "save_memory":
		if content == "" {
			return nil, fmt.Errorf("content e obrigatorio para save_memory")
		}
		_, err := db.SQL.ExecContext(ctx, `
			INSERT INTO marketplace_memory_entries (id, namespace, memory_key, content, metadata_json)
			VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (namespace, memory_key) DO UPDATE
			SET content = EXCLUDED.content, metadata_json = EXCLUDED.metadata_json, updated_at = now()`,
			"mem_"+strings.ReplaceAll(NewID(), "-", ""), namespace, key, content, normalizeJSONRaw(event.Input, `{}`))
		if err != nil {
			return nil, err
		}
		raw, _ := json.Marshal(map[string]any{"mode": "real", "provider": "chainfx-memory", "operation": "save_memory", "memoryId": key, "saved": true})
		return raw, nil
	case "get_memory":
		var id, stored string
		err := db.SQL.QueryRowContext(ctx, `
			SELECT id, content FROM marketplace_memory_entries
			WHERE namespace = $1 AND memory_key = $2 AND status = 'active'`, namespace, key).Scan(&id, &stored)
		if err == sql.ErrNoRows {
			raw, _ := json.Marshal(map[string]any{"mode": "real", "provider": "chainfx-memory", "operation": "get_memory", "memoryId": key, "found": false})
			return raw, nil
		}
		if err != nil {
			return nil, err
		}
		raw, _ := json.Marshal(map[string]any{"mode": "real", "provider": "chainfx-memory", "operation": "get_memory", "id": id, "memoryId": key, "content": stored, "found": true})
		return raw, nil
	case "semantic_search", "knowledge_lookup":
		if query == "" {
			return nil, fmt.Errorf("query e obrigatoria para busca de memoria")
		}
		rows, err := db.SQL.QueryContext(ctx, `
			SELECT memory_key, content, created_at
			FROM marketplace_memory_entries
			WHERE namespace = $1 AND status = 'active' AND content ILIKE '%' || $2 || '%'
			ORDER BY updated_at DESC
			LIMIT 10`, namespace, query)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		results := []map[string]any{}
		for rows.Next() {
			var memoryKey, stored string
			var createdAt time.Time
			if err := rows.Scan(&memoryKey, &stored, &createdAt); err != nil {
				return nil, err
			}
			results = append(results, map[string]any{"memoryId": memoryKey, "content": stored, "createdAt": createdAt})
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		raw, _ := json.Marshal(map[string]any{"mode": "real", "provider": "chainfx-memory", "operation": event.Operation, "query": query, "results": results})
		return raw, nil
	default:
		return nil, fmt.Errorf("operacao de memoria nao suportada: %s", event.Operation)
	}
}

func (db *DB) listMarketplaceProductByWhere(ctx context.Context, where string, args ...any) ([]*MarketplaceProduct, error) {
	rows, err := db.SQL.QueryContext(ctx, `
		SELECT p.id, p.provider_id, p.slug, p.name, p.description, p.category, p.delivery_type, p.status,
		       COALESCE(p.capability_id, ''), p.capability, COALESCE(p.documentation_url,''), COALESCE(p.endpoint_base_url,''), COALESCE(p.metadata_json, '{}'::jsonb), p.created_at,
		       pr.id, pr.slug, pr.name, pr.description, COALESCE(pr.website_url,''), pr.settlement_wallet,
		       pr.settlement_asset, pr.settlement_network, pr.status, COALESCE(pr.metadata_json, '{}'::jsonb), pr.created_at,
		       pl.id, pl.product_id, pl.slug, pl.name, pl.price_amount::text, pl.payment_asset, pl.network,
		       pl.take_rate_bps, pl.quota, pl.validity_seconds, pl.status, COALESCE(pl.metadata_json, '{}'::jsonb), pl.created_at
		FROM marketplace_products p
		JOIN marketplace_providers pr ON pr.id = p.provider_id
		JOIN marketplace_plans pl ON pl.product_id = p.id
		WHERE `+where+`
		ORDER BY pl.price_amount ASC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := map[string]*MarketplaceProduct{}
	order := []string{}
	for rows.Next() {
		product, provider, plan, err := scanMarketplaceProductRow(rows)
		if err != nil {
			return nil, err
		}
		if byID[product.ID] == nil {
			product.Provider = provider
			product.Plans = []*MarketplacePlan{}
			byID[product.ID] = product
			order = append(order, product.ID)
		}
		byID[product.ID].Plans = append(byID[product.ID].Plans, plan)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := []*MarketplaceProduct{}
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out, nil
}

func (db *DB) CreateMarketplacePurchase(ctx context.Context, in MarketplacePurchaseInput) (*MarketplacePurchase, *MarketplaceProduct, *MarketplacePlan, error) {
	tx, err := db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, nil, err
	}
	defer tx.Rollback()
	product, provider, plan, err := db.getMarketplacePlanBundleActiveTx(ctx, tx, in.PlanID)
	if err != nil {
		return nil, nil, nil, err
	}
	if plan == nil || product == nil || provider == nil {
		return nil, nil, nil, fmt.Errorf("plano nao encontrado")
	}
	if err := EnforceCapabilityTopUpMinimum(plan.PriceAmount, plan.PaymentAsset, plan.Network); err != nil {
		return nil, nil, nil, err
	}
	gross, err := ParseMicroAmount(plan.PriceAmount)
	if err != nil {
		return nil, nil, nil, err
	}
	capabilityID := firstNonEmptyDB(product.CapabilityID, product.Capability)
	if _, decision, err := db.ValidateAgentPurchasePolicy(ctx, in.AgentWallet, capabilityID, plan.PaymentAsset, plan.PriceAmount); err != nil {
		return nil, nil, nil, err
	} else if !decision.Allowed {
		return nil, nil, nil, fmt.Errorf("%s: %s", decision.Code, decision.Message)
	}
	chainfx := gross * int64(plan.TakeRateBps) / 10000
	providerAmount := gross - chainfx
	idempotencyKey := strings.TrimSpace(in.IdempotencyKey)
	var idempotency any
	if idempotencyKey != "" {
		idempotency = idempotencyKey
	}
	p := &MarketplacePurchase{
		ID:              "mp_" + strings.ReplaceAll(NewID(), "-", ""),
		ProviderID:      provider.ID,
		ProductID:       product.ID,
		PlanID:          plan.ID,
		AgentWallet:     strings.ToLower(strings.TrimSpace(in.AgentWallet)),
		PayerWallet:     strings.ToLower(strings.TrimSpace(in.PayerWallet)),
		PaymentAddress:  strings.ToLower(strings.TrimSpace(in.PaymentAddress)),
		PaymentAsset:    plan.PaymentAsset,
		PaymentContract: strings.ToLower(strings.TrimSpace(in.PaymentContract)),
		Network:         plan.Network,
		ChainID:         marketplaceNetworkChainID(plan.Network),
		GrossAmount:     FormatMicroAmount(gross),
		ChainFXAmount:   FormatMicroAmount(chainfx),
		ProviderAmount:  FormatMicroAmount(providerAmount),
		TakeRateBps:     plan.TakeRateBps,
		Nonce:           in.Nonce,
		ExpiresAt:       in.ExpiresAt,
		Status:          MarketplacePurchasePendingPayment,
	}
	p.RequestHash = MarketplaceRequestHash(
		p.ProviderID,
		p.ProductID,
		p.PlanID,
		p.AgentWallet,
		p.PayerWallet,
		p.PaymentAddress,
		p.PaymentAsset,
		p.PaymentContract,
		p.GrossAmount,
		p.ChainFXAmount,
		p.ProviderAmount,
		fmt.Sprintf("%d", p.TakeRateBps),
		fmt.Sprintf("%d", p.ChainID),
		p.Nonce,
		p.ExpiresAt.UTC().Format(time.RFC3339Nano),
	)
	_, _ = tx.ExecContext(ctx, `
		INSERT INTO agent_wallets (address, first_seen_at, last_seen_at)
		VALUES ($1, now(), now())
		ON CONFLICT (address) DO UPDATE SET last_seen_at = now()`, p.AgentWallet)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO marketplace_purchases (
		  id, provider_id, product_id, plan_id, agent_wallet, payer_wallet, payment_address,
		  payment_asset, payment_contract, network, chain_id, gross_amount, chainfx_amount,
		  provider_amount, take_rate_bps, request_hash, nonce, idempotency_key, expires_at, status
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)`,
		p.ID, p.ProviderID, p.ProductID, p.PlanID, p.AgentWallet, p.PayerWallet, p.PaymentAddress,
		p.PaymentAsset, p.PaymentContract, p.Network, p.ChainID, p.GrossAmount, p.ChainFXAmount,
		p.ProviderAmount, p.TakeRateBps, p.RequestHash, p.Nonce, idempotency, p.ExpiresAt, p.Status)
	if err != nil {
		if pqErr, ok := err.(*pq.Error); ok && pqErr.Code == "23505" && idempotencyKey != "" {
			_ = tx.Rollback()
			existing, getErr := db.GetMarketplacePurchaseByIdempotency(ctx, idempotencyKey)
			return existing, product, plan, getErr
		}
		return nil, nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, nil, err
	}
	return p, product, plan, nil
}

func marketplaceNetworkChainID(network string) int64 {
	switch strings.ToUpper(strings.TrimSpace(network)) {
	case "POLYGON", "POL", "MATIC":
		return 137
	default:
		return 56
	}
}

func (db *DB) GetMarketplacePurchase(ctx context.Context, id string) (*MarketplacePurchase, error) {
	p, err := scanMarketplacePurchase(db.SQL.QueryRowContext(ctx, marketplacePurchaseSelect()+` WHERE id = $1`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return p, err
}

func (db *DB) GetMarketplacePurchaseByIdempotency(ctx context.Context, key string) (*MarketplacePurchase, error) {
	p, err := scanMarketplacePurchase(db.SQL.QueryRowContext(ctx, marketplacePurchaseSelect()+` WHERE idempotency_key = $1`, key))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return p, err
}

func (db *DB) ActivateMarketplacePurchase(ctx context.Context, purchaseID string, receipt AgentTradeReceipt) (*MarketplaceActivationResult, error) {
	tx, err := db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	purchase, err := scanMarketplacePurchase(tx.QueryRowContext(ctx, marketplacePurchaseSelect()+` WHERE id = $1 FOR UPDATE`, purchaseID))
	if err != nil {
		return nil, err
	}
	if purchase.Status == MarketplacePurchaseActive {
		grant, _ := db.getMarketplaceGrantByPurchase(ctx, tx, purchase.ID)
		return &MarketplaceActivationResult{Purchase: purchase, Grant: grant}, tx.Commit()
	}
	if purchase.Status != MarketplacePurchasePendingPayment {
		return nil, fmt.Errorf("purchase em estado invalido: %s", purchase.Status)
	}
	if time.Now().UTC().After(purchase.ExpiresAt) {
		_, _ = tx.ExecContext(ctx, `UPDATE marketplace_purchases SET status = $2, failed_at = now(), failure_code = 'expired', failure_message = 'purchase expired', updated_at = now() WHERE id = $1`, purchase.ID, MarketplacePurchaseExpired)
		return nil, fmt.Errorf("purchase expirada")
	}
	_, err = tx.ExecContext(ctx, `UPDATE marketplace_purchases SET status = $2, updated_at = now() WHERE id = $1`, purchase.ID, MarketplacePurchasePaymentDetected)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE marketplace_purchases SET status = $2, updated_at = now() WHERE id = $1`, purchase.ID, MarketplacePurchaseVerifying)
	if err != nil {
		return nil, err
	}
	if receipt.ChainID != purchase.ChainID {
		_, _ = tx.ExecContext(ctx, `UPDATE marketplace_purchases SET status = $2, failed_at = now(), failure_code = 'wrong_chain', failure_message = 'chainId mismatch', updated_at = now() WHERE id = $1`, purchase.ID, MarketplacePurchasePaymentInvalid)
		return nil, fmt.Errorf("chainId nao confere")
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE marketplace_purchases
		SET status = $2, tx_hash = $3, tx_log_index = $4, tx_block_number = $5, tx_block_hash = $6,
		    transfer_from = $7, transfer_to = $8, transfer_amount_raw = $9, overpayment_amount = $10,
		    paid_at = now(), updated_at = now()
		WHERE id = $1`,
		purchase.ID, MarketplacePurchasePaid, receipt.TxHash, receipt.LogIndex, receipt.BlockNumber,
		receipt.BlockHash, receipt.TransferFrom, receipt.TransferTo, receipt.TransferAmountRaw,
		FormatMicroAmount(MustMicroFromFloat(receipt.OverpaymentAmount)))
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE marketplace_purchases SET status = $2, updated_at = now() WHERE id = $1`, purchase.ID, MarketplacePurchaseGrantingAccess)
	if err != nil {
		return nil, err
	}
	_, _, plan, err := db.getMarketplacePlanBundleTx(ctx, tx, purchase.PlanID)
	if err != nil {
		return nil, err
	}
	token := "cfx_access_" + NewAccessToken()
	tokenHash := db.accessTokenHash(token)
	grant := &APIAccessGrant{
		ID:             NewID(),
		PaymentID:      "",
		ProductID:      purchase.ProductID,
		BuyerWallet:    purchase.AgentWallet,
		QuotaTotal:     plan.Quota,
		QuotaRemaining: plan.Quota,
		ExpiresAt:      time.Now().UTC().Add(time.Duration(plan.ValiditySeconds) * time.Second),
		Status:         GrantStatusActive,
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO api_access_grants (
		  id, purchase_id, plan_id, product_id, buyer_wallet, access_token_hash,
		  quota_total, quota_remaining, quota_used, valid_from, expires_at, status
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,0,now(),$9,$10)
		ON CONFLICT (purchase_id) DO NOTHING`,
		grant.ID, purchase.ID, purchase.PlanID, purchase.ProductID, purchase.AgentWallet, tokenHash,
		grant.QuotaTotal, grant.QuotaRemaining, grant.ExpiresAt, grant.Status)
	if err != nil {
		_, _ = tx.ExecContext(ctx, `UPDATE marketplace_purchases SET status = $2, failed_at = now(), failure_code = 'grant_failed', failure_message = $3, updated_at = now() WHERE id = $1`, purchase.ID, MarketplacePurchaseGrantFailed, err.Error())
		return nil, err
	}
	if existing, err := db.getMarketplaceGrantByPurchase(ctx, tx, purchase.ID); err == nil && existing != nil {
		grant = existing
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO marketplace_provider_settlements (
		  id, provider_id, purchase_id, asset, network, gross_amount, chainfx_amount,
		  provider_amount, status, settlement_wallet
		) SELECT $1, p.provider_id, p.id, p.payment_asset, p.network, p.gross_amount,
		         p.chainfx_amount, p.provider_amount, $2, pr.settlement_wallet
		  FROM marketplace_purchases p JOIN marketplace_providers pr ON pr.id = p.provider_id
		  WHERE p.id = $3
		ON CONFLICT (purchase_id) DO NOTHING`,
		"mps_"+strings.ReplaceAll(NewID(), "-", ""), MarketplaceSettlementPending, purchase.ID)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE marketplace_purchases
		SET status = $2, granted_at = now(), updated_at = now()
		WHERE id = $1`, purchase.ID, MarketplacePurchaseActive)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	activated, _ := db.GetMarketplacePurchase(ctx, purchase.ID)
	return &MarketplaceActivationResult{Purchase: activated, AccessToken: token, Grant: grant}, nil
}

func (db *DB) ConsumeMarketplaceUsage(ctx context.Context, token string, units int, requestID, idempotencyKey string) (*APIAccessGrant, bool, error) {
	return db.consumeMarketplaceUsage(ctx, token, units, requestID, idempotencyKey, "")
}

func (db *DB) ConsumeMarketplaceCapabilityUsage(ctx context.Context, token string, units int, requestID, idempotencyKey, capabilityID string) (*APIAccessGrant, bool, error) {
	return db.consumeMarketplaceUsage(ctx, token, units, requestID, idempotencyKey, strings.TrimSpace(capabilityID))
}

func (db *DB) consumeMarketplaceUsage(ctx context.Context, token string, units int, requestID, idempotencyKey, capabilityID string) (*APIAccessGrant, bool, error) {
	if units <= 0 {
		units = 1
	}
	if strings.TrimSpace(idempotencyKey) == "" {
		return nil, false, fmt.Errorf("idempotencyKey obrigatorio")
	}
	tokenHash := db.accessTokenHash(token)
	tx, err := db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	query := `
		SELECT id, COALESCE(payment_id, '00000000-0000-0000-0000-000000000000'::uuid), product_id, buyer_wallet,
		       quota_total, quota_remaining, expires_at, status, created_at
		FROM api_access_grants WHERE access_token_hash = $1`
	args := []any{tokenHash}
	if capabilityID != "" {
		args = append(args, capabilityID)
		query += ` AND EXISTS (
			SELECT 1 FROM marketplace_products p
			WHERE p.id = api_access_grants.product_id
			  AND (p.capability_id = $2 OR p.capability = $2)
		)`
	}
	query += ` FOR UPDATE`
	grant, err := scanAccessGrant(tx.QueryRowContext(ctx, query, args...))
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, false, fmt.Errorf("access token invalido")
		}
		return nil, false, err
	}
	if grant.Status != GrantStatusActive || time.Now().UTC().After(grant.ExpiresAt) {
		return nil, false, fmt.Errorf("grant expirado ou inativo")
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM api_usage_events WHERE grant_id = $1 AND idempotency_key = $2)`, grant.ID, idempotencyKey).Scan(&exists); err != nil {
		return nil, false, err
	}
	if exists {
		return grant, true, tx.Commit()
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE api_access_grants
		SET quota_remaining = quota_remaining - $2,
		    quota_used = quota_used + $2,
		    updated_at = now()
		WHERE id = $1 AND quota_remaining >= $2`, grant.ID, units)
	if err != nil {
		return nil, false, err
	}
	affected, _ := res.RowsAffected()
	if affected != 1 {
		return nil, false, fmt.Errorf("quota insuficiente")
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO api_usage_events (id, grant_id, product_id, units, request_hash, idempotency_key, metadata)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		NewID(), grant.ID, grant.ProductID, units, requestID, idempotencyKey, json.RawMessage(`{"source":"marketplace"}`))
	if err != nil {
		return nil, false, err
	}
	grant.QuotaRemaining -= units
	if grant.QuotaRemaining == 0 {
		_, _ = tx.ExecContext(ctx, `UPDATE api_access_grants SET status = $2, updated_at = now() WHERE id = $1`, grant.ID, MarketplacePurchaseExhausted)
	}
	return grant, false, tx.Commit()
}

func (db *DB) getMarketplaceGrantByPurchase(ctx context.Context, tx *sql.Tx, purchaseID string) (*APIAccessGrant, error) {
	grant, err := scanAccessGrant(tx.QueryRowContext(ctx, `
		SELECT id, COALESCE(payment_id, '00000000-0000-0000-0000-000000000000'::uuid), product_id, buyer_wallet,
		       quota_total, quota_remaining, expires_at, status, created_at
		FROM api_access_grants WHERE purchase_id = $1`, purchaseID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return grant, err
}

func (db *DB) getMarketplacePlanBundle(ctx context.Context, planID string) (*MarketplaceProduct, *MarketplaceProvider, *MarketplacePlan, error) {
	return db.getMarketplacePlanBundleScanner(db.SQL.QueryRowContext(ctx, marketplacePlanBundleSelect()+` WHERE pl.id = $1 AND pl.status = 'active' AND p.status = 'active' AND pr.status = 'active'`, planID))
}

func (db *DB) getMarketplacePlanBundleActiveTx(ctx context.Context, tx *sql.Tx, planID string) (*MarketplaceProduct, *MarketplaceProvider, *MarketplacePlan, error) {
	return db.getMarketplacePlanBundleScanner(tx.QueryRowContext(ctx, marketplacePlanBundleSelect()+` WHERE pl.id = $1 AND pl.status = 'active' AND p.status = 'active' AND pr.status = 'active' FOR SHARE OF pl, p, pr`, planID))
}

func (db *DB) getMarketplacePlanBundleTx(ctx context.Context, tx *sql.Tx, planID string) (*MarketplaceProduct, *MarketplaceProvider, *MarketplacePlan, error) {
	return db.getMarketplacePlanBundleScanner(tx.QueryRowContext(ctx, marketplacePlanBundleSelect()+` WHERE pl.id = $1`, planID))
}

func (db *DB) getMarketplacePlanBundleScanner(row rowScanner) (*MarketplaceProduct, *MarketplaceProvider, *MarketplacePlan, error) {
	product, provider, plan, err := scanMarketplaceProductRow(row)
	return product, provider, plan, err
}

func marketplacePlanBundleSelect() string {
	return `SELECT p.id, p.provider_id, p.slug, p.name, p.description, p.category, p.delivery_type, p.status,
	       COALESCE(p.capability_id, ''), p.capability, COALESCE(p.documentation_url,''), COALESCE(p.endpoint_base_url,''), COALESCE(p.metadata_json, '{}'::jsonb), p.created_at,
	       pr.id, pr.slug, pr.name, pr.description, COALESCE(pr.website_url,''), pr.settlement_wallet,
	       pr.settlement_asset, pr.settlement_network, pr.status, COALESCE(pr.metadata_json, '{}'::jsonb), pr.created_at,
	       pl.id, pl.product_id, pl.slug, pl.name, pl.price_amount::text, pl.payment_asset, pl.network,
	       pl.take_rate_bps, pl.quota, pl.validity_seconds, pl.status, COALESCE(pl.metadata_json, '{}'::jsonb), pl.created_at
	FROM marketplace_plans pl
	JOIN marketplace_products p ON p.id = pl.product_id
	JOIN marketplace_providers pr ON pr.id = p.provider_id`
}

func scanMarketplaceProductRow(row rowScanner) (*MarketplaceProduct, *MarketplaceProvider, *MarketplacePlan, error) {
	var p MarketplaceProduct
	var pr MarketplaceProvider
	var pl MarketplacePlan
	if err := row.Scan(
		&p.ID, &p.ProviderID, &p.Slug, &p.Name, &p.Description, &p.Category, &p.DeliveryType, &p.Status,
		&p.CapabilityID, &p.Capability, &p.DocumentationURL, &p.EndpointBaseURL, &p.Metadata, &p.CreatedAt,
		&pr.ID, &pr.Slug, &pr.Name, &pr.Description, &pr.WebsiteURL, &pr.SettlementWallet,
		&pr.SettlementAsset, &pr.SettlementNetwork, &pr.Status, &pr.Metadata, &pr.CreatedAt,
		&pl.ID, &pl.ProductID, &pl.Slug, &pl.Name, &pl.PriceAmount, &pl.PaymentAsset, &pl.Network,
		&pl.TakeRateBps, &pl.Quota, &pl.ValiditySeconds, &pl.Status, &pl.Metadata, &pl.CreatedAt,
	); err != nil {
		return nil, nil, nil, err
	}
	return &p, &pr, &pl, nil
}

func scanMarketplaceCapability(row rowScanner) (*MarketplaceCapability, error) {
	var c MarketplaceCapability
	if err := row.Scan(&c.ID, &c.Slug, &c.DisplayName, &c.Description, &c.Category, &c.RoutingMode, &c.Status, &c.Operations, &c.Metadata, &c.CreatedAt); err != nil {
		return nil, err
	}
	c.Providers = []string{}
	c.Plans = []map[string]any{}
	return &c, nil
}

func (db *DB) fillMarketplaceCapabilityDetails(ctx context.Context, capability *MarketplaceCapability) error {
	rows, err := db.SQL.QueryContext(ctx, `
		SELECT provider_slug
		FROM marketplace_capability_providers
		WHERE capability_id = $1 AND status IN ('active','planned')
		ORDER BY routing_priority ASC, provider_slug ASC`, capability.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var provider string
		if err := rows.Scan(&provider); err != nil {
			return err
		}
		capability.Providers = append(capability.Providers, provider)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	planRows, err := db.SQL.QueryContext(ctx, `
		SELECT pl.id, pl.name, pl.price_amount::text, pl.payment_asset, pl.network,
		       pl.take_rate_bps, pl.quota, pl.validity_seconds, p.id, p.name
		FROM marketplace_products p
		JOIN marketplace_providers pr ON pr.id = p.provider_id
		JOIN marketplace_plans pl ON pl.product_id = p.id
		WHERE (p.capability_id = $1 OR p.capability = $1)
		  AND p.status = 'active' AND pr.status = 'active' AND pl.status = 'active'
		ORDER BY pl.price_amount ASC`, capability.ID)
	if err != nil {
		return err
	}
	defer planRows.Close()
	for planRows.Next() {
		var planID, planName, price, asset, network, productID, productName string
		var takeRateBps, quota, validitySeconds int
		if err := planRows.Scan(&planID, &planName, &price, &asset, &network, &takeRateBps, &quota, &validitySeconds, &productID, &productName); err != nil {
			return err
		}
		capability.Plans = append(capability.Plans, map[string]any{
			"id":              planID,
			"name":            planName,
			"price":           price,
			"asset":           asset,
			"network":         network,
			"takeRateBps":     takeRateBps,
			"quota":           quota,
			"validitySeconds": validitySeconds,
			"product":         map[string]string{"id": productID, "name": productName},
		})
	}
	return planRows.Err()
}

func (db *DB) selectMarketplaceRouteTx(ctx context.Context, tx *sql.Tx, in MarketplaceCapabilityExecuteInput) (*MarketplaceRouteCandidate, error) {
	candidates, err := db.listMarketplaceRouteCandidatesTx(ctx, tx, in)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("rota de capability nao encontrada")
	}
	return SelectMarketplaceRouteCandidate(candidates, in.RoutingMode), nil
}

func (db *DB) ListMarketplaceRouteCandidates(ctx context.Context, in MarketplaceCapabilityExecuteInput) ([]*MarketplaceRouteCandidate, error) {
	if in.CapabilityID == "" {
		return nil, fmt.Errorf("capability obrigatoria")
	}
	tx, err := db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	candidates, err := db.listMarketplaceRouteCandidatesTx(ctx, tx, in)
	if err != nil {
		return nil, err
	}
	return SortMarketplaceRouteCandidates(candidates, in.RoutingMode), tx.Commit()
}

func (db *DB) listMarketplaceRouteCandidatesTx(ctx context.Context, tx *sql.Tx, in MarketplaceCapabilityExecuteInput) ([]*MarketplaceRouteCandidate, error) {
	args := []any{strings.TrimSpace(in.CapabilityID)}
	providerFilter := ""
	if strings.TrimSpace(in.RequestedProvider) != "" {
		args = append(args, strings.ToLower(strings.TrimSpace(in.RequestedProvider)))
		providerFilter = fmt.Sprintf(" AND cp.provider_slug = $%d", len(args))
	}
	unitFilter := ""
	if in.Units > 0 {
		args = append(args, in.Units)
		unitFilter = fmt.Sprintf(" AND (pp.max_units_per_request IS NULL OR pp.max_units_per_request >= $%d)", len(args))
	}
	regionFilter := ""
	if strings.TrimSpace(in.Region) != "" {
		args = append(args, strings.ToLower(strings.TrimSpace(in.Region)))
		regionFilter = fmt.Sprintf(" AND (COALESCE(pp.region, 'global') IN ('global', $%d))", len(args))
	}
	latencyFilter := ""
	if in.MaxLatencyMS > 0 {
		args = append(args, in.MaxLatencyMS)
		latencyFilter = fmt.Sprintf(" AND COALESCE(pp.latency_ms, 1000) <= $%d", len(args))
	}
	costFilter := ""
	if in.MaxCostScore > 0 {
		args = append(args, in.MaxCostScore)
		costFilter = fmt.Sprintf(" AND COALESCE(pp.cost_score, 100) <= $%d", len(args))
	}
	statusFilter := "cp.status IN ('active','planned') AND COALESCE(pp.status, 'active') IN ('active','planned')"
	if in.RequireReal {
		statusFilter = "cp.status = 'active' AND COALESCE(pp.status, 'active') = 'active'"
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT c.id,
		       COALESCE(r.route_name, 'default'),
		       COALESCE(NULLIF($$`+sanitizeRoutingMode(in.RoutingMode)+`$$, ''), r.routing_mode, c.routing_mode),
		       COALESCE(r.fallback_enabled, true),
		       cp.provider_slug,
		       cp.provider_name,
		       cp.status,
		       COALESCE(pp.priority, cp.routing_priority, 100),
		       COALESCE(pp.cost_score, 100),
		       COALESCE(pp.latency_ms, 1000),
		       COALESCE(pp.quality_score, 50),
		       COALESCE(pp.success_rate_bps, 10000),
		       COALESCE(pp.region, 'global'),
		       COALESCE(pp.fallback_order, COALESCE(pp.priority, cp.routing_priority, 100)),
		       COALESCE(pp.policy_json, '{}'::jsonb)
		FROM marketplace_capabilities c
		JOIN marketplace_capability_providers cp ON cp.capability_id = c.id
		LEFT JOIN marketplace_routes r ON r.capability_id = c.id AND r.status = 'active'
		LEFT JOIN marketplace_provider_policies pp ON pp.capability_id = c.id AND pp.provider_slug = cp.provider_slug
		WHERE c.status = 'active'
		  AND (c.id = $1 OR c.slug = $1)
		  AND `+statusFilter+`
		  `+providerFilter+unitFilter+regionFilter+latencyFilter+costFilter, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	candidates := []*MarketplaceRouteCandidate{}
	for rows.Next() {
		c := &MarketplaceRouteCandidate{}
		if err := rows.Scan(
			&c.CapabilityID,
			&c.RouteName,
			&c.RoutingMode,
			&c.FallbackEnabled,
			&c.ProviderSlug,
			&c.ProviderName,
			&c.Status,
			&c.Priority,
			&c.CostScore,
			&c.LatencyMS,
			&c.QualityScore,
			&c.SuccessRateBps,
			&c.Region,
			&c.FallbackOrder,
			&c.Policy,
		); err != nil {
			return nil, err
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return SortMarketplaceRouteCandidates(candidates, in.RoutingMode), nil
}

func sanitizeRoutingMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "cheapest", "lowest_latency", "highest_quality", "best_available":
		return strings.ToLower(strings.TrimSpace(mode))
	default:
		return ""
	}
}

func SelectMarketplaceRouteCandidate(candidates []*MarketplaceRouteCandidate, routingMode string) *MarketplaceRouteCandidate {
	sorted := SortMarketplaceRouteCandidates(candidates, routingMode)
	if len(sorted) == 0 {
		return nil
	}
	return sorted[0]
}

func SortMarketplaceRouteCandidates(candidates []*MarketplaceRouteCandidate, routingMode string) []*MarketplaceRouteCandidate {
	out := append([]*MarketplaceRouteCandidate(nil), candidates...)
	mode := sanitizeRoutingMode(routingMode)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if activeRank(a.Status) != activeRank(b.Status) {
			return activeRank(a.Status) < activeRank(b.Status)
		}
		switch mode {
		case "cheapest":
			if a.CostScore != b.CostScore {
				return a.CostScore < b.CostScore
			}
		case "lowest_latency":
			if a.LatencyMS != b.LatencyMS {
				return a.LatencyMS < b.LatencyMS
			}
		case "highest_quality":
			if a.QualityScore != b.QualityScore {
				return a.QualityScore > b.QualityScore
			}
			if a.SuccessRateBps != b.SuccessRateBps {
				return a.SuccessRateBps > b.SuccessRateBps
			}
		default:
			if a.Priority != b.Priority {
				return a.Priority < b.Priority
			}
			if a.SuccessRateBps != b.SuccessRateBps {
				return a.SuccessRateBps > b.SuccessRateBps
			}
		}
		if a.FallbackOrder != b.FallbackOrder {
			return a.FallbackOrder < b.FallbackOrder
		}
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		if a.CostScore != b.CostScore {
			return a.CostScore < b.CostScore
		}
		if a.LatencyMS != b.LatencyMS {
			return a.LatencyMS < b.LatencyMS
		}
		return a.ProviderSlug < b.ProviderSlug
	})
	return out
}

func activeRank(status string) int {
	if strings.EqualFold(status, "active") {
		return 0
	}
	return 1
}

func (db *DB) getMarketplaceExecutionByIdempotencyTx(ctx context.Context, tx *sql.Tx, idempotencyKey string) (*MarketplaceCapabilityExecution, error) {
	event := &MarketplaceCapabilityExecution{}
	var errorCode, errorMessage sql.NullString
	err := tx.QueryRowContext(ctx, `
		SELECT id, capability_id, provider_slug, provider_name, route_name, routing_mode,
		       operation, request_id, idempotency_key, units_consumed, quota_remaining,
		       status, COALESCE(input_json, '{}'::jsonb), COALESCE(output_json, '{}'::jsonb),
		       COALESCE(latency_ms, 0), error_code, error_message, created_at
		FROM marketplace_execution_events
		WHERE idempotency_key = $1`, strings.TrimSpace(idempotencyKey)).Scan(
		&event.ID, &event.CapabilityID, &event.ProviderSlug, &event.ProviderName, &event.RouteName,
		&event.RoutingMode, &event.Operation, &event.RequestID, &event.IdempotencyKey,
		&event.UnitsConsumed, &event.QuotaRemaining, &event.Status, &event.Input, &event.Output,
		&event.LatencyMS, &errorCode, &errorMessage, &event.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if errorCode.Valid {
		event.ErrorCode = errorCode.String
	}
	if errorMessage.Valid {
		event.ErrorMessage = errorMessage.String
	}
	return event, err
}

func marketplaceMockOutput(capabilityID, operation, provider string) json.RawMessage {
	payload := map[string]any{
		"mode":       "mock",
		"capability": capabilityID,
		"operation":  operation,
		"provider":   provider,
		"status":     "completed",
	}
	switch capabilityID {
	case "llm_chat":
		payload["text"] = "Mock LLM response from ChainFX Capability Router."
	case "document_ocr":
		payload["text"] = "Mock OCR text extracted by ChainFX Capability Router."
		payload["pages"] = 1
	case "semantic_memory":
		payload["memoryId"] = "mem_" + strings.ReplaceAll(NewID(), "-", "")
		payload["matched"] = []string{}
	case "payments_fx":
		payload["message"] = "Mock payments/fx capability execution. Use Agent Rail endpoints for real settlement."
	case "capability_discovery":
		payload["results"] = []string{"llm_chat", "document_ocr", "semantic_memory", "payments_fx"}
	case "aml_screening":
		payload["risk"] = "mock_low"
		payload["matches"] = []string{}
	default:
		payload["message"] = "Mock capability execution completed."
	}
	raw, _ := json.Marshal(payload)
	return raw
}

func normalizeJSONRaw(raw json.RawMessage, fallback string) json.RawMessage {
	if len(raw) == 0 || !json.Valid(raw) {
		return json.RawMessage(fallback)
	}
	return raw
}

func stringFromMap(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	switch v := values[key].(type) {
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	case json.Number:
		return v.String()
	default:
		if v == nil {
			return ""
		}
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func marketplacePurchaseSelect() string {
	return `SELECT id, provider_id, product_id, plan_id, agent_wallet, payer_wallet, payment_address,
	       payment_asset, payment_contract, network, chain_id, gross_amount::text, chainfx_amount::text,
	       provider_amount::text, take_rate_bps, request_hash, nonce, idempotency_key, expires_at,
	       status, tx_hash, tx_log_index, tx_block_number, tx_block_hash, transfer_from, transfer_to,
	       transfer_amount_raw, overpayment_amount::text, paid_at, granted_at, failed_at,
	       failure_code, failure_message, created_at
	FROM marketplace_purchases`
}

func scanMarketplacePurchase(row rowScanner) (*MarketplacePurchase, error) {
	var p MarketplacePurchase
	var idempotency, txHash, blockHash, transferFrom, transferTo, raw, code, message sql.NullString
	var logIndex, blockNumber sql.NullInt64
	var paidAt, grantedAt, failedAt sql.NullTime
	if err := row.Scan(&p.ID, &p.ProviderID, &p.ProductID, &p.PlanID, &p.AgentWallet, &p.PayerWallet,
		&p.PaymentAddress, &p.PaymentAsset, &p.PaymentContract, &p.Network, &p.ChainID, &p.GrossAmount,
		&p.ChainFXAmount, &p.ProviderAmount, &p.TakeRateBps, &p.RequestHash, &p.Nonce, &idempotency,
		&p.ExpiresAt, &p.Status, &txHash, &logIndex, &blockNumber, &blockHash, &transferFrom, &transferTo,
		&raw, &p.OverpaymentAmount, &paidAt, &grantedAt, &failedAt, &code, &message, &p.CreatedAt); err != nil {
		return nil, err
	}
	if idempotency.Valid {
		p.IdempotencyKey = &idempotency.String
	}
	if txHash.Valid {
		p.TxHash = &txHash.String
	}
	if logIndex.Valid {
		v := int(logIndex.Int64)
		p.TxLogIndex = &v
	}
	if blockNumber.Valid {
		v := uint64(blockNumber.Int64)
		p.TxBlockNumber = &v
	}
	if blockHash.Valid {
		p.TxBlockHash = &blockHash.String
	}
	if transferFrom.Valid {
		p.TransferFrom = &transferFrom.String
	}
	if transferTo.Valid {
		p.TransferTo = &transferTo.String
	}
	if raw.Valid {
		p.TransferAmountRaw = &raw.String
	}
	if paidAt.Valid {
		p.PaidAt = &paidAt.Time
	}
	if grantedAt.Valid {
		p.GrantedAt = &grantedAt.Time
	}
	if failedAt.Valid {
		p.FailedAt = &failedAt.Time
	}
	if code.Valid {
		p.FailureCode = &code.String
	}
	if message.Valid {
		p.FailureMessage = &message.String
	}
	return &p, nil
}

func ParseMicroAmount(value string) (int64, error) {
	parts := strings.SplitN(strings.TrimSpace(value), ".", 2)
	if parts[0] == "" {
		return 0, fmt.Errorf("amount invalido")
	}
	var whole int64
	if _, err := fmt.Sscanf(parts[0], "%d", &whole); err != nil {
		return 0, err
	}
	micros := whole * 1_000_000
	if len(parts) == 2 {
		frac := parts[1]
		if len(frac) > 6 {
			frac = frac[:6]
		}
		frac += strings.Repeat("0", 6-len(frac))
		var f int64
		if strings.Trim(frac, "0") != "" {
			if _, err := fmt.Sscanf(frac, "%d", &f); err != nil {
				return 0, err
			}
		}
		micros += f
	}
	return micros, nil
}

func FormatMicroAmount(value int64) string {
	if value < 0 {
		value = 0
	}
	return fmt.Sprintf("%d.%06d", value/1_000_000, value%1_000_000)
}

func MustMicroFromFloat(value float64) int64 {
	if value <= 0 {
		return 0
	}
	return int64(value*1_000_000 + 0.5)
}

func MarketplaceRequestHash(parts ...string) string {
	raw := strings.Join(parts, "|")
	hash := privacy.Hash(raw, "marketplace-request-hash")
	if strings.HasPrefix(hash, "0x") {
		return hash
	}
	return "0x" + hash
}

func ConstantTimeTokenEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func firstNonEmptyDB(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
