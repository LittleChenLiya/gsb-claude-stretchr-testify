package mock

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingT is a TestingT that records failures instead of aborting the
// test, so that unexpected-call diagnostics can be asserted.
type recordingT struct {
	mu     sync.Mutex
	errors []string
	logs   []string
}

func (t *recordingT) Logf(format string, args ...interface{}) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.logs = append(t.logs, fmt.Sprintf(format, args...))
}

func (t *recordingT) Errorf(format string, args ...interface{}) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.errors = append(t.errors, fmt.Sprintf(format, args...))
}

// failNowSentinel is panicked by recordingT.FailNow to emulate the
// non-returning behaviour of testing.T.FailNow.
type failNowSentinel struct{}

func (t *recordingT) FailNow() { panic(failNowSentinel{}) }

// expectMockFail runs fn, recovering the sentinel panic raised by
// recordingT.FailNow when the mock reports a failure.
func expectMockFail(fn func()) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(failNowSentinel); !ok {
				panic(r)
			}
		}
	}()
	fn()
}

func (t *recordingT) recorded() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string{}, t.errors...)
}

func (t *recordingT) recordedLogs() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string{}, t.logs...)
}

// newProtocolMock builds a mock with a branch group modelling two mutually
// exclusive handshakes: Hello->Data(1) and Legacy->Data(2).
func newProtocolMock() (*Mock, *BranchGroup, *Branch, *Branch) {
	m := &Mock{}
	g := m.NewBranchGroup()
	modern := g.Branch()
	modern.On("Hello").Return("hi")
	modern.On("Data", 1).Return("ok")
	legacy := g.Branch()
	legacy.On("Legacy").Return("old")
	legacy.On("Data", 2).Return("done")
	return m, g, modern, legacy
}

func TestBranchGroupSelectsMatchingBranch(t *testing.T) {
	m, g, modern, legacy := newProtocolMock()

	assert.Equal(t, "old", m.MethodCalled("Legacy").String(0))
	assert.Equal(t, 1, g.SelectedBranch())

	assert.Equal(t, "done", m.MethodCalled("Data", 2).String(0))

	m.AssertExpectations(t)
	assert.Equal(t, 0, modern.calls[0].totalCalls, "unselected branch must not be consumed")
	assert.Equal(t, 1, legacy.calls[0].totalCalls)
	assert.Equal(t, 1, legacy.calls[1].totalCalls)
}

func TestBranchGroupCannotSwitchAfterSelection(t *testing.T) {
	m, g, modern, legacy := newProtocolMock()
	rt := &recordingT{}
	m.Test(rt)

	m.MethodCalled("Hello")
	require.Equal(t, 0, g.SelectedBranch())

	// Legacy belongs to the other branch: it must fail and must not switch.
	expectMockFail(func() { m.MethodCalled("Legacy") })

	errs := rt.recorded()
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "unexpected")
	assert.Contains(t, errs[0], "Candidate branches:")
	assert.Contains(t, errs[0], "next expected call is Data")
	assert.Equal(t, 0, g.SelectedBranch(), "selection must not switch")
	assert.Equal(t, 0, legacy.calls[0].totalCalls, "losing branch must not be consumed")

	// The selected branch is still consumable.
	m.MethodCalled("Data", 1)
	assert.Equal(t, 1, modern.calls[1].totalCalls)
}

