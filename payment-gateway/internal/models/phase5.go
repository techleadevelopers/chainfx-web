// Package models contains Phase 5 data structures (mobile-only).
// These types support multi-asset, multi-country, multi-rail, KYC async,
// swap, push notifications and webhook delivery.
package models

import "time"

// ─── Asset ────────────────────────────────────────────────────────────────────

// Asset is a tradeable crypto asset configured in the gateway.
type Asset struct {
	Symbol          string    `json:"symbol"           db:"symbol"`
	Name            string    `json:"name"             db:"name"`
	Network         string    `json:"network"          db:"network"`
	ContractAddress *string   `json:"contract_address" db:"contract_address"`
	Decimals        int       `json:"decimals"         db:"decimals"`
	MinAmount       float64   `json:"min_amount"       db:"min_amount"`
	MaxAmount       float64   `json:"max_amount"       db:"max_amount"`
	DailyLimit      float64   `json:"daily_limit"      db:"daily_limit"`
	MonthlyLimit    float64   `json:"monthly_limit"    db:"monthly_limit"`
	FeeBPS          int       `json:"fee_bps"          db:"fee_bps"`
	Active          bool      `json:"active"           db:"active"`
	CreatedAt       time.Time `json:"created_at"       db:"created_at"`
}

// AssetRate is a live price quote for one asset.
type AssetRate struct {
	Symbol    string  `json:"symbol"`
	PriceBRL  float64 `json:"price_brl"`
	PriceUSD  float64 `json:"price_usd"`
	Change24h float64 `json:"change_24h"`
	UpdatedAt int64   `json:"updated_at_unix"`
}

// ─── Country ──────────────────────────────────────────────────────────────────

// Country holds per-country configuration and localisation.
type Country struct {
	Code      string    `json:"code"       db:"code"`
	Name      string    `json:"name"       db:"name"`
	Currency  string    `json:"currency"   db:"currency"`
	Language  string    `json:"language"   db:"language"`
	Active    bool      `json:"active"     db:"active"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
}

// ─── PaymentRail ──────────────────────────────────────────────────────────────

// PaymentRail represents one fiat payment method available for a country.
type PaymentRail struct {
	ID          string    `json:"id"           db:"id"`
	CountryCode string    `json:"country_code" db:"country_code"`
	Name        string    `json:"name"         db:"name"`
	Currency    string    `json:"currency"     db:"currency"`
	Active      bool      `json:"active"       db:"active"`
	Metadata    *string   `json:"metadata"     db:"metadata"`
	CreatedAt   time.Time `json:"created_at"   db:"created_at"`
}

// ─── KYC ──────────────────────────────────────────────────────────────────────

// KYCLevel defines tiered daily limits.
//
//	Level 0 – email+phone only  → R$ 500 /day
//	Level 1 – doc+selfie        → R$ 5 000 /day
//	Level 2 – address+income    → R$ 50 000 /day
//	Level 3 – manual review     → R$ 500 000 /day
type KYCLevel int

const (
	KYCLevel0 KYCLevel = 0
	KYCLevel1 KYCLevel = 1
	KYCLevel2 KYCLevel = 2
	KYCLevel3 KYCLevel = 3
)

// KYCDailyLimits maps each level to its daily BRL ceiling.
var KYCDailyLimits = map[KYCLevel]float64{
	KYCLevel0: 500,
	KYCLevel1: 5_000,
	KYCLevel2: 50_000,
	KYCLevel3: 500_000,
}

type KYCRequestStatus string

const (
	KYCReqPending  KYCRequestStatus = "pending"
	KYCReqInReview KYCRequestStatus = "in_review"
	KYCReqApproved KYCRequestStatus = "approved"
	KYCReqRejected KYCRequestStatus = "rejected"
)

// KYCRequest is an async identity verification submitted by a mobile user.
type KYCRequest struct {
	ID                string           `json:"id"                            db:"id"`
	UserID            string           `json:"user_id"                       db:"user_id"`
	Level             KYCLevel         `json:"level"                         db:"level"`
	Status            KYCRequestStatus `json:"status"                        db:"status"`
	DocumentType      *string          `json:"document_type,omitempty"       db:"document_type"`
	DocumentURL       *string          `json:"document_url,omitempty"        db:"document_url"`
	DocumentBackURL   *string          `json:"document_back_url,omitempty"   db:"document_back_url"`
	SelfieURL         *string          `json:"selfie_url,omitempty"          db:"selfie_url"`
	FacialVideoURL    *string          `json:"facial_video_url,omitempty"    db:"facial_video_url"`
	ProofOfAddressURL *string          `json:"proof_of_address_url,omitempty" db:"proof_of_address_url"`
	ProofOfIncomeURL  *string          `json:"proof_of_income_url,omitempty"  db:"proof_of_income_url"`
	ReviewerNotes     *string          `json:"reviewer_notes,omitempty"      db:"reviewer_notes"`
	SubmittedAt       time.Time        `json:"submitted_at"                  db:"submitted_at"`
	ReviewedAt        *time.Time       `json:"reviewed_at,omitempty"         db:"reviewed_at"`
	CreatedAt         time.Time        `json:"created_at"                    db:"created_at"`
	UpdatedAt         time.Time        `json:"updated_at"                    db:"updated_at"`
}

// ─── Swap ─────────────────────────────────────────────────────────────────────

type SwapStatus string

const (
	SwapPending   SwapStatus = "pending"
	SwapExecuting SwapStatus = "executing"
	SwapCompleted SwapStatus = "completed"
	SwapFailed    SwapStatus = "failed"
)

// Swap is a direct crypto-to-crypto exchange (e.g. USDT→BTCB).
type Swap struct {
	ID                string     `json:"id"                  db:"id"`
	UserID            string     `json:"user_id"             db:"user_id"`
	FromAsset         string     `json:"from_asset"          db:"from_asset"`
	ToAsset           string     `json:"to_asset"            db:"to_asset"`
	FromAmount        float64    `json:"from_amount"         db:"from_amount"`
	ToAmount          *float64   `json:"to_amount,omitempty" db:"to_amount"`
	Rate              *float64   `json:"rate,omitempty"      db:"rate"`
	FeeBPS            int        `json:"fee_bps"             db:"fee_bps"`
	SlippageTolerance float64    `json:"slippage_tolerance"  db:"slippage_tolerance"`
	Status            SwapStatus `json:"status"              db:"status"`
	TxHash            *string    `json:"tx_hash,omitempty"   db:"tx_hash"`
	Error             *string    `json:"error,omitempty"     db:"error"`
	CreatedAt         time.Time  `json:"created_at"          db:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"          db:"updated_at"`
}

