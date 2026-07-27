package transactions

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

var (
	operationIDUint256, _ = abi.NewType("uint256", "", nil)
	operationIDAddress, _ = abi.NewType("address", "", nil)
	operationIDBytes32, _ = abi.NewType("bytes32", "", nil)
)

type Side string

const (
	SideBuy  Side = "BUY"
	SideSell Side = "SELL"
)

type TradeStatus string

const (
	StatusCreated              TradeStatus = "CREATED"
	StatusQuoteCreated         TradeStatus = "QUOTE_CREATED"
	StatusQuoteAccepted        TradeStatus = "QUOTE_ACCEPTED"
	StatusPaymentPending       TradeStatus = "PAYMENT_PENDING"
	StatusPaymentConfirmed     TradeStatus = "PAYMENT_CONFIRMED"
	StatusComplianceApproved   TradeStatus = "COMPLIANCE_APPROVED"
	StatusSettlementPending    TradeStatus = "SETTLEMENT_PENDING"
	StatusSigning              TradeStatus = "SIGNING"
	StatusBroadcasted          TradeStatus = "BROADCASTED"
	StatusOnchainConfirmed     TradeStatus = "ONCHAIN_CONFIRMED"
	StatusDelivered            TradeStatus = "DELIVERED"
	StatusCompleted            TradeStatus = "COMPLETED"
	StatusDepositPending       TradeStatus = "DEPOSIT_PENDING"
	StatusOnchainDetected      TradeStatus = "ONCHAIN_DETECTED"
	StatusConfirmationsPending TradeStatus = "CONFIRMATIONS_PENDING"
	StatusConfirmed            TradeStatus = "CONFIRMED"
	StatusPixPending           TradeStatus = "PIX_PENDING"
	StatusPixSent              TradeStatus = "PIX_SENT"
	StatusFailed               TradeStatus = "FAILED"
)

type NetworkAsset struct {
	Asset   string `json:"asset"`
	Network string `json:"network"`
	ChainID int64  `json:"chainId"`
}

type WalletRef struct {
	Address string `json:"address"`
	Network string `json:"network"`
	ChainID int64  `json:"chainId"`
	Role    string `json:"role,omitempty"`
}

type Trace struct {
	TransactionID  string `json:"transactionId,omitempty"`
	OrderID        string `json:"orderId,omitempty"`
	IntentID       string `json:"intentId,omitempty"`
	CorrelationID  string `json:"correlationId,omitempty"`
	TraceID        string `json:"traceId,omitempty"`
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
	RequestHash    string `json:"requestHash,omitempty"`
}

type Risk struct {
	Status string   `json:"status"`
	Score  int      `json:"score"`
	Flags  []string `json:"flags,omitempty"`
}

type TradeContract struct {
	Version           string         `json:"version"`
	Side              Side           `json:"side"`
	Trace             Trace          `json:"trace"`
	CustomerID        string         `json:"customerId,omitempty"`
	Source            NetworkAsset   `json:"source"`
	Destination       NetworkAsset   `json:"destination"`
	SourceAmount      float64        `json:"sourceAmount"`
	DestinationAmount float64        `json:"destinationAmount"`
	ExchangeRate      float64        `json:"exchangeRate"`
	SpreadBps         int            `json:"spreadBps,omitempty"`
	FeeAmount         float64        `json:"feeAmount"`
	FeeAsset          string         `json:"feeAsset"`
	Wallet            WalletRef      `json:"wallet"`
	TreasuryWallet    WalletRef      `json:"treasuryWallet"`
	PaymentMethod     string         `json:"paymentMethod,omitempty"`
	PSPProvider       string         `json:"pspProvider,omitempty"`
	Status            TradeStatus    `json:"status"`
	Risk              Risk           `json:"risk"`
	Metadata          map[string]any `json:"metadata,omitempty"`
	CreatedAt         time.Time      `json:"createdAt"`
}

type SettlementContract struct {
	Version        string       `json:"version"`
	Trace          Trace        `json:"trace"`
	Side           Side         `json:"side"`
	From           WalletRef    `json:"from"`
	To             WalletRef    `json:"to"`
	Asset          NetworkAsset `json:"asset"`
	Amount         float64      `json:"amount"`
	SettlementRail string       `json:"settlementRail"`
	SignerRequired bool         `json:"signerRequired"`
	Status         TradeStatus  `json:"status"`
}

type LedgerContract struct {
	Version string       `json:"version"`
	Trace   Trace        `json:"trace"`
	Entries []LedgerLine `json:"entries"`
	Status  TradeStatus  `json:"status"`
}

