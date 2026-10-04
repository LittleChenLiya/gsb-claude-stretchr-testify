package mock

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// failingTestingT records the last failure reported without stopping the test
// process.
type failingTestingT struct {
	failed  bool
	message string
}

func (f *failingTestingT) Logf(string, ...interface{}) {}

func (f *failingTestingT) Errorf(format string, args ...interface{}) {
	f.failed = true
	f.message = fmt.Sprintf(format, args...)
}

func (f *failingTestingT) FailNow() {
	f.failed = true
}

// TestFindExpectationShadows_UnlimitedAnythingShadowsExact is the first
// acceptance scenario: an unlimited Anything expectation registered first
// definitely shadows the later exact-constant expectation.
func TestFindExpectationShadows_UnlimitedAnythingShadowsExact(t *testing.T) {
	m := &Mock{}
	first := m.On("M", Anything).Return(0)
	exact := m.On("M", 1).Return(1)

	shadows := m.FindExpectationShadows()
	if assert.Len(t, shadows, 1) {
		s := shadows[0]
		assert.Same(t, first, s.Shadowing)
		assert.Same(t, exact, s.Shadowed)
		assert.Equal(t, 0, s.ShadowingIndex)
		assert.Equal(t, 1, s.ShadowedIndex)
		assert.True(t, s.Unlimited)
		assert.False(t, s.ReachableAfterExhaustion)
		assert.Equal(t, CertaintyDefinite, s.Certainty)
		assert.Contains(t, s.Condition(), "can never be selected")

		report := s.String()
		assert.Contains(t, report, "registered at:")
		assert.Contains(t, report, "certainty: definite")
	}

	ft := &failingTestingT{}
	assert.False(t, m.AssertNoPermanentlyShadowedExpectations(ft))
	assert.True(t, ft.failed)
}

// TestFindExpectationShadows_OnceBecomesReachable is the second acceptance
// scenario: with the earlier expectation limited to Once, the later exact
// expectation is reachable once the first expectation is exhausted and must not
// be reported as a permanent shadow.
func TestFindExpectationShadows_OnceBecomesReachable(t *testing.T) {
	m := &Mock{}
	first := m.On("M", Anything).Return(0).Once()
	exact := m.On("M", 1).Return(1)

	shadows := m.FindExpectationShadows()
	if assert.Len(t, shadows, 1) {
		s := shadows[0]
		assert.Same(t, first, s.Shadowing)
		assert.Same(t, exact, s.Shadowed)
		assert.False(t, s.Unlimited)
		assert.True(t, s.ReachableAfterExhaustion)
		assert.Equal(t, 1, s.RemainingMatches)
		assert.Equal(t, CertaintyDefinite, s.Certainty)
		assert.Contains(t, s.Condition(), "becomes reachable")
		assert.NotContains(t, s.Condition(), "can never be selected")
	}

	// The opt-in assertion only fails for definite permanent shadows.
	ft := &failingTestingT{}
	assert.True(t, m.AssertNoPermanentlyShadowedExpectations(ft))
	assert.False(t, ft.failed)

	// Dispatch proves reachability: the first call consumes the Once budget and
	// the second call selects the exact expectation.
	assert.Equal(t, 0, m.MethodCalled("M", 1).Int(0))
	assert.Equal(t, 1, m.MethodCalled("M", 1).Int(0))

	// After exhaustion the spent expectation no longer shadows anything.
	assert.Empty(t, m.FindExpectationShadows())
}

// TestFindExpectationShadows_TimesBudgetReportsRemaining covers the Times
// count range and verifies the remaining budget decreases after calls.
func TestFindExpectationShadows_TimesBudgetReportsRemaining(t *testing.T) {
	m := &Mock{}
	m.On("M", Anything).Return(0).Times(2)
	m.On("M", 1).Return(1)

	shadows := m.FindExpectationShadows()
	if assert.Len(t, shadows, 1) {
		s := shadows[0]
		assert.False(t, s.Unlimited)
		assert.True(t, s.ReachableAfterExhaustion)
		assert.Equal(t, 2, s.RemainingMatches)
		assert.Equal(t, CertaintyDefinite, s.Certainty)
	}

	m.MethodCalled("M", 7)
	shadows = m.FindExpectationShadows()
	if assert.Len(t, shadows, 1) {
		assert.Equal(t, 1, shadows[0].RemainingMatches)
	}
}

