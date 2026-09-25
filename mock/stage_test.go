package mock

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

// recordingTestingT is a TestingT implementation that records everything
// instead of aborting the test, so scenario failures can be asserted without
// killing the test process.
type recordingTestingT struct {
	mu     sync.Mutex
	logs   []string
	errors []string
	failed bool
}

func (r *recordingTestingT) Helper() {}

func (r *recordingTestingT) Logf(format string, args ...interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, fmt.Sprintf(format, args...))
}

func (r *recordingTestingT) Errorf(format string, args ...interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

func (r *recordingTestingT) FailNow() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failed = true
}

func (r *recordingTestingT) lastError() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.errors) == 0 {
		return ""
	}
	return r.errors[len(r.errors)-1]
}

// newCommitScenario builds the acceptance scenario: stage "prepare" expects
// A twice and B once, stage "commit" expects Commit once.
func newCommitScenario(m *Mock) *StageScenario {
	scenario := m.NewStageScenario()
	scenario.Stage("prepare")
	scenario.Expect("A").Return("a").Times(2)
	scenario.Expect("B").Return("b").Once()
	scenario.Stage("commit")
	scenario.Expect("Commit").Return("c").Once()
	scenario.Freeze()
	return scenario
}

func TestStageScenario_InterleavedCallsAdvance(t *testing.T) {
	t.Parallel()

	m := &Mock{}
	rt := new(recordingTestingT)
	m.Test(rt)
	scenario := newCommitScenario(m)

	assert.Equal(t, 0, scenario.current)

	// Calls within a stage may be interleaved freely: A, B, A.
	assert.Equal(t, "a", m.MethodCalled("A").String(0))
	assert.Equal(t, 0, scenario.current, "stage must not advance before B")

	assert.Equal(t, "b", m.MethodCalled("B").String(0))
	assert.Equal(t, 0, scenario.current, "stage must not advance before the second A")

	assert.Equal(t, "a", m.MethodCalled("A").String(0))
	assert.Equal(t, 1, scenario.current, "stage must advance once A x2 and B x1 are done")

	assert.Equal(t, "c", m.MethodCalled("Commit").String(0))
	assert.Equal(t, 2, scenario.current, "all stages must be finished")

	assert.True(t, m.AssertExpectations(rt))
	assert.False(t, rt.failed)
	assert.Equal(t, 4, scenario.seq)
}

func TestStageScenario_PrematureCallFails(t *testing.T) {
	t.Parallel()

	m := &Mock{}
	rt := new(recordingTestingT)
	m.Test(rt)
	scenario := newCommitScenario(m)

	m.MethodCalled("A")
	m.MethodCalled("B")
	assert.Equal(t, 0, scenario.current)

	// One A is still missing, so Commit is out of stage.
	assert.Nil(t, m.MethodCalled("Commit"))

	assert.True(t, rt.failed)
	msg := rt.lastError()
	assert.Contains(t, msg, "call #3")
	assert.Contains(t, msg, "\"Commit\"")
	assert.Contains(t, msg, "\"commit\"")
	assert.Contains(t, msg, "\"prepare\"")

	// The violation must not consume anything.
	assert.Equal(t, 0, scenario.current)
	assert.Equal(t, 0, scenario.stages[1].expectations[0].consumed)

	// The scenario can still be completed afterwards.
	m.MethodCalled("A")
	assert.Equal(t, 1, scenario.current)
	m.MethodCalled("Commit")
	assert.True(t, m.AssertExpectations(rt))
}

func TestStageScenario_ViolationReportsMethodAndSeqOnly(t *testing.T) {
	t.Parallel()

	m := &Mock{}
	rt := new(recordingTestingT)
	m.Test(rt)

	scenario := m.NewStageScenario()
	scenario.Stage("prepare")
	scenario.Expect("A").Return(nil).Once()
	scenario.Stage("commit")
	scenario.Expect("Commit", Anything).Return(nil).Once()
	scenario.Freeze()

	m.MethodCalled("Commit", "top-secret-argument")

	msg := rt.lastError()
	assert.Contains(t, msg, "call #1")
	assert.Contains(t, msg, "\"Commit\"")
	assert.NotContains(t, msg, "top-secret-argument",
		"scenario errors must only contain the method name and call sequence number")
}

func TestStageScenario_OverCountFails(t *testing.T) {
	t.Parallel()

	m := &Mock{}
	rt := new(recordingTestingT)
	m.Test(rt)
	scenario := newCommitScenario(m)

	m.MethodCalled("A")
	m.MethodCalled("A")
	// B is still pending, so the stage is active but A is exhausted.
	assert.Nil(t, m.MethodCalled("A"))

	msg := rt.lastError()
	assert.Contains(t, msg, "call #3")
	assert.Contains(t, msg, "\"A\"")
	assert.Contains(t, msg, "\"prepare\"")
	assert.Contains(t, msg, "exceeded")
	assert.Equal(t, 2, scenario.stages[0].expectations[0].consumed)
}