type LedgerLine struct {
	Account string  `json:"account"`
	Asset   string  `json:"asset"`
	Network string  `json:"network,omitempty"`
	Amount  float64 `json:"amount"`
	Side    string  `json:"side"`
}

type ContractSet struct {
	Trade      TradeContract      `json:"tradeContract"`
	Settlement SettlementContract `json:"settlementContract"`
	Ledger     LedgerContract     `json:"ledgerContract"`
}

type BuildInput struct {
	Side               Side
	OrderID            string
	CustomerID         string
	SourceAsset        string
	DestinationAsset   string
	SourceNetwork      string
	DestinationNetwork string
	SourceChainID      int64
	DestinationChainID int64
	SourceAmount       float64
	DestinationAmount  float64
	ExchangeRate       float64
	SpreadBps          int
	FeeAmount          float64
	FeeAsset           string
	WalletAddress      string
	TreasuryAddress    string
	PaymentMethod      string
	PSPProvider        string
	Status             TradeStatus
	Request            *http.Request
	Metadata           map[string]any
}

func Build(input BuildInput) ContractSet {
	trace := TraceFromRequest(input.Request, input.OrderID, input.Metadata)
	status := input.Status
	if status == "" {
		status = StatusCreated
	}
	source := NetworkAsset{Asset: normalizeAsset(input.SourceAsset), Network: normalizeNetwork(input.SourceNetwork), ChainID: input.SourceChainID}
	destination := NetworkAsset{Asset: normalizeAsset(input.DestinationAsset), Network: normalizeNetwork(input.DestinationNetwork), ChainID: input.DestinationChainID}
	wallet := WalletRef{Address: strings.TrimSpace(input.WalletAddress), Network: destination.Network, ChainID: destination.ChainID, Role: "customer"}
	treasury := WalletRef{Address: strings.TrimSpace(input.TreasuryAddress), Network: source.Network, ChainID: source.ChainID, Role: "treasury"}
	if input.Side == SideSell {
		wallet.Network, wallet.ChainID = source.Network, source.ChainID
		treasury.Network, treasury.ChainID = source.Network, source.ChainID
	}
	trade := TradeContract{
		Version:           "2026-07-19",
		Side:              input.Side,
		Trace:             trace,
		CustomerID:        strings.TrimSpace(input.CustomerID),
		Source:            source,
		Destination:       destination,
		SourceAmount:      input.SourceAmount,
		DestinationAmount: input.DestinationAmount,
		ExchangeRate:      input.ExchangeRate,
		SpreadBps:         input.SpreadBps,
		FeeAmount:         input.FeeAmount,
		FeeAsset:          normalizeAsset(input.FeeAsset),
		Wallet:            wallet,
		TreasuryWallet:    treasury,
		PaymentMethod:     strings.ToLower(strings.TrimSpace(input.PaymentMethod)),
		PSPProvider:       strings.ToLower(strings.TrimSpace(input.PSPProvider)),
		Status:            status,
		Risk:              Risk{Status: "PENDING", Score: 0},
		Metadata:          input.Metadata,
		CreatedAt:         time.Now().UTC(),
	}
	settlement := SettlementContract{
		Version:        trade.Version,
		Trace:          trace,
		Side:           input.Side,
		From:           treasury,
		To:             wallet,
		Asset:          destination,
		Amount:         input.DestinationAmount,
		SettlementRail: "blockchain",
		SignerRequired: true,
		Status:         StatusSettlementPending,
	}
	if input.Side == SideSell {
		settlement.From = wallet
		settlement.To = treasury
		settlement.Asset = source
		settlement.Amount = input.SourceAmount
		settlement.SettlementRail = "customer_onchain_deposit_then_pix"
		settlement.SignerRequired = false
		settlement.Status = StatusDepositPending
	}
	ledger := LedgerContract{
		Version: trade.Version,
		Trace:   trace,
		Status:  status,
		Entries: []LedgerLine{
			{Account: "customer", Asset: source.Asset, Network: source.Network, Amount: input.SourceAmount, Side: "debit"},
			{Account: "customer", Asset: destination.Asset, Network: destination.Network, Amount: input.DestinationAmount, Side: "credit"},
			{Account: "chainfx_fee", Asset: trade.FeeAsset, Amount: input.FeeAmount, Side: "credit"},
		},
	}
	return ContractSet{Trade: trade, Settlement: settlement, Ledger: ledger}
}