// TestFindExpectationShadows_MatchedByIsUnknownAndNotExecuted proves that a
// MatchedBy matcher forces CertaintyUnknown, is never executed by the check
// and cannot be reported as a definite conflict.
func TestFindExpectationShadows_MatchedByIsUnknownAndNotExecuted(t *testing.T) {
	matcherCalls := 0
	m := &Mock{}
	m.On("M", Anything).Return(0)
	m.On("M", MatchedBy(func(v int) bool {
		matcherCalls++
		return v == 1
	})).Return(1)

	shadows := m.FindExpectationShadows()
	if assert.Len(t, shadows, 1) {
		s := shadows[0]
		assert.Equal(t, CertaintyUnknown, s.Certainty)
		// Unknown never constitutes a definite permanent conflict.
		assert.False(t, s.Certainty == CertaintyDefinite && s.Unlimited)
		assert.Contains(t, s.Condition(), "possibly unreachable")
	}

	// Counter proof: the static check never invoked the matcher.
	assert.Equal(t, 0, matcherCalls)

	ft := &failingTestingT{}
	assert.True(t, m.AssertNoPermanentlyShadowedExpectations(ft))
	assert.False(t, ft.failed)
	assert.Equal(t, 0, matcherCalls)
}

// TestFindExpectationShadows_CustomMatcherOnShadowingSideIsUnknown covers a
// custom matcher on the shadowing side, and custom matcher on both sides.
func TestFindExpectationShadows_CustomMatcherOnShadowingSideIsUnknown(t *testing.T) {
	m := &Mock{}
	m.On("M", MatchedBy(func(v int) bool { return true })).Return(0)
	m.On("M", 1).Return(1)
	shadows := m.FindExpectationShadows()
	if assert.Len(t, shadows, 1) {
		assert.Equal(t, CertaintyUnknown, shadows[0].Certainty)
		assert.True(t, shadows[0].Unlimited)
	}

	m2 := &Mock{}
	m2.On("N", MatchedBy(func(v int) bool { return true })).Return(0)
	m2.On("N", MatchedBy(func(v int) bool { return v == 1 })).Return(1)
	shadows2 := m2.FindExpectationShadows()
	if assert.Len(t, shadows2, 1) {
		assert.Equal(t, CertaintyUnknown, shadows2[0].Certainty)
	}
}

// TestFindExpectationShadows_Optional is the Optional-counts example: Maybe
// does not change dispatch overlap, but the report carries both optional flags.
func TestFindExpectationShadows_Optional(t *testing.T) {
	m := &Mock{}
	first := m.On("M", Anything).Maybe().Return(0)
	exact := m.On("M", 1).Maybe().Return(1)

	shadows := m.FindExpectationShadows()
	if assert.Len(t, shadows, 1) {
		s := shadows[0]
		assert.Same(t, first, s.Shadowing)
		assert.Same(t, exact, s.Shadowed)
		assert.True(t, s.ShadowingOptional)
		assert.True(t, s.ShadowedOptional)
		assert.True(t, s.Unlimited)
		assert.Equal(t, CertaintyDefinite, s.Certainty)
	}
}

// TestFindExpectationShadows_RegistrationLocations verifies both registration
// file:line locations are reported.
func TestFindExpectationShadows_RegistrationLocations(t *testing.T) {
	m := &Mock{}
	m.On("M", Anything).Return(0) // first registration line
	m.On("M", 1).Return(1)        // second registration line

	shadows := m.FindExpectationShadows()
	if assert.Len(t, shadows, 1) {
		s := shadows[0]
		assert.Contains(t, s.ShadowingRegistration, "shadow_test.go:")
		assert.Contains(t, s.ShadowedRegistration, "shadow_test.go:")
		assert.NotEqual(t, s.ShadowingRegistration, s.ShadowedRegistration)
		// Both locations appear in the rendered report.
		report := s.String()
		assert.Contains(t, report, s.ShadowingRegistration)
		assert.Contains(t, report, s.ShadowedRegistration)
	}
}

