package mock

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type snapshotPayload struct {
	Name  string
	Items []int
}

func TestArgumentSnapshot_PointerWithSliceKeepsCallTimeValues(t *testing.T) {
	m := new(Mock)
	m.EnableArgumentSnapshots()
	m.On("Save", Anything).Return().Once()

	payload := &snapshotPayload{Name: "before", Items: []int{1, 2, 3}}
	m.MethodCalled("Save", payload)

	// Mutate the original object after the call, through the same pointer
	// that was handed to the mock.
	payload.Name = "after"
	payload.Items[0] = 99

	// Assertions and the recorded call still observe the values as they were
	// when the call entered the mock.
	m.AssertCalled(t, "Save", &snapshotPayload{Name: "before", Items: []int{1, 2, 3}})
	m.AssertExpectations(t)

	require.Len(t, m.Calls, 1)
	recorded, ok := m.Calls[0].Arguments[0].(*snapshotPayload)
	require.True(t, ok)
	assert.NotSame(t, payload, recorded)
	assert.Equal(t, "before", recorded.Name)
	assert.Equal(t, []int{1, 2, 3}, recorded.Items)
}

func TestArgumentSnapshot_DisabledByDefault(t *testing.T) {
	m := new(Mock)
	m.On("Save", Anything).Return().Once()

	payload := &snapshotPayload{Name: "before", Items: []int{1, 2, 3}}
	m.MethodCalled("Save", payload)

	payload.Name = "after"
	payload.Items[0] = 99

	// Default mode keeps the original references, so the mutation is visible
	// and the assertion against the call-time values fails.
	mockT := new(MockTestingT)
	assert.False(t, m.AssertCalled(mockT, "Save", &snapshotPayload{Name: "before", Items: []int{1, 2, 3}}))
	assert.Same(t, payload, m.Calls[0].Arguments[0].(*snapshotPayload))
}

type snapshotNode struct {
	Value  int
	Next   *snapshotNode
	Shared *snapshotShared
}

type snapshotShared struct {
	N int
}

func TestArgumentSnapshot_PreservesCyclesAndSharedReferences(t *testing.T) {
	m := new(Mock)
	m.EnableArgumentSnapshots()
	m.On("Link", Anything, Anything).Return().Once()

	shared := &snapshotShared{N: 7}
	first := &snapshotNode{Value: 1, Shared: shared}
	second := &snapshotNode{Value: 2, Shared: shared}
	first.Next = second
	second.Next = first // cycle

	m.MethodCalled("Link", first, second)

	// Mutate everything reachable from the original arguments.
	first.Value = 100
	second.Value = 200
	shared.N = 70

	expectedShared := &snapshotShared{N: 7}
	expectedFirst := &snapshotNode{Value: 1, Shared: expectedShared}
	expectedSecond := &snapshotNode{Value: 2, Shared: expectedShared}
	expectedFirst.Next = expectedSecond
	expectedSecond.Next = expectedFirst

	m.AssertCalled(t, "Link", expectedFirst, expectedSecond)

	require.Len(t, m.Calls, 1)
	recordedFirst := m.Calls[0].Arguments[0].(*snapshotNode)
	recordedSecond := m.Calls[0].Arguments[1].(*snapshotNode)

	// The cycle survives the snapshot.
	assert.Same(t, recordedFirst, recordedFirst.Next.Next)
	assert.Same(t, recordedSecond, recordedFirst.Next)
	// The sub-object shared between the two arguments is still shared.
	assert.Same(t, recordedFirst.Shared, recordedSecond.Shared)
	assert.Equal(t, 7, recordedFirst.Shared.N)
}

func TestArgumentSnapshot_RunAndMatchingUseOriginalArguments(t *testing.T) {
	m := new(Mock)
	m.EnableArgumentSnapshots()
	m.On("Fill", &snapshotPayload{Name: "orig", Items: []int{1}}).Return().Once().
		Run(func(args Arguments) {
			arg := args.Get(0).(*snapshotPayload)
			arg.Name = "mutated-by-run"
			arg.Items[0] = 42
		})

	payload := &snapshotPayload{Name: "orig", Items: []int{1}}
	m.MethodCalled("Fill", payload)

	// Run received the original argument and mutated it.
	assert.Equal(t, "mutated-by-run", payload.Name)
	assert.Equal(t, []int{42}, payload.Items)

	// The snapshot was taken before Run executed, so the record still holds
	// the values as of entering the mock.
	m.AssertCalled(t, "Fill", &snapshotPayload{Name: "orig", Items: []int{1}})
	m.AssertExpectations(t)
}

