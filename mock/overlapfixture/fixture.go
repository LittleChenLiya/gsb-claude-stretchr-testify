// Package overlapfixture registers expectations on behalf of the overlap
// tests so that the registration positions captured by CallerInfo point at a
// real, non-mock package file. The mock package's own tests are filtered out
// of CallerInfo, so a helper living outside the mock package is needed to
// demonstrate registration position reporting.
package overlapfixture

import (
	"github.com/stretchr/testify/mock"
)

// RegisterOverlapping registers an unlimited Anything expectation and an
// exact constant expectation on the same method, then returns the mock. The
// exact expectation is permanently occluded by the unlimited one.
func RegisterOverlapping() *mock.Mock {
	var m mock.Mock
	m.On("foo", mock.Anything).Return()
	m.On("foo", 1).Return()
	return &m
}

// RegisterOnceOverlapping registers a Once Anything expectation and an exact
// constant expectation on the same method, then returns the mock. The exact
// expectation is reachable once the Once expectation is exhausted.
func RegisterOnceOverlapping() *mock.Mock {
	var m mock.Mock
	m.On("foo", mock.Anything).Return().Once()
	m.On("foo", 1).Return()
	return &m
}

// RegisterMatchedByOverlapping registers a MatchedBy expectation and an exact
// constant expectation on the same method, then returns the mock. The
// MatchedBy matcher is never executed by the overlap check.
func RegisterMatchedByOverlapping(calls *int) *mock.Mock {
	var m mock.Mock
	m.On("foo", mock.MatchedBy(func(i int) bool {
		*calls++
		return true
	})).Return()
	m.On("foo", 1).Return()
	return &m
}