func TraceFromRequest(r *http.Request, orderID string, payload any) Trace {
	trace := Trace{TransactionID: orderID, OrderID: orderID, IntentID: orderID}
	if r != nil {
		trace.CorrelationID = firstHeader(r, "X-Correlation-Id", "X-Request-Id")
		trace.TraceID = firstHeader(r, "X-Trace-Id", "Traceparent")
		trace.IdempotencyKey = firstHeader(r, "Idempotency-Key", "X-Idempotency-Key")
	}
	trace.RequestHash = Hash(payload)
	return trace
}

func Hash(payload any) string {
	if payload == nil {
		return ""
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func SettlementOperationID(chainID int64, vaultAddress, settlementIntentID, tokenAddress, recipientAddress string, amountRaw *big.Int) (common.Hash, error) {
	if chainID <= 0 {
		return common.Hash{}, fmt.Errorf("chainID must be positive")
	}
	if !common.IsHexAddress(vaultAddress) {
		return common.Hash{}, fmt.Errorf("vaultAddress must be an EVM address")
	}
	if !common.IsHexAddress(tokenAddress) {
		return common.Hash{}, fmt.Errorf("tokenAddress must be an EVM address")
	}
	if !common.IsHexAddress(recipientAddress) {
		return common.Hash{}, fmt.Errorf("recipientAddress must be an EVM address")
	}
	if amountRaw == nil || amountRaw.Sign() <= 0 {
		return common.Hash{}, fmt.Errorf("amountRaw must be positive")
	}
	args := abi.Arguments{
		{Type: operationIDUint256},
		{Type: operationIDAddress},
		{Type: operationIDBytes32},
		{Type: operationIDAddress},
		{Type: operationIDAddress},
		{Type: operationIDUint256},
	}
	encoded, err := args.Pack(
		big.NewInt(chainID),
		common.HexToAddress(vaultAddress),
		settlementIntentHash(settlementIntentID),
		common.HexToAddress(tokenAddress),
		common.HexToAddress(recipientAddress),
		amountRaw,
	)
	if err != nil {
		return common.Hash{}, err
	}
	return crypto.Keccak256Hash(encoded), nil
}

func settlementIntentHash(id string) [32]byte {
	id = strings.TrimSpace(id)
	if len(id) == 66 && strings.HasPrefix(strings.ToLower(id), "0x") {
		return common.HexToHash(id)
	}
	return crypto.Keccak256Hash([]byte(id))
}

func ChainID(network string) int64 {
	switch normalizeNetwork(network) {
	case "BSC":
		return 56
	case "POLYGON":
		return 137
	case "BASE":
		return 8453
	case "ARBITRUM":
		return 42161
	case "OPTIMISM":
		return 10
	case "ETHEREUM":
		return 1
	default:
		return 0
	}
}

func CanonicalBuyStatus(status string) TradeStatus {
	s := strings.ToLower(strings.TrimSpace(status))
	switch {
	case strings.Contains(s, "aguardando_pix"), strings.Contains(s, "aguardando_card"), strings.Contains(s, "payment_provider_pending"):
		return StatusPaymentPending
	case strings.Contains(s, "pago"):
		return StatusPaymentConfirmed
	case strings.Contains(s, "processando"), strings.Contains(s, "settlement"):
		return StatusSettlementPending
	case strings.Contains(s, "enviado"), strings.Contains(s, "delivered"):
		return StatusDelivered
	case strings.Contains(s, "conclu"):
		return StatusCompleted
	case strings.Contains(s, "erro"), strings.Contains(s, "failed"):
		return StatusFailed
	default:
		return StatusCreated
	}
}

func CanonicalSellStatus(status string) TradeStatus {
	s := strings.ToLower(strings.TrimSpace(status))
	switch {
	case strings.Contains(s, "aguardando_deposito"):
		return StatusDepositPending
	case strings.Contains(s, "pago"):
		return StatusOnchainDetected
	case strings.Contains(s, "processando"):
		return StatusPixPending
	case strings.Contains(s, "conclu"):
		return StatusCompleted
	case strings.Contains(s, "erro"), strings.Contains(s, "expirada"), strings.Contains(s, "incidente"):
		return StatusFailed
	default:
		return StatusCreated
	}
}

func firstHeader(r *http.Request, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(r.Header.Get(name)); value != "" {
			return value
		}
	}
	return ""
}

func normalizeAsset(asset string) string {
	asset = strings.ToUpper(strings.TrimSpace(asset))
	if asset == "" {
		return "USDT"
	}
	return asset
}

func normalizeNetwork(network string) string {
	network = strings.ToUpper(strings.TrimSpace(network))
	switch network {
	case "BNB", "BEP20", "BINANCE", "BINANCE_SMART_CHAIN":
		return "BSC"
	case "MATIC":
		return "POLYGON"
	case "":
		return "BSC"
	default:
		return network
	}
}