func TestArgumentSnapshot_UnsupportedValuesFailWithoutConsumingExpectation(t *testing.T) {
	t.Run("func", func(t *testing.T) {
		m := new(Mock)
		m.EnableArgumentSnapshots()
		m.On("Do", Anything).Return().Once()

		assert.PanicsWithValue(t, mockTestingTFailNowCalled, func() {
			m.Test(new(MockTestingT))
			m.MethodCalled("Do", func() {})
		})

		// The failed call must not consume the Times allowance nor leave a
		// partially recorded call behind.
		assert.Empty(t, m.Calls)
		require.Len(t, m.ExpectedCalls, 1)
		assert.Equal(t, 1, m.ExpectedCalls[0].Repeatability)
		assert.Equal(t, 0, m.ExpectedCalls[0].totalCalls)
	})

	t.Run("chan", func(t *testing.T) {
		m := new(Mock)
		m.EnableArgumentSnapshots()
		m.On("Do", Anything).Return().Twice()

		assert.PanicsWithValue(t, mockTestingTFailNowCalled, func() {
			m.Test(new(MockTestingT))
			m.MethodCalled("Do", make(chan int))
		})

		assert.Empty(t, m.Calls)
		require.Len(t, m.ExpectedCalls, 1)
		assert.Equal(t, 2, m.ExpectedCalls[0].Repeatability)
		assert.Equal(t, 0, m.ExpectedCalls[0].totalCalls)
	})

	t.Run("unexported field", func(t *testing.T) {
		type withUnexported struct {
			Exported   int
			unexported int
		}
		m := new(Mock)
		m.EnableArgumentSnapshots()
		m.On("Do", Anything).Return().Once()

		assert.PanicsWithValue(t, mockTestingTFailNowCalled, func() {
			m.Test(new(MockTestingT))
			m.MethodCalled("Do", withUnexported{Exported: 1})
		})

		assert.Empty(t, m.Calls)
		require.Len(t, m.ExpectedCalls, 1)
		assert.Equal(t, 1, m.ExpectedCalls[0].Repeatability)
	})

	t.Run("nil func and chan are kept", func(t *testing.T) {
		m := new(Mock)
		m.EnableArgumentSnapshots()
		m.On("Do", Anything, Anything).Return().Once()

		var fn func()
		var ch chan int
		m.MethodCalled("Do", fn, ch)

		require.Len(t, m.Calls, 1)
		assert.Nil(t, m.Calls[0].Arguments[0])
		assert.Nil(t, m.Calls[0].Arguments[1])
		m.AssertExpectations(t)
	})
}

type snapshotCustomType struct {
	secret string
}

func TestArgumentSnapshot_CustomSnapshotFunc(t *testing.T) {
	RegisterSnapshotFunc(snapshotCustomType{}, func(value interface{}) (interface{}, error) {
		v := value.(snapshotCustomType)
		return snapshotCustomType{secret: v.secret + "-snapshotted"}, nil
	})
	defer UnregisterSnapshotFunc(snapshotCustomType{})

	m := new(Mock)
	m.EnableArgumentSnapshots()
	m.On("Do", Anything).Return().Once()

	m.MethodCalled("Do", snapshotCustomType{secret: "value"})

	require.Len(t, m.Calls, 1)
	assert.Equal(t, snapshotCustomType{secret: "value-snapshotted"}, m.Calls[0].Arguments[0])
	m.AssertCalled(t, "Do", snapshotCustomType{secret: "value-snapshotted"})
	m.AssertExpectations(t)
}

func TestArgumentSnapshot_CustomSnapshotFuncFailureDoesNotConsumeExpectation(t *testing.T) {
	snapshotErr := errors.New("cannot snapshot this value")
	RegisterSnapshotFunc(snapshotCustomType{}, func(interface{}) (interface{}, error) {
		return nil, snapshotErr
	})
	defer UnregisterSnapshotFunc(snapshotCustomType{})

	m := new(Mock)
	m.EnableArgumentSnapshots()
	m.On("Do", Anything).Return().Once()

	assert.PanicsWithValue(t, mockTestingTFailNowCalled, func() {
		m.Test(new(MockTestingT))
		m.MethodCalled("Do", snapshotCustomType{secret: "value"})
	})

	// The failed snapshot must not silently consume the expected call.
	assert.Empty(t, m.Calls)
	require.Len(t, m.ExpectedCalls, 1)
	assert.Equal(t, 1, m.ExpectedCalls[0].Repeatability)
	assert.Equal(t, 0, m.ExpectedCalls[0].totalCalls)
}

