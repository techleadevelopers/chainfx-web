package models

import (
	"time"
)

// OrderStatus define os estados possíveis de uma ordem no sistema
type OrderStatus string

const (
	StatusAguardandoDeposito  OrderStatus = "aguardando_deposito"
	StatusAguardandoValidacao OrderStatus = "aguardando_validacao" // Caso fuja da tolerância de %
	StatusExpirada            OrderStatus = "expirada"
	StatusPago                OrderStatus = "pago"
	StatusAguardandoPixManual OrderStatus = "aguardando_pix_manual"
	StatusProcessandoPayout   OrderStatus = "processando_payout"
	StatusIncidenteValidacao  OrderStatus = "incidente_validacao"
	StatusConcluida           OrderStatus = "concluida"
	StatusErro                OrderStatus = "erro"
)

// Statuses válidos para validação
var ValidStatuses = map[OrderStatus]bool{
	StatusAguardandoDeposito:  true,
	StatusAguardandoValidacao: true,
	StatusExpirada:            true,
	StatusPago:                true,
	StatusAguardandoPixManual: true,
	StatusProcessandoPayout:   true,
	StatusIncidenteValidacao:  true,
	StatusConcluida:           true,
	StatusErro:                true,
}

// IsValid verifica se o status é válido
func (s OrderStatus) IsValid() bool {
	return ValidStatuses[s]
}

// IsFinal verifica se o status é terminal (não muda mais)
func (s OrderStatus) IsFinal() bool {
	return s == StatusConcluida || s == StatusExpirada || s == StatusErro || s == StatusIncidenteValidacao
}

// IsPaid verifica se o status indica pagamento confirmado
func (s OrderStatus) IsPaid() bool {
	return s == StatusPago
}

// CanTransition verifica se a transição de status é válida
func (s OrderStatus) CanTransition(to OrderStatus) bool {
	// Definir transições válidas
	transitions := map[OrderStatus][]OrderStatus{
		StatusAguardandoDeposito:  {StatusAguardandoValidacao, StatusExpirada, StatusErro},
		StatusAguardandoValidacao: {StatusPago, StatusExpirada, StatusErro, StatusIncidenteValidacao},
		StatusPago:                {StatusAguardandoPixManual, StatusProcessandoPayout, StatusErro, StatusIncidenteValidacao},
		StatusAguardandoPixManual: {StatusConcluida, StatusErro, StatusIncidenteValidacao},
		StatusProcessandoPayout:   {StatusConcluida, StatusErro, StatusIncidenteValidacao},
		StatusIncidenteValidacao:  {},
		StatusConcluida:           {}, // Terminal
		StatusExpirada:            {}, // Terminal
		StatusErro:                {}, // Terminal
	}

	allowed, exists := transitions[s]
	if !exists {
		return false
	}

	for _, status := range allowed {
		if status == to {
			return true
		}
	}
	return false
}

// Order representa a tabela 'orders' do banco de dados
type Order struct {
	ID                string      `json:"id" db:"id"` // UUID da ordem
	AccessToken       string      `json:"accessToken,omitempty" db:"access_token"`
	AmountBRL         float64     `json:"amount_brl" db:"amount_brl"`
	AmountUSDT        float64     `json:"amount_usdt" db:"amount_usdt"`
	FeeBRL            float64     `json:"fee_brl" db:"fee_brl"`
	PayoutBRL         float64     `json:"payout_brl" db:"payout_brl"`
	Status            OrderStatus `json:"status" db:"status"`
	PixKey            string      `json:"pix_key" db:"pix_key"`
	PixType           string      `json:"pix_type" db:"pix_type"` // "cpf" ou "phone"
	PixCpf            string      `json:"-" db:"pix_cpf"`
	PixPhone          string      `json:"-" db:"pix_phone"`
	BSCAddress        string      `json:"BSC_address" db:"bsc_address"`
	Address           string      `json:"address" db:"address"`
	Asset             string      `json:"asset" db:"asset"`
	Network           string      `json:"network" db:"network"`
	RateLocked        float64     `json:"rate_locked" db:"rate_locked"`
	TxHash            *string     `json:"tx_hash,omitempty" db:"tx_hash"`
	DepositTx         *string     `json:"deposit_tx,omitempty" db:"deposit_tx"`
	DepositAmount     *float64    `json:"deposit_amount,omitempty" db:"deposit_amount"`
	Error             *string     `json:"error,omitempty" db:"error"`
	DerivationIndex   *int        `json:"derivation_index,omitempty" db:"derivation_index"`
	RateLockExpiresAt time.Time   `json:"rate_lock_expires_at" db:"rate_lock_expires_at"`
	CreatedAt         time.Time   `json:"created_at" db:"created_at"`
	UpdatedAt         time.Time   `json:"updated_at" db:"updated_at"`
}

