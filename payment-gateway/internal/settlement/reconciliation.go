package settlement

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

const (
	ReconciliationPending               = "PENDING"
	ReconciliationReceiptVerified       = "RECEIPT_VERIFIED"
	ReconciliationVaultEventVerified    = "VAULT_EVENT_VERIFIED"
	ReconciliationTokenTransferVerified = "TOKEN_TRANSFER_VERIFIED"
	ReconciliationConfirming            = "CONFIRMING"
	ReconciliationFullyReconciled       = "FULLY_RECONCILED"
	ReconciliationMismatch              = "MISMATCH"
)

const (
	FailureReceiptReverted        = "RECEIPT_REVERTED"
	FailureTxHashMismatch         = "TX_HASH_MISMATCH"
	FailureTxToMismatch           = "TX_TO_MISMATCH"
	FailureVaultEventMissing      = "VAULT_EVENT_MISSING"
	FailureVaultEventDuplicate    = "VAULT_EVENT_DUPLICATE"
	FailureTransferEventMissing   = "TRANSFER_EVENT_MISSING"
	FailureTransferEventDuplicate = "TRANSFER_EVENT_DUPLICATE"
	FailureOperationIDMismatch    = "OPERATION_ID_MISMATCH"
	FailureTokenMismatch          = "TOKEN_MISMATCH"
	FailureRecipientMismatch      = "RECIPIENT_MISMATCH"
	FailureAmountMismatch         = "AMOUNT_MISMATCH"
	FailureVaultMismatch          = "VAULT_MISMATCH"
)

var (
	vaultPayoutTopic         = crypto.Keccak256Hash([]byte("Payout(bytes32,address,address,uint256,address)"))
	vaultPayoutExecutedTopic = crypto.Keccak256Hash([]byte("PayoutExecuted(bytes32,address,address,uint256,address)"))
	erc20TransferTopic       = crypto.Keccak256Hash([]byte("Transfer(address,address,uint256)"))
)

type SettlementInstruction struct {
	OperationID      common.Hash
	TxHash           common.Hash
	ChainID          uint64
	VaultAddress     common.Address
	TokenAddress     common.Address
	RecipientAddress common.Address
	AmountRaw        *big.Int
	OperatorAddress  common.Address
}

type ReceiptObservation struct {
	TxHash           common.Hash
	To               common.Address
	ChainID          uint64
	Status           uint64
	BlockNumber      uint64
	BlockHash        common.Hash
	TransactionIndex uint
	Confirmations    uint64
	Logs             []types.Log
}

type VaultPayoutEvent struct {
	OperationID common.Hash
	Token       common.Address
	Recipient   common.Address
	Amount      *big.Int
	Operator    common.Address
	LogIndex    uint
}

type ERC20TransferEvent struct {
	Token    common.Address
	From     common.Address
	To       common.Address
	Value    *big.Int
	LogIndex uint
}

type ReconciliationResult struct {
	OperationID           string
	TxHash                string
	ReceiptVerified       bool
	VaultEventVerified    bool
	TransferEventVerified bool
	ConfirmationsVerified bool
	VaultEventLogIndex    uint
	TransferEventLogIndex uint
	BlockNumber           uint64
	BlockHash             string
	Status                string
	FailureCode           string
	FailureField          string
	ReconciledAt          time.Time
}

type FinalityPolicy struct {
	Network               string
	ChainID               uint64
	RequiredConfirmations uint64
	ReorgSafetyDepth      uint64
	Version               string
}

