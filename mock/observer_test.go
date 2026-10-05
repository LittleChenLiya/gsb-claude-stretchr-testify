package mock

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ExampleMock_observer shows the three events produced for a single mocked
// call: entry, match and return. Every event of the call carries the same
// call ID.
func ExampleMock_observer() {
	var events []CallEvent
	m := &Mock{}
	m.SetObserver(CallObserverFunc(func(event CallEvent) error {
		events = append(events, event)
		return nil
	}))
	m.On("Greet", "hello").Return("world")

	m.MethodCalled("Greet", "hello")

	for _, e := range events {
		fmt.Printf("call %d: %s %s\n", e.ID, e.Phase, e.Method)
	}
	// Output:
	// call 1: entry Greet
	// call 1: match Greet
	// call 1: return Greet
}

func TestMockObserver_RecordsEntryMatchReturn(t *testing.T) {
	var events []CallEvent
	m := &Mock{}
	m.SetObserver(CallObserverFunc(func(event CallEvent) error {
		events = append(events, event)
		return nil
	}))
	m.On("Foo", Anything).Return(42)

	args := m.MethodCalled("Foo", "bar")

	require.Equal(t, 42, args.Get(0))
	require.Len(t, events, 3)

	assert.Equal(t, CallEventEntry, events[0].Phase)
	assert.Equal(t, "Foo", events[0].Method)
	assert.Equal(t, CallEventMatch, events[1].Phase)
	assert.True(t, events[1].Matched)
	assert.Equal(t, CallEventReturn, events[2].Phase)
}

func TestMockObserver_RecordsTypesNotValues(t *testing.T) {
	var events []CallEvent
	m := &Mock{}
	m.SetObserver(CallObserverFunc(func(event CallEvent) error {
		events = append(events, event)
		return nil
	}))
	m.On("Foo", Anything, Anything).Return("result", 42)

	m.MethodCalled("Foo", "secret", 123)

	// Only types are recorded, never values.
	entry := events[0]
	assert.Equal(t, []reflect.Type{reflect.TypeOf(""), reflect.TypeOf(0)}, entry.ParamTypes)
	ret := events[2]
	assert.Equal(t, []reflect.Type{reflect.TypeOf(""), reflect.TypeOf(0)}, ret.ReturnTypes)
}

func TestMockObserver_MatchedCallIdentity(t *testing.T) {
	var events []CallEvent
	m := &Mock{}
	m.SetObserver(CallObserverFunc(func(event CallEvent) error {
		events = append(events, event)
		return nil
	}))
	expected := m.On("Foo", Anything).Return(0)

	m.MethodCalled("Foo", 1)

	match := events[1]
	assert.Equal(t, CallEventMatch, match.Phase)
	assert.True(t, match.Matched)
	assert.Same(t, expected, match.Call)
}

func TestMockObserver_DoesNotAffectCallCounts(t *testing.T) {
	m := &Mock{}
	m.SetObserver(CallObserverFunc(func(event CallEvent) error { return nil }))
	m.On("Foo", Anything).Return(0).Times(2)

	m.MethodCalled("Foo", 1)
	m.MethodCalled("Foo", 2)

	m.AssertNumberOfCalls(t, "Foo", 2)
	m.AssertExpectations(t)
}

// TestMockObserver_ConcurrentReverseCompletion is a barrier test: two
// goroutines call the same method. A barrier ensures both have entered and
// matched before either returns, and they are released to complete in reverse
// order. The observer must still pair every event by call ID, not by time.
func TestMockObserver_ConcurrentReverseCompletion(t *testing.T) {
	m := &Mock{}
	var mu sync.Mutex
	var events []CallEvent
	m.SetObserver(CallObserverFunc(func(event CallEvent) error {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
		return nil
	}))

	releaseA := make(chan struct{})
	releaseB := make(chan struct{})
	inRun := make(chan struct{}, 2)

	m.On("Foo", Anything).Return(0).Run(func(args Arguments) {
		inRun <- struct{}{}
		switch args.Get(0).(int) {
		case 1:
			<-releaseA
		case 2:
			<-releaseB
		}
	})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); m.MethodCalled("Foo", 1) }()
	go func() { defer wg.Done(); m.MethodCalled("Foo", 2) }()

	// Barrier: wait until both calls are blocked inside RunFn.
	<-inRun
	<-inRun
	// Release B first, then A, so they complete in reverse order.
	close(releaseB)
	close(releaseA)
	wg.Wait()

	byID := make(map[uint64][]CallEvent)
	for _, e := range events {
		byID[e.ID] = append(byID[e.ID], e)
	}
	require.Len(t, byID, 2, "each call must get its own call ID")

	for id, evs := range byID {
		phases := make([]CallEventPhase, 0, len(evs))
		for _, e := range evs {
			assert.Equal(t, "Foo", e.Method, "id %d", id)
			if e.Phase == CallEventMatch {
				assert.True(t, e.Matched, "id %d", id)
				assert.NotNil(t, e.Call, "id %d", id)
			}
			phases = append(phases, e.Phase)
		}
		assert.Equal(t, []CallEventPhase{CallEventEntry, CallEventMatch, CallEventReturn}, phases, "id %d", id)
	}
}

