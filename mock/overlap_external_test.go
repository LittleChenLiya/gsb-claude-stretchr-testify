package mock_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/mock/overlapfixture"
)

// External test package: registration positions are captured because the
// expectations are registered from the overlapfixture helper, which lives
// outside the mock package, so CallerInfo records the real file and line of
// each On() call.
func TestCheckExpectationOverlaps_ExternalPositions(t *testing.T) {
	m := overlapfixture.RegisterOverlapping()

	findings := m.CheckExpectationOverlaps(t)
	if assert.Len(t, findings, 1) {
		assert.Equal(t, mock.OverlapDefinite, findings[0].Level)
		report := findings[0].String()
		assert.Contains(t, report, "overlapfixture/fixture.go")
		assert.Contains(t, report, "definite occlusion")
		assert.Contains(t, report, "registered at:")
		assert.Contains(t, report, "condition:")
	}
}

// An unlimited Anything expectation occludes a later exact one; changing the
// earlier expectation to Once makes the later one reachable after exhaustion.
func TestCheckExpectationOverlaps_UnlimitedVsOnce(t *testing.T) {
	m := overlapfixture.RegisterOnceOverlapping()

	findings := m.CheckExpectationOverlaps(t)
	if assert.Len(t, findings, 1) {
		assert.Equal(t, mock.OverlapAfterExhaustion, findings[0].Level)
		assert.NotEqual(t, mock.OverlapDefinite, findings[0].Level)
	}
}

// A MatchedBy matcher is never executed and the pair is reported as unknown
// rather than as a definite conflict.
func TestCheckExpectationOverlaps_MatchedByUnknown(t *testing.T) {
	var calls int
	m := overlapfixture.RegisterMatchedByOverlapping(&calls)

	findings := m.CheckExpectationOverlaps(t)
	if assert.Len(t, findings, 1) {
		assert.Equal(t, mock.OverlapUnknown, findings[0].Level)
		assert.NotEqual(t, mock.OverlapDefinite, findings[0].Level)
	}
	assert.Equal(t, 0, calls, "MatchedBy must not be executed by the check")
}