// ─── Webhook ──────────────────────────────────────────────────────────────────

// WebhookSubscription is a user-owned endpoint for receiving gateway events
// (compatible with n8n, Zapier, Make).
type WebhookSubscription struct {
	ID        string    `json:"id"         db:"id"`
	UserID    *string   `json:"user_id"    db:"user_id"`
	TargetURL string    `json:"target_url" db:"target_url"`
	Events    []string  `json:"events"     db:"-"` // serialized in queries
	Secret    string    `json:"-"          db:"secret"`
	Active    bool      `json:"active"     db:"active"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
	UpdatedAt time.Time `json:"updated_at" db:"updated_at"`
}

type WebhookDeliveryStatus string

const (
	WebhookDeliveryPending   WebhookDeliveryStatus = "pending"
	WebhookDeliveryDelivered WebhookDeliveryStatus = "delivered"
	WebhookDeliveryFailed    WebhookDeliveryStatus = "failed"
	WebhookDeliveryRetrying  WebhookDeliveryStatus = "retrying"
)

// WebhookDelivery tracks a single outbound webhook attempt.
type WebhookDelivery struct {
	ID             string                `json:"id"              db:"id"`
	SubscriptionID string                `json:"subscription_id" db:"subscription_id"`
	EventType      string                `json:"event_type"      db:"event_type"`
	Payload        string                `json:"payload"         db:"payload"`
	Status         WebhookDeliveryStatus `json:"status"          db:"status"`
	Attempts       int                   `json:"attempts"        db:"attempts"`
	NextRetryAt    *time.Time            `json:"next_retry_at"   db:"next_retry_at"`
	ResponseStatus *int                  `json:"response_status" db:"response_status"`
	ResponseBody   *string               `json:"response_body"   db:"response_body"`
	LastError      *string               `json:"last_error"      db:"last_error"`
	CreatedAt      time.Time             `json:"created_at"      db:"created_at"`
	UpdatedAt      time.Time             `json:"updated_at"      db:"updated_at"`
}

// Phase5WebhookEvents lists all events the webhook system can emit.
var Phase5WebhookEvents = []string{
	"order.created",
	"order.completed",
	"order.failed",
	"payment.received",
	"payout.sent",
	"price.change",
	"dca.executed",
	"swap.completed",
	"swap.failed",
	"kyc.approved",
	"kyc.rejected",
}