// TestFindExpectationShadows_NoOverlap covers methods, arities and constants
// that do not overlap: no report should be produced.
func TestFindExpectationShadows_NoOverlap(t *testing.T) {
	m := &Mock{}
	m.On("A", 1).Return(0)        // exact 1 ...
	m.On("B", Anything).Return(0) // ... cannot shadow a different method
	m.On("A", 1, 2).Return(0)     // ... cannot shadow a different argument count
	m.On("A", 2).Return(0)        // ... cannot shadow a different constant
	m.On("C", 3).Return(0)        // yet another method

	assert.Empty(t, m.FindExpectationShadows())
}

// TestFindExpectationShadows_ExactConstantsCoverage checks same/different
// constant coverage and mixed constant+Anything argument lists.
func TestFindExpectationShadows_ExactConstantsCoverage(t *testing.T) {
	m := &Mock{}
	base := m.On("M", 1, Anything).Return(0)
	covered := m.On("M", 1, 2).Return(1)
	notCovered := m.On("M", 2, Anything).Return(3)

	type pair struct{ earlier, later int }
	got := map[pair]bool{}
	for _, s := range m.FindExpectationShadows() {
		assert.Equal(t, CertaintyDefinite, s.Certainty)
		assert.True(t, s.Unlimited)
		got[pair{s.ShadowingIndex, s.ShadowedIndex}] = true
	}

	assert.True(t, got[pair{0, 1}], "expected (1,Anything) at index 0 to cover exact (1,2) at index 1")
	assert.False(t, got[pair{0, 2}], "(1,Anything) must not cover (2,Anything)")
	assert.Len(t, got, 1)

	// Sanity: the report points at the expected call pointers too.
	shadows := m.FindExpectationShadows()
	assert.Same(t, base, shadows[0].Shadowing)
	assert.Same(t, covered, shadows[0].Shadowed)
	assert.NotSame(t, notCovered, shadows[0].Shadowed)
}

// TestFindExpectationShadows_DispatchUnchanged verifies that running the check
// does not alter call selection or Repeatability.
func TestFindExpectationShadows_DispatchUnchanged(t *testing.T) {
	m := &Mock{}
	first := m.On("M", Anything).Return(0).Once()
	exact := m.On("M", 1).Return(1)

	m.FindExpectationShadows()
	assert.Equal(t, 1, first.Repeatability)
	assert.Equal(t, 0, exact.Repeatability)

	assert.Equal(t, 0, m.MethodCalled("M", 1).Int(0))
	assert.Equal(t, 1, m.MethodCalled("M", 1).Int(0))
}

// TestFindExpectationShadows_EmptyMock ensures the check is safe with no
// expectations.
func TestFindExpectationShadows_EmptyMock(t *testing.T) {
	m := &Mock{}
	assert.Empty(t, m.FindExpectationShadows())
	ft := &failingTestingT{}
	assert.True(t, m.AssertNoPermanentlyShadowedExpectations(ft))
	assert.False(t, ft.failed)
}

// TestFindExpectationShadows_ChainedOnCallerSite verifies registration
// locations are captured through a chained [Call.On] as well.
func TestFindExpectationShadows_ChainedOnCallerSite(t *testing.T) {
	m := &Mock{}
	m.On("First").Return(0).
		On("Second", Anything).Return(0)
	m.On("Second", 1).Return(1)

	shadows := m.FindExpectationShadows()
	if assert.Len(t, shadows, 1) {
		s := shadows[0]
		assert.Contains(t, s.ShadowingRegistration, "shadow_test.go:")
		assert.Contains(t, s.ShadowedRegistration, "shadow_test.go:")
	}
}

// TestExpectationShadow_StringContainsAllFields checks the rendered report
// shape once for documentation purposes.
func TestExpectationShadow_StringContainsAllFields(t *testing.T) {
	m := &Mock{}
	m.On("M", Anything).Return(0)
	m.On("M", 1).Return(1)

	report := m.FindExpectationShadows()[0].String()
	for _, fragment := range []string{
		"expectation overlap report",
		"shadowing expectation (registered first)",
		"shadowed expectation  (registered later)",
		"registered at:",
		"certainty: definite",
		"condition:",
	} {
		if !strings.Contains(report, fragment) {
			t.Errorf("report missing %q:\n%s", fragment, report)
		}
	}
}
