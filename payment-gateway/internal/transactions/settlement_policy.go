package transactions

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

const (
	SettlementPolicyVersion = "settlement-policy-v1.0.0"
	NetworkPolicyBSCUSDT    = "bsc-usdt-v1"
	NetworkPolicyPolyUSDT   = "polygon-usdt-v1"
	RiskPolicyRetailBuy     = "retail-buy-v2"
	ContractTreasuryV110    = "treasury-v1.1.0"
)

type SourceChannel string

const (
	SourceWeb          SourceChannel = "web"
	SourceMobile       SourceChannel = "mobile"
	SourceDeveloperAPI SourceChannel = "developer_api"
	SourceMCP          SourceChannel = "mcp"
	SourceA2A          SourceChannel = "a2a"
	SourceAdmin        SourceChannel = "admin"
	SourceWorker       SourceChannel = "worker"
)

type SettlementInstruction struct {
	OperationID        [32]byte       `json:"operationId"`
	SettlementIntentID string         `json:"settlementIntentId"`
	OrderID            string         `json:"orderId"`
	Side               string         `json:"side"`
	Network            string         `json:"network"`
	ChainID            uint64         `json:"chainId"`
	Vault              common.Address `json:"vault"`
	Token              common.Address `json:"token"`
	Recipient          common.Address `json:"recipient"`
	AmountRaw          *big.Int       `json:"amountRaw"`
	SourceChannel      SourceChannel  `json:"sourceChannel"`
	RiskDecision       string         `json:"riskDecision"`
	PolicyVersion      string         `json:"policyVersion"`
	NetworkPolicy      string         `json:"networkPolicy"`
	RiskPolicy         string         `json:"riskPolicy"`
	ContractVersion    string         `json:"contractVersion"`
	CreatedAt          time.Time      `json:"createdAt"`
	ExpiresAt          time.Time      `json:"expiresAt"`
}

type SettlementPolicy struct {
	Version            string
	NetworkPolicy      string
	RiskPolicy         string
	ContractVersion    string
	Network            string
	ChainID            uint64
	Vault              common.Address
	AllowedTokens      map[common.Address]bool
	BlockedRecipients  map[common.Address]bool
	MaxTransferRaw     *big.Int
	DailyLimitRaw      *big.Int
	RequireRiskApprove bool
	LiquidatableStates map[TradeStatus]bool
	QuoteMaxAge        time.Duration
	InstructionTTL     time.Duration
}

type SettlementValidationInput struct {
	SettlementIntentID string
	OrderID            string
	Side               Side
	Network            string
	ChainID            uint64
	Vault              common.Address
	Token              common.Address
	Recipient          common.Address
	AmountRaw          *big.Int
	SourceChannel      SourceChannel
	RiskDecision       string
	IntentStatus       TradeStatus
	QuoteCreatedAt     time.Time
	Now                time.Time
	OperationUsed      bool
	TreasuryBalanceRaw *big.Int
	DailySpentRaw      *big.Int
}

type SettlementPolicyValidator struct {
	policies map[string]SettlementPolicy
}

func NewSettlementPolicyValidator(policies []SettlementPolicy) (*SettlementPolicyValidator, error) {
	out := &SettlementPolicyValidator{policies: map[string]SettlementPolicy{}}
	for _, policy := range policies {
		network := normalizeNetwork(policy.Network)
		if network == "" {
			return nil, errors.New("settlement policy network is required")
		}
		if policy.ChainID == 0 {
			return nil, fmt.Errorf("settlement policy %s chainID is required", network)
		}
		if policy.Vault == (common.Address{}) {
			return nil, fmt.Errorf("settlement policy %s vault is required", network)
		}
		if len(policy.AllowedTokens) == 0 {
			return nil, fmt.Errorf("settlement policy %s needs at least one token", network)
		}
		if policy.Version == "" {
			policy.Version = SettlementPolicyVersion
		}
		if policy.ContractVersion == "" {
			policy.ContractVersion = ContractTreasuryV110
		}
		if policy.InstructionTTL <= 0 {
			policy.InstructionTTL = 10 * time.Minute
		}
		policy.Network = network
		out.policies[network] = policy
	}
	return out, nil
}

func (v *SettlementPolicyValidator) BuildInstruction(input SettlementValidationInput) (SettlementInstruction, error) {
	if v == nil {
		return SettlementInstruction{}, errors.New("settlement policy validator is nil")
	}
	policy, ok := v.policies[normalizeNetwork(input.Network)]
	if !ok {
		return SettlementInstruction{}, fmt.Errorf("settlement network not enabled: %s", input.Network)
	}
	now := input.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if err := policy.validate(input, now); err != nil {
		return SettlementInstruction{}, err
	}
	operationID, err := SettlementOperationID(int64(policy.ChainID), policy.Vault.Hex(), input.SettlementIntentID, input.Token.Hex(), input.Recipient.Hex(), input.AmountRaw)
	if err != nil {
		return SettlementInstruction{}, err
	}
	var operationIDBytes [32]byte
	copy(operationIDBytes[:], operationID.Bytes())
	return SettlementInstruction{
		OperationID:        operationIDBytes,
		SettlementIntentID: strings.TrimSpace(input.SettlementIntentID),
		OrderID:            strings.TrimSpace(input.OrderID),
		Side:               string(input.Side),
		Network:            policy.Network,
		ChainID:            policy.ChainID,
		Vault:              policy.Vault,
		Token:              input.Token,
		Recipient:          input.Recipient,
		AmountRaw:          new(big.Int).Set(input.AmountRaw),
		SourceChannel:      normalizeSourceChannel(input.SourceChannel),
		RiskDecision:       normalizeRiskDecision(input.RiskDecision),
		PolicyVersion:      policy.Version,
		NetworkPolicy:      policy.NetworkPolicy,
		RiskPolicy:         policy.RiskPolicy,
		ContractVersion:    policy.ContractVersion,
		CreatedAt:          now,
		ExpiresAt:          now.Add(policy.InstructionTTL),
	}, nil
}

