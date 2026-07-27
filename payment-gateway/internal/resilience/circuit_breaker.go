// Package resilience provides fault-tolerance primitives: circuit breaker and retry.
package resilience

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

type IgnoreError func(error) bool

// State represents the circuit breaker state machine.
type State int

const (
	StateClosed   State = iota // Normal — requests flow through
	StateOpen                  // Tripped — requests fail fast
	StateHalfOpen              // Probing — exactly one request allowed at a time
)

func (s State) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half-open"
	}
	return "unknown"
}

// ErrCircuitOpen is returned when a request is blocked by an open circuit.
var ErrCircuitOpen = errors.New("circuit breaker open")

// CBConfig controls circuit breaker thresholds.
type CBConfig struct {
	MaxFailures     int           // consecutive failures before tripping
	ResetTimeout    time.Duration // how long to stay open before probing
	HalfOpenSuccess int           // successes needed to close from half-open
	IgnoreError IgnoreError
}

// CircuitBreaker implements the circuit breaker pattern with mutex-based state.
// Half-open state allows exactly ONE concurrent probe — subsequent requests are
// rejected until that probe resolves, preventing thundering-herd on recovery.
type CircuitBreaker struct {
	name string
	mu   sync.Mutex

	state       State
	failures    int
	successes   int
	lastTrip    time.Time
	probeInFlight atomic.Bool // guard: only one probe at a time in half-open

	maxFailures     int
	resetTimeout    time.Duration
	halfOpenSuccess int
	ignore IgnoreError
}

// NewCircuitBreaker creates a new, closed circuit breaker.
func NewCircuitBreaker(name string, cfg CBConfig) *CircuitBreaker {
	if cfg.MaxFailures <= 0 {
		cfg.MaxFailures = 5
	}
	if cfg.ResetTimeout <= 0 {
		cfg.ResetTimeout = 30 * time.Second
	}
	if cfg.HalfOpenSuccess <= 0 {
		cfg.HalfOpenSuccess = 2
	}
	return &CircuitBreaker{
		name:            name,
		state:           StateClosed,
		maxFailures:     cfg.MaxFailures,
		resetTimeout:    cfg.ResetTimeout,
		halfOpenSuccess: cfg.HalfOpenSuccess,
	}
}

// Execute runs fn through the circuit breaker.
// Returns ErrCircuitOpen immediately if the circuit is open or half-open
// and another probe is already in flight.
func (cb *CircuitBreaker) Execute(fn func() error) error {
	if err := cb.allow(); err != nil {
		return err
	}
	err := fn()
	cb.record(err)
	return err
}

// GetState returns the current state (safe for external inspection).
func (cb *CircuitBreaker) GetState() State {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.maybeTransitionToHalfOpen()
	return cb.state
}

// ─── internal ─────────────────────────────────────────────────────────────────

func (cb *CircuitBreaker) allow() error {
	cb.mu.Lock()
	cb.maybeTransitionToHalfOpen()
	state := cb.state
	cb.mu.Unlock()

	switch state {
	case StateClosed:
		return nil
	case StateOpen:
		return fmt.Errorf("%w: %s", ErrCircuitOpen, cb.name)
	case StateHalfOpen:
		// Exactly one probe allowed — use atomic CAS to prevent concurrent probes
		if cb.probeInFlight.CompareAndSwap(false, true) {
			return nil // this goroutine is the designated probe
		}
		return fmt.Errorf("%w: %s (half-open, probe in flight)", ErrCircuitOpen, cb.name)
	}
	return nil
}

func (cb *CircuitBreaker) record(err error) {

	if err != nil && cb.ignore != nil && cb.ignore(err) {
		return
	}

	cb.mu.Lock()
	defer cb.mu.Unlock()

	if err == nil {
		cb.onSuccess()
	} else {
		cb.onFailure()
	}
}

func (cb *CircuitBreaker) onSuccess() {
	cb.failures = 0
	if cb.state == StateHalfOpen {
		cb.probeInFlight.Store(false)
		cb.successes++
		if cb.successes >= cb.halfOpenSuccess {
			slog.Info("CircuitBreaker: closed", "name", cb.name)
			cb.state = StateClosed
			cb.successes = 0
		}
	}
}

func (cb *CircuitBreaker) onFailure() {
	if cb.state == StateHalfOpen {
		cb.probeInFlight.Store(false)
		// Probe failed — back to open, reset timer
		slog.Warn("CircuitBreaker: half-open probe failed, re-opening", "name", cb.name)
		cb.state = StateOpen
		cb.lastTrip = time.Now()
		cb.successes = 0
		return
	}
	cb.failures++
	if cb.failures >= cb.maxFailures && cb.state != StateOpen {
		slog.Warn("CircuitBreaker: tripped", "name", cb.name, "failures", cb.failures)
		cb.state = StateOpen
		cb.lastTrip = time.Now()
	}
}

// maybeTransitionToHalfOpen must be called with cb.mu held.
func (cb *CircuitBreaker) maybeTransitionToHalfOpen() {
	if cb.state == StateOpen && time.Since(cb.lastTrip) >= cb.resetTimeout {
		slog.Info("CircuitBreaker: probing (half-open)", "name", cb.name)
		cb.state = StateHalfOpen
		cb.successes = 0
		cb.probeInFlight.Store(false)
	}
}
