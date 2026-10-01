package mock

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scenarioRecordingT is a TestingT that records diagnostics instead of
// killing the test process. FailNow panics with errScenarioFailNow to stop
// the current call stack, mirroring how MockTestingT behaves.
type scenarioRecordingT struct {
	mu      sync.Mutex
	logs    []string
	errors  []string
	failNow int
}

var errScenarioFailNow = errors.New("FailNow was called")

func (r *scenarioRecordingT) Logf(format string, args ...interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, fmt.Sprintf(format, args...))
}

func (r *scenarioRecordingT) Errorf(format string, args ...interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

func (r *scenarioRecordingT) FailNow() {
	r.mu.Lock()
	r.failNow++
	r.mu.Unlock()
	panic(errScenarioFailNow)
}

func (r *scenarioRecordingT) errorfCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.errors)
}

func (r *scenarioRecordingT) lastError() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.errors[len(r.errors)-1]
}

func (r *scenarioRecordingT) joinedLogs() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.logs, "\n")
}

// expectScenarioFailure runs fn, which must trigger a scenario failure on a
// mock bound to rec, and verifies that the failure was reported through rec
// instead of killing the test process.
func expectScenarioFailure(t *testing.T, rec *scenarioRecordingT, fn func()) {
	t.Helper()
	before := rec.errorfCount()
	func() {
		defer func() {
			require.Equal(t, errScenarioFailNow, recover())
		}()
		fn()
	}()
	require.Equal(t, before+1, rec.errorfCount())
}

// newCommitScenario builds the acceptance scenario: phase "stage1" requires
// A twice and B once, phase "stage2" requires Commit once.
func newCommitScenario(m *Mock) *Scenario {
	scenario := NewScenario(m)
	scenario.Phase("stage1")
	scenario.On("A").Return().Twice()
	scenario.On("B").Return().Once()
	scenario.Phase("stage2")
	scenario.On("Commit").Return().Once()
	scenario.Freeze()
	return scenario
}

func TestScenarioInterleavedOrderAccepted(t *testing.T) {
	t.Parallel()

	m := &Mock{}
	rec := &scenarioRecordingT{}
	m.Test(rec)
	newCommitScenario(m)

	// Calls within stage1 may interleave freely; Commit starts stage2.
	m.MethodCalled("A")
	m.MethodCalled("B")
	m.MethodCalled("A")
	m.MethodCalled("Commit")

	assert.Zero(t, rec.errorfCount())
	assert.True(t, m.AssertExpectations(rec))
}

func TestScenarioPrematureCallFails(t *testing.T) {
	t.Parallel()

	m := &Mock{}
	rec := &scenarioRecordingT{}
	m.Test(rec)
	newCommitScenario(m)

	m.MethodCalled("A")
	m.MethodCalled("B")
	// One A is still missing, so Commit must be rejected.
	expectScenarioFailure(t, rec, func() { m.MethodCalled("Commit") })

	msg := rec.lastError()
	assert.Contains(t, msg, "#3")
	assert.Contains(t, msg, fmt.Sprintf("%q", "Commit"))
	assert.Contains(t, msg, fmt.Sprintf("%q", "stage1"))
	assert.Contains(t, msg, "not complete yet")

	assert.False(t, m.AssertExpectations(rec))
	assert.Contains(t, rec.joinedLogs(), fmt.Sprintf("%q", "stage1"))
}

func TestScenarioOverCountFails(t *testing.T) {
	t.Parallel()

	m := &Mock{}
	rec := &scenarioRecordingT{}
	m.Test(rec)
	newCommitScenario(m)

	m.MethodCalled("A")
	m.MethodCalled("A")
	// A is only expected twice in stage1.
	expectScenarioFailure(t, rec, func() { m.MethodCalled("A") })

	msg := rec.lastError()
	assert.Contains(t, msg, "#3")
	assert.Contains(t, msg, fmt.Sprintf("%q", "A"))
	assert.Contains(t, msg, fmt.Sprintf("%q", "stage1"))
	assert.Contains(t, msg, "call count exceeded")
}