func TestStageScenario_OptionalExpectations(t *testing.T) {
	t.Parallel()

	newScenario := func(m *Mock) *StageScenario {
		scenario := m.NewStageScenario()
		scenario.Stage("prepare")
		scenario.Expect("A").Return(nil).Once()
		scenario.Expect("Cache").Return(nil).Maybe().Once()
		scenario.Stage("commit")
		scenario.Expect("Commit").Return(nil).Once()
		scenario.Freeze()
		return scenario
	}

	t.Run("optional does not block advancement", func(t *testing.T) {
		m := &Mock{}
		rt := new(recordingTestingT)
		m.Test(rt)
		scenario := newScenario(m)

		m.MethodCalled("A")
		assert.Equal(t, 1, scenario.current, "optional Cache must not block the stage")
		m.MethodCalled("Commit")
		assert.True(t, m.AssertExpectations(rt))
		assert.False(t, rt.failed)
	})

	t.Run("optional consumed while active", func(t *testing.T) {
		m := &Mock{}
		rt := new(recordingTestingT)
		m.Test(rt)
		newScenario(m)

		m.MethodCalled("Cache")
		m.MethodCalled("A")
		m.MethodCalled("Commit")
		assert.True(t, m.AssertExpectations(rt))
		assert.False(t, rt.failed)
	})

	t.Run("closed stage cannot consume optional", func(t *testing.T) {
		m := &Mock{}
		rt := new(recordingTestingT)
		m.Test(rt)
		scenario := newScenario(m)

		m.MethodCalled("A")
		assert.Equal(t, 1, scenario.current)
		assert.Nil(t, m.MethodCalled("Cache"))

		msg := rt.lastError()
		assert.Contains(t, msg, "call #2")
		assert.Contains(t, msg, "\"Cache\"")
		assert.Contains(t, msg, "already closed")
	})

	t.Run("optional over count fails", func(t *testing.T) {
		m := &Mock{}
		rt := new(recordingTestingT)
		m.Test(rt)
		newScenario(m)

		m.MethodCalled("Cache")
		assert.Nil(t, m.MethodCalled("Cache"))
		assert.Contains(t, rt.lastError(), "exceeded")
	})
}

func TestStageScenario_DoubleFreezePanics(t *testing.T) {
	t.Parallel()

	m := &Mock{}
	scenario := m.NewStageScenario()
	scenario.Stage("prepare")
	scenario.Expect("A").Return(nil).Once()
	scenario.Freeze()

	assert.PanicsWithValue(t, "mock: StageScenario is already frozen", func() {
		scenario.Freeze()
	})
	assert.PanicsWithValue(t, "mock: cannot add a stage to a frozen StageScenario", func() {
		scenario.Stage("late")
	})
	assert.PanicsWithValue(t, "mock: cannot add an expectation to a frozen StageScenario", func() {
		scenario.Expect("B")
	})
	assert.PanicsWithValue(t, "mock: a StageScenario is already registered on this Mock", func() {
		m.NewStageScenario()
	})
}

func TestStageScenario_PanicDiagnostics(t *testing.T) {
	t.Parallel()

	m := &Mock{}
	rt := new(recordingTestingT)
	m.Test(rt)

	scenario := m.NewStageScenario()
	scenario.Stage("prepare")
	scenario.Expect("A").Return(nil).Once().Panic("boom")
	scenario.Expect("B").Return(nil).Once()
	scenario.Stage("commit")
	scenario.Expect("Commit").Return(nil).Once()
	scenario.Freeze()

	assert.PanicsWithValue(t, "boom", func() {
		m.MethodCalled("A")
	})

	// The panicking call was still consumed exactly once.
	assert.Equal(t, 1, scenario.stages[0].expectations[0].consumed)
	assert.Equal(t, 0, scenario.current, "B is still pending")

	// Diagnostics keep working after the panic.
	assert.False(t, m.AssertExpectations(rt))
	assert.Contains(t, rt.logs[0], "stage \"prepare\" incomplete after call #1: method \"B\" was called 0 of 1 required time(s)")

	// And the scenario can still be driven to completion.
	m.MethodCalled("B")
	m.MethodCalled("Commit")
	assert.True(t, m.AssertExpectations(rt))
}

func TestStageScenario_ConcurrentFinalCalls(t *testing.T) {
	t.Parallel()

	m := &Mock{}
	rt := new(recordingTestingT)
	m.Test(rt)

	scenario := m.NewStageScenario()
	scenario.Stage("prepare")
	scenario.Expect("A").Return(nil).Times(2)
	scenario.Stage("commit")
	scenario.Expect("Commit").Return(nil).Once()
	scenario.Freeze()

	// The last two calls of the stage race each other; the stage must
	// advance exactly once and both calls must be counted.
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.MethodCalled("A")
		}()
	}
	wg.Wait()

	assert.Equal(t, 2, scenario.stages[0].expectations[0].consumed)
	assert.Equal(t, 1, scenario.current)

	m.MethodCalled("Commit")
	assert.True(t, m.AssertExpectations(rt))
	assert.False(t, rt.failed)
}

