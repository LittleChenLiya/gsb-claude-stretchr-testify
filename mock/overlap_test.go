package mock

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An unlimited Anything expectation registered before an exact one
// permanently occludes the latter.
func Test_CheckExpectationOverlaps_UnlimitedThenExact(t *testing.T) {
	var m Mock
	m.On("foo", Anything).Return().
		On("foo", 1).Return()

	findings := m.CheckExpectationOverlaps(t)
	require.Len(t, findings, 1)
	assert.Equal(t, OverlapDefinite, findings[0].Level)
	assert.Equal(t, "foo", findings[0].Earlier.Method)
	assert.Equal(t, "foo", findings[0].Later.Method)
}

// An exact constant expectation registered twice with unlimited repeatability
// permanently occludes the second registration.
func Test_CheckExpectationOverlaps_ExactEqualDefinite(t *testing.T) {
	var m Mock
	m.On("foo", 1).Return().
		On("foo", 1).Return()

	findings := m.CheckExpectationOverlaps(t)
	require.Len(t, findings, 1)
	assert.Equal(t, OverlapDefinite, findings[0].Level)
}

// A finite Anything expectation (Once) registered before an exact one does
// not permanently occlude it: the latter becomes reachable once the former is
// exhausted.
func Test_CheckExpectationOverlaps_OnceThenExactReachable(t *testing.T) {
	var m Mock
	m.On("foo", Anything).Return().Once().
		On("foo", 1).Return()

	findings := m.CheckExpectationOverlaps(t)
	require.Len(t, findings, 1)
	assert.Equal(t, OverlapAfterExhaustion, findings[0].Level)
}

// A finite Anything expectation (Times(3)) registered before an exact one is
// reported as reachable after exhaustion, not as a permanent occlusion.
func Test_CheckExpectationOverlaps_TimesThenExactReachable(t *testing.T) {
	var m Mock
	m.On("foo", Anything).Return().Times(3).
		On("foo", 1).Return()

	findings := m.CheckExpectationOverlaps(t)
	require.Len(t, findings, 1)
	assert.Equal(t, OverlapAfterExhaustion, findings[0].Level)
}

// An expectation whose repeatability is already exhausted is reported as
// reachable now.
func Test_CheckExpectationOverlaps_AlreadyExhausted(t *testing.T) {
	var m Mock
	m.On("foo", Anything).Return().Once().
		On("foo", 1).Return()

	// exhaust the Once expectation
	m.MethodCalled("foo", 1)
	require.Equal(t, -1, m.ExpectedCalls[0].Repeatability)

	findings := m.CheckExpectationOverlaps(t)
	require.Len(t, findings, 1)
	assert.Equal(t, OverlapAfterExhaustion, findings[0].Level)
}

// A MatchedBy matcher is never executed by the check and the pair is reported
// as unknown rather than as a definite conflict.
func Test_CheckExpectationOverlaps_MatchedByNotExecuted(t *testing.T) {
	var calls int
	var m Mock
	m.On("foo", MatchedBy(func(i int) bool {
		calls++
		return true
	})).Return().
		On("foo", 1).Return()

	findings := m.CheckExpectationOverlaps(t)
	require.Len(t, findings, 1)
	assert.Equal(t, OverlapUnknown, findings[0].Level)
	assert.NotEqual(t, OverlapDefinite, findings[0].Level)
	assert.Equal(t, 0, calls, "MatchedBy must not be executed by the check")
}

// AnythingOfType is a custom matcher: the pair is reported as unknown.
func Test_CheckExpectationOverlaps_AnythingOfTypeUnknown(t *testing.T) {
	var m Mock
	m.On("foo", AnythingOfType("int")).Return().
		On("foo", 1).Return()

	findings := m.CheckExpectationOverlaps(t)
	require.Len(t, findings, 1)
	assert.Equal(t, OverlapUnknown, findings[0].Level)
}

// IsType is a custom matcher: the pair is reported as unknown.
func Test_CheckExpectationOverlaps_IsTypeUnknown(t *testing.T) {
	var m Mock
	m.On("foo", IsType(0)).Return().
		On("foo", 1).Return()

	findings := m.CheckExpectationOverlaps(t)
	require.Len(t, findings, 1)
	assert.Equal(t, OverlapUnknown, findings[0].Level)
}

// FunctionalOptions is a custom matcher: the pair is reported as unknown.
func Test_CheckExpectationOverlaps_FunctionalOptionsUnknown(t *testing.T) {
	var m Mock
	m.On("foo", FunctionalOptions(OpNum(1))).Return().
		On("foo", 1).Return()

	findings := m.CheckExpectationOverlaps(t)
	require.Len(t, findings, 1)
	assert.Equal(t, OverlapUnknown, findings[0].Level)
}