func TestScenarioUnfinishedReportedAtAssert(t *testing.T) {
	t.Parallel()

	m := &Mock{}
	rec := &scenarioRecordingT{}
	m.Test(rec)
	newCommitScenario(m)

	m.MethodCalled("A")

	assert.False(t, m.AssertExpectations(rec))
	logs := rec.joinedLogs()
	assert.Contains(t, logs, fmt.Sprintf("%q", "stage1"))
	assert.Contains(t, logs, "after 1 call(s)")
	assert.Contains(t, logs, fmt.Sprintf("%q", "A"))
}

func TestScenarioOptionalExpectations(t *testing.T) {
	t.Parallel()

	newOptionalScenario := func(m *Mock) {
		scenario := NewScenario(m)
		scenario.Phase("stage1")
		scenario.On("A").Return().Once()
		scenario.On("Log").Return().Maybe()
		scenario.Phase("stage2")
		scenario.On("Commit").Return().Once()
		scenario.Freeze()
	}

	t.Run("optional calls never block advancement", func(t *testing.T) {
		t.Parallel()

		m := &Mock{}
		rec := &scenarioRecordingT{}
		m.Test(rec)
		newOptionalScenario(m)

		// Log is consumed while stage1 is active, then A closes the phase.
		m.MethodCalled("Log")
		m.MethodCalled("A")
		m.MethodCalled("Commit")

		assert.Zero(t, rec.errorfCount())
		assert.True(t, m.AssertExpectations(rec))
	})

	t.Run("optional calls may be skipped entirely", func(t *testing.T) {
		t.Parallel()

		m := &Mock{}
		rec := &scenarioRecordingT{}
		m.Test(rec)
		newOptionalScenario(m)

		m.MethodCalled("A")
		m.MethodCalled("Commit")

		assert.Zero(t, rec.errorfCount())
		assert.True(t, m.AssertExpectations(rec))
	})

	t.Run("closed phases reject optional calls", func(t *testing.T) {
		t.Parallel()

		m := &Mock{}
		rec := &scenarioRecordingT{}
		m.Test(rec)
		newOptionalScenario(m)

		m.MethodCalled("A")
		m.MethodCalled("Commit")
		// stage1 is closed, so its optional Log can no longer be consumed.
		expectScenarioFailure(t, rec, func() { m.MethodCalled("Log") })

		msg := rec.lastError()
		assert.Contains(t, msg, "#3")
		assert.Contains(t, msg, fmt.Sprintf("%q", "Log"))
		assert.Contains(t, msg, fmt.Sprintf("%q", "stage1"))
		assert.Contains(t, msg, "already closed")
	})
}

func TestScenarioDoubleFreezePanics(t *testing.T) {
	t.Parallel()

	m := &Mock{}
	scenario := NewScenario(m)
	scenario.Phase("stage1")
	scenario.On("A").Return().Once()
	scenario.Freeze()

	assert.PanicsWithValue(t, "mock: scenario is already frozen", func() {
		scenario.Freeze()
	})
}

func TestScenarioFreezeValidation(t *testing.T) {
	t.Parallel()

	t.Run("missing call count", func(t *testing.T) {
		t.Parallel()

		m := &Mock{}
		scenario := NewScenario(m)
		scenario.Phase("stage1")
		scenario.On("A").Return()

		assert.PanicsWithValue(t,
			"mock: scenario expectation \"A\" in phase \"stage1\" must declare a call count via Once/Times, or be marked optional via Maybe",
			func() { scenario.Freeze() })
	})

	t.Run("no phases", func(t *testing.T) {
		t.Parallel()

		scenario := NewScenario(&Mock{})
		assert.PanicsWithValue(t, "mock: scenario has no phases", func() {
			scenario.Freeze()
		})
	})

	t.Run("registration after freeze", func(t *testing.T) {
		t.Parallel()

		m := &Mock{}
		scenario := NewScenario(m)
		scenario.Phase("stage1")
		scenario.On("A").Return().Once()
		scenario.Freeze()

		assert.PanicsWithValue(t, "mock: scenario is frozen; cannot add phase \"stage2\"", func() {
			scenario.Phase("stage2")
		})
		assert.PanicsWithValue(t, "mock: scenario is frozen; cannot add expectation \"B\"", func() {
			scenario.On("B")
		})
	})
}