func TestStageScenario_ConcurrentLoad(t *testing.T) {
	t.Parallel()

	m := &Mock{}
	rt := new(recordingTestingT)
	m.Test(rt)

	scenario := m.NewStageScenario()
	scenario.Stage("prepare")
	scenario.Expect("A").Return(nil).Times(10)
	scenario.Expect("B").Return(nil).Times(10)
	scenario.Stage("commit")
	scenario.Expect("Commit").Return(nil).Once()
	scenario.Freeze()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			m.MethodCalled("A")
		}()
		go func() {
			defer wg.Done()
			m.MethodCalled("B")
		}()
	}
	wg.Wait()

	assert.Equal(t, 10, scenario.stages[0].expectations[0].consumed)
	assert.Equal(t, 10, scenario.stages[0].expectations[1].consumed)
	assert.Equal(t, 1, scenario.current)

	m.MethodCalled("Commit")
	assert.True(t, m.AssertExpectations(rt))
	assert.False(t, rt.failed)
	assert.Equal(t, 21, scenario.seq)
}

func TestStageScenario_NotBeforeAndPlainExpectationsUnaffected(t *testing.T) {
	t.Parallel()

	m := &Mock{}
	mt := new(MockTestingT)
	m.Test(mt)
	scenario := newCommitScenario(m)

	// Plain expectations on the same mock keep working, including NotBefore.
	first := m.On("First").Return(nil).Once()
	m.On("Plain").Return("ok").NotBefore(first)

	// NotBefore is still enforced while a scenario is frozen. MockTestingT
	// panics on FailNow, mimicking how testing.T aborts the call.
	assert.PanicsWithValue(t, mockTestingTFailNowCalled, func() {
		m.MethodCalled("Plain")
	})
	assert.Equal(t, 1, mt.errorfCount)

	// Untagged calls are not governed by the scenario.
	m.MethodCalled("First")
	assert.Equal(t, "ok", m.MethodCalled("Plain").String(0))

	// The scenario itself is untouched and still completes.
	m.MethodCalled("A")
	m.MethodCalled("B")
	m.MethodCalled("A")
	m.MethodCalled("Commit")
	assert.Equal(t, 2, scenario.current)
	assert.True(t, m.AssertExpectations(mt))
}

func TestStageScenario_UnfinishedAtEnd(t *testing.T) {
	t.Parallel()

	m := &Mock{}
	rt := new(recordingTestingT)
	m.Test(rt)
	newCommitScenario(m)

	m.MethodCalled("A")

	assert.False(t, m.AssertExpectations(rt))
	assert.Len(t, rt.logs, 3)
	assert.Contains(t, rt.logs[0], "stage \"prepare\" incomplete after call #1: method \"A\" was called 1 of 2 required time(s)")
	assert.Contains(t, rt.logs[1], "stage \"prepare\" incomplete after call #1: method \"B\" was called 0 of 1 required time(s)")
	assert.Contains(t, rt.logs[2], "stage \"commit\" incomplete after call #1: method \"Commit\" was called 0 of 1 required time(s)")
	assert.NotEmpty(t, rt.errors)
}

// ExampleMock_NewStageScenario demonstrates a legal call sequence and an
// out-of-stage sequence for the same scenario.
func ExampleMock_NewStageScenario() {
	newScenario := func(m *Mock) {
		scenario := m.NewStageScenario()
		scenario.Stage("prepare")
		scenario.Expect("A").Return(nil).Times(2)
		scenario.Expect("B").Return(nil).Once()
		scenario.Stage("commit")
		scenario.Expect("Commit").Return(nil).Once()
		scenario.Freeze()
	}

	// Legal sequence: calls within a stage may interleave freely.
	m := &Mock{}
	rt := new(recordingTestingT)
	m.Test(rt)
	newScenario(m)
	for _, method := range []string{"A", "B", "A", "Commit"} {
		m.MethodCalled(method)
		fmt.Printf("%s ok\n", method)
	}
	fmt.Println("expectations met:", m.AssertExpectations(rt))

	// Out-of-stage sequence: Commit before the second A is rejected, and the
	// unfinished stages are reported with the stage name and call number.
	m2 := &Mock{}
	rt2 := new(recordingTestingT)
	m2.Test(rt2)
	newScenario(m2)
	m2.MethodCalled("A")
	m2.MethodCalled("Commit")
	for _, err := range rt2.errors {
		fmt.Println("recorded:", err)
	}
	m2.AssertExpectations(rt2)
	for _, log := range rt2.logs {
		fmt.Println("recorded:", log)
	}

	// Output:
	// A ok
	// B ok
	// A ok
	// Commit ok
	// expectations met: true
	// recorded: mock: stage scenario violation at call #2: method "Commit" of stage "commit" was called before stage "prepare" was completed
	// recorded: FAIL: stage "prepare" incomplete after call #2: method "A" was called 1 of 2 required time(s)
	// recorded: FAIL: stage "prepare" incomplete after call #2: method "B" was called 0 of 1 required time(s)
	// recorded: FAIL: stage "commit" incomplete after call #2: method "Commit" was called 0 of 1 required time(s)
}
