package mock

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/*
	Test objects
*/

type snapshotPayload struct {
	Name string
	Tags []string
	Meta map[string]interface{}
}

type snapshotNode struct {
	Value int
	Next  *snapshotNode
}

type snapshotFuncHolder struct {
	Fn func()
}

type snapshotCustom struct {
	Secret string
}

type snapshotPrivate struct {
	Public  string
	private string
}

type snapshotMockService struct {
	Mock
}

func (s *snapshotMockService) Process(p *snapshotPayload) {
	s.Called(p)
}

func (s *snapshotMockService) ProcessNode(n *snapshotNode) {
	s.Called(n)
}

func (s *snapshotMockService) ProcessPair(a, b *snapshotNode) {
	s.Called(a, b)
}

func (s *snapshotMockService) ProcessHolder(h *snapshotFuncHolder) {
	s.Called(h)
}

func (s *snapshotMockService) ProcessCustom(c *snapshotCustom) {
	s.Called(c)
}

func (s *snapshotMockService) ProcessAnything(v interface{}) {
	s.Called(v)
}

// snapshotErrorRecorder is a TestingT that captures error output so tests
// can inspect failure messages.
type snapshotErrorRecorder struct {
	errors []string
}

func (snapshotErrorRecorder) Helper() {}

func (r *snapshotErrorRecorder) Logf(string, ...interface{}) {}

func (r *snapshotErrorRecorder) Errorf(format string, args ...interface{}) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

func (r *snapshotErrorRecorder) FailNow() {
	panic(mockTestingTFailNowCalled)
}

/*
	Snapshot mode behavior through the mock
*/

func TestArgumentSnapshots_PointerArgumentMutatedAfterCall(t *testing.T) {
	t.Parallel()

	service := new(snapshotMockService)
	service.EnableArgumentSnapshots()

	original := &snapshotPayload{Name: "first", Tags: []string{"a", "b"}}
	service.On("Process", original).Return().Run(func(args Arguments) {
		// Run still receives the original argument and may mutate it.
		args.Get(0).(*snapshotPayload).Name = "mutated-by-run"
	}).Once()

	service.Process(original)

	// The Run callback mutated the original object.
	require.Equal(t, "mutated-by-run", original.Name)

	// Mutate the original object after the call, including the slice.
	original.Name = "changed-later"
	original.Tags[0] = "z"
	original.Tags = append(original.Tags, "c")

	// The recorded call still holds the values from call time.
	require.Len(t, service.Calls, 1)
	recorded, ok := service.Calls[0].Arguments.Get(0).(*snapshotPayload)
	require.True(t, ok)
	assert.Equal(t, "first", recorded.Name)
	assert.Equal(t, []string{"a", "b"}, recorded.Tags)
	assert.NotSame(t, original, recorded)

	// AssertCalled matches against the snapshot, not the mutated object.
	service.AssertCalled(t, "Process", &snapshotPayload{Name: "first", Tags: []string{"a", "b"}})
	service.AssertExpectations(t)
}

func TestArgumentSnapshots_DefaultModeStillSeesMutations(t *testing.T) {
	t.Parallel()

	service := new(snapshotMockService)
	service.On("Process", Anything).Return()

	original := &snapshotPayload{Name: "first", Tags: []string{"a"}}
	service.Process(original)
	original.Name = "changed-later"

	// Without snapshot mode the record is the very same pointer.
	recorded := service.Calls[0].Arguments.Get(0).(*snapshotPayload)
	assert.Same(t, original, recorded)
	assert.Equal(t, "changed-later", recorded.Name)

	// ... so asserting against the call-time value fails.
	tt := new(MockTestingT)
	assert.False(t, service.AssertCalled(tt, "Process", &snapshotPayload{Name: "first", Tags: []string{"a"}}))
	assert.Equal(t, 1, tt.errorfCount)
}

