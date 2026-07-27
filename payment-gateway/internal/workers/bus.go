package workers

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Event is the internal worker bus message.
type Event struct {
	Type    string
	OrderID string
	Payload map[string]interface{}
}

// EventBus distributes events to worker subscribers.
type EventBus struct {
	mu          sync.RWMutex
	subscribers map[string][]chan Event
	closed      bool
	published   atomic.Uint64
	dropped     atomic.Uint64
}

type EventBusMetrics struct {
	Subscribers int    `json:"subscribers"`
	QueueSize   int    `json:"queueSize"`
	Published   uint64 `json:"published"`
	Dropped     uint64 `json:"dropped"`
	Closed      bool   `json:"closed"`
}

func NewEventBus() *EventBus {
	return &EventBus{
		subscribers: make(map[string][]chan Event),
	}
}

const (
	defaultSubscriberBuffer = 100
	criticalEventWait       = 2 * time.Second
)

// Subscribe creates a buffered channel for the given event type.
func (b *EventBus) Subscribe(eventType string) chan Event {
	b.mu.Lock()
	defer b.mu.Unlock()

	ch := make(chan Event, defaultSubscriberBuffer)
	if b.closed {
		close(ch)
		return ch
	}
	b.subscribers[eventType] = append(b.subscribers[eventType], ch)
	return ch
}

// Unsubscribe removes a subscriber channel and closes it.
func (b *EventBus) Unsubscribe(eventType string, ch chan Event) {
	b.mu.Lock()
	defer b.mu.Unlock()

	subs := b.subscribers[eventType]
	for i, sub := range subs {
		if sub == ch {
			// Remove channel from slice
			b.subscribers[eventType] = append(subs[:i], subs[i+1:]...)
			close(ch)
			break
		}
	}
	// Clean up empty event type
	if len(b.subscribers[eventType]) == 0 {
		delete(b.subscribers, eventType)
	}
}

// Publish sends an event to all subscribers of its type.
// Money-moving events wait briefly for backpressure instead of dropping
// immediately; low-value telemetry remains non-blocking.
func (b *EventBus) Publish(event Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if b.closed {
		b.dropped.Add(1)
		return
	}
	b.published.Add(1)

	subs := b.subscribers[event.Type]
	// Early return if no subscribers
	if len(subs) == 0 {
		return
	}

	for _, ch := range subs {
		if b.publishOne(ch, event) {
			continue
		}
		b.dropped.Add(1)
		slog.Error("worker bus dropped event", "type", event.Type, "order_id", event.OrderID, "critical", isCriticalMoneyEvent(event.Type))
	}
}

func (b *EventBus) publishOne(ch chan Event, event Event) bool {
	select {
	case ch <- event:
		return true
	default:
	}
	if !isCriticalMoneyEvent(event.Type) {
		return false
	}
	timer := time.NewTimer(criticalEventWait)
	defer timer.Stop()
	select {
	case ch <- event:
		return true
	case <-timer.C:
		return false
	}
}

func isCriticalMoneyEvent(eventType string) bool {
	switch eventType {
	case "buy.paid", "buy.sent", "buy.failed",
		"payout.requested", "payout.settled", "payout.manual_required",
		"onchain.detected", "m2m.deposit.confirmed", "m2m.settlement.done", "m2m.settlement.failed",
		"nfc.capture.completed", "nfc.authorization.reversed", "nfc.authorization.expired",
		"nfc.settlement.manual_required", "nfc.settlement.submitted", "nfc.settlement.submission_unknown", "nfc.settlement.confirmed", "nfc.settlement.failed",
		"mobile.payout.requested", "sweep.sent":
		return true
	default:
		return false
	}
}

// Metrics returns current bus statistics.
func (b *EventBus) Metrics() EventBusMetrics {
	b.mu.RLock()
	defer b.mu.RUnlock()

	var subscribers int
	var queueSize int
	for _, subs := range b.subscribers {
		subscribers += len(subs)
		for _, ch := range subs {
			queueSize += len(ch)
		}
	}

	return EventBusMetrics{
		Subscribers: subscribers,
		QueueSize:   queueSize,
		Published:   b.published.Load(),
		Dropped:     b.dropped.Load(),
		Closed:      b.closed,
	}
}

// Close shuts down the bus and closes all subscriber channels.
func (b *EventBus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return
	}
	b.closed = true

	// Close all subscriber channels
	for eventType, subs := range b.subscribers {
		for _, ch := range subs {
			close(ch)
		}
		delete(b.subscribers, eventType)
	}
}

// Shutdown is an alias for Close() for clarity.
func (b *EventBus) Shutdown() {
	b.Close()
}
