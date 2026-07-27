package settlement

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

func TestReconcileSettlementFullyReconciled(t *testing.T) {
	instruction, receipt := settlementFixture(t)

	result := ReconcileSettlement(instruction, receipt, FinalityPolicy{RequiredConfirmations: 1})
	if result.Status != ReconciliationFullyReconciled {
		t.Fatalf("expected FULLY_RECONCILED, got %+v", result)
	}
	if !result.ReceiptVerified || !result.VaultEventVerified || !result.TransferEventVerified || !result.ConfirmationsVerified {
		t.Fatalf("expected all verification flags true: %+v", result)
	}
}

func TestReconcileSettlementConfirmingWhenFinalityInsufficient(t *testing.T) {
	instruction, receipt := settlementFixture(t)
	receipt.Confirmations = 1

	result := ReconcileSettlement(instruction, receipt, FinalityPolicy{RequiredConfirmations: 3})
	if result.Status != ReconciliationConfirming {
		t.Fatalf("expected CONFIRMING, got %+v", result)
	}
	if result.ConfirmationsVerified {
		t.Fatalf("confirmations should not be verified")
	}
}

func TestReconcileSettlementMismatches(t *testing.T) {
	tests := []struct {
		name string
		edit func(SettlementInstruction, ReceiptObservation) (SettlementInstruction, ReceiptObservation)
		code string
	}{
		{
			name: "receipt reverted",
			edit: func(i SettlementInstruction, r ReceiptObservation) (SettlementInstruction, ReceiptObservation) {
				r.Status = types.ReceiptStatusFailed
				return i, r
			},
			code: FailureReceiptReverted,
		},
		{
			name: "tx hash mismatch",
			edit: func(i SettlementInstruction, r ReceiptObservation) (SettlementInstruction, ReceiptObservation) {
				r.TxHash = crypto.Keccak256Hash([]byte("other-tx"))
				return i, r
			},
			code: FailureTxHashMismatch,
		},
		{
			name: "tx to mismatch",
			edit: func(i SettlementInstruction, r ReceiptObservation) (SettlementInstruction, ReceiptObservation) {
				r.To = common.HexToAddress("0x9999999999999999999999999999999999999999")
				return i, r
			},
			code: FailureTxToMismatch,
		},
		{
			name: "vault event missing",
			edit: func(i SettlementInstruction, r ReceiptObservation) (SettlementInstruction, ReceiptObservation) {
				r.Logs = []types.Log{r.Logs[1]}
				return i, r
			},
			code: FailureVaultEventMissing,
		},
		{
			name: "transfer event missing",
			edit: func(i SettlementInstruction, r ReceiptObservation) (SettlementInstruction, ReceiptObservation) {
				r.Logs = []types.Log{r.Logs[0]}
				return i, r
			},
			code: FailureTransferEventMissing,
		},
		{
			name: "operation id mismatch",
			edit: func(i SettlementInstruction, r ReceiptObservation) (SettlementInstruction, ReceiptObservation) {
				r.Logs[0].Topics[1] = crypto.Keccak256Hash([]byte("other-op"))
				return i, r
			},
			code: FailureOperationIDMismatch,
		},
		{
			name: "token mismatch",
			edit: func(i SettlementInstruction, r ReceiptObservation) (SettlementInstruction, ReceiptObservation) {
				r.Logs[0].Topics[2] = addressTopic(common.HexToAddress("0x7777777777777777777777777777777777777777"))
				return i, r
			},
			code: FailureTokenMismatch,
		},
		{
			name: "recipient mismatch",
			edit: func(i SettlementInstruction, r ReceiptObservation) (SettlementInstruction, ReceiptObservation) {
				r.Logs[0].Topics[3] = addressTopic(common.HexToAddress("0x8888888888888888888888888888888888888888"))
				return i, r
			},
			code: FailureRecipientMismatch,
		},
		{
			name: "amount mismatch",
			edit: func(i SettlementInstruction, r ReceiptObservation) (SettlementInstruction, ReceiptObservation) {
				r.Logs[0].Data = packPayoutData(t, big.NewInt(11_000_000), common.HexToAddress("0x4444444444444444444444444444444444444444"))
				return i, r
			},
			code: FailureAmountMismatch,
		},
		{
			name: "vault mismatch",
			edit: func(i SettlementInstruction, r ReceiptObservation) (SettlementInstruction, ReceiptObservation) {
				r.Logs[0].Address = common.HexToAddress("0x9999999999999999999999999999999999999999")
				return i, r
			},
			code: FailureVaultMismatch,
		},
		{
			name: "duplicate vault event",
			edit: func(i SettlementInstruction, r ReceiptObservation) (SettlementInstruction, ReceiptObservation) {
				dup := r.Logs[0]
				dup.Index = 9
				r.Logs = append(r.Logs, dup)
				return i, r
			},
			code: FailureVaultEventDuplicate,
		},
		{
			name: "duplicate transfer event",
			edit: func(i SettlementInstruction, r ReceiptObservation) (SettlementInstruction, ReceiptObservation) {
				dup := r.Logs[1]
				dup.Index = 10
				r.Logs = append(r.Logs, dup)
				return i, r
			},
			code: FailureTransferEventDuplicate,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			instruction, receipt := settlementFixture(t)
			instruction, receipt = tt.edit(instruction, receipt)

			result := ReconcileSettlement(instruction, receipt, FinalityPolicy{RequiredConfirmations: 1})
			if result.Status != ReconciliationMismatch {
				t.Fatalf("expected MISMATCH, got %+v", result)
			}
			if result.FailureCode != tt.code {
				t.Fatalf("failure code mismatch: got %s want %s", result.FailureCode, tt.code)
			}
		})
	}
}