func TestArgumentSnapshots_CircularStructure(t *testing.T) {
	t.Parallel()

	service := new(snapshotMockService)
	service.EnableArgumentSnapshots()
	service.On("ProcessNode", Anything).Return()

	a := &snapshotNode{Value: 1}
	b := &snapshotNode{Value: 2}
	a.Next = b
	b.Next = a // cycle

	service.ProcessNode(a)

	// Mutate the original list after the call.
	a.Value = 100
	b.Next = nil

	recorded := service.Calls[0].Arguments.Get(0).(*snapshotNode)
	assert.NotSame(t, a, recorded)
	assert.Equal(t, 1, recorded.Value)
	require.NotNil(t, recorded.Next)
	assert.Equal(t, 2, recorded.Next.Value)
	assert.True(t, recorded.Next.Next == recorded, "the cycle must be preserved in the snapshot")
}

func TestArgumentSnapshots_SharedReferencesPreserved(t *testing.T) {
	t.Parallel()

	service := new(snapshotMockService)
	service.EnableArgumentSnapshots()
	service.On("ProcessPair", Anything, Anything).Return()

	shared := &snapshotNode{Value: 7}
	service.ProcessPair(shared, shared)
	shared.Value = 99

	first := service.Calls[0].Arguments.Get(0).(*snapshotNode)
	second := service.Calls[0].Arguments.Get(1).(*snapshotNode)
	assert.Equal(t, 7, first.Value)
	assert.NotSame(t, shared, first)
	assert.True(t, first == second, "arguments that shared a reference must share the snapshot copy")
}

func TestArgumentSnapshots_UnsupportedValuesFailWithoutConsuming(t *testing.T) {
	service := new(snapshotMockService)
	service.EnableArgumentSnapshots()
	tt := new(snapshotErrorRecorder)
	service.Test(tt)

	call := service.On("ProcessHolder", Anything).Return().Once()

	holder := &snapshotFuncHolder{Fn: func() {}}
	assert.PanicsWithValue(t, mockTestingTFailNowCalled, func() {
		service.ProcessHolder(holder)
	})

	require.Len(t, tt.errors, 1)
	assert.Contains(t, tt.errors[0], "cannot be snapshotted")
	assert.Contains(t, tt.errors[0], "RegisterSnapshotFunc")

	// The failed call must not consume the Times allowance nor leave a record.
	assert.Equal(t, 1, call.Repeatability)
	assert.Equal(t, 0, call.totalCalls)
	assert.Empty(t, service.Calls)

	// Registering a custom snapshot function for the type fixes the call.
	RegisterSnapshotFunc(&snapshotFuncHolder{}, func(interface{}) (interface{}, error) {
		return &snapshotFuncHolder{}, nil
	})
	defer UnregisterSnapshotFunc(&snapshotFuncHolder{})

	service.ProcessHolder(holder)
	assert.Equal(t, 1, call.totalCalls)
	assert.Len(t, service.Calls, 1)
	service.AssertExpectations(t)
}

func TestArgumentSnapshots_CustomSnapshotFuncErrorDoesNotConsume(t *testing.T) {
	service := new(snapshotMockService)
	service.EnableArgumentSnapshots()
	tt := new(snapshotErrorRecorder)
	service.Test(tt)

	call := service.On("ProcessCustom", Anything).Return().Times(2)

	RegisterSnapshotFunc(&snapshotCustom{}, func(interface{}) (interface{}, error) {
		return nil, errors.New("boom")
	})
	defer UnregisterSnapshotFunc(&snapshotCustom{})

	assert.PanicsWithValue(t, mockTestingTFailNowCalled, func() {
		service.ProcessCustom(&snapshotCustom{Secret: "s3cr3t"})
	})

	require.Len(t, tt.errors, 1)
	assert.Contains(t, tt.errors[0], "boom")

	// Neither of the two allowed calls was consumed and nothing was recorded.
	assert.Equal(t, 2, call.Repeatability)
	assert.Equal(t, 0, call.totalCalls)
	assert.Empty(t, service.Calls)
}