func TestBranchGroupConcurrentSelection(t *testing.T) {
	for i := 0; i < 100; i++ {
		m, g, modern, legacy := newProtocolMock()
		rt := &recordingT{}
		m.Test(rt)

		// Barrier: both goroutines are released at the same time so that
		// Hello and Legacy race for the branch selection.
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			expectMockFail(func() { m.MethodCalled("Hello") })
		}()
		go func() {
			defer wg.Done()
			<-start
			expectMockFail(func() { m.MethodCalled("Legacy") })
		}()
		close(start)
		wg.Wait()

		selected := g.SelectedBranch()
		require.NotEqual(t, -1, selected, "iteration %d: exactly one branch must win", i)

		winner, loser := modern, legacy
		if selected == 1 {
			winner, loser = legacy, modern
		}
		assert.Equal(t, 1, winner.calls[0].totalCalls, "iteration %d: winner head consumed once", i)
		assert.Equal(t, 0, loser.calls[0].totalCalls, "iteration %d: loser head must not be consumed", i)
		assert.Equal(t, 0, loser.calls[0].Repeatability, "iteration %d: loser repeatability untouched", i)

		errs := rt.recorded()
		require.Len(t, errs, 1, "iteration %d: the losing call must produce one diagnostic", i)
		assert.Contains(t, errs[0], "Candidate branches:")
	}
}

func TestBranchGroupAmbiguousHeads(t *testing.T) {
	m := &Mock{}
	rt := &recordingT{}
	m.Test(rt)

	g := m.NewBranchGroup()
	b1 := g.Branch()
	b1.On("Hello").Return(1)
	b2 := g.Branch()
	b2.On("Hello").Return(2)

	expectMockFail(func() { m.MethodCalled("Hello") })

	errs := rt.recorded()
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "Ambiguous Method Call")
	assert.Contains(t, errs[0], "group 0, branch 0")
	assert.Contains(t, errs[0], "group 0, branch 1")
	assert.Equal(t, -1, g.SelectedBranch(), "ambiguous call must not select a branch")
	assert.Equal(t, 0, b1.calls[0].totalCalls)
	assert.Equal(t, 0, b2.calls[0].totalCalls)
}

