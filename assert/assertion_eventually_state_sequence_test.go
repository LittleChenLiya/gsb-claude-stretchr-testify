package assert

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"
)

// fakeStateTicker is a controllable Ticker: each send on tick triggers one
// more sampling round.
type fakeStateTicker struct {
	ch chan time.Time
}

func (f *fakeStateTicker) C() <-chan time.Time { return f.ch }

func (f *fakeStateTicker) Stop() {}

type fakeStateClock struct {
	ticker   *fakeStateTicker
	interval time.Duration
}

func newFakeStateClock() *fakeStateClock {
	return &fakeStateClock{ticker: &fakeStateTicker{ch: make(chan time.Time, 1)}}
}

func (c *fakeStateClock) NewTicker(d time.Duration) Ticker {
	c.interval = d
	return c.ticker
}

func (c *fakeStateClock) tryTick(t *testing.T) {
	t.Helper()
	select {
	case c.ticker.ch <- time.Time{}:
	case <-time.After(time.Second):
		t.Fatal("the assertion loop is not waiting for the next tick")
	}
}

// scriptedStateSampler serves a fixed sequence of states and optionally
// returns an error on a given 1-based call.
type scriptedStateSampler struct {
	mu     sync.Mutex
	values []interface{}
	calls  int
	errOn  int
	err    error
}

func (s *scriptedStateSampler) sample() (interface{}, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.errOn > 0 && s.calls == s.errOn {
		return nil, s.err
	}
	return s.values[(s.calls-1)%len(s.values)], nil
}

func (s *scriptedStateSampler) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// pumpTicks drives the fake clock until the sampler has been called want
// times. Each tick is only sent after the previous one produced a sample.
func pumpTicks(t *testing.T, clock *fakeStateClock, sampler *scriptedStateSampler, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	waitForSample := func(current int) {
		for sampler.callCount() == current {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for sample %d, got %d", want, sampler.callCount())
			}
			runtime.Gosched()
		}
	}

	// The first sample is taken immediately, before any tick is sent.
	waitForSample(0)
	for sampler.callCount() < want {
		current := sampler.callCount()
		clock.tryTick(t)
		waitForSample(current)
	}
}

// progressMatcher records how often a stage matcher ran and the 1-based
// sample number on which it was satisfied (only the current stage is ever
// evaluated).
func progressMatcher(want string, sampler *scriptedStateSampler, matchedOn *[]int) (StateMatcherFunc, *int) {
	calls := 0
	return func(state interface{}) bool {
		calls++
		if state == want {
			*matchedOn = append(*matchedOn, sampler.callCount())
			return true
		}
		return false
	}, &calls
}

func TestEventuallyStateSequenceOrdered(t *testing.T) {
	clock := newFakeStateClock()
	sampler := &scriptedStateSampler{values: []interface{}{"ready", "ready", "working", "done"}}

	var matchedOn []int
	readyMatcher, readyCalls := progressMatcher("ready", sampler, &matchedOn)
	workingMatcher, workingCalls := progressMatcher("working", sampler, &matchedOn)
	doneMatcher, doneCalls := progressMatcher("done", sampler, &matchedOn)
	states := []StateMatcherFunc{readyMatcher, workingMatcher, doneMatcher}

	mockT := new(mockTestingT)
	result := make(chan bool, 1)
	go func() {
		result <- EventuallyStateSequence(mockT, context.Background(), sampler.sample, states,
			[]StateSequenceOption{WithStateSequenceClock(clock), WithStateSequenceInterval(7 * time.Millisecond)})
	}()

	pumpTicks(t, clock, sampler, 4)

	select {
	case ok := <-result:
		True(t, ok)
	case <-time.After(5 * time.Second):
		t.Fatal("EventuallyStateSequence did not return")
	}

	False(t, mockT.Failed())
	Equal(t, 4, sampler.callCount(), "all four samples must be consumed")
	Equal(t, 7*time.Millisecond, clock.interval, "the sampling interval must be injected")

	// The second, repeated "ready" sample is evaluated by the current
	// (working) matcher instead of re-entering ready: stages never rewind.
	Equal(t, 1, *readyCalls, "ready matcher is evaluated until ready is reached")
	Equal(t, 2, *workingCalls, "working matcher sees the repeated ready and the working sample")
	Equal(t, 1, *doneCalls, "done matcher is evaluated once")

	// Each stage advanced exactly once and strictly in sample order.
	Equal(t, []int{1, 3, 4}, matchedOn, "stages advanced once each, in order")
}

