package assert

import (
	"context"
	"fmt"
	"time"
)

// SamplingClock abstracts the passage of time used by [EventuallySequence] to
// schedule samples. It allows tests to drive sampling deterministically rather
// than relying on real wall-clock time.
//
// An implementation backed by the standard library time package is provided
// by [SystemClock]; pass nil as the clock to [EventuallySequence] to use it.
type SamplingClock interface {
	// After waits for the given duration to elapse and then sends the current
	// time on the returned channel. It mirrors time.After.
	After(d time.Duration) <-chan time.Time
	// NewTicker returns a ticker that delivers ticks every d. It mirrors
	// time.NewTicker.
	NewTicker(d time.Duration) SamplingTicker
}

// SamplingTicker is the ticker side of [SamplingClock].
type SamplingTicker interface {
	// C returns the channel on which ticks are delivered.
	C() <-chan time.Time
	// Stop turns off a ticker. After Stop, no more ticks are sent.
	Stop()
}

// SystemClock is a [SamplingClock] backed by the standard library time
// package. It is the clock used when nil is supplied to [EventuallySequence].
type SystemClock struct{}

// After implements [SamplingClock] using time.After.
func (SystemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// NewTicker implements [SamplingClock] using time.NewTicker.
func (SystemClock) NewTicker(d time.Duration) SamplingTicker {
	return &systemTicker{Ticker: time.NewTicker(d)}
}

type systemTicker struct {
	*time.Ticker
}

func (t *systemTicker) C() <-chan time.Time { return t.Ticker.C }

// Stage describes one ordered state awaited by [EventuallySequence].
//
// Name identifies the stage in failure messages. Match receives the value
// returned by the sampling function and reports whether the stage is present.
// A stage checker must only match its own state: later stages must not match
// earlier states, otherwise a skipped intermediate state would be hidden.
type Stage struct {
	// Name identifies the stage in failure messages.
	Name string
	// Match reports whether the sample represents this stage.
	Match func(sample interface{}) bool
}

// NewStage is a convenience constructor for [Stage].
func NewStage(name string, match func(sample interface{}) bool) Stage {
	return Stage{Name: name, Match: match}
}

// EventuallySequence asserts that a sampled object visits an ordered
// sequence of states rather than merely reaching the final state.
//
// The sample function is invoked once immediately and again after every tick.
// Each successful sample is evaluated against the stage currently awaited.
// Stages must be matched strictly in order: a sample may advance at most one
// stage per invocation, and a sample that skips a stage matches neither the
// earlier nor the later checker. The assertion succeeds once every stage has
// matched in turn.
//
// Errors returned by sample do not advance any stage and do not fail the
// assertion immediately; sampling continues until ctx is canceled or its
// deadline is reached. If the wait ends first, the number of samples taken,
// the stage that was awaited, and the last sampling error (if any) are
// reported.
//
// clock is an injectable [SamplingClock] allowing tests to drive ticks
// deterministically; pass nil to use [SystemClock].
//
// The example below waits for "ready", then "working", then "done":
//
//	assert.EventuallySequence(t, ctx, func() (interface{}, error) {
//		return job.State(), nil
//	}, []assert.Stage{
//		assert.NewStage("ready", func(s interface{}) bool { return s == "ready" }),
//		assert.NewStage("working", func(s interface{}) bool { return s == "working" }),
//		assert.NewStage("done", func(s interface{}) bool { return s == "done" }),
//	}, 10*time.Millisecond, nil)
func EventuallySequence(
	t TestingT,
	ctx context.Context,
	sample func() (interface{}, error),
	stages []Stage,
	tick time.Duration,
	clock SamplingClock,
	msgAndArgs ...interface{},
) bool {
	if h, ok := t.(tHelper); ok {
		h.Helper()
	}

	if len(stages) == 0 {
		return Fail(t, "No stages were provided to EventuallySequence; at least one ordered stage is required", msgAndArgs...)
	}
	if sample == nil {
		return Fail(t, "EventuallySequence requires a non-nil sampling function", msgAndArgs...)
	}
	for i := range stages {
		if stages[i].Match == nil {
			return Fail(t, fmt.Sprintf("EventuallySequence stage %d (%q) requires a non-nil Match function", i, stages[i].Name), msgAndArgs...)
		}
	}
	if clock == nil {
		clock = SystemClock{}
	}

	ticker := clock.NewTicker(tick)
	defer ticker.Stop()

	var deadlineC <-chan time.Time
	if deadline, ok := ctx.Deadline(); ok {
		deadlineC = clock.After(time.Until(deadline))
	}

	current := 0
	sampleCount := 0
	var lastSampleErr error

	sampleCh := make(chan sampleResult, 1)
	takeSample := func() {
		go func() {
			value, err := sample()
			sampleCh <- sampleResult{value: value, err: err}
		}()
	}

	sampleInFlight := true
	takeSample()

	for {
		select {
		case <-ctx.Done():
			return Fail(t, sequenceFailureMessage(current, stages, sampleCount, lastSampleErr, ctx.Err()), msgAndArgs...)
		case <-deadlineC:
			return Fail(t, sequenceFailureMessage(current, stages, sampleCount, lastSampleErr, context.DeadlineExceeded), msgAndArgs...)
		case <-ticker.C():
			if !sampleInFlight {
				takeSample()
				sampleInFlight = true
			}
		case result := <-sampleCh:
			sampleInFlight = false
			sampleCount++
			if result.err != nil {
				lastSampleErr = result.err
				continue
			}
			if stages[current].Match(result.value) {
				current++
				if current == len(stages) {
					return true
				}
			}
		}
	}
}

type sampleResult struct {
	value interface{}
	err   error
}

func sequenceFailureMessage(current int, stages []Stage, sampleCount int, lastSampleErr, endedErr error) string {
	msg := fmt.Sprintf(
		"Ordered state sequence was not completed: stuck at stage %d of %d (awaiting %q) after %d sample(s)",
		current+1, len(stages), stageName(stages, current), sampleCount,
	)
	if lastSampleErr != nil {
		msg += fmt.Sprintf("; last sampling error: %v", lastSampleErr)
	}
	if endedErr != nil {
		msg += fmt.Sprintf("; wait ended: %v", endedErr)
	}
	return msg
}

func stageName(stages []Stage, current int) string {
	if current >= 0 && current < len(stages) {
		return stages[current].Name
	}
	return ""
}