func TestBranchGroupRunReentrant(t *testing.T) {
	m, g, modern, _ := newProtocolMock()

	modern.calls[0].Run(func(args Arguments) {
		// Re-entrant call into the same mock while Run executes: the mock
		// lock must not be held, so this consumes the next branch call.
		m.MethodCalled("Data", 1)
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		m.MethodCalled("Hello")
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("re-entrant Run callback deadlocked")
	}

	assert.Equal(t, 0, g.SelectedBranch())
	assert.Equal(t, 1, modern.calls[1].totalCalls)
	m.AssertExpectations(t)
}

func TestBranchGroupPanicAfterSelection(t *testing.T) {
	m := &Mock{}
	g := m.NewBranchGroup()
	b1 := g.Branch()
	b1.On("Hello").Panic("boom")
	b1.On("Data", 1)
	b2 := g.Branch()
	b2.On("Legacy")

	assert.PanicsWithValue(t, "boom", func() {
		m.MethodCalled("Hello")
	})

	assert.Equal(t, 0, g.SelectedBranch(), "selection happens before Panic triggers")
	assert.Equal(t, 1, b1.calls[0].totalCalls)
	assert.Equal(t, 0, b2.calls[0].totalCalls)
}

func TestBranchGroupTimesAndOnce(t *testing.T) {
	m := &Mock{}
	g := m.NewBranchGroup()
	b := g.Branch()
	b.On("Hello").Return("hi").Once()
	b.On("Data").Return("ok").Times(2)
	g.Branch().On("Legacy")

	m.MethodCalled("Hello")
	m.MethodCalled("Data")
	m.MethodCalled("Data")

	m.AssertExpectations(t)
	assert.Equal(t, -1, b.calls[0].Repeatability)
	assert.Equal(t, -1, b.calls[1].Repeatability)
}

func TestBranchGroupUnselectedGroupFailsAssert(t *testing.T) {
	m, _, _, _ := newProtocolMock()
	rt := &recordingT{}
	assert.False(t, m.AssertExpectations(rt), "a group that was never entered must fail by default")
	require.NotEmpty(t, rt.recorded())
	require.NotEmpty(t, rt.recordedLogs())
	assert.Contains(t, rt.recordedLogs()[0], "branch group 0 was not entered")
}

func TestBranchGroupOptionalGroupPassesAssert(t *testing.T) {
	m, g, _, _ := newProtocolMock()
	g.Optional()
	rt := &recordingT{}
	assert.True(t, m.AssertExpectations(rt), "an optional group that was never entered must pass")
	assert.Empty(t, rt.recorded())
}

func TestBranchGroupUnselectedBranchesAreNotRequired(t *testing.T) {
	m, _, _, _ := newProtocolMock()
	m.MethodCalled("Hello")
	m.MethodCalled("Data", 1)
	// Only the selected branch is asserted; the Legacy branch is ignored.
	m.AssertExpectations(t)
}

func TestBranchGroupEmptyBranchPanics(t *testing.T) {
	m := &Mock{}
	g := m.NewBranchGroup()
	g.Branch().On("Hello")
	g.Branch() // empty

	assert.PanicsWithValue(t,
		"mock: branch group contains an empty branch; add calls with Branch.On()",
		func() { m.MethodCalled("Hello") })
}

func TestBranchGroupDuplicateCallOwnershipPanics(t *testing.T) {
	m := &Mock{}
	g := m.NewBranchGroup()

	c := m.On("Hello")
	g.Branch(c)
	// The call was moved out of the regular expectations.
	assert.Empty(t, m.ExpectedCalls)

	assert.Panics(t, func() { g.Branch(c) }, "a call cannot belong to two branches")

	g2 := m.NewBranchGroup()
	assert.Panics(t, func() { g2.Branch(c) }, "a call cannot belong to two groups")
}

func TestBranchGroupNotBeforeCyclePanics(t *testing.T) {
	m := &Mock{}
	g := m.NewBranchGroup()
	b := g.Branch()
	hello := b.On("Hello")

	init := m.On("Init")
	hello.NotBefore(init)

	assert.PanicsWithValue(t,
		"mock: NotBefore creates a cyclic dependency involving a branch group call",
		func() { init.NotBefore(hello) })
}

func TestBranchGroupNotBeforeOutsideGroup(t *testing.T) {
	m := &Mock{}
	g := m.NewBranchGroup()
	b := g.Branch()
	b.On("Hello").NotBefore(m.On("Init"))
	b.On("Data", 1)
	g.Branch().On("Legacy")

	rt := &recordingT{}
	m.Test(rt)

	expectMockFail(func() { m.MethodCalled("Hello") })
	require.Len(t, rt.recorded(), 1, "Hello must not be accepted before Init")
	assert.Equal(t, -1, g.SelectedBranch())

	m.MethodCalled("Init")
	m.MethodCalled("Hello")
	m.MethodCalled("Data", 1)
	m.AssertExpectations(t)
}

// exampleTestingT forwards the first line of each log/error to stdout so the
// example output stays deterministic (caller paths are stripped).
type exampleTestingT struct{}

func (exampleTestingT) Logf(format string, args ...interface{}) {
	fmt.Println(strings.SplitN(fmt.Sprintf(format, args...), "\n", 2)[0])
}

func (exampleTestingT) Errorf(format string, args ...interface{}) {
	fmt.Println(strings.SplitN(fmt.Sprintf(format, args...), "\n", 2)[0])
}

func (exampleTestingT) FailNow() {}

func ExampleMock_NewBranchGroup() {
	t := exampleTestingT{}
	m := &Mock{}

	// Two mutually exclusive handshakes: modern Hello/Data and legacy
	// Legacy/Data. The first matching call picks the branch.
	group := m.NewBranchGroup()
	modern := group.Branch()
	modern.On("Hello").Return("hi")
	modern.On("Data", 1).Return("ok")
	legacy := group.Branch()
	legacy.On("Legacy").Return("old")
	legacy.On("Data", 2).Return("done")

	m.MethodCalled("Legacy")
	fmt.Println("selected branch:", group.SelectedBranch())

	// Data(2) was never called, so AssertExpectations reports the unmet
	// calls of the selected branch.
	if !m.AssertExpectations(t) {
		fmt.Println("expectations unmet")
	}

	// Output:
	// selected branch: 1
	// FAIL:	Data(int)
	// FAIL: 1 out of 2 expectation(s) were met.
	// expectations unmet
}
