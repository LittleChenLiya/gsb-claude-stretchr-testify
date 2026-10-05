package mock

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingObserver collects invocation events in arrival order and groups
// them by invocation ID. It never looks at argument values.
type recordingObserver struct {
	mu     sync.Mutex
	events []InvocationEvent
}

func (o *recordingObserver) ObserveInvocation(event InvocationEvent) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, event)
}

func (o *recordingObserver) snapshot() []InvocationEvent {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]InvocationEvent{}, o.events...)
}

func (o *recordingObserver) forID(id uint64) []InvocationEvent {
	var grouped []InvocationEvent
	for _, event := range o.snapshot() {
		if event.ID == id {
			grouped = append(grouped, event)
		}
	}
	return grouped
}

// countingMatcher is a [mock.MatchedBy]-style matcher whose Matches calls are
// counted, so tests can prove observing does not re-run matchers.
type countingMatcher struct {
	mu      sync.Mutex
	matches int
	fn      func(int) bool
}

func newCountingMatcher(fn func(int) bool) *countingMatcher {
	return &countingMatcher{fn: fn}
}

func (m *countingMatcher) Match(v int) bool {
	m.mu.Lock()
	m.matches++
	m.mu.Unlock()
	return m.fn(v)
}

func (m *countingMatcher) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.matches
}

func TestInvocationObserver_MatchedReturnCarriesIDTypesCallAndReturns(t *testing.T) {
	t.Parallel()

	var mockedService = new(TestExampleImplementation)
	expectedCall := mockedService.On("TheExampleMethod", 1, 2, 3).Return(42, nil)

	observer := &recordingObserver{}
	mockedService.AddObserver(observer)

	args := mockedService.MethodCalled("TheExampleMethod", 1, 2, 3)
	require.Equal(t, 42, args.Int(0))

	events := observer.snapshot()
	require.Len(t, events, 3, "one matched invocation emits exactly three events")

	started, matched, returned := events[0], events[1], events[2]
	assert.Equal(t, InvocationStarted, started.Phase)
	assert.Equal(t, InvocationMatched, matched.Phase)
	assert.Equal(t, InvocationReturned, returned.Phase)

	// every event shares the invocation's explicit ID
	for _, event := range events {
		assert.Equal(t, uint64(1), event.ID)
		assert.Equal(t, "TheExampleMethod", event.Method)
		require.Len(t, event.ArgumentTypes, 3)
		assert.Equal(t, "int", event.ArgumentTypes[0].String())
		assert.Equal(t, "int", event.ArgumentTypes[1].String())
		assert.Equal(t, "int", event.ArgumentTypes[2].String())
	}

	assert.Nil(t, started.MatchedCall)
	assert.Same(t, expectedCall, matched.MatchedCall, "matched event identifies the actual Call")
	assert.Same(t, expectedCall, returned.MatchedCall)
	require.Len(t, returned.ReturnArguments, 2)
	assert.Equal(t, 42, returned.ReturnArguments[0])
	assert.Nil(t, returned.ReturnArguments[1])
	assert.Nil(t, returned.PanicValue)

	// events describe entering argument types only, never values; in
	// particular the matched event carries no Arguments to introspect
	assert.Equal(t, []string{"int", "int", "int"},
		[]string{
			started.ArgumentTypes[0].String(),
			started.ArgumentTypes[1].String(),
			started.ArgumentTypes[2].String(),
		})
	assert.Nil(t, started.ReturnArguments)
	assert.Nil(t, matched.ReturnArguments)
	assert.Nil(t, started.PanicValue)

	// ordinary call accounting is unchanged while observing
	mockedService.AssertNumberOfCalls(t, "TheExampleMethod", 1)
	assert.Empty(t, mockedService.ObserverFailures())
}

func TestInvocationObserver_PanicRecordedAtPanicPhase(t *testing.T) {
	t.Parallel()

	var mockedService = new(TestExampleImplementation)
	expectedCall := mockedService.On("TheExampleMethod", 1, 2, 3).Panic("boom")

	observer := &recordingObserver{}
	mockedService.AddObserver(observer)

	assert.PanicsWithValue(t, "boom", func() {
		mockedService.MethodCalled("TheExampleMethod", 1, 2, 3)
	})

	events := observer.snapshot()
	require.Len(t, events, 3)

	assert.Equal(t, InvocationStarted, events[0].Phase)
	assert.Equal(t, InvocationMatched, events[1].Phase)
	panicked := events[2]
	assert.Equal(t, InvocationPanicked, panicked.Phase)
	assert.Same(t, expectedCall, panicked.MatchedCall)
	assert.Equal(t, "boom", panicked.PanicValue)
	assert.Nil(t, panicked.ReturnArguments)

	// the panic still counted the call like an ordinary invocation
	mockedService.AssertNumberOfCalls(t, "TheExampleMethod", 1)
}

