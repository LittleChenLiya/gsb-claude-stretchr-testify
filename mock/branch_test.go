package mock

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/*
	Branch group tests
*/

type branchTestService struct {
	Mock
}

func (s *branchTestService) Hello() string {
	return s.Called().String(0)
}

func (s *branchTestService) Legacy() string {
	return s.Called().String(0)
}

func (s *branchTestService) Data(n int) string {
	return s.Called(n).String(0)
}

// newHelloLegacyService builds a mock with a branch group whose branches are
// "Hello then Data(1)" and "Legacy then Data(1)".
func newHelloLegacyService() (*branchTestService, *BranchGroup) {
	svc := new(branchTestService)
	group := svc.BranchGroup()

	modern := group.Branch()
	modern.On("Hello").Return("hi")
	modern.On("Data", 1).Return("ok")

	legacy := group.Branch()
	legacy.On("Legacy").Return("old")
	legacy.On("Data", 1).Return("ok")

	return svc, group
}

// recordingTestingT captures failure output so tests can assert on the
// diagnostics produced for unexpected or ambiguous calls.
type recordingTestingT struct {
	MockTestingT
	messages []string
}

func (r *recordingTestingT) Errorf(format string, args ...interface{}) {
	r.messages = append(r.messages, fmt.Sprintf(format, args...))
	r.MockTestingT.Errorf(format, args...)
}

func TestBranchGroup_SelectsMatchingBranch(t *testing.T) {
	svc, group := newHelloLegacyService()

	assert.Equal(t, -1, group.Selected())

	assert.Equal(t, "old", svc.Legacy())
	assert.Equal(t, 1, group.Selected(), "calling Legacy must select the second branch")

	assert.Equal(t, "ok", svc.Data(1))
	svc.AssertExpectations(t)
}

func TestBranchGroup_CannotSwitchAfterSelection(t *testing.T) {
	svc, group := newHelloLegacyService()
	tt := new(recordingTestingT)
	svc.Test(tt)

	assert.Equal(t, "hi", svc.Hello())
	require.Equal(t, 0, group.Selected())

	// Legacy belongs to the other branch: it must fail and must not switch
	// the selection.
	assert.PanicsWithValue(t, mockTestingTFailNowCalled, func() {
		svc.Legacy()
	})
	assert.Equal(t, 0, group.Selected(), "a failed call must not switch the selected branch")
	require.Len(t, tt.messages, 1)
	assert.Contains(t, tt.messages[0], "the method call was unexpected")
	assert.Contains(t, tt.messages[0], "group 0: branch 0 is selected")

	// The selected branch is still fully consumable.
	assert.Equal(t, "ok", svc.Data(1))
	svc.AssertExpectations(t)
}

func TestBranchGroup_ConcurrentSelection(t *testing.T) {
	for i := 0; i < 200; i++ {
		svc, group := newHelloLegacyService()
		tt := new(recordingTestingT)
		svc.Test(tt)

		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		call := func(fn func() string) {
			defer wg.Done()
			<-start
			defer func() {
				// The losing call fails the mocked test, which panics.
				_ = recover()
			}()
			fn()
		}
		go call(svc.Hello)
		go call(svc.Legacy)
		close(start)
		wg.Wait()

		selected := group.Selected()
		require.NotEqual(t, -1, selected, "iteration %d: exactly one branch must win the race", i)
		assert.Equal(t, 1, tt.errorfCount, "iteration %d: the losing call must produce one diagnostic", i)
		require.Len(t, tt.messages, 1)
		assert.Contains(t, tt.messages[0], "Branch group candidate call(s):")

		// The winning branch''s head call must have been consumed exactly
		// once; the losing call must not have deducted anything.
		winner := group.branches[selected]
		assert.Equal(t, 1, winner.calls[0].totalCalls, "iteration %d", i)
		assert.Equal(t, 1, winner.head, "iteration %d", i)
		loser := group.branches[1-selected]
		assert.Equal(t, 0, loser.calls[0].totalCalls, "iteration %d", i)
		assert.Equal(t, 0, loser.head, "iteration %d", i)

		// The selected branch can still be completed.
		assert.Equal(t, "ok", svc.Data(1))
		svc.AssertExpectations(t)
	}
}

