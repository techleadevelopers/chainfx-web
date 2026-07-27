package database

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

type SettlementReceiptRecord struct {
	ID                    string
	OperationID           []byte
	TxHash                string
	ChainID               uint64
	BlockNumber           uint64
	BlockHash             string
	TransactionIndex      uint
	ReceiptStatus         uint64
	Confirmations         uint64
	VaultEventLogIndex    *uint
	TransferEventLogIndex *uint
	ReceiptVerified       bool
	VaultEventVerified    bool
	TransferEventVerified bool
	ConfirmationsVerified bool
	ReconciliationStatus  string
	FailureCode           string
	FailureField          string
	FailureDetails        map[string]any
	FirstSeenAt           *time.Time
	ConfirmedAt           *time.Time
	ReconciledAt          *time.Time
}

type SettlementReconciliationEventInput struct {
	ID             string
	OperationID    []byte
	TxHash         string
	EventType      string
	PreviousStatus string
	NewStatus      string
	Expected       map[string]any
	Observed       map[string]any
	FailureCode    string
}

func (db *DB) UpsertSettlementReceipt(ctx context.Context, in SettlementReceiptRecord) error {
	if db == nil || db.SQL == nil {
		return nil
	}
	if strings.TrimSpace(in.ID) == "" {
		in.ID = NewID()
	}
	failureDetails, _ := json.Marshal(in.FailureDetails)
	_, err := db.SQL.ExecContext(ctx, `
		INSERT INTO settlement_receipts (
			id, operation_id, tx_hash, chain_id, block_number, block_hash, transaction_index,
			receipt_status, confirmations, vault_event_log_index, transfer_event_log_index,
			receipt_verified, vault_event_verified, transfer_event_verified, confirmations_verified,
			reconciliation_status, failure_code, failure_field, failure_details,
			first_seen_at, confirmed_at, reconciled_at, updated_at
		)
		VALUES (
			$1, $2, $3, $4, NULLIF($5, 0), NULLIF($6, ''), $7,
			$8, $9, $10, $11,
			$12, $13, $14, $15,
			$16, NULLIF($17, ''), NULLIF($18, ''), $19,
			$20, $21, $22, NOW()
		)
		ON CONFLICT (operation_id) DO UPDATE SET
			tx_hash = EXCLUDED.tx_hash,
			chain_id = EXCLUDED.chain_id,
			block_number = EXCLUDED.block_number,
			block_hash = EXCLUDED.block_hash,
			transaction_index = EXCLUDED.transaction_index,
			receipt_status = EXCLUDED.receipt_status,
			confirmations = EXCLUDED.confirmations,
			vault_event_log_index = EXCLUDED.vault_event_log_index,
			transfer_event_log_index = EXCLUDED.transfer_event_log_index,
			receipt_verified = EXCLUDED.receipt_verified,
			vault_event_verified = EXCLUDED.vault_event_verified,
			transfer_event_verified = EXCLUDED.transfer_event_verified,
			confirmations_verified = EXCLUDED.confirmations_verified,
			reconciliation_status = EXCLUDED.reconciliation_status,
			failure_code = EXCLUDED.failure_code,
			failure_field = EXCLUDED.failure_field,
			failure_details = EXCLUDED.failure_details,
			first_seen_at = COALESCE(settlement_receipts.first_seen_at, EXCLUDED.first_seen_at),
			confirmed_at = COALESCE(EXCLUDED.confirmed_at, settlement_receipts.confirmed_at),
			reconciled_at = COALESCE(EXCLUDED.reconciled_at, settlement_receipts.reconciled_at),
			updated_at = NOW()
	`, in.ID, in.OperationID, in.TxHash, in.ChainID, in.BlockNumber, in.BlockHash, in.TransactionIndex,
		in.ReceiptStatus, in.Confirmations, optionalUint(in.VaultEventLogIndex), optionalUint(in.TransferEventLogIndex),
		in.ReceiptVerified, in.VaultEventVerified, in.TransferEventVerified, in.ConfirmationsVerified,
		in.ReconciliationStatus, in.FailureCode, in.FailureField, failureDetails,
		in.FirstSeenAt, in.ConfirmedAt, in.ReconciledAt)
	return err
}

func (db *DB) AppendSettlementReconciliationEvent(ctx context.Context, in SettlementReconciliationEventInput) error {
	if db == nil || db.SQL == nil {
		return nil
	}
	if strings.TrimSpace(in.ID) == "" {
		in.ID = NewID()
	}
	expected, _ := json.Marshal(in.Expected)
	observed, _ := json.Marshal(in.Observed)
	_, err := db.SQL.ExecContext(ctx, `
		INSERT INTO settlement_reconciliation_events (
			id, operation_id, tx_hash, event_type, previous_status, new_status,
			expected, observed, failure_code
		)
		VALUES ($1, $2, NULLIF($3, ''), $4, NULLIF($5, ''), NULLIF($6, ''), $7, $8, NULLIF($9, ''))
	`, in.ID, in.OperationID, in.TxHash, in.EventType, in.PreviousStatus, in.NewStatus, expected, observed, in.FailureCode)
	return err
}

func OperationIDBytes(hexValue string) []byte {
	trimmed := strings.TrimPrefix(strings.TrimSpace(hexValue), "0x")
	out, err := hex.DecodeString(trimmed)
	if err != nil {
		return nil
	}
	return out
}

func optionalUint(value *uint) any {
	if value == nil {
		return nil
	}
	return *value
}
