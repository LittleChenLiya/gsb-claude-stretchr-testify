package mock

import (
	"fmt"
	"reflect"
)

// InvocationPhase identifies the point in the lifecycle of a single mocked
// method invocation at which an [InvocationEvent] is reported.
type InvocationPhase int

const (
	// InvocationStarted is reported before the mock looks for an expectation
	// matching the invocation.
	InvocationStarted InvocationPhase = iota

	// InvocationMatched is reported once an actual expectation has been
	// selected for the invocation, before any blocking, panic or return.
	InvocationMatched

	// InvocationReturned is reported when the invocation completes normally
	// with its return arguments.
	InvocationReturned

	// InvocationPanicked is reported when the matched expectation is
	// configured to panic (via [Call.Panic]); the panic itself is then
	// re-raised so the code under test observes it unchanged.
	InvocationPanicked

	// InvocationUnmatched is reported when no expectation can satisfy the
	// invocation (the call is unexpected or a declared ordering requirement
	// is not met), before the mock reports the failure.
	InvocationUnmatched
)

// String returns the name of the phase.
func (p InvocationPhase) String() string {
	switch p {
	case InvocationStarted:
		return "started"
	case InvocationMatched:
		return "matched"
	case InvocationReturned:
		return "returned"
	case InvocationPanicked:
		return "panicked"
	case InvocationUnmatched:
		return "unmatched"
	default:
		return fmt.Sprintf("phase(%d)", int(p))
	}
}

// InvocationEvent describes one point in the lifecycle of a single mocked
// method invocation. Every event produced for the same invocation carries the
// same [InvocationEvent.ID], so concurrent invocations of the same method can
// be correlated without guessing from event ordering.
//
// ArgumentTypes describes the static types of the entering arguments; the
// actual argument values are intentionally never retained here.
type InvocationEvent struct {
	// ID uniquely identifies one invocation within the [Mock] instance.
	ID uint64

	// Method is the name of the mocked method.
	Method string

	// ArgumentTypes holds the reflect.Type of each entering argument.
	// It is populated for every phase and never carries argument values.
	ArgumentTypes []reflect.Type

	// Phase is the lifecycle phase the event reports.
	Phase InvocationPhase

	// MatchedCall is the expectation selected for the invocation during
	// [InvocationMatched] and carried by the later [InvocationReturned] and
	// [InvocationPanicked] events; it is nil for [InvocationStarted] and
	// [InvocationUnmatched].
	MatchedCall *Call

	// ReturnArguments carries the values returned to the caller for
	// [InvocationReturned]; it is nil for every other phase.
	ReturnArguments Arguments

	// PanicValue carries the value the matched expectation panics with for
	// [InvocationPanicked]; it is nil for every other phase.
	PanicValue interface{}
}

// InvocationObserver receives [InvocationEvent]s as invocations flow through a
// [Mock].
//
// An observer is observational only: the mock never re-runs argument matching
// or matchers on its behalf, never changes which [Call] is selected, and the
// number of times the mock and its matchers run is unaffected by observing.
// A panic raised by an observer is recovered and reported independently (see
// [Mock.ObserverFailures]) without affecting the invocation or other
// observers.
type InvocationObserver interface {
	ObserveInvocation(InvocationEvent)
}

// InvocationObserverFunc adapts a plain function to an [InvocationObserver].
type InvocationObserverFunc func(InvocationEvent)

// ObserveInvocation implements [InvocationObserver].
func (f InvocationObserverFunc) ObserveInvocation(event InvocationEvent) {
	f(event)
}

// invocationObserverEntry pairs a registered observer with the set of methods
// it wants to observe. An empty methods set observes every method.
type invocationObserverEntry struct {
	observer InvocationObserver
	methods  map[string]struct{}
}

func (e invocationObserverEntry) wants(method string) bool {
	if len(e.methods) == 0 {
		return true
	}
	_, ok := e.methods[method]
	return ok
}

// AddObserver registers an [InvocationObserver] with the mock. When method
// names are given, the observer only receives events for those methods;
// without method names it receives events for every method.
//
// The mock never stores argument values for the observer, never re-executes
// matching on its behalf, and keeps ordinary call counting and match
// selection unchanged while observers are present.
//
// It panics if observer is nil.
func (m *Mock) AddObserver(observer InvocationObserver, methods ...string) {
	if observer == nil {
		panic("mock: observer must not be nil")
	}

	entry := invocationObserverEntry{observer: observer}
	if len(methods) > 0 {
		entry.methods = make(map[string]struct{}, len(methods))
		for _, method := range methods {
			entry.methods[method] = struct{}{}
		}
	}

	m.mutex.Lock()
	m.observers = append(m.observers, entry)
	m.mutex.Unlock()
}