func ReconcileSettlement(instruction SettlementInstruction, receipt ReceiptObservation, finality FinalityPolicy) ReconciliationResult {
	result := ReconciliationResult{
		OperationID:  instruction.OperationID.Hex(),
		TxHash:       receipt.TxHash.Hex(),
		BlockNumber:  receipt.BlockNumber,
		BlockHash:    receipt.BlockHash.Hex(),
		Status:       ReconciliationPending,
		ReconciledAt: time.Now().UTC(),
	}
	if instruction.AmountRaw == nil || instruction.AmountRaw.Sign() <= 0 {
		return result.fail(FailureAmountMismatch, "amountRaw")
	}
	if receipt.TxHash != instruction.TxHash {
		return result.fail(FailureTxHashMismatch, "txHash")
	}
	if receipt.Status != types.ReceiptStatusSuccessful {
		return result.fail(FailureReceiptReverted, "receipt.status")
	}
	if receipt.ChainID != instruction.ChainID {
		return result.fail("CHAIN_ID_MISMATCH", "chainId")
	}
	if receipt.To != instruction.VaultAddress {
		return result.fail(FailureTxToMismatch, "receipt.to")
	}
	result.ReceiptVerified = true
	result.Status = ReconciliationReceiptVerified

	payout, err := findMatchingVaultPayout(instruction, receipt.Logs)
	if err != nil {
		code, field := classifyReconciliationError(err)
		return result.fail(code, field)
	}
	result.VaultEventVerified = true
	result.VaultEventLogIndex = payout.LogIndex
	result.Status = ReconciliationVaultEventVerified

	transfer, err := findMatchingERC20Transfer(instruction, receipt.Logs)
	if err != nil {
		code, field := classifyReconciliationError(err)
		return result.fail(code, field)
	}
	result.TransferEventVerified = true
	result.TransferEventLogIndex = transfer.LogIndex
	result.Status = ReconciliationTokenTransferVerified

	requiredConfirmations := finality.RequiredConfirmations
	if requiredConfirmations == 0 {
		requiredConfirmations = 1
	}
	if receipt.Confirmations < requiredConfirmations {
		result.Status = ReconciliationConfirming
		return result
	}
	result.ConfirmationsVerified = true
	result.Status = ReconciliationFullyReconciled
	return result
}

func DecodeVaultPayoutLog(log types.Log) (VaultPayoutEvent, bool, error) {
	if len(log.Topics) == 0 || (log.Topics[0] != vaultPayoutTopic && log.Topics[0] != vaultPayoutExecutedTopic) {
		return VaultPayoutEvent{}, false, nil
	}
	if len(log.Topics) != 4 {
		return VaultPayoutEvent{}, true, errors.New("vault payout topics invalid")
	}
	uint256Type, err := abi.NewType("uint256", "", nil)
	if err != nil {
		return VaultPayoutEvent{}, true, err
	}
	addressType, err := abi.NewType("address", "", nil)
	if err != nil {
		return VaultPayoutEvent{}, true, err
	}
	args := abi.Arguments{{Type: uint256Type}, {Type: addressType}}
	values, err := args.Unpack(log.Data)
	if err != nil {
		return VaultPayoutEvent{}, true, err
	}
	amount, ok := values[0].(*big.Int)
	if !ok {
		return VaultPayoutEvent{}, true, errors.New("vault payout amount invalid")
	}
	operator, ok := values[1].(common.Address)
	if !ok {
		return VaultPayoutEvent{}, true, errors.New("vault payout operator invalid")
	}
	return VaultPayoutEvent{
		OperationID: log.Topics[1],
		Token:       common.BytesToAddress(log.Topics[2].Bytes()[12:]),
		Recipient:   common.BytesToAddress(log.Topics[3].Bytes()[12:]),
		Amount:      new(big.Int).Set(amount),
		Operator:    operator,
		LogIndex:    log.Index,
	}, true, nil
}

func DecodeERC20TransferLog(log types.Log) (ERC20TransferEvent, bool, error) {
	if len(log.Topics) == 0 || log.Topics[0] != erc20TransferTopic {
		return ERC20TransferEvent{}, false, nil
	}
	if len(log.Topics) != 3 {
		return ERC20TransferEvent{}, true, errors.New("erc20 transfer topics invalid")
	}
	uint256Type, err := abi.NewType("uint256", "", nil)
	if err != nil {
		return ERC20TransferEvent{}, true, err
	}
	args := abi.Arguments{{Type: uint256Type}}
	values, err := args.Unpack(log.Data)
	if err != nil {
		return ERC20TransferEvent{}, true, err
	}
	value, ok := values[0].(*big.Int)
	if !ok {
		return ERC20TransferEvent{}, true, errors.New("erc20 transfer value invalid")
	}
	return ERC20TransferEvent{
		Token:    log.Address,
		From:     common.BytesToAddress(log.Topics[1].Bytes()[12:]),
		To:       common.BytesToAddress(log.Topics[2].Bytes()[12:]),
		Value:    new(big.Int).Set(value),
		LogIndex: log.Index,
	}, true, nil
}

