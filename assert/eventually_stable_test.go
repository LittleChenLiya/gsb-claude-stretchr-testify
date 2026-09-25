package assert

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeClock is a deterministic Clock implementation for tests. Time only
// advances when Advance is called.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []fakeTimer
}

type fakeTimer struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Unix(0, 0)}
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
	at := c.now.Add(d)
	if !at.After(c.now) {
		ch <- c.now
		return ch
	}
	c.timers = append(c.timers, fakeTimer{at: at, ch: ch})
	return ch
}

// Advance moves the clock forward by d, firing any timers that expire in
// that window in chronological order.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	target := c.now.Add(d)
	for {
		idx := -1
		for i, tm := range c.timers {
			if !tm.at.After(target) && (idx == -1 || tm.at.Before(c.timers[idx].at)) {
				idx = i
			}
		}
		if idx == -1 {
			break
		}
		tm := c.timers[idx]
		c.timers = append(c.timers[:idx], c.timers[idx+1:]...)
		c.now = tm.at
		c.mu.Unlock()
		tm.ch <- tm.at
		c.mu.Lock()
	}
	c.now = target
	c.mu.Unlock()
}

// waitForTick blocks (in real time) until the clock has a pending timer
// expiring within tick, i.e. the assertion loop has finished the current
// check and is parked waiting for the next tick. The overall timeout timer
// (further in the future) is ignored.
func (c *fakeClock) waitForTick(t *testing.T, tick time.Duration) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		c.mu.Lock()
		now := c.now
		found := false
		for _, tm := range c.timers {
			if !tm.at.After(now.Add(tick)) {
				found = true
				break
			}
		}
		c.mu.Unlock()
		if found {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for EventuallyStable to schedule the next tick")
		}
		time.Sleep(time.Millisecond)
	}
}

// eventuallyStableResult collects the outcome of an EventuallyStable call
// running in a separate goroutine.
type eventuallyStableResult struct {
	ok  bool
	err string
}

func runEventuallyStable(mt *mockTestingT, ctx context.Context, condition func() bool, waitFor, stableFor, tick time.Duration, clock Clock) <-chan eventuallyStableResult {
	res := make(chan eventuallyStableResult, 1)
	go func() {
		ok := EventuallyStable(mt, ctx, condition, waitFor, stableFor, tick, clock)
		err := ""
		if mt.Failed() {
			err = mt.errorString()
		}
		res <- eventuallyStableResult{ok: ok, err: err}
	}()
	return res
}

func waitResult(t *testing.T, res <-chan eventuallyStableResult) eventuallyStableResult {
	t.Helper()
	select {
	case r := <-res:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("EventuallyStable did not finish in time")
		return eventuallyStableResult{}
	}
}

func TestEventuallyStableRequiresContinuousStability(t *testing.T) {
	t.Parallel()

	clock := newFakeClock()
	mt := new(mockTestingT)

	const tick = time.Second
	const stableFor = 2 * time.Second
	const waitFor = 10 * time.Second

	// true, true, false, true, true, then true forever. The first two
	// successes must NOT pass: the false check resets the stability
	// window, so success is only possible on the sixth check.
	sequence := []bool{true, true, false, true, true}
	var mu sync.Mutex
	calls := 0
	condition := func() bool {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls <= len(sequence) {
			return sequence[calls-1]
		}
		return true
	}

	res := runEventuallyStable(mt, context.Background(), condition, waitFor, stableFor, tick, clock)

	// Each check except the final one schedules exactly one tick.
	for i := 0; i < 5; i++ {
		clock.waitForTick(t, tick)
		clock.Advance(tick)
	}

	r := waitResult(t, res)
	if !r.ok {
		t.Fatalf("expected EventuallyStable to succeed, got failure: %s", r.err)
	}

	mu.Lock()
	defer mu.Unlock()
	if calls != 6 {
		t.Fatalf("expected exactly 6 condition checks (success only after the reset window elapsed), got %d", calls)
	}
}