// An exact constant registered before Anything leaves the later expectation
// reachable: no calls the latter matches are all taken by the former.
func Test_CheckExpectationOverlaps_ExactThenAnythingReachable(t *testing.T) {
	var m Mock
	m.On("foo", 1).Return().
		On("foo", Anything).Return()

	findings := m.CheckExpectationOverlaps(t)
	assert.Empty(t, findings)
}

// Two different exact constants never match the same call: no overlap.
func Test_CheckExpectationOverlaps_DifferentExactReachable(t *testing.T) {
	var m Mock
	m.On("foo", 1).Return().
		On("foo", 2).Return()

	findings := m.CheckExpectationOverlaps(t)
	assert.Empty(t, findings)
}

// Expectations on different method names never overlap.
func Test_CheckExpectationOverlaps_DifferentMethods(t *testing.T) {
	var m Mock
	m.On("foo", 1).Return().
		On("bar", 1).Return()

	findings := m.CheckExpectationOverlaps(t)
	assert.Empty(t, findings)
}

// Expectations declaring a different number of arguments never overlap.
func Test_CheckExpectationOverlaps_DifferentArgCount(t *testing.T) {
	var m Mock
	m.On("foo", 1).Return().
		On("foo", 1, 2).Return()

	findings := m.CheckExpectationOverlaps(t)
	assert.Empty(t, findings)
}

// Optional (Maybe) expectations are still reported: the optional flag does not
// change call selection.
func Test_CheckExpectationOverlaps_OptionalReported(t *testing.T) {
	var m Mock
	m.On("foo", Anything).Return().Maybe().
		On("foo", 1).Return().Maybe()

	findings := m.CheckExpectationOverlaps(t)
	require.Len(t, findings, 1)
	assert.Equal(t, OverlapDefinite, findings[0].Level)
	assert.Contains(t, findings[0].String(), "optional")
}

// The package-level entry point checks several mocks at once.
func Test_CheckExpectationOverlaps_MultipleMocks(t *testing.T) {
	var m1, m2 Mock
	m1.On("foo", Anything).Return().
		On("foo", 1).Return()
	m2.On("bar", Anything).Return().
		On("bar", 2).Return()

	findings := CheckExpectationOverlaps(t, &m1, &m2)
	assert.Len(t, findings, 2)
}

// The package-level entry point returns nothing when no mocks are given.
func Test_CheckExpectationOverlaps_NoMocks(t *testing.T) {
	findings := CheckExpectationOverlaps(t)
	assert.Empty(t, findings)
}

// The report contains both registration positions, the occlusion condition
// and the certainty level. CallerInfo filters out frames from the mock
// package itself, so the positions are set directly here; the external
// test package demonstrates real registration positions.
func Test_CheckExpectationOverlaps_ReportContainsPositions(t *testing.T) {
	var m Mock
	c1 := m.On("foo", Anything).Return()
	c2 := m.On("foo", 1).Return()
	c1.callerInfo = []string{"example_test.go:10"}
	c2.callerInfo = []string{"example_test.go:11"}

	findings := m.CheckExpectationOverlaps(t)
	require.Len(t, findings, 1)
	report := findings[0].String()
	assert.Contains(t, report, "example_test.go:10")
	assert.Contains(t, report, "example_test.go:11")
	assert.Contains(t, report, "definite occlusion")
	assert.Contains(t, report, "registered at:")
	assert.Contains(t, report, "condition:")
}

// The check does not modify call selection: the unlimited Anything expectation
// still takes the call and the exact one never matches.
func Test_CheckExpectationOverlaps_BehaviorUnchanged(t *testing.T) {
	var m Mock
	m.On("foo", Anything).Return("unlimited").
		On("foo", 1).Return("exact")

	findings := m.CheckExpectationOverlaps(t)
	require.Len(t, findings, 1)
	assert.Equal(t, OverlapDefinite, findings[0].Level)

	args := m.MethodCalled("foo", 1)
	assert.Equal(t, "unlimited", args.Get(0))
	assert.Equal(t, 1, m.ExpectedCalls[0].totalCalls)
	assert.Equal(t, 0, m.ExpectedCalls[1].totalCalls)
}

// The check does not modify assertion behavior: AssertExpectations still
// passes for satisfied expectations.
func Test_CheckExpectationOverlaps_AssertBehaviorUnchanged(t *testing.T) {
	var m Mock
	m.On("foo", Anything).Return().Once().
		On("foo", 1).Return().Maybe()

	findings := m.CheckExpectationOverlaps(t)
	require.Len(t, findings, 1)
	assert.Equal(t, OverlapAfterExhaustion, findings[0].Level)

	m.MethodCalled("foo", 1)
	assert.True(t, m.AssertExpectations(t))
}