func findMatchingVaultPayout(instruction SettlementInstruction, logs []types.Log) (VaultPayoutEvent, error) {
	var matches []VaultPayoutEvent
	for _, item := range logs {
		decoded, ok, err := DecodeVaultPayoutLog(item)
		if err != nil {
			return VaultPayoutEvent{}, err
		}
		if !ok {
			continue
		}
		if item.Address != instruction.VaultAddress {
			return VaultPayoutEvent{}, reconciliationError{code: FailureVaultMismatch, field: "vaultAddress"}
		}
		if decoded.OperationID != instruction.OperationID {
			return VaultPayoutEvent{}, reconciliationError{code: FailureOperationIDMismatch, field: "operationId"}
		}
		if decoded.Token != instruction.TokenAddress {
			return VaultPayoutEvent{}, reconciliationError{code: FailureTokenMismatch, field: "tokenAddress"}
		}
		if decoded.Recipient != instruction.RecipientAddress {
			return VaultPayoutEvent{}, reconciliationError{code: FailureRecipientMismatch, field: "recipientAddress"}
		}
		if decoded.Amount.Cmp(instruction.AmountRaw) != 0 {
			return VaultPayoutEvent{}, reconciliationError{code: FailureAmountMismatch, field: "amountRaw"}
		}
		if instruction.OperatorAddress != (common.Address{}) && decoded.Operator != instruction.OperatorAddress {
			return VaultPayoutEvent{}, reconciliationError{code: "OPERATOR_MISMATCH", field: "operatorAddress"}
		}
		matches = append(matches, decoded)
	}
	if len(matches) == 0 {
		return VaultPayoutEvent{}, reconciliationError{code: FailureVaultEventMissing, field: "vaultEvent"}
	}
	if len(matches) > 1 {
		return VaultPayoutEvent{}, reconciliationError{code: FailureVaultEventDuplicate, field: "vaultEvent"}
	}
	return matches[0], nil
}

func findMatchingERC20Transfer(instruction SettlementInstruction, logs []types.Log) (ERC20TransferEvent, error) {
	var matches []ERC20TransferEvent
	for _, item := range logs {
		decoded, ok, err := DecodeERC20TransferLog(item)
		if err != nil {
			return ERC20TransferEvent{}, err
		}
		if !ok {
			continue
		}
		if decoded.Token != instruction.TokenAddress {
			continue
		}
		if decoded.From != instruction.VaultAddress || decoded.To != instruction.RecipientAddress || decoded.Value.Cmp(instruction.AmountRaw) != 0 {
			return ERC20TransferEvent{}, reconciliationError{code: FailureTransferEventMissing, field: "transferEvent"}
		}
		matches = append(matches, decoded)
	}
	if len(matches) == 0 {
		return ERC20TransferEvent{}, reconciliationError{code: FailureTransferEventMissing, field: "transferEvent"}
	}
	if len(matches) > 1 {
		return ERC20TransferEvent{}, reconciliationError{code: FailureTransferEventDuplicate, field: "transferEvent"}
	}
	return matches[0], nil
}

type reconciliationError struct {
	code  string
	field string
}

func (e reconciliationError) Error() string {
	return strings.TrimSpace(fmt.Sprintf("%s %s", e.code, e.field))
}

func classifyReconciliationError(err error) (string, string) {
	var recErr reconciliationError
	if errors.As(err, &recErr) {
		return recErr.code, recErr.field
	}
	return "LOG_DECODE_FAILED", "logs"
}

func (r ReconciliationResult) fail(code, field string) ReconciliationResult {
	r.Status = ReconciliationMismatch
	r.FailureCode = code
	r.FailureField = field
	return r
}