func TestArgumentSnapshots_CustomSnapshotFuncResultIsRecorded(t *testing.T) {
	service := new(snapshotMockService)
	service.EnableArgumentSnapshots()

	var snapshotCalls int
	RegisterSnapshotFunc(&snapshotCustom{}, func(v interface{}) (interface{}, error) {
		snapshotCalls++
		return &snapshotCustom{Secret: "redacted"}, nil
	})
	defer UnregisterSnapshotFunc(&snapshotCustom{})

	service.On("ProcessCustom", Anything).Return().Once()
	service.ProcessCustom(&snapshotCustom{Secret: "s3cr3t"})

	assert.Equal(t, 1, snapshotCalls, "exactly one snapshot must be taken per call")
	recorded := service.Calls[0].Arguments.Get(0).(*snapshotCustom)
	assert.Equal(t, "redacted", recorded.Secret)
	service.AssertCalled(t, "ProcessCustom", &snapshotCustom{Secret: "redacted"})
}

func TestArgumentSnapshots_ConcurrentCallsRecordedInEntryOrder(t *testing.T) {
	t.Parallel()

	service := new(snapshotMockService)
	service.EnableArgumentSnapshots()

	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})

	service.On("Process", Anything).Return().Run(func(args Arguments) {
		p := args.Get(0).(*snapshotPayload)
		if p.Name == "first" {
			close(firstEntered)
			<-releaseFirst
		}
		// Run still mutates the original objects.
		p.Name = "touched-by-run"
	}).Times(2)

	first := &snapshotPayload{Name: "first", Tags: []string{"x"}}
	second := &snapshotPayload{Name: "second", Tags: []string{"y"}}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		service.Process(first)
	}()
	// Wait until the first call has been recorded and is blocked in Run,
	// guaranteeing it entered the mock before the second call.
	<-firstEntered
	go func() {
		defer wg.Done()
		service.Process(second)
	}()
	close(releaseFirst)
	wg.Wait()

	// Run mutated both original objects.
	assert.Equal(t, "touched-by-run", first.Name)
	assert.Equal(t, "touched-by-run", second.Name)

	// Records hold call-time values, ordered by entry into the mock.
	service.AssertNumberOfCalls(t, "Process", 2)
	require.Len(t, service.Calls, 2)
	recordedFirst := service.Calls[0].Arguments.Get(0).(*snapshotPayload)
	recordedSecond := service.Calls[1].Arguments.Get(0).(*snapshotPayload)
	assert.Equal(t, "first", recordedFirst.Name)
	assert.Equal(t, []string{"x"}, recordedFirst.Tags)
	assert.Equal(t, "second", recordedSecond.Name)
	assert.Equal(t, []string{"y"}, recordedSecond.Tags)

	service.AssertExpectations(t)
}

func TestArgumentSnapshots_DisableRestoresDefault(t *testing.T) {
	t.Parallel()

	service := new(snapshotMockService)
	service.EnableArgumentSnapshots()
	service.DisableArgumentSnapshots()
	service.On("Process", Anything).Return()

	original := &snapshotPayload{Name: "first"}
	service.Process(original)
	original.Name = "changed-later"

	recorded := service.Calls[0].Arguments.Get(0).(*snapshotPayload)
	assert.Same(t, original, recorded)
}

/*
	Snapshot engine unit tests
*/

func TestSnapshotArguments_BasicKindsAndNils(t *testing.T) {
	t.Parallel()

	snapped, err := snapshotArguments(Arguments{nil, 42, "str", true, 3.14, []int(nil), map[string]int(nil), (*snapshotNode)(nil)})
	require.NoError(t, err)
	assert.Equal(t, Arguments{nil, 42, "str", true, 3.14, []int(nil), map[string]int(nil), (*snapshotNode)(nil)}, snapped)
}

