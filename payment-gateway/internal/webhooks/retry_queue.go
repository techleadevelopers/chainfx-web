package webhooks

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"payment-gateway/internal/database"
)

// retryJob is one pending delivery attempt sequence for a subscription.
type retryJob struct {
	sub     *database.WebhookSubscription
	event   string
	payload map[string]any
}

// backoffSchedule defines the delay before each retry attempt (index 0 is
// the delay before attempt #2, since attempt #1 fires immediately).
var backoffSchedule = []time.Duration{
	5 * time.Second,
	30 * time.Second,
	2 * time.Minute,
	10 * time.Minute,
	30 * time.Minute,
}

// RetryQueue processes webhook deliveries asynchronously with exponential
// backoff, so a slow or unreachable receiver never blocks the event bus
// consumer or piles up goroutines unbounded.
type RetryQueue struct {
	dispatcher *Dispatcher
	jobs       chan retryJob
	workers    int
	wg         sync.WaitGroup
	enqueued   atomic.Uint64
	dropped    atomic.Uint64
}

// NewRetryQueue creates a queue with the given number of worker goroutines
// and a buffered channel so bursts of events don't get dropped.
func NewRetryQueue(dispatcher *Dispatcher, workers int) *RetryQueue {
	if workers <= 0 {
		workers = 4
	}
	return &RetryQueue{
		dispatcher: dispatcher,
		jobs:       make(chan retryJob, 1000),
		workers:    workers,
	}
}

// Start launches the worker pool. Call Enqueue to submit deliveries; Stop to
// drain and shut down.
func (q *RetryQueue) Start(ctx context.Context) {
	for i := 0; i < q.workers; i++ {
		q.wg.Add(1)
		go q.worker(ctx)
	}
	slog.Info("Webhooks: fila de retry iniciada", "workers", q.workers)
}

// Stop waits (bounded by ctx) for in-flight jobs to finish and closes the
// queue so no further jobs are accepted.
func (q *RetryQueue) Stop() {
	close(q.jobs)
	q.wg.Wait()
}

// Enqueue submits a delivery job. Non-blocking: if the queue is saturated
// the job is dropped and logged, rather than backing up the event bus.
func (q *RetryQueue) Enqueue(sub *database.WebhookSubscription, event string, payload map[string]any) {
	select {
	case q.jobs <- retryJob{sub: sub, event: event, payload: payload}:
		q.enqueued.Add(1)
	default:
		q.dropped.Add(1)
		slog.Warn("Webhooks: fila de retry cheia, entrega descartada", "subscriptionId", sub.ID, "event", event)
	}
}

func (q *RetryQueue) Metrics() map[string]any {
	if q == nil {
		return map[string]any{"workers": 0, "queued": 0, "capacity": 0}
	}
	return map[string]any{
		"workers":  q.workers,
		"queued":   len(q.jobs),
		"capacity": cap(q.jobs),
		"enqueued": q.enqueued.Load(),
		"dropped":  q.dropped.Load(),
	}
}

func (q *RetryQueue) worker(ctx context.Context) {
	defer q.wg.Done()
	for job := range q.jobs {
		q.process(ctx, job)
	}
}

// process attempts delivery with exponential backoff, persisting every
// attempt via database.RecordWebhookDelivery so internal/webhooks/logs.go
// and the dashboard can report on it.
func (q *RetryQueue) process(ctx context.Context, job retryJob) {
	maxAttempts := q.dispatcher.maxRetries
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		result := q.dispatcher.deliverOnce(ctx, job.sub, job.event, job.payload)
		_ = q.dispatcher.db.RecordWebhookDelivery(ctx, job.sub.ID, job.event, job.payload, result.StatusCode, result.OK, result.Error, attempt)
		if result.OK {
			return
		}
		if attempt == maxAttempts {
			slog.Warn("Webhooks: entrega falhou apos todas as tentativas",
				"subscriptionId", job.sub.ID, "event", job.event, "attempts", attempt, "error", result.Error)
			return
		}
		delay := backoffSchedule[len(backoffSchedule)-1]
		if attempt-1 < len(backoffSchedule) {
			delay = backoffSchedule[attempt-1]
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}