func (p SettlementPolicy) validate(input SettlementValidationInput, now time.Time) error {
	if strings.TrimSpace(input.SettlementIntentID) == "" {
		return errors.New("settlementIntentID is required")
	}
	if strings.TrimSpace(input.OrderID) == "" {
		return errors.New("orderID is required")
	}
	if input.Side != SideBuy && input.Side != SideSell {
		return errors.New("side must be BUY or SELL")
	}
	if normalizeNetwork(input.Network) != p.Network {
		return fmt.Errorf("network mismatch: expected %s got %s", p.Network, input.Network)
	}
	if input.ChainID != p.ChainID {
		return fmt.Errorf("chainId mismatch: expected %d got %d", p.ChainID, input.ChainID)
	}
	if input.Vault != p.Vault {
		return errors.New("vault mismatch for network")
	}
	if !p.AllowedTokens[input.Token] {
		return errors.New("token is not allowed by settlement policy")
	}
	if input.Recipient == (common.Address{}) {
		return errors.New("recipient is required")
	}
	if p.BlockedRecipients[input.Recipient] {
		return errors.New("recipient is blocked")
	}
	if input.AmountRaw == nil || input.AmountRaw.Sign() <= 0 {
		return errors.New("amountRaw must be positive")
	}
	if p.MaxTransferRaw != nil && p.MaxTransferRaw.Sign() > 0 && input.AmountRaw.Cmp(p.MaxTransferRaw) > 0 {
		return errors.New("amount exceeds max transfer")
	}
	spent := big.NewInt(0)
	if input.DailySpentRaw != nil {
		spent = new(big.Int).Set(input.DailySpentRaw)
	}
	if p.DailyLimitRaw != nil && p.DailyLimitRaw.Sign() > 0 && new(big.Int).Add(spent, input.AmountRaw).Cmp(p.DailyLimitRaw) > 0 {
		return errors.New("daily limit unavailable")
	}
	if p.RequireRiskApprove && normalizeRiskDecision(input.RiskDecision) != "APPROVED" {
		return errors.New("risk decision is not approved")
	}
	if len(p.LiquidatableStates) > 0 && !p.LiquidatableStates[input.IntentStatus] {
		return errors.New("intent is not in a liquidatable state")
	}
	if p.QuoteMaxAge > 0 {
		if input.QuoteCreatedAt.IsZero() {
			return errors.New("quote timestamp is required")
		}
		if now.After(input.QuoteCreatedAt.Add(p.QuoteMaxAge)) {
			return errors.New("quote is expired")
		}
	}
	if input.OperationUsed {
		return errors.New("operationId already used")
	}
	if input.TreasuryBalanceRaw == nil || input.TreasuryBalanceRaw.Cmp(input.AmountRaw) < 0 {
		return errors.New("treasury balance is insufficient")
	}
	return nil
}

func DefaultBSCUSDTSettlementPolicy(vaultAddress, tokenAddress string, maxTransferRaw, dailyLimitRaw *big.Int) (SettlementPolicy, error) {
	if !common.IsHexAddress(vaultAddress) {
		return SettlementPolicy{}, errors.New("BSC vault address is invalid")
	}
	if !common.IsHexAddress(tokenAddress) {
		return SettlementPolicy{}, errors.New("BSC token address is invalid")
	}
	return SettlementPolicy{
		Version:            SettlementPolicyVersion,
		NetworkPolicy:      NetworkPolicyBSCUSDT,
		RiskPolicy:         RiskPolicyRetailBuy,
		ContractVersion:    ContractTreasuryV110,
		Network:            "BSC",
		ChainID:            56,
		Vault:              common.HexToAddress(vaultAddress),
		AllowedTokens:      map[common.Address]bool{common.HexToAddress(tokenAddress): true},
		BlockedRecipients:  map[common.Address]bool{},
		MaxTransferRaw:     cloneBig(maxTransferRaw),
		DailyLimitRaw:      cloneBig(dailyLimitRaw),
		RequireRiskApprove: true,
		LiquidatableStates: map[TradeStatus]bool{
			StatusPaymentConfirmed:   true,
			StatusComplianceApproved: true,
			StatusSettlementPending:  true,
		},
		QuoteMaxAge:    10 * time.Minute,
		InstructionTTL: 10 * time.Minute,
	}, nil
}

func cloneBig(v *big.Int) *big.Int {
	if v == nil {
		return nil
	}
	return new(big.Int).Set(v)
}

func normalizeSourceChannel(channel SourceChannel) SourceChannel {
	switch channel {
	case SourceWeb, SourceMobile, SourceDeveloperAPI, SourceMCP, SourceA2A, SourceAdmin, SourceWorker:
		return channel
	default:
		return SourceWorker
	}
}

func normalizeRiskDecision(decision string) string {
	decision = strings.ToUpper(strings.TrimSpace(decision))
	if decision == "" {
		return "PENDING"
	}
	return decision
}
