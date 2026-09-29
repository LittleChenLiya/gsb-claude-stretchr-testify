package assert

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"runtime"
	"time"
)

// StateSampleFunc is a function that returns the current state of the
// system under test. Returning a non-nil error aborts the assertion and the
// error is reported on t.
type StateSampleFunc func() (state interface{}, err error)

// StateMatcherFunc is a predicate that reports whether a sampled state
// represents a given stage of the expected state sequence.
type StateMatcherFunc func(state interface{}) bool

// Ticker is a drop-in replacement for *time.Ticker, which allows the
// sampling cadence used by EventuallyStateSequence to be injected in tests.
type Ticker interface {
	C() <-chan time.Time
	Stop()
}

// Clock creates tickers for periodic sampling. The default implementation
// wraps [time.NewTicker]; inject a custom Clock to drive sampling
// deterministically without waiting for wall-clock time.
type Clock interface {
	NewTicker(d time.Duration) Ticker
}

type wallClock struct{}

func (wallClock) NewTicker(d time.Duration) Ticker {
	return &wallTicker{ticker: time.NewTicker(d)}
}

type wallTicker struct {
	ticker *time.Ticker
}

func (t *wallTicker) C() <-chan time.Time { return t.ticker.C }

func (t *wallTicker) Stop() { t.ticker.Stop() }

// StateSequenceOption allows customizing EventuallyStateSequence, such as
// injecting the Clock used to schedule sampling.
type StateSequenceOption func(*stateSequenceConfig)

type stateSequenceConfig struct {
	clock    Clock
	interval time.Duration
}

// WithStateSequenceClock injects a Clock used to schedule the periodic
// sampling. When not provided the wall clock is used.
func WithStateSequenceClock(clock Clock) StateSequenceOption {
	return func(c *stateSequenceConfig) {
		c.clock = clock
	}
}

// WithStateSequenceInterval overrides the sampling interval, which defaults
// to 10 milliseconds. It is only used by the injected Clock when creating
// its ticker.
func WithStateSequenceInterval(interval time.Duration) StateSequenceOption {
	return func(c *stateSequenceConfig) {
		c.interval = interval
	}
}

// EventuallyStateSequence asserts that the system under test goes through an
// ordered sequence of states. The sampler is invoked periodically and each
// sampled state is checked against the matcher of the current stage only;
// a stage is advanced to the next one once its matcher is satisfied. A
// single sample advances at most one stage, so the system under test must
// pass through every stage in order (states that skip a stage fail the
// assertion). The assertion succeeds when all stages have been reached
// before ctx is cancelled.
//
// The clock used for sampling can be injected with WithStateSequenceClock,
// which makes the assertion fully deterministic in tests.
//
//	states := []StateMatcherFunc{
//		func(state interface{}) bool { return state == "ready" },
//		func(state interface{}) bool { return state == "working" },
//		func(state interface{}) bool { return state == "done" },
//	}
//	sampler := func() (interface{}, error) {
//		return currentStatus(), nil
//	}
//	EventuallyStateSequence(t, ctx, sampler, states)
func EventuallyStateSequence(t TestingT, ctx context.Context, sampler StateSampleFunc, states []StateMatcherFunc, opts []StateSequenceOption, msgAndArgs ...interface{}) bool {
	if h, ok := t.(tHelper); ok {
		h.Helper()
	}

	if len(states) == 0 {
		return Fail(t, "At least one state matcher must be provided to EventuallyStateSequence", msgAndArgs...)
	}
	for i, state := range states {
		if state == nil {
			return Fail(t, fmt.Sprintf("State matcher at index %d is nil", i), msgAndArgs...)
		}
	}

	config := stateSequenceConfig{
		clock:    wallClock{},
		interval: 10 * time.Millisecond,
	}
	for _, opt := range opts {
		opt(&config)
	}
	if config.clock == nil {
		config.clock = wallClock{}
	}

	type sampleResult struct {
		state interface{}
		err   error
	}
	ch := make(chan sampleResult, 1)
	takeSample := func() {
		state, err := sampler()
		ch <- sampleResult{state: state, err: err}
	}

	ticker := config.clock.NewTicker(config.interval)
	defer ticker.Stop()

	var tickC <-chan time.Time
	stage := 0
	sampleCount := 0

	// Sample once immediately, before waiting for the first tick.
	go takeSample()

	for {
		select {
		case <-ctx.Done():
			return Fail(t, fmt.Sprintf("State sequence was not satisfied: reached stage %d of %d (%s) after %d samples; %s",
				stage, len(states), stateMatcherName(states[stage]), sampleCount, ctx.Err()), msgAndArgs...)
		case <-tickC:
			tickC = nil
			go takeSample()
		case result := <-ch:
			sampleCount++
			if result.err != nil {
				return Fail(t, fmt.Sprintf("State sequence sampler returned an error at stage %d of %d (%s) after %d samples: %s",
					stage, len(states), stateMatcherName(states[stage]), sampleCount, result.err), msgAndArgs...)
			}
			// Only the current stage is checked and at most one stage is
			// advanced per sample.
			if states[stage](result.state) {
				stage++
				if stage == len(states) {
					return true
				}
			}
			tickC = ticker.C()
		}
	}
}

func stateMatcherName(matcher StateMatcherFunc) string {
	fullName := runtime.FuncForPC(reflect.ValueOf(matcher).Pointer()).Name()
	return filepath.Base(fullName)
}
