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

	StatusCreated            = "created"
	StatusReserved           = "reserved"
	StatusSigned             = "signed"
	StatusSubmitted          = "submitted"
	StatusSubmitUnknown      = "submit_unknown"
	StatusFinalized          = "finalized"
	StatusFailedBeforeSubmit = "failed_before_submit"
	StatusManualReview       = "manual_review"
	StatusRebuildRequired    = "rebuild_required"

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
	ID                   string     `json:"id"`
	UserID               string     `json:"user_id"`
	Network              string     `json:"network"`
	Signature            string     `json:"signature"`
	Asset                string     `json:"asset"`
	MintAddress          string     `json:"mint_address,omitempty"`
	Direction            string     `json:"direction"`
	AmountRaw            string     `json:"amount_raw"`
	Decimals             int        `json:"decimals"`
	Status               string     `json:"status"`
	Confirmations        int        `json:"confirmations"`
	Slot                 int64      `json:"slot"`
	OperationID          string     `json:"operation_id,omitempty"`
	IdempotencyKey       string     `json:"idempotency_key,omitempty"`
	RequestHash          string     `json:"request_hash,omitempty"`
	SourceAddress        string     `json:"source_address,omitempty"`
	DestinationAddress   string     `json:"destination_address,omitempty"`
	FeeLamports          int64      `json:"fee_lamports,omitempty"`
	ReservedLamports     int64      `json:"reserved_lamports,omitempty"`
	SignedRawTx          string     `json:"-"`
	FeePayer             string     `json:"fee_payer,omitempty"`
	RecentBlockhash      string     `json:"recent_blockhash,omitempty"`
	LastValidBlockHeight int64      `json:"last_valid_block_height,omitempty"`
	SignedAt             *time.Time `json:"signed_at,omitempty"`
	SubmittedAt          *time.Time `json:"submitted_at,omitempty"`
	ConfirmedAt          *time.Time `json:"confirmed_at,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
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
