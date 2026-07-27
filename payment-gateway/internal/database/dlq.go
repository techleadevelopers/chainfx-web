package database

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// SaveWorkerDLQ persists a dead-lettered worker event for manual reconciliation.
func (db *DB) SaveWorkerDLQ(ctx context.Context, eventType, orderID string, attempts int, reason string, payload any, failedAt time.Time) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		raw = []byte(`{"marshal_error":true}`)
	}
	const q = `
		INSERT INTO worker_dlq (event_type, order_id, attempts, reason, payload, failed_at)
		VALUES ($1,$2,$3,$4,$5,$6)`
	if _, err := db.SQL.ExecContext(ctx, q, eventType, nullableString(orderID), attempts, reason, raw, failedAt); err != nil {
		return fmt.Errorf("SaveWorkerDLQ: %w", err)
	}
	return nil
}