func TestBranchGroup_AmbiguousFirstCall(t *testing.T) {
	svc := new(branchTestService)
	tt := new(recordingTestingT)
	svc.Test(tt)

	group := svc.BranchGroup()
	b1 := group.Branch()
	b1.On("Hello").Return("one")
	b2 := group.Branch()
	b2.On("Hello").Return("two")

	assert.PanicsWithValue(t, mockTestingTFailNowCalled, func() {
		svc.Hello()
	})
	require.Len(t, tt.messages, 1)
	assert.Contains(t, tt.messages[0], "mock: Ambiguous Method Call")
	assert.Contains(t, tt.messages[0], "group 0 branch 0")
	assert.Contains(t, tt.messages[0], "group 0 branch 1")
	assert.Equal(t, -1, group.Selected(), "an ambiguous call must not select a branch")
	assert.Equal(t, 0, b1.calls[0].totalCalls)
	assert.Equal(t, 0, b2.calls[0].totalCalls)
}

func TestBranchGroup_RunCallbackReentrancy(t *testing.T) {
	svc := new(branchTestService)

	group := svc.BranchGroup()
	b := group.Branch()
	b.On("Hello").Return("hi").Run(func(args Arguments) {
		// Re-enter the mock from inside the callback: this must not
		// deadlock, and it consumes the next expectation of the selected
		// branch.
		svc.Data(1)
	})
	b.On("Data", 1).Return("ok")

	assert.Equal(t, "hi", svc.Hello())
	assert.Equal(t, 0, group.Selected())
	svc.AssertExpectations(t)
}

func TestBranchGroup_PanicAfterSelection(t *testing.T) {
	svc := new(branchTestService)

	group := svc.BranchGroup()
	b1 := group.Branch()
	b1.On("Hello").Return("hi")
	b1.On("Data", 1).Panic("boom")
	b2 := group.Branch()
	b2.On("Legacy").Return("old")

	assert.Equal(t, "hi", svc.Hello())
	require.Equal(t, 0, group.Selected())

	assert.PanicsWithValue(t, "boom", func() {
		svc.Data(1)
	})
	assert.Equal(t, 0, group.Selected(), "a panicking call must not lose the selection")
	// Data was admitted (and panicked) so the selected branch is fully
	// consumed; the unselected branch is ignored.
	svc.AssertExpectations(t)
}

func TestBranchGroup_TimesDeductedOnAdmission(t *testing.T) {
	svc := new(branchTestService)
	tt := new(recordingTestingT)
	svc.Test(tt)

	group := svc.BranchGroup()
	b := group.Branch()
	head := b.On("Hello").Return("hi").Times(2)
	b.On("Data", 1).Return("ok").Once()

	assert.Equal(t, "hi", svc.Hello())
	assert.Equal(t, 1, head.Repeatability)
	assert.Equal(t, "hi", svc.Hello())
	assert.Equal(t, -1, head.Repeatability)

	// A third Hello is no longer acceptable: the head has moved on.
	assert.PanicsWithValue(t, mockTestingTFailNowCalled, func() {
		svc.Hello()
	})
	assert.Equal(t, 2, head.totalCalls, "the rejected call must not be deducted again")

	assert.Equal(t, "ok", svc.Data(1))
	svc.AssertExpectations(t)
}

func TestBranchGroup_UnselectedGroupFailsAssertion(t *testing.T) {
	svc, _ := newHelloLegacyService()
	tt := new(MockTestingT)

	assert.False(t, svc.AssertExpectations(tt), "a group in which no branch was selected must fail")
	assert.Equal(t, 1, tt.errorfCount)
}

func TestBranchGroup_OptionalGroupMayBeUnselected(t *testing.T) {
	svc, group := newHelloLegacyService()
	group.Optional()

	svc.AssertExpectations(t)
}