func TestEventuallyStateSequenceSkipsStage(t *testing.T) {
	clock := newFakeStateClock()
	// Goes straight from ready to done, never working.
	sampler := &scriptedStateSampler{values: []interface{}{"ready", "done", "done", "done"}}

	var matchedOn []int
	readyMatcher, readyCalls := progressMatcher("ready", sampler, &matchedOn)
	workingMatcher, workingCalls := progressMatcher("working", sampler, &matchedOn)
	doneMatcher, doneCalls := progressMatcher("done", sampler, &matchedOn)
	states := []StateMatcherFunc{readyMatcher, workingMatcher, doneMatcher}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mockT := new(mockTestingT)
	result := make(chan bool, 1)
	go func() {
		result <- EventuallyStateSequence(mockT, ctx, sampler.sample, states,
			[]StateSequenceOption{WithStateSequenceClock(clock)})
	}()

	pumpTicks(t, clock, sampler, 4)
	cancel()

	select {
	case ok := <-result:
		False(t, ok)
	case <-time.After(5 * time.Second):
		t.Fatal("EventuallyStateSequence did not return after cancellation")
	}

	True(t, mockT.Failed())
	Contains(t, mockT.errorString(), "State sequence was not satisfied")
	Contains(t, mockT.errorString(), "reached stage 1 of 3")
	Contains(t, mockT.errorString(), "after 4 samples")
	Contains(t, mockT.errorString(), context.Canceled.Error())
	Equal(t, 4, sampler.callCount())
	Equal(t, []int{1}, matchedOn, "only the ready stage ever advanced")
	Equal(t, 1, *readyCalls)
	Equal(t, 3, *workingCalls, "working matcher is evaluated for every later sample")
	Equal(t, 0, *doneCalls, "done matcher is never reached while waiting on working")
}

func TestEventuallyStateSequenceRepeatedState(t *testing.T) {
	clock := newFakeStateClock()
	sampler := &scriptedStateSampler{values: []interface{}{"ready"}}

	var matchedOn []int
	readyMatcher, readyCalls := progressMatcher("ready", sampler, &matchedOn)
	workingMatcher, workingCalls := progressMatcher("working", sampler, &matchedOn)
	states := []StateMatcherFunc{readyMatcher, workingMatcher}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mockT := new(mockTestingT)
	result := make(chan bool, 1)
	go func() {
		result <- EventuallyStateSequence(mockT, ctx, sampler.sample, states,
			[]StateSequenceOption{WithStateSequenceClock(clock)})
	}()

	pumpTicks(t, clock, sampler, 3)
	cancel()

	select {
	case ok := <-result:
		False(t, ok)
	case <-time.After(5 * time.Second):
		t.Fatal("EventuallyStateSequence did not return after cancellation")
	}

	True(t, mockT.Failed())
	Contains(t, mockT.errorString(), "reached stage 1 of 2")
	Contains(t, mockT.errorString(), "after 3 samples")
	Equal(t, 3, sampler.callCount())
	Equal(t, 1, *readyCalls, "ready matcher stopped being evaluated after ready")
	Equal(t, 2, *workingCalls, "working matcher ran on the repeated ready samples")
	Equal(t, []int{1}, matchedOn, "repeated ready samples advance the stage only once")
}