func TestSnapshotArguments_DeepIndependence(t *testing.T) {
	t.Parallel()

	original := &snapshotPayload{
		Name: "x",
		Tags: []string{"a"},
		Meta: map[string]interface{}{"k": []int{1, 2}, "n": &snapshotNode{Value: 5}},
	}
	snapped, err := snapshotArguments(Arguments{original})
	require.NoError(t, err)

	copied := snapped[0].(*snapshotPayload)
	original.Tags[0] = "b"
	original.Meta["k"].([]int)[0] = 99
	original.Meta["n"].(*snapshotNode).Value = 6

	assert.Equal(t, []string{"a"}, copied.Tags)
	assert.Equal(t, []int{1, 2}, copied.Meta["k"])
	assert.Equal(t, 5, copied.Meta["n"].(*snapshotNode).Value)
}

func TestSnapshotArguments_ArraysAndInterfaces(t *testing.T) {
	t.Parallel()

	arr := [2]*snapshotNode{{Value: 1}, {Value: 2}}
	var iface interface{} = &snapshotNode{Value: 3}
	snapped, err := snapshotArguments(Arguments{arr, iface})
	require.NoError(t, err)

	arr[0].Value = 10
	iface.(*snapshotNode).Value = 30

	snappedArr := snapped[0].([2]*snapshotNode)
	assert.Equal(t, 1, snappedArr[0].Value)
	assert.Equal(t, 3, snapped[1].(*snapshotNode).Value)
}

func TestSnapshotArguments_UnsupportedKinds(t *testing.T) {
	t.Parallel()

	_, err := snapshotArguments(Arguments{func() {}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "func")

	_, err = snapshotArguments(Arguments{make(chan int)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "chan")

	_, err = snapshotArguments(Arguments{snapshotPrivate{Public: "x", private: "y"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexported field")
}

func TestSnapshotArguments_UnsupportedValuesAreNotSilentlyNiled(t *testing.T) {
	t.Parallel()

	// A func nested deep inside a supported structure must still surface an
	// error instead of being replaced with nil.
	payload := &snapshotPayload{Meta: map[string]interface{}{"cb": func() {}}}
	snapped, err := snapshotArguments(Arguments{payload})
	require.Error(t, err)
	assert.Nil(t, snapped)
}

func TestRegisterSnapshotFunc_PanicsOnNilArguments(t *testing.T) {
	t.Parallel()

	assert.PanicsWithValue(t, "mock: RegisterSnapshotFunc requires a non-nil sample value", func() {
		RegisterSnapshotFunc(nil, func(interface{}) (interface{}, error) { return nil, nil })
	})
	assert.PanicsWithValue(t, "mock: RegisterSnapshotFunc requires a non-nil snapshot function", func() {
		RegisterSnapshotFunc(&snapshotCustom{}, nil)
	})
}

/*
	Example
*/

func ExampleMock_EnableArgumentSnapshots() {
	// Default mode: the recorded call keeps pointing at the original object,
	// so later mutations leak into the call record.
	plain := new(snapshotMockService)
	plain.On("Process", Anything).Return()
	p1 := &snapshotPayload{Name: "original", Tags: []string{"a"}}
	plain.Process(p1)
	p1.Name = "mutated"
	p1.Tags[0] = "b"
	fmt.Println("default mode:", plain.Calls[0].Arguments.Get(0).(*snapshotPayload))

	// Snapshot mode: the recorded call freezes the values from call time.
	snapping := new(snapshotMockService)
	snapping.EnableArgumentSnapshots()
	snapping.On("Process", Anything).Return()
	p2 := &snapshotPayload{Name: "original", Tags: []string{"a"}}
	snapping.Process(p2)
	p2.Name = "mutated"
	p2.Tags[0] = "b"
	fmt.Println("snapshot mode:", snapping.Calls[0].Arguments.Get(0).(*snapshotPayload))

	// Output:
	// default mode: &{mutated [b] map[]}
	// snapshot mode: &{original [a] map[]}
}
