package mock

import (
	"fmt"
	"reflect"
	"sync/atomic"
)

// CallEventPhase identifies the phase of a mocked method call's lifecycle.
type CallEventPhase int

const (
	// CallEventEntry is emitted when a mocked method is entered, before any
	// matching is attempted.
	CallEventEntry CallEventPhase = iota
	// CallEventMatch is emitted after the call has been matched against an
	// expectation (or found to be unmatched).
	CallEventMatch
	// CallEventReturn is emitted when the call is about to return normally.
	CallEventReturn
	// CallEventPanic is emitted when the call is about to panic because the
	// matched expectation declared a panic.
	CallEventPanic
)

// String returns the name of the phase.
func (p CallEventPhase) String() string {
	switch p {
	case CallEventEntry:
		return "entry"
	case CallEventMatch:
		return "match"
	case CallEventReturn:
		return "return"
	case CallEventPanic:
		return "panic"
	default:
		return fmt.Sprintf("CallEventPhase(%d)", int(p))
	}
}

// CallEvent describes a single phase of a single mocked method call.
//
// CallEvents are paired by ID, not by time or order: two concurrent calls to
// the same method each receive their own ID and every event of a call carries
// that same ID.
//
// A CallEvent never carries argument values or return values, only their
// types. It also never re-runs argument matchers: the matched Call is the
// exact expectation the normal matching logic selected.
type CallEvent struct {
	// ID is the unique call identifier assigned when the call is entered. It
	// is stable across every event of the same call.
	ID uint64

	// Phase is the lifecycle phase this event represents.
	Phase CallEventPhase

	// Method is the name of the mocked method.
	Method string

	// ParamTypes holds the reflect.Type of each entry parameter, in order.
	// Values are never recorded.
	ParamTypes []reflect.Type

	// Call is the matched expectation for CallEventMatch events. It is nil
	// for unmatched calls and for non-match phases.
	Call *Call

	// Matched reports whether the call matched an expectation. It is only
	// meaningful for CallEventMatch events.
	Matched bool

	// ReturnTypes holds the reflect.Type of each return value, in order, for
	// CallEventReturn events. Values are never recorded.
	ReturnTypes []reflect.Type

	// PanicValue holds the value the call is about to panic with for
	// CallEventPanic events.
	PanicValue interface{}
}

// CallObserver receives CallEvents as mocked methods are called.
//
// Implementations must be safe for concurrent use. They must not block for
// long and must not call back into the Mock that produced the event (doing so
// may deadlock). If ObserveCall returns an error or panics, the failure is
// reported independently through Mock.ObserverErrors and never affects the
// mocked call.
type CallObserver interface {
	ObserveCall(event CallEvent) error
}

// CallObserverFunc adapts a function to the CallObserver interface.
type CallObserverFunc func(event CallEvent) error

// ObserveCall calls f(event).
func (f CallObserverFunc) ObserveCall(event CallEvent) error {
	return f(event)
}

// SetObserver sets the CallObserver that will receive events for mocked
// calls. Pass nil to disable observation.
//
// The observer is called synchronously while the mock's internal lock is
// held, so it must be fast and non-blocking. See CallObserver for the full
// contract.
func (m *Mock) SetObserver(observer CallObserver) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.observer = observer
}

// ObserveMethods restricts observation to the given method names. Pass no
// arguments to observe every method (the default).
func (m *Mock) ObserveMethods(methods ...string) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	if len(methods) == 0 {
		m.observeMethods = nil
		return
	}
	set := make(map[string]struct{}, len(methods))
	for _, method := range methods {
		set[method] = struct{}{}
	}
	m.observeMethods = set
}

// ObserverErrors returns the errors reported by the observer since the last
// call to ClearObserverErrors.
//
// Observer failures never affect the mocked call; they are collected here
// independently.
func (m *Mock) ObserverErrors() []error {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	errs := make([]error, len(m.observerErrors))
	copy(errs, m.observerErrors)
	return errs
}

// ClearObserverErrors clears the collected observer errors.
func (m *Mock) ClearObserverErrors() {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.observerErrors = nil
}

// nextCallID returns a unique call identifier.
func (m *Mock) nextCallID() uint64 {
	return atomic.AddUint64(&m.callCounter, 1)
}

// observe reports an event to the observer, if one is set and the method is
// observed. Observer failures are recovered and collected independently.
//
// The mock's mutex is held when this is called, so the observer runs under
// the same lock as the rest of the matching logic.
func (m *Mock) observe(event CallEvent) {
	if m.observer == nil {
		return
	}
	if m.observeMethods != nil {
		if _, ok := m.observeMethods[event.Method]; !ok {
			return
		}
	}
	func() {
		defer func() {
			if r := recover(); r != nil {
				m.observerErrors = append(m.observerErrors, fmt.Errorf("mock observer panic: %v", r))
			}
		}()
		if err := m.observer.ObserveCall(event); err != nil {
			m.observerErrors = append(m.observerErrors, err)
		}
	}()
}

// typesOf returns the reflect.Type of each value, in order. A nil value maps
// to a nil reflect.Type.
func typesOf(values []interface{}) []reflect.Type {
	types := make([]reflect.Type, len(values))
	for i, v := range values {
		types[i] = reflect.TypeOf(v)
	}
	return types
}
