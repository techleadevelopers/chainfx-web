package bitcoin

import "time"

// ─── Status constants ──────────────────────────────────────────────────────────

// UTXOStatus representa o ciclo de vida de um UTXO.
const (
	UTXOStatusPending   = "pending"
	UTXOStatusConfirmed = "confirmed"
	UTXOStatusReserved  = "reserved"
	UTXOStatusSpent     = "spent"
	UTXOStatusOrphaned  = "orphaned"
)

// TxStatus representa o ciclo de vida de uma transação BTC.
const (
	TxStatusCreated   = "created"
	TxStatusBuilding  = "building"
	TxStatusSigned    = "signed"
	TxStatusBroadcast = "broadcast"
	TxStatusPending   = "pending"
	TxStatusConfirmed = "confirmed"
	TxStatusFailed    = "failed"
	TxStatusReplaced  = "replaced"
	TxStatusDropped   = "dropped"
)

// TxDirection indica se a transação é depósito, saque ou interna.
const (
	TxDirectionDeposit    = "deposit"
	TxDirectionWithdrawal = "withdrawal"
	TxDirectionInternal   = "internal"
)

// AddressType indica o tipo de endereço Bitcoin.
const (
	AddressTypeP2WPKH = "p2wpkh" // native SegWit bech32
)

// ─── Domain types ──────────────────────────────────────────────────────────────

// BTCAddress representa um endereço Bitcoin alocado para um usuário.
type BTCAddress struct {
	ID              string
	UserID          string
	Network         string
	Address         string
	DerivationPath  string
	DerivationIndex int
	AddressType     string
	Status          string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// UTXO representa um Unspent Transaction Output monitorado pelo sistema.
type UTXO struct {
	ID              string
	Network         string
	UserID          string
	WalletAddressID string
	Address         string // campo auxiliar, não está na tabela mas útil em memória
	Txid            string
	Vout            uint32
	ValueSats       int64
	ScriptPubKey    string
	BlockHeight     int64
	Confirmations   int
	Status          string
	SpentByTxid     string
	DetectedAt      time.Time
	ConfirmedAt     *time.Time
	SpentAt         *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// BTCTransaction representa uma transação BTC rastreada (depósito ou saque).
type BTCTransaction struct {
	ID               string
	UserID           string
	Network          string
	Direction        string
	Txid             string
	RawTxHash        string
	DestinationAddr  string
	AmountSats       int64
	FeeSats          int64
	FeeRateSatVByte  int64
	Status           string
	Confirmations    int
	BlockHeight      int64
	IdempotencyKey   string
	RequestHash      string
	ErrorCode        string
	ErrorMessage     string
	BroadcastAt      *time.Time
	ConfirmedAt      *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Balance representa o saldo BTC de um usuário.
type Balance struct {
	// Sats (int64 — nunca float para cálculo financeiro)
	ConfirmedSats int64 `json:"confirmed_sats"`
	PendingSats   int64 `json:"pending_sats"`
	ReservedSats  int64 `json:"reserved_sats"`  // UTXOs reservados para saques em andamento
	AvailableSats int64 `json:"available_sats"`  // confirmed - reserved
	TotalSats     int64 `json:"total_sats"`
	// Representação em BTC — string com 8 casas decimais, nunca float para cálculo
	ConfirmedBTC string `json:"confirmed_btc"`
	PendingBTC   string `json:"pending_btc"`
	AvailableBTC string `json:"available_btc"`
	// Campos de contexto enriquecidos para o mobile
	Asset                string    `json:"asset,omitempty"`
	Network              string    `json:"network,omitempty"`
	MinimumConfirmations int       `json:"minimum_confirmations,omitempty"`
	UpdatedAt            time.Time `json:"updated_at,omitempty"`
}

// DepositEventSink permite ao BTCWorker publicar eventos de depósito sem importar
// o pacote workers (evita importação circular). Implementado em workers.btcEventSinkAdapter.
type DepositEventSink interface {
	PublishBTCEvent(eventType string, payload map[string]any)
}

// FeeEstimate representa uma estimativa de fee para envio BTC.
type FeeEstimate struct {
	FeeRateSatVByte int64  `json:"fee_rate_sat_vbyte"`
	EstimatedFeeSat int64  `json:"estimated_fee_sats"`
	VirtualSize     int    `json:"virtual_size_vbytes"`
	Policy          string `json:"policy"`
}

// SendRequest é o pedido de saque BTC.
type SendRequest struct {
	UserID         string
	ToAddress      string
	AmountSats     int64
	FeeRateSatVB   int64  // 0 = usar estimativa automática
	IdempotencyKey string
	RequestHash    string
}

// SendResult é o resultado de um saque BTC.
type SendResult struct {
	TxID        string `json:"txid"`
	FeeSats     int64  `json:"fee_sats"`
	AmountSats  int64  `json:"amount_sats"`
	Status      string `json:"status"`
}

// ProviderUTXO é o formato bruto retornado pela API do provider.
type ProviderUTXO struct {
	Txid   string `json:"txid"`
	Vout   uint32 `json:"vout"`
	Status struct {
		Confirmed   bool  `json:"confirmed"`
		BlockHeight int64 `json:"block_height"`
	} `json:"status"`
	Value int64 `json:"value"` // sats
}

// ProviderTxStatus é o estado de uma transação retornado pelo provider.
type ProviderTxStatus struct {
	Txid   string `json:"txid"`
	Status struct {
		Confirmed   bool  `json:"confirmed"`
		BlockHeight int64 `json:"block_height"`
	} `json:"status"`
	Fee int64 `json:"fee"`
}

// ProviderFeeRecommended é a resposta de fee do mempool.space.
type ProviderFeeRecommended struct {
	FastestFee  int64 `json:"fastestFee"`
	HalfHourFee int64 `json:"halfHourFee"`
	HourFee     int64 `json:"hourFee"`
	EconomyFee  int64 `json:"economyFee"`
	MinimumFee  int64 `json:"minimumFee"`
}