func TestDecodeVaultPayoutSupportsPayoutExecutedAlias(t *testing.T) {
	instruction, _ := settlementFixture(t)
	log := vaultPayoutLog(t, instruction, 3)
	log.Topics[0] = vaultPayoutExecutedTopic

	decoded, ok, err := DecodeVaultPayoutLog(log)
	if err != nil || !ok {
		t.Fatalf("expected PayoutExecuted decode ok=%v err=%v", ok, err)
	}
	if decoded.OperationID != instruction.OperationID || decoded.Token != instruction.TokenAddress || decoded.Recipient != instruction.RecipientAddress {
		t.Fatalf("decoded wrong event: %+v", decoded)
	}
}

func settlementFixture(t *testing.T) (SettlementInstruction, ReceiptObservation) {
	instruction := SettlementInstruction{
		OperationID:      crypto.Keccak256Hash([]byte("settlement-001")),
		TxHash:           crypto.Keccak256Hash([]byte("tx-001")),
		ChainID:          56,
		VaultAddress:     common.HexToAddress("0x1111111111111111111111111111111111111111"),
		TokenAddress:     common.HexToAddress("0x2222222222222222222222222222222222222222"),
		RecipientAddress: common.HexToAddress("0x3333333333333333333333333333333333333333"),
		AmountRaw:        big.NewInt(10_000_000),
		OperatorAddress:  common.HexToAddress("0x4444444444444444444444444444444444444444"),
	}
	receipt := ReceiptObservation{
		TxHash:           instruction.TxHash,
		To:               instruction.VaultAddress,
		ChainID:          instruction.ChainID,
		Status:           types.ReceiptStatusSuccessful,
		BlockNumber:      100,
		BlockHash:        crypto.Keccak256Hash([]byte("block-100")),
		TransactionIndex: 2,
		Confirmations:    2,
		Logs: []types.Log{
			vaultPayoutLog(t, instruction, 0),
			erc20TransferLog(t, instruction, 1),
		},
	}
	return instruction, receipt
}

func vaultPayoutLog(t interface{ Fatalf(string, ...any) }, instruction SettlementInstruction, index uint) types.Log {
	return types.Log{
		Address: instruction.VaultAddress,
		Topics: []common.Hash{
			vaultPayoutTopic,
			instruction.OperationID,
			addressTopic(instruction.TokenAddress),
			addressTopic(instruction.RecipientAddress),
		},
		Data:  packPayoutData(t, instruction.AmountRaw, instruction.OperatorAddress),
		Index: index,
	}
}

func erc20TransferLog(t interface{ Fatalf(string, ...any) }, instruction SettlementInstruction, index uint) types.Log {
	return types.Log{
		Address: instruction.TokenAddress,
		Topics: []common.Hash{
			erc20TransferTopic,
			addressTopic(instruction.VaultAddress),
			addressTopic(instruction.RecipientAddress),
		},
		Data:  packTransferData(t, instruction.AmountRaw),
		Index: index,
	}
}

func addressTopic(address common.Address) common.Hash {
	return common.BytesToHash(common.LeftPadBytes(address.Bytes(), 32))
}

func packPayoutData(t interface{ Fatalf(string, ...any) }, amount *big.Int, operator common.Address) []byte {
	uint256Type, err := abi.NewType("uint256", "", nil)
	if err != nil {
		t.Fatalf("uint256 type: %v", err)
	}
	addressType, err := abi.NewType("address", "", nil)
	if err != nil {
		t.Fatalf("address type: %v", err)
	}
	out, err := abi.Arguments{{Type: uint256Type}, {Type: addressType}}.Pack(amount, operator)
	if err != nil {
		t.Fatalf("pack payout: %v", err)
	}
	return out
}

func packTransferData(t interface{ Fatalf(string, ...any) }, amount *big.Int) []byte {
	uint256Type, err := abi.NewType("uint256", "", nil)
	if err != nil {
		t.Fatalf("uint256 type: %v", err)
	}
	out, err := abi.Arguments{{Type: uint256Type}}.Pack(amount)
	if err != nil {
		t.Fatalf("pack transfer: %v", err)
	}
	return out
}