// TestMockObserver_DoesNotReRunMatcher verifies that observing does not
// re-execute argument matchers. The matcher runs once per actual match and
// never because of observing.
func TestMockObserver_DoesNotReRunMatcher(t *testing.T) {
	var matcherCalls int64
	m := &Mock{}
	m.SetObserver(CallObserverFunc(func(event CallEvent) error { return nil }))
	m.On("Foo", MatchedBy(func(i int) bool {
		atomic.AddInt64(&matcherCalls, 1)
		return true
	})).Return(0)

	m.MethodCalled("Foo", 1)
	m.MethodCalled("Foo", 2)

	assert.Equal(t, int64(2), matcherCalls, "observing must not re-run the matcher")
}

func TestMockObserver_PanicRecordsCorrectPhase(t *testing.T) {
	var events []CallEvent
	m := &Mock{}
	m.SetObserver(CallObserverFunc(func(event CallEvent) error {
		events = append(events, event)
		return nil
	}))
	m.On("Foo", Anything).Panic("boom")

	require.Panics(t, func() { m.MethodCalled("Foo", 1) })

	require.Len(t, events, 3)
	assert.Equal(t, CallEventEntry, events[0].Phase)
	assert.Equal(t, CallEventMatch, events[1].Phase)
	assert.Equal(t, CallEventPanic, events[2].Phase)
	assert.Equal(t, "boom", events[2].PanicValue)
}

func TestMockObserver_UnmatchedHasIndependentResult(t *testing.T) {
	var events []CallEvent
	m := &Mock{}
	m.SetObserver(CallObserverFunc(func(event CallEvent) error {
		events = append(events, event)
		return nil
	}))
	m.On("Foo", 1).Return(0)

	// Call with wrong arguments: unmatched.
	require.Panics(t, func() { m.MethodCalled("Foo", 2) })

	require.Len(t, events, 2)
	assert.Equal(t, CallEventEntry, events[0].Phase)
	assert.Equal(t, CallEventMatch, events[1].Phase)
	assert.False(t, events[1].Matched)
	assert.Nil(t, events[1].Call)
}

func TestMockObserver_ObserveOnlySpecifiedMethods(t *testing.T) {
	var events []CallEvent
	m := &Mock{}
	m.SetObserver(CallObserverFunc(func(event CallEvent) error {
		events = append(events, event)
		return nil
	}))
	m.ObserveMethods("Foo")
	m.On("Foo", Anything).Return(0)
	m.On("Bar", Anything).Return(0)

	m.MethodCalled("Foo", 1)
	m.MethodCalled("Bar", 2)

	for _, e := range events {
		assert.Equal(t, "Foo", e.Method)
	}
	assert.Len(t, events, 3) // entry, match, return for Foo only
}

func TestMockObserver_FailureReportedIndependently(t *testing.T) {
	m := &Mock{}
	m.SetObserver(CallObserverFunc(func(event CallEvent) error {
		return errors.New("observer failed")
	}))
	m.On("Foo", Anything).Return(42)

	// The mocked call is unaffected by the observer failure.
	args := m.MethodCalled("Foo", 1)
	require.Equal(t, 42, args.Get(0))

	errs := m.ObserverErrors()
	require.Len(t, errs, 3) // entry, match, return each failed
	for _, err := range errs {
		assert.EqualError(t, err, "observer failed")
	}
}

func TestMockObserver_PanicRecovered(t *testing.T) {
	m := &Mock{}
	m.SetObserver(CallObserverFunc(func(event CallEvent) error {
		panic("observer panic")
	}))
	m.On("Foo", Anything).Return(42)

	args := m.MethodCalled("Foo", 1)
	require.Equal(t, 42, args.Get(0))

	errs := m.ObserverErrors()
	require.Len(t, errs, 3)
	for _, err := range errs {
		assert.Contains(t, err.Error(), "observer panic")
	}
}

func TestMockObserver_ClearObserverErrors(t *testing.T) {
	m := &Mock{}
	m.SetObserver(CallObserverFunc(func(event CallEvent) error {
		return errors.New("observer failed")
	}))
	m.On("Foo", Anything).Return(0)

	m.MethodCalled("Foo", 1)
	require.NotEmpty(t, m.ObserverErrors())

	m.ClearObserverErrors()
	assert.Empty(t, m.ObserverErrors())
}

func TestMockObserver_NilObserverIsNoop(t *testing.T) {
	m := &Mock{}
	m.On("Foo", Anything).Return(42)

	args := m.MethodCalled("Foo", 1)
	require.Equal(t, 42, args.Get(0))
	assert.Empty(t, m.ObserverErrors())
}
