package transactions

import (
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

func TestSettlementPolicyValidatorBuildsInstitutionalInstruction(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	validator := testSettlementValidator(t, nil)
	input := testSettlementInput(now)

	instruction, err := validator.BuildInstruction(input)
	if err != nil {
		t.Fatalf("BuildInstruction returned error: %v", err)
	}
	if instruction.PolicyVersion != SettlementPolicyVersion {
		t.Fatalf("unexpected policy version: %s", instruction.PolicyVersion)
	}
	if instruction.NetworkPolicy != NetworkPolicyBSCUSDT {
		t.Fatalf("unexpected network policy: %s", instruction.NetworkPolicy)
	}
	if instruction.ContractVersion != ContractTreasuryV110 {
		t.Fatalf("unexpected contract version: %s", instruction.ContractVersion)
	}
	if instruction.SourceChannel != SourceMobile {
		t.Fatalf("unexpected source channel: %s", instruction.SourceChannel)
	}
	if instruction.AmountRaw.Cmp(input.AmountRaw) != 0 {
		t.Fatalf("amount raw mismatch")
	}
	if instruction.ExpiresAt.Sub(instruction.CreatedAt) != 10*time.Minute {
		t.Fatalf("unexpected instruction ttl")
	}
}

func TestSettlementPolicyValidatorRejectsInstitutionalGaps(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	recipient := common.HexToAddress("0x3000000000000000000000000000000000000003")
	blocked := map[common.Address]bool{recipient: true}

	tests := []struct {
		name    string
		policy  func() SettlementPolicy
		mutate  func(*SettlementValidationInput)
		wantErr string
	}{
		{name: "disabled network", mutate: func(in *SettlementValidationInput) { in.Network = "POLYGON" }, wantErr: "settlement network not enabled"},
		{name: "chain mismatch", mutate: func(in *SettlementValidationInput) { in.ChainID = 137 }, wantErr: "chainId mismatch"},
		{name: "vault mismatch", mutate: func(in *SettlementValidationInput) {
			in.Vault = common.HexToAddress("0x9000000000000000000000000000000000000009")
		}, wantErr: "vault mismatch"},
		{name: "token not allowed", mutate: func(in *SettlementValidationInput) {
			in.Token = common.HexToAddress("0x9000000000000000000000000000000000000009")
		}, wantErr: "token is not allowed"},
		{name: "recipient blocked", policy: func() SettlementPolicy { return testSettlementPolicy(t, blocked) }, wantErr: "recipient is blocked"},
		{name: "amount exceeds max", mutate: func(in *SettlementValidationInput) { in.AmountRaw = big.NewInt(2_000_000) }, wantErr: "amount exceeds max transfer"},
		{name: "daily limit unavailable", mutate: func(in *SettlementValidationInput) { in.DailySpentRaw = big.NewInt(4_999_001) }, wantErr: "daily limit unavailable"},
		{name: "risk not approved", mutate: func(in *SettlementValidationInput) { in.RiskDecision = "REVIEW" }, wantErr: "risk decision is not approved"},
		{name: "intent not liquidatable", mutate: func(in *SettlementValidationInput) { in.IntentStatus = StatusPaymentPending }, wantErr: "intent is not in a liquidatable state"},
		{name: "quote expired", mutate: func(in *SettlementValidationInput) { in.QuoteCreatedAt = now.Add(-11 * time.Minute) }, wantErr: "quote is expired"},
		{name: "operation used", mutate: func(in *SettlementValidationInput) { in.OperationUsed = true }, wantErr: "operationId already used"},
		{name: "insufficient balance", mutate: func(in *SettlementValidationInput) { in.TreasuryBalanceRaw = big.NewInt(999) }, wantErr: "treasury balance is insufficient"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy := testSettlementPolicy(t, nil)
			if tt.policy != nil {
				policy = tt.policy()
			}
			validator, err := NewSettlementPolicyValidator([]SettlementPolicy{policy})
			if err != nil {
				t.Fatalf("NewSettlementPolicyValidator returned error: %v", err)
			}
			input := testSettlementInput(now)
			if tt.mutate != nil {
				tt.mutate(&input)
			}
			_, err = validator.BuildInstruction(input)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestSettlementInstructionOperationIDChangesAcrossDomain(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	validator := testSettlementValidator(t, nil)
	input := testSettlementInput(now)

	first, err := validator.BuildInstruction(input)
	if err != nil {
		t.Fatalf("BuildInstruction returned error: %v", err)
	}
	input.AmountRaw = big.NewInt(2_000)
	input.TreasuryBalanceRaw = big.NewInt(10_000_000)
	second, err := validator.BuildInstruction(input)
	if err != nil {
		t.Fatalf("BuildInstruction returned error on second input: %v", err)
	}
	if first.OperationID == second.OperationID {
		t.Fatalf("operationID must change when authorized amount changes")
	}
}

func testSettlementValidator(t *testing.T, blocked map[common.Address]bool) *SettlementPolicyValidator {
	t.Helper()
	policy := testSettlementPolicy(t, blocked)
	validator, err := NewSettlementPolicyValidator([]SettlementPolicy{policy})
	if err != nil {
		t.Fatalf("NewSettlementPolicyValidator returned error: %v", err)
	}
	return validator
}

func testSettlementPolicy(t *testing.T, blocked map[common.Address]bool) SettlementPolicy {
	t.Helper()
	policy, err := DefaultBSCUSDTSettlementPolicy(
		"0x1000000000000000000000000000000000000001",
		"0x2000000000000000000000000000000000000002",
		big.NewInt(1_000_000),
		big.NewInt(5_000_000),
	)
	if err != nil {
		t.Fatalf("DefaultBSCUSDTSettlementPolicy returned error: %v", err)
	}
	if blocked != nil {
		policy.BlockedRecipients = blocked
	}
	return policy
}

func testSettlementInput(now time.Time) SettlementValidationInput {
	return SettlementValidationInput{
		SettlementIntentID: "settlement_intent_1",
		OrderID:            "buy_1",
		Side:               SideBuy,
		Network:            "BSC",
		ChainID:            56,
		Vault:              common.HexToAddress("0x1000000000000000000000000000000000000001"),
		Token:              common.HexToAddress("0x2000000000000000000000000000000000000002"),
		Recipient:          common.HexToAddress("0x3000000000000000000000000000000000000003"),
		AmountRaw:          big.NewInt(1_000),
		SourceChannel:      SourceMobile,
		RiskDecision:       "APPROVED",
		IntentStatus:       StatusPaymentConfirmed,
		QuoteCreatedAt:     now.Add(-1 * time.Minute),
		Now:                now,
		TreasuryBalanceRaw: big.NewInt(10_000_000),
		DailySpentRaw:      big.NewInt(0),
	}
}