func TestArgumentSnapshot_CustomSnapshotFuncMustNotReturnNil(t *testing.T) {
	type ptrType struct{ N int }
	RegisterSnapshotFunc(&ptrType{}, func(interface{}) (interface{}, error) {
		return nil, nil // silently turning the value into nil is forbidden
	})
	defer UnregisterSnapshotFunc(&ptrType{})

	m := new(Mock)
	m.EnableArgumentSnapshots()
	m.On("Do", Anything).Return().Once()

	assert.PanicsWithValue(t, mockTestingTFailNowCalled, func() {
		m.Test(new(MockTestingT))
		m.MethodCalled("Do", &ptrType{N: 1})
	})

	assert.Empty(t, m.Calls)
	require.Len(t, m.ExpectedCalls, 1)
	assert.Equal(t, 1, m.ExpectedCalls[0].Repeatability)
}

func TestArgumentSnapshot_ConcurrentCalls(t *testing.T) {
	m := new(Mock)
	m.EnableArgumentSnapshots()
	m.On("Work", Anything).Return().Times(2)

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			payload := &snapshotPayload{Name: fmt.Sprintf("payload-%d", i), Items: []int{i}}
			m.MethodCalled("Work", payload)
			// Mutate right after the call; the snapshot must be unaffected.
			payload.Name = "mutated"
			payload.Items[0] = -1
		}(i)
	}
	wg.Wait()

	require.Len(t, m.Calls, 2)
	seen := map[string]bool{}
	for _, call := range m.Calls {
		payload := call.Arguments[0].(*snapshotPayload)
		seen[payload.Name] = true
		assert.NotEqual(t, "mutated", payload.Name)
		assert.NotEqual(t, []int{-1}, payload.Items)
	}
	assert.True(t, seen["payload-0"])
	assert.True(t, seen["payload-1"])
	m.AssertCalled(t, "Work", &snapshotPayload{Name: "payload-0", Items: []int{0}})
	m.AssertCalled(t, "Work", &snapshotPayload{Name: "payload-1", Items: []int{1}})
	m.AssertExpectations(t)
}

type snapshotOrderedArg struct {
	ID int
}

func TestArgumentSnapshot_RecordsFollowMockEntryOrder(t *testing.T) {
	var mu sync.Mutex
	var snapshotOrder []int
	RegisterSnapshotFunc(snapshotOrderedArg{}, func(value interface{}) (interface{}, error) {
		v := value.(snapshotOrderedArg)
		mu.Lock()
		snapshotOrder = append(snapshotOrder, v.ID)
		mu.Unlock()
		return v, nil
	})
	defer UnregisterSnapshotFunc(snapshotOrderedArg{})

	m := new(Mock)
	m.EnableArgumentSnapshots()
	m.On("Seq", Anything).Return()

	const calls = 8
	var wg sync.WaitGroup
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			m.MethodCalled("Seq", snapshotOrderedArg{ID: i})
		}(i)
	}
	wg.Wait()

	// Each call is snapshotted exactly once, while the mock is entered, so the
	// recorded calls appear in the same order as the snapshots were taken.
	require.Len(t, m.Calls, calls)
	require.Len(t, snapshotOrder, calls)
	for i, call := range m.Calls {
		assert.Equal(t, snapshotOrder[i], call.Arguments[0].(snapshotOrderedArg).ID)
	}
}

func ExampleMock_EnableArgumentSnapshots() {
	type request struct {
		ID   int
		Tags []string
	}

	// Default mode: the mock records the original pointer, so mutations made
	// after the call are visible in the recorded call.
	plain := new(Mock)
	plain.On("Save", Anything).Return()
	req := &request{ID: 1, Tags: []string{"a"}}
	plain.MethodCalled("Save", req)
	req.ID = 2
	req.Tags[0] = "b"
	fmt.Println("default:", plain.Calls[0].Arguments[0])

	// Snapshot mode: the mock records an isolated copy taken when the call
	// entered the mock, so later mutations are not visible.
	snapping := new(Mock)
	snapping.EnableArgumentSnapshots()
	snapping.On("Save", Anything).Return()
	req = &request{ID: 1, Tags: []string{"a"}}
	snapping.MethodCalled("Save", req)
	req.ID = 2
	req.Tags[0] = "b"
	fmt.Println("snapshot:", snapping.Calls[0].Arguments[0])

	// Output:
	// default: &{2 [b]}
	// snapshot: &{1 [a]}
}
