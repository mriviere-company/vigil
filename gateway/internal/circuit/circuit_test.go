package circuit

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock is a manually-advanced clock so every state-transition test is
// deterministic with zero time.Sleep.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

var errFail = errors.New("downstream failed")

func fail() error    { return errFail }
func succeed() error { return nil }

func testBreaker(clk Clock, threshold, probes int, timeout time.Duration) *Breaker {
	return New(Config{
		FailureThreshold:  threshold,
		OpenTimeout:       timeout,
		HalfOpenMaxProbes: probes,
		Clock:             clk,
	})
}

func TestStartsClosed(t *testing.T) {
	b := testBreaker(newFakeClock(), 3, 1, time.Minute)
	if b.State() != StateClosed {
		t.Fatalf("new breaker state = %v, want closed", b.State())
	}
}

func TestClosedResetsFailuresOnSuccess(t *testing.T) {
	b := testBreaker(newFakeClock(), 3, 1, time.Minute)
	_ = b.Execute(fail)
	_ = b.Execute(fail) // 2 consecutive, below threshold 3
	_ = b.Execute(succeed)
	// Counter reset; two more failures must not open (would need 3 in a row).
	_ = b.Execute(fail)
	_ = b.Execute(fail)
	if b.State() != StateClosed {
		t.Fatalf("state = %v, want closed (success should have reset the counter)", b.State())
	}
}

func TestOpensAtFailureThreshold(t *testing.T) {
	b := testBreaker(newFakeClock(), 3, 1, time.Minute)
	if err := b.Execute(fail); !errors.Is(err, errFail) {
		t.Fatalf("1st failure err = %v", err)
	}
	_ = b.Execute(fail)
	if b.State() != StateClosed {
		t.Fatalf("state after 2 failures = %v, want closed", b.State())
	}
	_ = b.Execute(fail) // 3rd consecutive -> opens
	if b.State() != StateOpen {
		t.Fatalf("state after 3 failures = %v, want open", b.State())
	}
}

func TestOpenRejectsBeforeTimeout(t *testing.T) {
	clk := newFakeClock()
	b := testBreaker(clk, 1, 1, time.Minute)
	_ = b.Execute(fail) // opens (threshold 1)

	clk.Advance(30 * time.Second) // still within the open timeout
	called := false
	err := b.Execute(func() error { called = true; return nil })
	if !errors.Is(err, ErrOpen) {
		t.Fatalf("err = %v, want ErrOpen", err)
	}
	if called {
		t.Fatal("fn must not run while the circuit is open")
	}
}

func TestHalfOpenProbeAfterTimeoutSuccessCloses(t *testing.T) {
	clk := newFakeClock()
	b := testBreaker(clk, 1, 1, time.Minute)
	_ = b.Execute(fail) // opens

	clk.Advance(time.Minute) // reach the open timeout -> next call is a probe
	called := false
	err := b.Execute(func() error { called = true; return nil })
	if err != nil {
		t.Fatalf("probe err = %v, want nil", err)
	}
	if !called {
		t.Fatal("probe fn should have run in half-open")
	}
	if b.State() != StateClosed {
		t.Fatalf("state = %v, want closed after successful probe", b.State())
	}
}

func TestHalfOpenProbeFailureReopens(t *testing.T) {
	clk := newFakeClock()
	b := testBreaker(clk, 1, 1, time.Minute)
	_ = b.Execute(fail) // opens
	clk.Advance(time.Minute)

	err := b.Execute(fail) // probe fails
	if !errors.Is(err, errFail) {
		t.Fatalf("probe err = %v, want errFail", err)
	}
	if b.State() != StateOpen {
		t.Fatalf("state = %v, want open after failed probe", b.State())
	}
	// And the reopen restarts the timeout window: an immediate call is rejected.
	if err := b.Execute(succeed); !errors.Is(err, ErrOpen) {
		t.Fatalf("err = %v, want ErrOpen (timeout window should have restarted)", err)
	}
}

func TestHalfOpenCapsConcurrentProbes(t *testing.T) {
	clk := newFakeClock()
	b := testBreaker(clk, 1, 1, time.Minute) // max 1 concurrent probe
	_ = b.Execute(fail)                      // opens
	clk.Advance(time.Minute)

	started := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_ = b.Execute(func() error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started // the single probe is now in flight

	// A second concurrent call in half-open must be rejected, not run.
	ran := false
	err := b.Execute(func() error { ran = true; return nil })
	if !errors.Is(err, ErrOpen) {
		t.Fatalf("second probe err = %v, want ErrOpen", err)
	}
	if ran {
		t.Fatal("second probe fn must not run while a probe is in flight")
	}
	close(release)
}

func TestOnStateChangeRecordsTransitions(t *testing.T) {
	clk := newFakeClock()
	var transitions [][2]State
	var mu sync.Mutex
	b := New(Config{
		FailureThreshold:  1,
		OpenTimeout:       time.Minute,
		HalfOpenMaxProbes: 1,
		Clock:             clk,
		OnStateChange: func(from, to State) {
			mu.Lock()
			transitions = append(transitions, [2]State{from, to})
			mu.Unlock()
		},
	})
	_ = b.Execute(fail) // closed->open
	clk.Advance(time.Minute)
	_ = b.Execute(succeed) // open->half-open->closed

	mu.Lock()
	defer mu.Unlock()
	want := [][2]State{
		{StateClosed, StateOpen},
		{StateOpen, StateHalfOpen},
		{StateHalfOpen, StateClosed},
	}
	if len(transitions) != len(want) {
		t.Fatalf("transitions = %v, want %v", transitions, want)
	}
	for i := range want {
		if transitions[i] != want[i] {
			t.Fatalf("transition %d = %v, want %v", i, transitions[i], want[i])
		}
	}
}

func TestStateString(t *testing.T) {
	cases := map[State]string{StateClosed: "closed", StateOpen: "open", StateHalfOpen: "half_open"}
	for s, want := range cases {
		if s.String() != want {
			t.Errorf("State(%d).String() = %q, want %q", s, s.String(), want)
		}
	}
}

// TestConcurrentHammer drives the breaker from many goroutines with the race
// detector on. It asserts nothing about the (nondeterministic) final state —
// only that concurrent Execute/State access is race-free and never panics.
func TestConcurrentHammer(t *testing.T) {
	clk := newFakeClock()
	b := testBreaker(clk, 5, 3, time.Millisecond)

	var wg sync.WaitGroup
	var ops atomic.Int64
	for g := 0; g < 50; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				fn := succeed
				if (g+i)%3 == 0 {
					fn = fail
				}
				_ = b.Execute(fn)
				_ = b.State()
				if i%20 == 0 {
					clk.Advance(time.Millisecond)
				}
				ops.Add(1)
			}
		}(g)
	}
	wg.Wait()

	if got := b.State(); got != StateClosed && got != StateOpen && got != StateHalfOpen {
		t.Fatalf("invalid final state %v", got)
	}
	if ops.Load() != 50*200 {
		t.Fatalf("ops = %d, want %d", ops.Load(), 50*200)
	}
}
