package bitcoin

import "time"

// TreasuryUTXO representa um UTXO no endereço da Treasury operacional.
// Armazenado em btc_treasury_utxos — completamente separado de btc_utxos (usuários).
type TreasuryUTXO struct {
	ID           string
	Network      string
	Address      string
	Txid         string
	Vout         uint32
	ValueSats    int64
	ScriptPubKey string
	BlockHeight  int64
	Confirmations int
	Status       string // pending | confirmed | reserved | spent | orphaned
	ReservedByOp string // btc_treasury_operations.id
	SpentByTxid  string
	DetectedAt   time.Time
	ConfirmedAt  *time.Time
	SpentAt      *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// TreasuryOperation é o registro auditável de cada tentativa de entrega BTC via Treasury.
// Uma operação por ordem BUY BTC — garantido por UNIQUE(order_id) e UNIQUE(idempotency_key).
type TreasuryOperation struct {
	ID                 string
	OrderID            string
	Asset              string
	Network            string
	FundingSource      string // treasury | bingx | unknown
	TreasuryAddress    string
	DestinationAddress string
	AmountSats         int64
	FeeSats            int64
	Txid               string
	RawTxHash          string // apagado após broadcast bem-sucedido
	SignerOperationID  string // BTC_TREASURY_SIGNER_KEY_ID — nunca expõe segredo
	Status             string
	ErrorCode          string
	ErrorMessage       string
	IdempotencyKey     string // 'btc_buy:<order_id>'
	SignedAt           *time.Time
	BroadcastAt        *time.Time
	ConfirmedAt        *time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// TreasurySendRequest encapsula um pedido de envio BTC a partir da Treasury.
type TreasurySendRequest struct {
	OrderID      string // buy_orders.id — chave de idempotência
	ToAddress    string
	AmountSats   int64
	FeeRateSatVB int64 // 0 = usar estimativa automática
}

// TreasurySendResult é o resultado de um envio via Treasury.
type TreasurySendResult struct {
	TxID          string
	FeeSats       int64
	AmountSats    int64
	Status        string // TxStatus* constants
	FundingSource string // sempre "treasury"
	SignerKeyID   string // para auditoria
}

// TreasuryBalance representa o saldo da Treasury com cálculo de spendable.
type TreasuryBalance struct {
	ConfirmedSats  int64 // total de UTXOs confirmados
	ReservedSats   int64 // UTXOs reservados para operações em andamento
	MinReserveSats int64 // BTC_TREASURY_MIN_RESERVE_SATS
	EstimatedFee   int64 // estimativa de fee para a ordem atual
	SpendableSats  int64 // confirmed - reserved - estimated_fee - min_reserve
}

// HasSufficientFunds retorna true se o saldo utilizável cobre amountSats.
func (b TreasuryBalance) HasSufficientFunds(amountSats int64) bool {
	return b.SpendableSats >= amountSats
}
