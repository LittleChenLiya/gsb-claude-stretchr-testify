package assert

import (
	"context"
	"sync"
	"testing"
	"time"
)

// fakeClock is a deterministic Clock implementation for EventuallyStable
// tests. Time only moves when Advance is called.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []*fakeClockWaiter
}

type fakeClockWaiter struct {
	deadline time.Time
	ch       chan time.Time
}

func newFakeClock(start time.Time) *fakeClock {
	return &fakeClock{now: start}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.now
		return ch
	}
	c.waiters = append(c.waiters, &fakeClockWaiter{deadline: c.now.Add(d), ch: ch})
	return ch
}

// Advance moves the fake clock forward and fires every waiter whose deadline
// has been reached.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	pending := c.waiters[:0]
	for _, w := range c.waiters {
		if !w.deadline.After(c.now) {
			w.ch <- c.now
		} else {
			pending = append(pending, w)
		}
	}
	c.waiters = pending
}

func (c *fakeClock) hasWaiter() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.waiters) > 0
}

// waitForFakeTimer blocks until the code under test registers a wait on the
// fake clock, so the test can advance time deterministically.
func waitForFakeTimer(t *testing.T, c *fakeClock) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !c.hasWaiter() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for EventuallyStable to wait on the fake clock")
		}
		time.Sleep(time.Millisecond)
	}
}

func waitForEventuallyStableResult(t *testing.T, result <-chan bool) bool {
	t.Helper()
	select {
	case ok := <-result:
		return ok
	case <-time.After(10 * time.Second):
		t.Fatal("EventuallyStable did not return in time")
		return false
	}
}

var fakeClockStart = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func TestEventuallyStable_ResetsStabilityWindowOnFailure(t *testing.T) {
	t.Parallel()

	clock := newFakeClock(fakeClockStart)

	// true, true, false, true, true, true: the false in the middle resets
	// the stability window, so the first two successes must not count
	// towards the final stable window.
	script := []bool{true, true, false, true, true, true}
	var mu sync.Mutex
	calls := 0
	condition := func() bool {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls > len(script) {
			return true
		}
		return script[calls-1]
	}

	mockT := new(mockTestingT)
	result := make(chan bool, 1)
	go func() {
		result <- EventuallyStable(mockT, context.Background(), condition, 100*time.Second, 20*time.Second, 10*time.Second, clock)
	}()

	// Checks happen at t=0s,10s,20s,30s,40s,50s. Without the reset the
	// assertion would already succeed at t=20s on the third check; the
	// failing check at t=20s resets the window, so the stable window only
	// completes at t=50s on the sixth check.
	for i := 0; i < 5; i++ {
		waitForFakeTimer(t, clock)
		clock.Advance(10 * time.Second)
	}

	True(t, waitForEventuallyStableResult(t, result))
	mu.Lock()
	defer mu.Unlock()
	Equal(t, len(script), calls, "condition must be checked once per tick until the stable window completes")
	Equal(t, fakeClockStart.Add(50*time.Second), clock.Now(), "total time must be measured by the injected clock")
	Empty(t, mockT.errorFmt)
}

func TestEventuallyStable_ConditionRuntimeCrossesDeadline(t *testing.T) {
	t.Parallel()

	clock := newFakeClock(fakeClockStart)

	calls := 0
	condition := func() bool {
		calls++
		// Simulate a slow condition: it consumes 25s of (fake) time,
		// crossing the waitFor deadline while running.
		clock.Advance(25 * time.Second)
		return true
	}

	mockT := new(mockTestingT)
	result := make(chan bool, 1)
	go func() {
		result <- EventuallyStable(mockT, context.Background(), condition, 30*time.Second, 10*time.Second, 10*time.Second, clock)
	}()

	// The first check runs at t=0s and finishes at t=25s. Only 5s remain
	// before the deadline, so the next wait is capped at 5s.
	waitForFakeTimer(t, clock)
	clock.Advance(5 * time.Second)

	False(t, waitForEventuallyStableResult(t, result))
	Equal(t, 1, calls, "no new check may be scheduled once the deadline has passed")
	msg := mockT.errorString()
	t.Logf("timeout reason and stable windows:\n%s", msg)
	Contains(t, msg, "did not stay satisfied")
	Contains(t, msg, "stable windows")
}

func TestEventuallyStable_PreCanceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	calls := 0
	condition := func() bool {
		calls++
		return true
	}

	mockT := new(mockTestingT)
	False(t, EventuallyStable(mockT, ctx, condition, time.Second, 100*time.Millisecond, 10*time.Millisecond, nil))
	Equal(t, 0, calls, "a canceled context must stop any check from being scheduled")
	Contains(t, mockT.errorString(), "context canceled")
}

func TestEventuallyStable_ContextCancelStopsScheduling(t *testing.T) {
	t.Parallel()

	clock := newFakeClock(fakeClockStart)
	ctx, cancel := context.WithCancel(context.Background())

	calls := 0
	condition := func() bool {
		calls++
		return true
	}

	mockT := new(mockTestingT)
	result := make(chan bool, 1)
	go func() {
		result <- EventuallyStable(mockT, ctx, condition, time.Hour, time.Hour, 10*time.Second, clock)
	}()

	waitForFakeTimer(t, clock)
	cancel()

	False(t, waitForEventuallyStableResult(t, result))
	Equal(t, 1, calls, "no new check may be scheduled after cancellation")
	Contains(t, mockT.errorString(), "context canceled")
}

