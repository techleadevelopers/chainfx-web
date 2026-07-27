package solana

import "time"

const (
	Network              = "SOLANA"
	AssetSOL             = "SOL"
	LamportsPerSOL int64 = 1_000_000_000

	StatusPending   = "pending"
	StatusBroadcast = "broadcast"
	StatusConfirmed = "confirmed"
	StatusFailed    = "failed"

	DirectionDeposit    = "deposit"
	DirectionWithdrawal = "withdrawal"
	DirectionRouter     = "router_delivery"
)

type Address struct {
	ID              string    `json:"id"`
	UserID          string    `json:"user_id"`
	Network         string    `json:"network"`
	Address         string    `json:"address"`
	DerivationKeyID string    `json:"derivation_key_id,omitempty"`
	Status          string    `json:"status"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type Balance struct {
	Asset                string    `json:"asset"`
	Network              string    `json:"network"`
	Address              string    `json:"address"`
	Lamports             int64     `json:"lamports"`
	SOL                  string    `json:"sol"`
	AvailableLamports    int64     `json:"available_lamports"`
	PendingLamports      int64     `json:"pending_lamports"`
	MinimumLamports      int64     `json:"minimum_lamports"`
	MinimumConfirmations int       `json:"minimum_confirmations"`
	UpdatedAt            time.Time `json:"updated_at"`
}

type Transaction struct {
	ID            string    `json:"id"`
	UserID        string    `json:"user_id"`
	Network       string    `json:"network"`
	Signature     string    `json:"signature"`
	Asset         string    `json:"asset"`
	MintAddress   string    `json:"mint_address,omitempty"`
	Direction     string    `json:"direction"`
	AmountRaw     string    `json:"amount_raw"`
	Decimals      int       `json:"decimals"`
	Status        string    `json:"status"`
	Confirmations int       `json:"confirmations"`
	Slot          int64     `json:"slot"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type FeeEstimate struct {
	Asset                string `json:"asset"`
	Network              string `json:"network"`
	EstimatedFeeLamports int64  `json:"estimated_fee_lamports"`
	EstimatedFeeSOL      string `json:"estimated_fee_sol"`
	PriorityFeeLamports  int64  `json:"priority_fee_lamports"`
	Policy               string `json:"policy"`
}

type SendRequest struct {
	UserID         string
	ToAddress      string
	AmountLamports int64
	IdempotencyKey string
	RequestHash    string
}

type SendResult struct {
	Signature      string `json:"signature"`
	AmountLamports int64  `json:"amount_lamports"`
	FeeLamports    int64  `json:"fee_lamports"`
	Status         string `json:"status"`
}

type EventSink interface {
	PublishSolanaEvent(eventType string, payload map[string]any)
}