func TestEventuallyStableTimeoutUsesInjectedClock(t *testing.T) {
	t.Parallel()

	clock := newFakeClock()
	mt := new(mockTestingT)

	const tick = 2 * time.Second
	const waitFor = 5 * time.Second

	var mu sync.Mutex
	calls := 0
	condition := func() bool {
		mu.Lock()
		defer mu.Unlock()
		calls++
		return false
	}

	start := time.Now()
	res := runEventuallyStable(mt, context.Background(), condition, waitFor, 0, tick, clock)

	// Checks at t=0, t=2, t=4 (all false); the timeout fires at t=5 while
	// waiting for the next tick (which would fire at t=6).
	for i := 0; i < 2; i++ {
		clock.waitForTick(t, tick)
		clock.Advance(tick)
	}
	clock.waitForTick(t, tick)
	clock.Advance(time.Second)

	r := waitResult(t, res)
	if r.ok {
		t.Fatal("expected EventuallyStable to time out, but it succeeded")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("timeout must be judged by the injected clock, took %v of real time", elapsed)
	}
	if !strings.Contains(r.err, "timed out after 5s") {
		t.Errorf("expected timeout reason in failure output, got: %s", r.err)
	}
	if !strings.Contains(r.err, "last failure: condition returned false") {
		t.Errorf("expected last failure diagnostic in failure output, got: %s", r.err)
	}
	if !strings.Contains(r.err, "no stable window in progress") {
		t.Errorf("expected stable window state in failure output, got: %s", r.err)
	}

	mu.Lock()
	defer mu.Unlock()
	if calls != 3 {
		t.Fatalf("expected exactly 3 condition checks before timeout, got %d", calls)
	}
}

func TestEventuallyStableConditionRunsPastDeadline(t *testing.T) {
	t.Parallel()

	clock := newFakeClock()
	mt := new(mockTestingT)

	// The condition itself advances the injected clock beyond the deadline
	// while it runs. The running check is allowed to finish, but no new
	// check is scheduled afterwards.
	calls := 0
	condition := func() bool {
		calls++
		clock.Advance(4 * time.Second)
		return true
	}

	res := runEventuallyStable(mt, context.Background(), condition, 3*time.Second, 10*time.Second, time.Second, clock)

	r := waitResult(t, res)
	if r.ok {
		t.Fatal("expected EventuallyStable to time out, but it succeeded")
	}
	if !strings.Contains(r.err, "timed out after 3s") {
		t.Errorf("expected timeout reason in failure output, got: %s", r.err)
	}
	if !strings.Contains(r.err, "stable window in progress: 0s of 10s") {
		t.Errorf("expected stable window progress in failure output, got: %s", r.err)
	}
	if calls != 1 {
		t.Fatalf("expected exactly 1 condition check, got %d", calls)
	}
}

func TestEventuallyStableContextCanceledBeforeStart(t *testing.T) {
	t.Parallel()

	clock := newFakeClock()
	mt := new(mockTestingT)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	calls := 0
	condition := func() bool {
		calls++
		return true
	}

	res := runEventuallyStable(mt, ctx, condition, 10*time.Second, time.Second, time.Second, clock)

	r := waitResult(t, res)
	if r.ok {
		t.Fatal("expected EventuallyStable to fail on canceled context, but it succeeded")
	}
	if !strings.Contains(r.err, "context canceled") {
		t.Errorf("expected cancellation reason in failure output, got: %s", r.err)
	}
	if calls != 0 {
		t.Fatalf("expected no condition checks after cancellation, got %d", calls)
	}
}

func TestEventuallyStableContextCanceledWhileWaiting(t *testing.T) {
	t.Parallel()

	clock := newFakeClock()
	mt := new(mockTestingT)

	ctx, cancel := context.WithCancel(context.Background())

	calls := 0
	condition := func() bool {
		calls++
		return false
	}

	res := runEventuallyStable(mt, ctx, condition, 10*time.Second, time.Second, time.Second, clock)

	// Wait until the loop is parked on the tick, then cancel: no new check
	// may be scheduled.
	clock.waitForTick(t, time.Second)
	cancel()

	r := waitResult(t, res)
	if r.ok {
		t.Fatal("expected EventuallyStable to fail on canceled context, but it succeeded")
	}
	if !strings.Contains(r.err, "context canceled") {
		t.Errorf("expected cancellation reason in failure output, got: %s", r.err)
	}
	if calls != 1 {
		t.Fatalf("expected exactly 1 condition check before cancellation, got %d", calls)
	}
}