func TestBranchGroup_EmptyBranchRejected(t *testing.T) {
	svc := new(branchTestService)
	group := svc.BranchGroup()
	group.Branch()

	assert.Panics(t, func() {
		group.Branch()
	}, "adding a branch after an empty branch must be rejected")
}

func TestBranchGroup_TrailingEmptyBranchRejectedOnUse(t *testing.T) {
	svc := new(branchTestService)
	group := svc.BranchGroup()
	b := group.Branch()
	b.On("Hello").Return("hi")
	group.Branch() // left empty

	assert.Panics(t, func() {
		svc.Hello()
	})
	assert.Panics(t, func() {
		svc.AssertExpectations(new(MockTestingT))
	})
}

func TestBranchGroup_DuplicateCallOwnershipRejected(t *testing.T) {
	svc := new(branchTestService)
	group := svc.BranchGroup()
	b1 := group.Branch()
	shared := b1.On("Hello").Return("hi")
	b2 := group.Branch()
	b2.On("Legacy").Return("old")
	// Simulate a call registered in two branches.
	b2.calls = append(b2.calls, shared)

	assert.Panics(t, func() {
		svc.Hello()
	})
}

func TestBranchGroup_NotBeforeCycleRejected(t *testing.T) {
	svc := new(branchTestService)
	group := svc.BranchGroup()
	b := group.Branch()
	hello := b.On("Hello").Return("hi")

	outside := svc.On("Init").Return()
	hello.NotBefore(outside)
	outside.NotBefore(hello) // cycle: Hello -> Init -> Hello

	assert.Panics(t, func() {
		svc.Hello()
	})
}

func TestBranchGroup_NotBeforeOutsideGroupRespected(t *testing.T) {
	svc := new(branchTestService)
	tt := new(recordingTestingT)
	svc.Test(tt)

	group := svc.BranchGroup()
	b := group.Branch()
	hello := b.On("Hello").Return("hi")
	b.On("Data", 1).Return("ok")

	init := svc.On("Init").Return()
	hello.NotBefore(init)

	// Hello before Init must be rejected.
	assert.PanicsWithValue(t, mockTestingTFailNowCalled, func() {
		svc.Hello()
	})
	assert.Equal(t, -1, group.Selected())

	svc.MethodCalled("Init")
	assert.Equal(t, "hi", svc.Hello())
	assert.Equal(t, "ok", svc.Data(1))
	svc.AssertExpectations(t)
}

func TestBranchGroup_UnselectedBranchesNotRequired(t *testing.T) {
	svc, group := newHelloLegacyService()

	assert.Equal(t, "hi", svc.Hello())
	assert.Equal(t, "ok", svc.Data(1))

	// The Legacy branch was never selected: its expectations must not be
	// required for the assertion to pass.
	require.Equal(t, 0, group.Selected())
	svc.AssertExpectations(t)
}

func TestBranchGroup_MocksWithoutGroupsUnchanged(t *testing.T) {
	svc := new(branchTestService)
	svc.On("Hello").Return("hi").Once()
	svc.On("Data", 1).Return("ok")

	assert.Equal(t, "hi", svc.Hello())
	assert.Equal(t, "ok", svc.Data(1))
	svc.AssertExpectations(t)
}

// ExampleMock_BranchGroup is a 90-second tour of branch groups: declare two
// mutually exclusive branches, let the first matching call select one, and
// print the selection result together with the still unmet calls.
func ExampleMock_BranchGroup() {
	m := &Mock{}
	group := m.BranchGroup()

	modern := group.Branch()
	modern.On("Hello").Return("hi")
	modern.On("Data", 1).Return("ok")

	legacy := group.Branch()
	legacy.On("Legacy").Return("old")
	legacy.On("Data", 1).Return("ok")

	// The first call that matches a branch head selects that branch.
	m.MethodCalled("Legacy")
	fmt.Println("selected branch:", group.Selected())
	fmt.Println("unmet calls:", group.UnmetExpectations())

	m.MethodCalled("Data", 1)
	fmt.Println("unmet calls:", group.UnmetExpectations())

	// Output:
	// selected branch: 1
	// unmet calls: [Data(int)]
	// unmet calls: []
}
