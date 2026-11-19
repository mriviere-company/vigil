// Package circuit is a hand-written circuit breaker, one instance per
// downstream inference service.
//
// Why hand-written (not sony/gobreaker): the breaker is this project's central
// resilience component and its behavior must be exhaustively unit-tested,
// including every state transition, with a deterministic clock and no
// time.Sleep. Owning the ~120 lines keeps the state machine explicit, lets the
// transition tests inject a fake Clock, and avoids taking a dependency for
// something this small. sony/gobreaker is a fine library and would be the
// pragmatic choice in most codebases; here the implementation itself is a
// deliverable.
//
// State machine (consecutive-failures policy — simpler to reason about and
// test than a rolling ratio):
//
//	Closed  --N consecutive failures-->  Open
//	Open    --after OpenTimeout, on next call-->  HalfOpen (single probe allowed)
//	HalfOpen --probe succeeds-->  Closed
//	HalfOpen --probe fails-->     Open (timeout window restarts)
//
// The Open->HalfOpen transition is evaluated lazily on request arrival (using
// the injected Clock) rather than via a background timer: no goroutine to leak,
// nothing to stop on shutdown, and the tests advance a fake clock instead of
// sleeping. HalfOpenMaxProbes bounds how many probes may run concurrently while
// half-open; the rest are rejected as if still open, so a recovering downstream
// gets a trickle, not a flood. All state is guarded by a single mutex; the
// guarded downstream call runs outside the lock.
package circuit

import (
	"errors"
	"sync"
	"time"
)

// ErrOpen is returned by Execute when the breaker is open (or half-open with no
// free probe slot) and the call was not attempted.
var ErrOpen = errors.New("circuit breaker is open")

// State is the breaker's state.
type State int

const (
	StateClosed State = iota
	StateOpen
	StateHalfOpen
)

func (s State) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half_open"
	default:
		return "unknown"
	}
}

// Clock abstracts time so state-transition tests are deterministic. Production
// uses the real wall clock; tests inject a manually-advanced fake.
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// Config configures a Breaker.
type Config struct {
	// FailureThreshold is the number of consecutive failures in Closed that
	// trips the breaker to Open. Must be >= 1.
	FailureThreshold int
	// OpenTimeout is how long the breaker stays Open before allowing a probe.
	OpenTimeout time.Duration
	// HalfOpenMaxProbes bounds concurrent probes in HalfOpen. Must be >= 1.
	HalfOpenMaxProbes int
	// Clock defaults to the real wall clock when nil.
	Clock Clock
	// OnStateChange, if set, is called on every transition with (from, to). It
	// is invoked while the breaker's lock is held, so it must be cheap and must
	// not call back into the breaker.
	OnStateChange func(from, to State)
}

// Breaker is a thread-safe circuit breaker.
type Breaker struct {
	mu                  sync.Mutex
	state               State
	consecutiveFailures int
	openedAt            time.Time
	halfOpenProbes      int

	failureThreshold  int
	openTimeout       time.Duration
	halfOpenMaxProbes int
	clock             Clock
	onStateChange     func(from, to State)
}

// New builds a Breaker. Missing threshold/probe values default to 1.
func New(cfg Config) *Breaker {
	if cfg.FailureThreshold < 1 {
		cfg.FailureThreshold = 1
	}
	if cfg.HalfOpenMaxProbes < 1 {
		cfg.HalfOpenMaxProbes = 1
	}
	clock := cfg.Clock
	if clock == nil {
		clock = realClock{}
	}
	return &Breaker{
		state:             StateClosed,
		failureThreshold:  cfg.FailureThreshold,
		openTimeout:       cfg.OpenTimeout,
		halfOpenMaxProbes: cfg.HalfOpenMaxProbes,
		clock:             clock,
		onStateChange:     cfg.OnStateChange,
	}
}

// State returns the current state (evaluating a due Open->HalfOpen transition
// so /v1/health reflects reality without a request having driven it).
func (b *Breaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.maybeHalfOpen()
	return b.state
}

// Execute runs fn if the breaker permits it. A nil error from fn counts as a
// success; a non-nil error counts as a failure and may trip or reopen the
// breaker. When the breaker is open (no probe slot), fn is not run and ErrOpen
// is returned. fn runs outside the lock so downstream latency never blocks
// other callers' state checks.
func (b *Breaker) Execute(fn func() error) error {
	if err := b.beforeRequest(); err != nil {
		return err
	}
	err := fn()
	b.afterRequest(err == nil)
	return err
}

// beforeRequest admits or rejects a call, transitioning Open->HalfOpen if the
// timeout has elapsed and reserving a probe slot in HalfOpen.
func (b *Breaker) beforeRequest() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.maybeHalfOpen()

	switch b.state {
	case StateClosed:
		return nil
	case StateHalfOpen:
		if b.halfOpenProbes >= b.halfOpenMaxProbes {
			return ErrOpen
		}
		b.halfOpenProbes++
		return nil
	default: // StateOpen
		return ErrOpen
	}
}

// afterRequest records the outcome of an admitted call and drives transitions.
func (b *Breaker) afterRequest(success bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case StateClosed:
		if success {
			b.consecutiveFailures = 0
			return
		}
		b.consecutiveFailures++
		if b.consecutiveFailures >= b.failureThreshold {
			b.transition(StateOpen)
		}
	case StateHalfOpen:
		// This probe has finished; free its slot before transitioning (the
		// transition resets the counter, but be explicit).
		if b.halfOpenProbes > 0 {
			b.halfOpenProbes--
		}
		if success {
			b.transition(StateClosed) // recovered
		} else {
			b.transition(StateOpen) // still broken; restart the timeout window
		}
	case StateOpen:
		// A call completing while Open should not happen (Open rejects before
		// running fn). Ignore defensively.
	}
}

// maybeHalfOpen transitions Open->HalfOpen if the open timeout has elapsed.
// Caller must hold b.mu.
func (b *Breaker) maybeHalfOpen() {
	if b.state == StateOpen && b.clock.Now().Sub(b.openedAt) >= b.openTimeout {
		b.transition(StateHalfOpen)
	}
}

// transition changes state, resets the relevant counters, and fires the hook.
// Caller must hold b.mu.
func (b *Breaker) transition(to State) {
	if b.state == to {
		return
	}
	from := b.state
	b.state = to
	switch to {
	case StateClosed:
		b.consecutiveFailures = 0
		b.halfOpenProbes = 0
	case StateOpen:
		b.openedAt = b.clock.Now()
		b.consecutiveFailures = 0
		b.halfOpenProbes = 0
	case StateHalfOpen:
		b.halfOpenProbes = 0
		b.consecutiveFailures = 0
	}
	if b.onStateChange != nil {
		b.onStateChange(from, to)
	}
}