func TestEventuallyStableConditionPanicRecordedAsFailure(t *testing.T) {
	t.Parallel()

	clock := newFakeClock()
	mt := new(mockTestingT)

	const tick = time.Second
	calls := 0
	condition := func() bool {
		calls++
		panic("boom")
	}

	res := runEventuallyStable(mt, context.Background(), condition, 2500*time.Millisecond, 0, tick, clock)

	// Checks at t=0, t=1, t=2 (all panic); the timeout fires at t=2.5 while
	// waiting for the next tick (which would fire at t=3).
	for i := 0; i < 2; i++ {
		clock.waitForTick(t, tick)
		clock.Advance(tick)
	}
	clock.waitForTick(t, tick)
	clock.Advance(600*time.Millisecond)

	r := waitResult(t, res)
	if r.ok {
		t.Fatal("expected EventuallyStable to time out, but it succeeded")
	}
	if !strings.Contains(r.err, "last failure: condition panicked: boom") {
		t.Errorf("expected panic diagnostic in failure output, got: %s", r.err)
	}
	if calls != 3 {
		t.Fatalf("expected exactly 3 condition checks before timeout, got %d", calls)
	}
}

func TestEventuallyStablePanicResetsStabilityWindow(t *testing.T) {
	t.Parallel()

	clock := newFakeClock()
	mt := new(mockTestingT)

	const tick = time.Second
	const stableFor = time.Second

	// true, panic, true, true: the panic must reset the stability window,
	// so success only happens on the fourth check.
	calls := 0
	condition := func() bool {
		calls++
		if calls == 2 {
			panic("boom")
		}
		return true
	}

	res := runEventuallyStable(mt, context.Background(), condition, 10*time.Second, stableFor, tick, clock)

	for i := 0; i < 3; i++ {
		clock.waitForTick(t, tick)
		clock.Advance(tick)
	}

	r := waitResult(t, res)
	if !r.ok {
		t.Fatalf("expected EventuallyStable to succeed, got failure: %s", r.err)
	}
	if calls != 4 {
		t.Fatalf("expected exactly 4 condition checks (panic resets the window), got %d", calls)
	}
}

func TestEventuallyStableInvalidTick(t *testing.T) {
	t.Parallel()

	for _, tick := range []time.Duration{0, -time.Second} {
		mt := new(mockTestingT)
		calls := 0
		ok := EventuallyStable(mt, context.Background(), func() bool {
			calls++
			return true
		}, 10*time.Second, time.Second, tick, newFakeClock())
		if ok {
			t.Errorf("expected EventuallyStable to reject tick %v", tick)
		}
		if !mt.Failed() || !strings.Contains(mt.errorString(), "positive tick interval") {
			t.Errorf("expected invalid tick failure for tick %v, got: %q", tick, mt.errorString())
		}
		if calls != 0 {
			t.Errorf("expected no condition checks for invalid tick %v, got %d", tick, calls)
		}
	}
}

func TestEventuallyStableRealClock(t *testing.T) {
	t.Parallel()

	// A nil clock uses the real system clock.
	if !EventuallyStable(t, context.Background(), func() bool { return true }, time.Second, 0, time.Millisecond, nil) {
		t.Fatal("expected EventuallyStable to succeed with the real clock")
	}

	// Zero stability window succeeds on the first successful check.
	mt := new(mockTestingT)
	calls := 0
	if !EventuallyStable(mt, context.Background(), func() bool {
		calls++
		return true
	}, time.Second, 0, time.Millisecond, nil) {
		t.Fatalf("expected EventuallyStable to succeed, got: %s", mt.errorString())
	}
	if calls != 1 {
		t.Fatalf("expected exactly 1 condition check, got %d", calls)
	}
}

func TestEventuallyStablefFormat(t *testing.T) {
	t.Parallel()

	mt := new(mockTestingT)
	ok := EventuallyStablef(mt, context.Background(), func() bool { return false }, 0, 0, 0, newFakeClock(), "extra %s", "info")
	if ok {
		t.Fatal("expected EventuallyStablef to fail with invalid tick")
	}
	if !strings.Contains(mt.errorString(), "extra info") {
		t.Errorf("expected formatted message in failure output, got: %s", mt.errorString())
	}
}
