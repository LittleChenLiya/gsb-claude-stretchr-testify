package assert

import (
	"context"
	"fmt"
	"time"
)

// Clock abstracts the source of time used by EventuallyStable, so tests can
// inject a fake clock and make timing deterministic.
//
// The zero value is not usable; pass nil to EventuallyStable to use the
// real system clock, or provide a custom implementation.
type Clock interface {
	// Now returns the current time according to the clock.
	Now() time.Time
	// After returns a channel that receives the current time once the
	// clock has advanced by at least d.
	After(d time.Duration) <-chan time.Time
}

// realClock implements Clock using the standard library time package.
type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// EventuallyStable asserts that the given condition becomes true and stays
// true for a continuous stability window (stableFor) within the overall
// timeout (waitFor). The condition is checked once immediately and then
// every tick. Unlike Eventually, a single successful check is not enough:
// every check must succeed for the whole stableFor window. Any failed check
// resets the stability window, which then restarts from the next successful
// check.
//
// Checks are never run concurrently: the next check is only scheduled after
// the previous one has finished. A condition that panics is recorded as a
// failed check, and the diagnostic of the last failure (including the
// panic value) is reported if the assertion fails. If ctx is canceled, no
// new checks are scheduled and the assertion fails immediately. If clock is
// nil, the real system clock is used; both the overall timeout and the
// stability window are measured against the provided clock.
//
//	assert.EventuallyStable(t, ctx, func() bool { return true; }, time.Second, 200*time.Millisecond, 10*time.Millisecond, nil)
//
// Returns whether the assertion was successful (true) or not (false).
func EventuallyStable(t TestingT, ctx context.Context, condition func() bool, waitFor time.Duration, stableFor time.Duration, tick time.Duration, clock Clock, msgAndArgs ...interface{}) bool {
	if h, ok := t.(tHelper); ok {
		h.Helper()
	}

	if tick <= 0 {
		return Fail(t, fmt.Sprintf("EventuallyStable requires a positive tick interval, got %v", tick), msgAndArgs...)
	}
	if stableFor < 0 {
		return Fail(t, fmt.Sprintf("EventuallyStable requires a non-negative stability window, got %v", stableFor), msgAndArgs...)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if clock == nil {
		clock = realClock{}
	}

	deadline := clock.Now().Add(waitFor)
	timeout := clock.After(waitFor)

	var (
		stable      bool
		stableSince time.Time
		resets      int
		checks      int
		lastDiag    string
	)

	report := func(reason string) string {
		out := reason
		if stable {
			out += fmt.Sprintf("; stable window in progress: %v of %v", clock.Now().Sub(stableSince), stableFor)
		} else {
			out += "; no stable window in progress"
		}
		out += fmt.Sprintf("; stability reset %d time(s) over %d check(s)", resets, checks)
		if lastDiag != "" {
			out += "; last failure: " + lastDiag
		}
		return out
	}

	// check runs the condition exactly once, converting a panic into a
	// failed check with a diagnostic.
	check := func() (ok bool, diag string) {
		defer func() {
			if r := recover(); r != nil {
				ok = false
				diag = fmt.Sprintf("condition panicked: %v", r)
			}
		}()
		if ok = condition(); !ok {
			diag = "condition returned false"
		}
		return ok, diag
	}

	for {
		if err := ctx.Err(); err != nil {
			return Fail(t, report(fmt.Sprintf("EventuallyStable canceled: %v", err)), msgAndArgs...)
		}

		checks++
		ok, diag := check()
		now := clock.Now()

		if ok {
			if !stable {
				stable = true
				stableSince = now
			}
			if now.Sub(stableSince) >= stableFor {
				return true
			}
		} else {
			if stable {
				resets++
			}
			stable = false
			stableSince = time.Time{}
			lastDiag = diag
		}

		if !now.Before(deadline) {
			return Fail(t, report(fmt.Sprintf("EventuallyStable timed out after %v: condition was not stable for %v", waitFor, stableFor)), msgAndArgs...)
		}

		select {
		case <-ctx.Done():
			return Fail(t, report(fmt.Sprintf("EventuallyStable canceled: %v", ctx.Err())), msgAndArgs...)
		case <-timeout:
			return Fail(t, report(fmt.Sprintf("EventuallyStable timed out after %v: condition was not stable for %v", waitFor, stableFor)), msgAndArgs...)
		case <-clock.After(tick):
		}
	}
}