func TestInvocationObserver_UnmatchedHasOwnTerminalResult(t *testing.T) {
	t.Parallel()

	t.Run("unexpected method", func(t *testing.T) {
		var mockedService = new(TestExampleImplementation)
		mockedService.On("TheExampleMethod", 1, 2, 3).Return(42, nil)

		observer := &recordingObserver{}
		mockedService.AddObserver(observer)

		assert.Panics(t, func() {
			mockedService.MethodCalled("TheExampleMethod", 9, 9, 9)
		})

		events := observer.snapshot()
		require.Len(t, events, 2)
		assert.Equal(t, InvocationStarted, events[0].Phase)
		assert.Equal(t, uint64(1), events[0].ID)
		assert.Equal(t, InvocationUnmatched, events[1].Phase)
		assert.Equal(t, uint64(1), events[1].ID)
		assert.Nil(t, events[1].MatchedCall)
		assert.Nil(t, events[1].ReturnArguments)
		assert.Nil(t, events[1].PanicValue)
	})

	t.Run("ordering requirement not met", func(t *testing.T) {
		var mockedService = new(TestExampleImplementation)
		first := mockedService.On("TheExampleMethod2", true).Return()
		mockedService.On("TheExampleMethod", 1, 2, 3).Return(42, nil).NotBefore(first)

		observer := &recordingObserver{}
		mockedService.AddObserver(observer)

		assert.Panics(t, func() {
			mockedService.MethodCalled("TheExampleMethod", 1, 2, 3)
		})

		var unmatched *InvocationEvent
		for _, event := range observer.snapshot() {
			if event.Phase == InvocationUnmatched {
				event := event
				unmatched = &event
			}
		}
		require.NotNil(t, unmatched)
		assert.Equal(t, "TheExampleMethod", unmatched.Method)
	})
}

// TestInvocationObserver_ConcurrentSameMethodOutOfOrder verifies the barrier
// scenario: two invocations of the same method enter in order but complete in
// reverse order. Each must still carry its own ID, and matchers must run the
// same number of times as without observers.
func TestInvocationObserver_ConcurrentSameMethodOutOfOrder(t *testing.T) {
	t.Parallel()

	var mockedService = new(TestExampleImplementation)
	matcher := newCountingMatcher(func(v int) bool { return v == 1 || v == 2 })

	firstEnteredRun := make(chan struct{})
	releaseFirst := make(chan struct{})

	// the first invocation (argument 1) blocks inside Run; the second
	// (argument 2) passes straight through, forcing out-of-order completion
	mockedService.On("TheExampleMethod", MatchedBy(matcher.Match), 0, 0).Return(0, nil).
		Run(func(args Arguments) {
			if args.Int(0) == 1 {
				close(firstEnteredRun)
				<-releaseFirst
			}
		})

	observer := &recordingObserver{}
	mockedService.AddObserver(observer)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		mockedService.MethodCalled("TheExampleMethod", 1, 0, 0)
	}()

	<-firstEnteredRun

	go func() {
		defer wg.Done()
		mockedService.MethodCalled("TheExampleMethod", 2, 0, 0)
	}()

	// wait for the second invocation to fully complete (and return) first
	require.Eventually(t, func() bool {
		observer.mu.Lock()
		defer observer.mu.Unlock()
		for _, event := range observer.events {
			if event.ID == 2 && event.Phase == InvocationReturned {
				return true
			}
		}
		return false
	}, 2_000_000_000, 1_000_000)

	// ID 1 is still parked, so only ID 2 has a return so far
	for _, event := range observer.snapshot() {
		if event.Phase == InvocationReturned {
			assert.Equalf(t, uint64(2), event.ID, "first return must belong to the second invocation")
		}
	}

	close(releaseFirst)
	wg.Wait()

	// despite reverse completion, each invocation keeps its own event chain
	id1 := observer.forID(1)
	id2 := observer.forID(2)
	require.Len(t, id1, 3)
	require.Len(t, id2, 3)
	for _, group := range [][]InvocationEvent{id1, id2} {
		assert.Equal(t, "TheExampleMethod", group[0].Method)
		assert.Equal(t, InvocationStarted, group[0].Phase)
		assert.Equal(t, InvocationMatched, group[1].Phase)
		assert.Equal(t, InvocationReturned, group[2].Phase)
		assert.Same(t, group[1].MatchedCall, group[2].MatchedCall)
	}

	// global arrival order interleaves: invocation 2 returns before
	// invocation 1, so pairing by completion order would misattribute the
	// return; only the per-event ID chains each result to its own call
	all := observer.snapshot()
	require.Len(t, all, 6)
	assert.Equal(t, []struct {
		id    uint64
		phase InvocationPhase
	}{
		{1, InvocationStarted},
		{1, InvocationMatched},
		{2, InvocationStarted},
		{2, InvocationMatched},
		{2, InvocationReturned},
		{1, InvocationReturned},
	}, []struct {
		id    uint64
		phase InvocationPhase
	}{
		{all[0].ID, all[0].Phase},
		{all[1].ID, all[1].Phase},
		{all[2].ID, all[2].Phase},
		{all[3].ID, all[3].Phase},
		{all[4].ID, all[4].Phase},
		{all[5].ID, all[5].Phase},
	})

	// observing must not run matchers extra times: each invocation runs the
	// matcher exactly once through normal matching, nothing more
	assert.Equal(t, 2, matcher.count())

	mockedService.AssertNumberOfCalls(t, "TheExampleMethod", 2)
	assert.Empty(t, mockedService.ObserverFailures())
}

