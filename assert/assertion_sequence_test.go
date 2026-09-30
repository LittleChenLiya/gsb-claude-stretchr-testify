package assert

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// controlledClock is a manually driven SamplingClock. The tick channel is
// unbuffered, so delivering a tick blocks until the assertion loop is ready
// for another sample. Combined with the fact that a sample is launched in a
// goroutine and its result is received before the next tick, this guarantees
// each delivered tick produces exactly one processed sample in scripted
// order. After is unused because the tests end via cancellation, not deadline.
type controlledClock struct {
	ch chan time.Time
}

func newControlledClock() *controlledClock {
	return &controlledClock{ch: make(chan time.Time)}
}

func (c *controlledClock) After(time.Duration) <-chan time.Time { return nil }

func (c *controlledClock) NewTicker(time.Duration) SamplingTicker {
	return c
}

func (c *controlledClock) C() <-chan time.Time { return c.ch }

func (c *controlledClock) Stop() {}

// deliver sends one tick and returns once the loop has accepted it.
func (c *controlledClock) deliver() {
	c.ch <- time.Time{}
}

// scriptedSampler returns scripted values in order and counts invocations.
// Samples beyond the script return the last value forever.
type scriptedSampler struct {
	mu     sync.Mutex
	values []interface{}
	errs   []error
	calls  int
}

func (s *scriptedSampler) sample() (interface{}, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.calls
	s.calls++
	if idx < len(s.errs) && s.errs[idx] != nil {
		return nil, s.errs[idx]
	}
	if idx < len(s.values) {
		return s.values[idx], nil
	}
	return s.values[len(s.values)-1], nil
}

func (s *scriptedSampler) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func jobStages() []Stage {
	isState := func(want string) func(interface{}) bool {
		return func(got interface{}) bool { return got == want }
	}
	return []Stage{
		NewStage("ready", isState("ready")),
		NewStage("working", isState("working")),
		NewStage("done", isState("done")),
	}
}

// runSequence executes EventuallySequence in a goroutine. ticks are
// delivered one at a time; each blocks until the loop has consumed the tick
// and launched the sample, after which runSequence waits for the sample
// result to be processed. If cancelAfterTicks is true, the context is
// canceled once all requested samples have been observed.
func runSequence(ctx context.Context, sampler *scriptedSampler, stages []Stage, clock *controlledClock, ticks int, cancelAfterTicks bool) (bool, *errorsCapturingT) {
	ctx, cancel := context.WithCancel(ctx)
	mockT := new(errorsCapturingT)
	done := make(chan bool, 1)
	go func() {
		done <- EventuallySequence(mockT, ctx, sampler.sample, stages, time.Millisecond, clock)
	}()

	waitForCalls := func(want int) {
		deadline := time.Now().Add(2 * time.Second)
		for sampler.callCount() < want && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
	}

	// The immediate sample always happens first.
	waitForCalls(1)
	for i := 0; i < ticks; i++ {
		select {
		case clock.ch <- time.Time{}:
		case result := <-done:
			cancel()
			return result, mockT
		}
		waitForCalls(i + 2)
	}
	if cancelAfterTicks {
		cancel()
	}
	select {
	case result := <-done:
		cancel()
		return result, mockT
	case <-time.After(2 * time.Second):
		cancel()
		panic("EventuallySequence did not return")
	}
}

func TestEventuallySequence_AdvancesThroughOrderedStates(t *testing.T) {
	t.Parallel()

	// ready, ready, working, done: the repeated ready must not skip working.
	sampler := &scriptedSampler{values: []interface{}{"ready", "ready", "working", "done"}}
	ok, mockT := runSequence(context.Background(), sampler, jobStages(), newControlledClock(), 3, false)

	True(t, ok)
	Len(t, mockT.errors, 0)
	Equal(t, 4, sampler.callCount(), "each stage advances once across 4 samples")
}

func TestEventuallySequence_FailsWhenStateIsSkipped(t *testing.T) {
	t.Parallel()

	// ready, done, done: the working stage is never observed.
	sampler := &scriptedSampler{values: []interface{}{"ready", "done", "done"}}
	ok, mockT := runSequence(context.Background(), sampler, jobStages(), newControlledClock(), 2, true)

	False(t, ok)
	Equal(t, 3, sampler.callCount(), "sampling continues while stuck at the skipped stage")
	Len(t, mockT.errors, 1)
	msg := mockT.errors[0].Error()
	Contains(t, msg, "stuck at stage 2 of 3")
	Contains(t, msg, `awaiting "working"`)
	Contains(t, msg, "3 sample(s)")
	Contains(t, msg, "context canceled")
}

func TestEventuallySequence_RepeatedStateAdvancesOnlyOnce(t *testing.T) {
	t.Parallel()

	sampler := &scriptedSampler{values: []interface{}{"ready", "ready", "ready"}}
	ok, mockT := runSequence(context.Background(), sampler, jobStages(), newControlledClock(), 2, true)

	False(t, ok)
	Equal(t, 3, sampler.callCount())
	Len(t, mockT.errors, 1)
	Contains(t, mockT.errors[0].Error(), "stuck at stage 2 of 3")
}

func TestEventuallySequence_ContextCanceledBeforeReady(t *testing.T) {
	t.Parallel()

	sampler := &scriptedSampler{values: []interface{}{"pending", "pending"}}
	ok, mockT := runSequence(context.Background(), sampler, jobStages(), newControlledClock(), 1, true)

	False(t, ok)
	Equal(t, 2, sampler.callCount())
	Len(t, mockT.errors, 1)
	msg := mockT.errors[0].Error()
	Contains(t, msg, "stuck at stage 1 of 3")
	Contains(t, msg, `awaiting "ready"`)
	Contains(t, msg, "context canceled")
}