func TestScenarioPanicDiagnostics(t *testing.T) {
	t.Parallel()

	// No TestingT is attached, so a scenario violation must panic with a
	// diagnostic carrying the phase name and the actual call sequence number.
	m := &Mock{}
	newCommitScenario(m)

	m.MethodCalled("A")
	m.MethodCalled("B")

	var panicked interface{}
	func() {
		defer func() { panicked = recover() }()
		m.MethodCalled("Commit")
	}()

	msg, ok := panicked.(string)
	require.True(t, ok, "expected a string panic, got %T", panicked)
	assert.Contains(t, msg, "#3")
	assert.Contains(t, msg, fmt.Sprintf("%q", "Commit"))
	assert.Contains(t, msg, fmt.Sprintf("%q", "stage1"))
}

func TestScenarioConcurrentPhaseCompletion(t *testing.T) {
	t.Parallel()

	m := &Mock{}
	rec := &scenarioRecordingT{}
	m.Test(rec)
	newCommitScenario(m)

	m.MethodCalled("B")

	// The last two calls of stage1 race with each other; exactly one of them
	// must observe the phase transition, and both must be counted.
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.MethodCalled("A")
		}()
	}
	wg.Wait()

	m.MethodCalled("Commit")

	assert.Zero(t, rec.errorfCount())
	assert.True(t, m.AssertExpectations(rec))
	m.AssertNumberOfCalls(rec, "A", 2)
	m.AssertNumberOfCalls(rec, "Commit", 1)
}

func TestScenarioCoexistsWithPlainExpectationsAndNotBefore(t *testing.T) {
	t.Parallel()

	m := &Mock{}
	rec := &scenarioRecordingT{}
	m.Test(rec)

	scenario := NewScenario(m)
	scenario.Phase("stage1")
	a := scenario.On("A").Return("a").Once()
	scenario.Phase("stage2")
	scenario.On("Commit").Return().Once().NotBefore(a)
	scenario.Freeze()

	// Plain On/Return expectations outside the scenario stay ungated.
	m.On("Other").Return(42)

	assert.Equal(t, 42, m.MethodCalled("Other").Int(0))
	assert.Equal(t, "a", m.MethodCalled("A").String(0))
	m.MethodCalled("Commit")
	assert.Equal(t, 42, m.MethodCalled("Other").Int(0))

	assert.Zero(t, rec.errorfCount())
	assert.True(t, m.AssertExpectations(rec))
}

func ExampleScenario() {
	newMock := func() *Mock {
		m := &Mock{}
		scenario := NewScenario(m)
		scenario.Phase("stage1")
		scenario.On("A").Return().Twice()
		scenario.On("B").Return().Once()
		scenario.Phase("stage2")
		scenario.On("Commit").Return().Once()
		scenario.Freeze()
		return m
	}

	// Legal sequence: calls within stage1 interleave freely, then stage2
	// starts once every required call of stage1 is done.
	m := newMock()
	m.MethodCalled("A")
	m.MethodCalled("B")
	m.MethodCalled("A")
	m.MethodCalled("Commit")
	fmt.Println("legal sequence A, B, A, Commit: accepted")

	// Out-of-phase sequence: Commit arrives while stage1 still misses one A.
	m = newMock()
	m.MethodCalled("A")
	m.MethodCalled("B")
	func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Println("out-of-phase sequence A, B, Commit:", r)
			}
		}()
		m.MethodCalled("Commit")
	}()

	// Output:
	// legal sequence A, B, A, Commit: accepted
	// out-of-phase sequence A, B, Commit: mock: scenario: unexpected call #3 of "Commit": phase "stage1" is not complete yet
}