func TestEventuallyStable_PanicResetsWindowAndRecovers(t *testing.T) {
	t.Parallel()

	clock := newFakeClock(fakeClockStart)

	var mu sync.Mutex
	calls := 0
	condition := func() bool {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 2 {
			panic("boom")
		}
		return true
	}

	mockT := new(mockTestingT)
	result := make(chan bool, 1)
	go func() {
		result <- EventuallyStable(mockT, context.Background(), condition, 100*time.Second, 20*time.Second, 10*time.Second, clock)
	}()

	// t=0s: true (window opens), t=10s: panic (window resets), t=20s: true
	// (window reopens), t=30s: true, t=40s: true -> stable for 20s.
	for i := 0; i < 4; i++ {
		waitForFakeTimer(t, clock)
		clock.Advance(10 * time.Second)
	}

	True(t, waitForEventuallyStableResult(t, result))
	mu.Lock()
	defer mu.Unlock()
	Equal(t, 5, calls)
	Equal(t, fakeClockStart.Add(40*time.Second), clock.Now())
}

func TestEventuallyStable_PanicDiagnosticsOnTimeout(t *testing.T) {
	t.Parallel()

	clock := newFakeClock(fakeClockStart)

	calls := 0
	condition := func() bool {
		calls++
		panic("boom")
	}

	mockT := new(mockTestingT)
	result := make(chan bool, 1)
	go func() {
		result <- EventuallyStable(mockT, context.Background(), condition, 30*time.Second, 5*time.Second, 10*time.Second, clock)
	}()

	// Checks at t=0s,10s,20s all panic; the deadline is hit at t=30s.
	for i := 0; i < 3; i++ {
		waitForFakeTimer(t, clock)
		clock.Advance(10 * time.Second)
	}

	False(t, waitForEventuallyStableResult(t, result))
	Equal(t, 3, calls)
	msg := mockT.errorString()
	t.Logf("timeout reason and stable windows:\n%s", msg)
	Contains(t, msg, "panicked")
	Contains(t, msg, "boom")
	Contains(t, msg, "check #3")
}

func TestEventuallyStable_InvalidParameters(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name      string
		waitFor   time.Duration
		stableFor time.Duration
		tick      time.Duration
	}{
		{name: "zero tick", waitFor: time.Second, stableFor: 100 * time.Millisecond, tick: 0},
		{name: "negative tick", waitFor: time.Second, stableFor: 100 * time.Millisecond, tick: -time.Millisecond},
		{name: "zero waitFor", waitFor: 0, stableFor: 0, tick: 10 * time.Millisecond},
		{name: "negative waitFor", waitFor: -time.Second, stableFor: 0, tick: 10 * time.Millisecond},
		{name: "negative stableFor", waitFor: time.Second, stableFor: -time.Millisecond, tick: 10 * time.Millisecond},
		{name: "stableFor exceeds waitFor", waitFor: time.Second, stableFor: 2 * time.Second, tick: 10 * time.Millisecond},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			calls := 0
			condition := func() bool {
				calls++
				return true
			}

			mockT := new(mockTestingT)
			False(t, EventuallyStable(mockT, context.Background(), condition, tt.waitFor, tt.stableFor, tt.tick, nil))
			Equal(t, 0, calls, "invalid parameters must fail before running the condition")
			Contains(t, mockT.errorString(), "EventuallyStable")
		})
	}
}

func TestEventuallyStable_TimeoutDiagnostics(t *testing.T) {
	t.Parallel()

	clock := newFakeClock(fakeClockStart)

	// true, true, false, true, false, false: two short stable windows (20s
	// and 10s) are recorded before the condition stays false until the
	// deadline.
	script := []bool{true, true, false, true, false, false}
	var mu sync.Mutex
	calls := 0
	condition := func() bool {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls > len(script) {
			return false
		}
		return script[calls-1]
	}

	mockT := new(mockTestingT)
	result := make(chan bool, 1)
	go func() {
		result <- EventuallyStable(mockT, context.Background(), condition, 60*time.Second, 25*time.Second, 10*time.Second, clock)
	}()

	for i := 0; i < 6; i++ {
		waitForFakeTimer(t, clock)
		clock.Advance(10 * time.Second)
	}

	False(t, waitForEventuallyStableResult(t, result))
	mu.Lock()
	defer mu.Unlock()
	Equal(t, 6, calls)
	msg := mockT.errorString()
	t.Logf("timeout reason and stable windows:\n%s", msg)
	Contains(t, msg, "did not stay satisfied")
	Contains(t, msg, "stable windows: [20s, 10s]")
	Contains(t, msg, "last failure: check #6 returned false")
}

func TestEventuallyStable_RealClockQuickSuccess(t *testing.T) {
	t.Parallel()

	// A nil clock falls back to the real clock; a condition that is
	// immediately stable succeeds without waiting.
	True(t, EventuallyStable(t, context.Background(), func() bool { return true }, time.Second, 0, 10*time.Millisecond, nil))
}