// RemoveObserver unregisters a previously added observer. It returns true if
// the observer was registered and removed.
func (m *Mock) RemoveObserver(observer InvocationObserver) bool {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	for i, entry := range m.observers {
		if entry.observer == observer {
			m.observers = append(m.observers[:i], m.observers[i+1:]...)
			return true
		}
	}
	return false
}

// ObserverFailures returns the values recovered from panics raised by
// registered observers. Failures are reported here instead of disturbing the
// observed invocations; the returned slice is ordered by occurrence.
func (m *Mock) ObserverFailures() []interface{} {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	return append([]interface{}{}, m.observerFailures...)
}

// invocationContext tracks one in-flight invocation through its lifecycle.
type invocationContext struct {
	parent        *Mock
	id            uint64
	method        string
	argumentTypes []reflect.Type
	observers     []invocationObserverEntry
	call          *Call
}

// beginInvocation assigns an ID to an entering invocation, snapshots the
// observers interested in it and reports [InvocationStarted]. It must be
// called without the mock mutex held.
//
// When no observer is registered it costs only a short lock/unlock: no ID is
// consumed and no argument types are reflected, leaving ordinary mocks as
// cheap as without observers.
func (m *Mock) beginInvocation(method string, arguments []interface{}) invocationContext {
	m.mutex.Lock()
	if len(m.observers) == 0 {
		m.mutex.Unlock()
		return invocationContext{parent: m, method: method}
	}

	m.nextInvocationID++
	ctx := invocationContext{
		parent:        m,
		id:            m.nextInvocationID,
		method:        method,
		argumentTypes: argumentTypesOf(arguments),
	}
	for _, observer := range m.observers {
		if observer.wants(method) {
			ctx.observers = append(ctx.observers, observer)
		}
	}
	m.mutex.Unlock()

	ctx.emit(InvocationEvent{
		ID:            ctx.id,
		Method:        method,
		ArgumentTypes: ctx.argumentTypes,
		Phase:         InvocationStarted,
	})

	return ctx
}

// matched records the selected expectation and reports [InvocationMatched].
func (ctx *invocationContext) matched(call *Call) {
	ctx.call = call
	ctx.emit(InvocationEvent{
		ID:            ctx.id,
		Method:        ctx.method,
		ArgumentTypes: ctx.argumentTypes,
		Phase:         InvocationMatched,
		MatchedCall:   call,
	})
}

// returned reports [InvocationReturned] with the return arguments.
func (ctx invocationContext) returned(returnArguments Arguments) {
	ctx.emit(InvocationEvent{
		ID:              ctx.id,
		Method:          ctx.method,
		ArgumentTypes:   ctx.argumentTypes,
		Phase:           InvocationReturned,
		MatchedCall:     ctx.call,
		ReturnArguments: returnArguments,
	})
}

// panicked reports [InvocationPanicked] with the panic value. The mock still
// re-raises the panic after reporting.
func (ctx invocationContext) panicked(panicValue interface{}) {
	ctx.emit(InvocationEvent{
		ID:            ctx.id,
		Method:        ctx.method,
		ArgumentTypes: ctx.argumentTypes,
		Phase:         InvocationPanicked,
		MatchedCall:   ctx.call,
		PanicValue:    panicValue,
	})
}

// unmatched reports [InvocationUnmatched] for an invocation the mock cannot
// satisfy.
func (ctx invocationContext) unmatched() {
	ctx.emit(InvocationEvent{
		ID:            ctx.id,
		Method:        ctx.method,
		ArgumentTypes: ctx.argumentTypes,
		Phase:         InvocationUnmatched,
	})
}

// emit delivers an event to every snapshotted observer. An observer that
// panics is isolated: the panic value is recorded on the mock and delivery to
// the remaining observers and to the invocation itself continues.
//
// Events are delivered without the mock mutex held so observers cannot
// deadlock the mock.
func (ctx invocationContext) emit(event InvocationEvent) {
	for _, entry := range ctx.observers {
		func() {
			defer func() {
				if r := recover(); r != nil {
					ctx.recordObserverFailure(r)
				}
			}()
			entry.observer.ObserveInvocation(event)
		}()
	}
}

// recordObserverFailure stores a value recovered from an observer without
// letting it affect the observed invocation.
func (ctx invocationContext) recordObserverFailure(value interface{}) {
	ctx.parent.mutex.Lock()
	ctx.parent.observerFailures = append(ctx.parent.observerFailures, value)
	ctx.parent.mutex.Unlock()
}

// argumentTypesOf returns the static types of the entering arguments without
// retaining the argument values.
func argumentTypesOf(arguments []interface{}) []reflect.Type {
	types := make([]reflect.Type, len(arguments))
	for i, argument := range arguments {
		types[i] = reflect.TypeOf(argument)
	}
	return types
}
