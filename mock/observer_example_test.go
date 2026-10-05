package mock_test

import (
	"fmt"
	"reflect"

	"github.com/stretchr/testify/mock"
)

type observedService struct {
	mock.Mock
}

func (s *observedService) Do(name string, count int) error {
	args := s.Called(name, count)
	return args.Error(0)
}

// ExampleAddObserver shows the three lifecycle events a single matched
// invocation produces, all correlated by one explicit invocation ID:
// started, matched (with the selected expectation) and returned (with the
// return arguments). Only entering argument types, never their values, are
// carried by the events.
func ExampleMock_AddObserver() {
	s := new(observedService)
	s.On("Do", "alice", mock.Anything).Return(nil)

	s.AddObserver(mock.InvocationObserverFunc(func(event mock.InvocationEvent) {
		switch event.Phase {
		case mock.InvocationStarted:
			fmt.Printf("#%d %s: started with argument types %v\n", event.ID, event.Method, typeNames(event.ArgumentTypes))
		case mock.InvocationMatched:
			fmt.Printf("#%d %s: matched expectation for %q\n", event.ID, event.Method, event.MatchedCall.Method)
		case mock.InvocationReturned:
			fmt.Printf("#%d %s: returned %v\n", event.ID, event.Method, event.ReturnArguments)
		case mock.InvocationPanicked:
			fmt.Printf("#%d %s: panicked with %v\n", event.ID, event.Method, event.PanicValue)
		case mock.InvocationUnmatched:
			fmt.Printf("#%d %s: unmatched\n", event.ID, event.Method)
		}
	}), "Do")

	_ = s.Do("alice", 7)
	fmt.Println("done")

	// Output:
	// #1 Do: started with argument types [string int]
	// #1 Do: matched expectation for "Do"
	// #1 Do: returned [<nil>]
	// done
}

func typeNames(types []reflect.Type) []string {
	names := make([]string, len(types))
	for i, t := range types {
		names[i] = t.String()
	}
	return names
}