func TestEventuallySequence_SamplingErrorReportedAndDoesNotAdvance(t *testing.T) {
	t.Parallel()

	boom := errors.New("sampler exploded")
	// ready then an error: stage 1 matches on the first sample, the error is
	// counted but must not move stage 2, and cancellation ends the wait.
	sampler := &scriptedSampler{
		values: []interface{}{"ready", "pending", "pending"},
		errs:   []error{nil, boom},
	}
	ok, mockT := runSequence(context.Background(), sampler, jobStages(), newControlledClock(), 2, true)

	False(t, ok)
	Equal(t, 3, sampler.callCount())
	Len(t, mockT.errors, 1)
	msg := mockT.errors[0].Error()
	Contains(t, msg, "stuck at stage 2 of 3")
	Contains(t, msg, "last sampling error: sampler exploded")
	Contains(t, msg, "3 sample(s)")
}

func TestEventuallySequence_RecoversAfterSamplingError(t *testing.T) {
	t.Parallel()

	boom := errors.New("transient")
	// Samples: ready, error, working, done -> the error counts as a sample but
	// advances nothing, and completion happens on the fourth sample.
	sampler := &scriptedSampler{
		values: []interface{}{"ready", "pending", "working", "done"},
		errs:   []error{nil, boom, nil, nil},
	}
	ok, mockT := runSequence(context.Background(), sampler, jobStages(), newControlledClock(), 3, false)

	True(t, ok)
	Len(t, mockT.errors, 0)
	Equal(t, 4, sampler.callCount())
}

func TestEventuallySequence_EmptyStagesFailsWithoutSampling(t *testing.T) {
	t.Parallel()

	sampler := &scriptedSampler{values: []interface{}{"ready"}}
	mockT := new(errorsCapturingT)
	ok := EventuallySequence(mockT, context.Background(), sampler.sample, nil, time.Millisecond, nil)

	False(t, ok)
	Equal(t, 0, sampler.callCount(), "the sampler must not be invoked for an empty stage list")
	Len(t, mockT.errors, 1)
	Contains(t, mockT.errors[0].Error(), "No stages were provided")
}

func TestEventuallySequence_NilSampleFails(t *testing.T) {
	t.Parallel()

	mockT := new(errorsCapturingT)
	ok := EventuallySequence(mockT, context.Background(), nil, jobStages(), time.Millisecond, nil)

	False(t, ok)
	Len(t, mockT.errors, 1)
	Contains(t, mockT.errors[0].Error(), "non-nil sampling function")
}

func TestEventuallySequence_NilStageMatchFails(t *testing.T) {
	t.Parallel()

	mockT := new(errorsCapturingT)
	ok := EventuallySequence(mockT, context.Background(),
		func() (interface{}, error) { return "x", nil },
		[]Stage{NewStage("x", nil)}, time.Millisecond, nil)

	False(t, ok)
	Len(t, mockT.errors, 1)
	Contains(t, mockT.errors[0].Error(), `requires a non-nil Match function`)
}

func TestEventuallySequence_CustomMessageAppended(t *testing.T) {
	t.Parallel()

	mockT := new(errorsCapturingT)
	ok := EventuallySequence(mockT, context.Background(),
		func() (interface{}, error) { return "ready", nil },
		nil, time.Millisecond, nil, "extra detail %d", 42)

	False(t, ok)
	Contains(t, fmt.Sprint(mockT.errors), "extra detail 42")
}

func TestEventuallySequence_SystemClockSmokeTest(t *testing.T) {
	t.Parallel()

	// Exercises the nil-clock (SystemClock) path with a deadline: a tiny
	// goroutine walks the object through ready -> working -> done.
	var mu sync.Mutex
	state := "ready"
	go func() {
		time.Sleep(15 * time.Millisecond)
		mu.Lock()
		state = "working"
		mu.Unlock()
		time.Sleep(15 * time.Millisecond)
		mu.Lock()
		state = "done"
		mu.Unlock()
	}()

	sampler := func() (interface{}, error) {
		mu.Lock()
		defer mu.Unlock()
		return state, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	mockT := new(errorsCapturingT)
	ok := EventuallySequence(mockT, ctx, sampler, jobStages(), 5*time.Millisecond, nil)

	True(t, ok)
	Len(t, mockT.errors, 0)
}

func TestEventuallySequence_ImmediateSampleBeforeAnyTick(t *testing.T) {
	t.Parallel()

	// No ticks are ever sent; with a context deadline far away, cancellation
	// happens once the immediate (and only) sample has been observed.
	sampler := &scriptedSampler{values: []interface{}{"ready"}}
	clock := newControlledClock()
	ctx, cancel := context.WithCancel(context.Background())
	mockT := new(errorsCapturingT)
	result := make(chan bool, 1)
	go func() {
		result <- EventuallySequence(mockT, ctx, sampler.sample, jobStages(), time.Millisecond, clock)
	}()

	deadline := time.Now().Add(time.Second)
	for sampler.callCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case ok := <-result:
		False(t, ok)
	case <-time.After(time.Second):
		t.Fatal("EventuallySequence did not return after cancel")
	}
	Equal(t, 1, sampler.callCount(), "one immediate sample is taken before ticks")
}