// TableName retorna o nome da tabela no banco
func (Order) TableName() string {
	return "orders"
}

// IsExpired verifica se a ordem expirou
func (o *Order) IsExpired() bool {
	return time.Now().After(o.RateLockExpiresAt)
}

// CanAcceptDeposit verifica se a ordem pode aceitar depósito
func (o *Order) CanAcceptDeposit() bool {
	return o.Status == StatusAguardandoDeposito && !o.IsExpired()
}

// OrderMeta representa os dados de auditoria
type OrderMeta struct {
	OrderID   string    `json:"order_id" db:"order_id"`
	IP        string    `json:"ip" db:"ip"`
	UserAgent string    `json:"user_agent" db:"user_agent"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
}

// TableName retorna o nome da tabela no banco
func (OrderMeta) TableName() string {
	return "order_meta"
}

// OnchainCursor ajuda o onchainWorker a saber de onde parou na paginação da BSC
type OnchainCursor struct {
	ID        int       `json:"id" db:"id"`
	LastBlock uint64    `json:"last_block" db:"last_block"`
	UpdatedAt time.Time `json:"updated_at" db:"updated_at"`
}

// TableName retorna o nome da tabela no banco
func (OnchainCursor) TableName() string {
	return "onchain_cursor"
}

// PendingSweep representa sweeps pendentes
type PendingSweep struct {
	ID         string    `json:"id" db:"id"`
	OrderID    *string   `json:"order_id,omitempty" db:"order_id"`
	ChildIndex int       `json:"child_index" db:"child_index"`
	FromAddr   string    `json:"from_addr" db:"from_addr"`
	ToAddr     string    `json:"to_addr" db:"to_addr"`
	Amount     float64   `json:"amount" db:"amount"`
	Status     string    `json:"status" db:"status"` // pending, sent, failed
	TxHash     *string   `json:"tx_hash,omitempty" db:"tx_hash"`
	Error      *string   `json:"error,omitempty" db:"error"`
	CreatedAt  time.Time `json:"created_at" db:"created_at"`
	UpdatedAt  time.Time `json:"updated_at" db:"updated_at"`
}

// TableName retorna o nome da tabela no banco
func (PendingSweep) TableName() string {
	return "pending_sweeps"
}

// BuyOrder representa ordens de compra
type BuyOrder struct {
	ID           string    `json:"id" db:"id"`
	DestAddress  string    `json:"dest_address" db:"dest_address"`
	CryptoAmount float64   `json:"crypto_amount" db:"crypto_amount"`
	FiatAmount   float64   `json:"fiat_amount" db:"fiat_amount"`
	FiatCurrency string    `json:"fiat_currency" db:"fiat_currency"`
	Status       string    `json:"status" db:"status"` // pago_fiat, pago_pix, enviado, erro
	TxHashOut    *string   `json:"tx_hash_out,omitempty" db:"tx_hash_out"`
	Error        *string   `json:"error,omitempty" db:"error"`
	CreatedAt    time.Time `json:"created_at" db:"created_at"`
	UpdatedAt    time.Time `json:"updated_at" db:"updated_at"`
}

// TableName retorna o nome da tabela no banco
func (BuyOrder) TableName() string {
	return "buy_orders"
}
