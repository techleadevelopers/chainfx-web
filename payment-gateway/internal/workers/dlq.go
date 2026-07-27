package workers

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"payment-gateway/internal/database"
)

// DLQEntry represents a failed event that could not be processed after all retries.
type DLQEntry struct {
	OriginalEvent Event
	FailedAt      time.Time
	Reason        string
	Attempts      int
}

// DeadLetterQueue stores events that exhausted all retry attempts.
// In production, drain this to a DB table or external queue for manual review.
type DeadLetterQueue struct {
	mu      sync.Mutex
	entries []DLQEntry
	maxSize int
	dbSink  func(DLQEntry) // optional async DB writer
}

// NewDLQ creates an in-memory DLQ. maxSize=0 means unlimited (bounded by RAM).
func NewDLQ(maxSize int, dbSink func(DLQEntry)) *DeadLetterQueue {
	if maxSize <= 0 {
		maxSize = 10_000
	}
	return &DeadLetterQueue{maxSize: maxSize, dbSink: dbSink}
}

func NewPersistentDLQ(db *database.DB, maxSize int) *DeadLetterQueue {
	if db == nil {
		return NewDLQ(maxSize, nil)
	}
	return NewDLQ(maxSize, func(entry DLQEntry) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := db.SaveWorkerDLQ(ctx,
			entry.OriginalEvent.Type,
			entry.OriginalEvent.OrderID,
			entry.Attempts,
			entry.Reason,
			entry.OriginalEvent.Payload,
			entry.FailedAt,
		); err != nil {
			slog.Error("DLQ: failed to persist event", "type", entry.OriginalEvent.Type, "order_id", entry.OriginalEvent.OrderID, "error", err)
		}
	})
}

// Push adds an entry to the DLQ and logs a warning.
func (q *DeadLetterQueue) Push(event Event, attempts int, reason string) {
	entry := DLQEntry{
		OriginalEvent: event,
		FailedAt:      time.Now(),
		Reason:        reason,
		Attempts:      attempts,
	}
	slog.Error("DLQ: event dead-lettered",
		"type", event.Type,
		"order_id", event.OrderID,
		"attempts", attempts,
		"reason", reason,
	)
	q.mu.Lock()
	if len(q.entries) >= q.maxSize {
		// Drop oldest to make room
		q.entries = q.entries[1:]
	}
	q.entries = append(q.entries, entry)
	q.mu.Unlock()

	if q.dbSink != nil {
		go q.dbSink(entry)
	}
}

// Drain returns all queued entries and clears the queue.
func (q *DeadLetterQueue) Drain() []DLQEntry {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]DLQEntry, len(q.entries))
	copy(out, q.entries)
	q.entries = q.entries[:0]
	return out
}

// Len returns the current DLQ depth.
func (q *DeadLetterQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.entries)
}

// StartPeriodicLog periodically logs DLQ depth so ops teams notice.
func (q *DeadLetterQueue) StartPeriodicLog(ctx context.Context, interval time.Duration) {
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if n := q.Len(); n > 0 {
					slog.Warn("DLQ has unprocessed events", "depth", n)
				}
			}
		}
	}()
}