// TestInvocationObserver_MatcherAndCallCountsUnchanged drives the same
// invocation sequence (including an unmatched call) through two mocks, one
// observed and one not, and proves matcher invocations, selected results and
// recorded call counts are identical.
func TestInvocationObserver_MatcherAndCallCountsUnchanged(t *testing.T) {
	t.Parallel()

	drive := func(observe bool) (matcherCount, callCount int, results []int) {
		matcher := newCountingMatcher(func(v int) bool { return v > 0 })
		mockedService := new(TestExampleImplementation)
		mockedService.On("TheExampleMethod", MatchedBy(matcher.Match), Anything, Anything).Return(7, nil)
		mockedService.On("TheExampleMethod", 9, 9, 9).Return(8, nil)

		if observe {
			mockedService.AddObserver(&recordingObserver{})
		}

		for _, args := range [][]int{{9, 9, 9}, {5, 0, 0}, {5, 1, 1}} {
			results = append(results, mockedService.MethodCalled("TheExampleMethod", args[0], args[1], args[2]).Int(0))
		}
		func() {
			defer func() { _ = recover() }()
			_ = mockedService.MethodCalled("TheExampleMethod", -1, 0, 0)
		}()

		return matcher.count(), len(mockedService.Calls), results
	}

	baseMatches, baseCalls, baseResults := drive(false)
	obsMatches, obsCalls, obsResults := drive(true)

	assert.Equal(t, baseMatches, obsMatches, "matcher must not run extra times while observing")
	assert.Equal(t, baseCalls, obsCalls, "recorded call count must not change while observing")
	assert.Equal(t, baseResults, obsResults, "match selection must not change while observing")
	assert.Greater(t, baseMatches, 0)
}

func TestInvocationObserver_FailureIsReportedIndependently(t *testing.T) {
	t.Parallel()

	var mockedService = new(TestExampleImplementation)
	mockedService.On("TheExampleMethod", 1, 2, 3).Return(42, nil)

	// first observer panics on every event; second observer must still see
	// everything and the invocation itself must still succeed
	bad := InvocationObserverFunc(func(InvocationEvent) {
		panic("observer down")
	})
	good := &recordingObserver{}
	mockedService.AddObserver(bad)
	mockedService.AddObserver(good)

	assert.NotPanics(t, func() {
		args := mockedService.MethodCalled("TheExampleMethod", 1, 2, 3)
		assert.Equal(t, 42, args.Int(0))
	})

	require.Len(t, good.snapshot(), 3)

	failures := mockedService.ObserverFailures()
	require.Len(t, failures, 3, "one recovered failure per delivered event")
	assert.Equal(t, "observer down", failures[0])

	// ordinary mock accounting is untouched by the broken observer
	mockedService.AssertNumberOfCalls(t, "TheExampleMethod", 1)
}

func TestInvocationObserver_CanObserveOnlySpecifiedMethods(t *testing.T) {
	t.Parallel()

	var mockedService = new(TestExampleImplementation)
	mockedService.On("TheExampleMethod", 1, 2, 3).Return(42, nil)
	mockedService.On("TheExampleMethod2", true).Return()

	onlyMethod2 := &recordingObserver{}
	mockedService.AddObserver(onlyMethod2, "TheExampleMethod2")

	mockedService.MethodCalled("TheExampleMethod", 1, 2, 3)
	mockedService.MethodCalled("TheExampleMethod2", true)

	events := onlyMethod2.snapshot()
	require.Len(t, events, 3)
	for _, event := range events {
		assert.Equal(t, "TheExampleMethod2", event.Method)
	}
	assert.Equal(t, []InvocationPhase{InvocationStarted, InvocationMatched, InvocationReturned},
		[]InvocationPhase{events[0].Phase, events[1].Phase, events[2].Phase})

	// after removal no more events are delivered
	assert.True(t, mockedService.RemoveObserver(onlyMethod2))
	assert.False(t, mockedService.RemoveObserver(onlyMethod2))
	mockedService.MethodCalled("TheExampleMethod2", true)
	assert.Len(t, onlyMethod2.snapshot(), 3)
}

func TestInvocationObserver_NilObserverPanics(t *testing.T) {
	t.Parallel()

	var mockedService = new(TestExampleImplementation)
	assert.PanicsWithValue(t, "mock: observer must not be nil", func() {
		mockedService.AddObserver(nil)
	})
}

func TestInvocationPhaseString(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "started", InvocationStarted.String())
	assert.Equal(t, "matched", InvocationMatched.String())
	assert.Equal(t, "returned", InvocationReturned.String())
	assert.Equal(t, "panicked", InvocationPanicked.String())
	assert.Equal(t, "unmatched", InvocationUnmatched.String())
}