func TestEventuallyStateSequenceCancelled(t *testing.T) {
	clock := newFakeStateClock()
	// Never reaches ready.
	sampler := &scriptedStateSampler{values: []interface{}{"working"}}

	states := []StateMatcherFunc{
		func(state interface{}) bool { return state == "ready" },
		func(state interface{}) bool { return state == "done" },
	}

	ctx, cancel := context.WithCancel(context.Background())
	mockT := new(mockTestingT)
	result := make(chan bool, 1)
	go func() {
		result <- EventuallyStateSequence(mockT, ctx, sampler.sample, states,
			[]StateSequenceOption{WithStateSequenceClock(clock)})
	}()

	pumpTicks(t, clock, sampler, 1)
	cancel()

	select {
	case ok := <-result:
		False(t, ok)
	case <-time.After(5 * time.Second):
		t.Fatal("EventuallyStateSequence did not return after cancellation")
	}

	True(t, mockT.Failed())
	Contains(t, mockT.errorString(), "reached stage 0 of 2")
	Contains(t, mockT.errorString(), "after 1 samples")
	Contains(t, mockT.errorString(), context.Canceled.Error())
	Equal(t, 1, sampler.callCount(), "no more samples after cancellation")
}

func TestEventuallyStateSequenceSamplerError(t *testing.T) {
	clock := newFakeStateClock()
	sampler := &scriptedStateSampler{
		values: []interface{}{"ready"},
		errOn:  2,
		err:    errors.New("boom: sampler broken"),
	}

	states := []StateMatcherFunc{
		func(state interface{}) bool { return state == "ready" },
		func(state interface{}) bool { return state == "working" },
		func(state interface{}) bool { return state == "done" },
	}

	mockT := new(mockTestingT)
	result := make(chan bool, 1)
	go func() {
		result <- EventuallyStateSequence(mockT, context.Background(), sampler.sample, states,
			[]StateSequenceOption{WithStateSequenceClock(clock)})
	}()

	pumpTicks(t, clock, sampler, 2)

	select {
	case ok := <-result:
		False(t, ok)
	case <-time.After(5 * time.Second):
		t.Fatal("EventuallyStateSequence did not return after sampler error")
	}

	True(t, mockT.Failed())
	Contains(t, mockT.errorString(), "State sequence sampler returned an error")
	Contains(t, mockT.errorString(), "at stage 1 of 3")
	Contains(t, mockT.errorString(), "after 2 samples")
	Contains(t, mockT.errorString(), "boom: sampler broken")
	Equal(t, 2, sampler.callCount(), "sampling stops on the first error")
}

func TestEventuallyStateSequenceEmptyStates(t *testing.T) {
	sampler := &scriptedStateSampler{values: []interface{}{"ready"}}
	mockT := new(mockTestingT)

	ok := EventuallyStateSequence(mockT, context.Background(), sampler.sample, nil, nil, "extra %s", "detail")

	False(t, ok)
	True(t, mockT.Failed())
	Contains(t, mockT.errorString(), "At least one state matcher must be provided")
	Contains(t, mockT.errorString(), "extra detail")
	Equal(t, 0, sampler.callCount(), "sampler must not run without any state matcher")
}

func TestEventuallyStateSequenceNilMatcher(t *testing.T) {
	sampler := &scriptedStateSampler{values: []interface{}{"ready"}}
	mockT := new(mockTestingT)

	ok := EventuallyStateSequence(mockT, context.Background(), sampler.sample, []StateMatcherFunc{nil}, nil)

	False(t, ok)
	True(t, mockT.Failed())
	Contains(t, mockT.errorString(), "State matcher at index 0 is nil")
	Equal(t, 0, sampler.callCount())
}

func TestEventuallyStateSequenceDefaultsToWallClock(t *testing.T) {
	sampler := &scriptedStateSampler{values: []interface{}{"ready"}}
	states := []StateMatcherFunc{
		func(state interface{}) bool { return state == "ready" },
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	True(t, EventuallyStateSequence(t, ctx, sampler.sample, states, nil))
	Equal(t, 1, sampler.callCount(), "the first sample is taken immediately")
}
