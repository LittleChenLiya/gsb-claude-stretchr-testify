package require

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

type stateSequenceClock struct{ ch chan time.Time }

func (c *stateSequenceClock) NewTicker(time.Duration) assert.Ticker {
	return &stateSequenceTicker{ch: c.ch}
}

type stateSequenceTicker struct{ ch chan time.Time }

func (t *stateSequenceTicker) C() <-chan time.Time { return t.ch }
func (t *stateSequenceTicker) Stop()               {}

func TestEventuallyStateSequence(t *testing.T) {
	var calls int32
	clock := &stateSequenceClock{ch: make(chan time.Time, 4)}
	defer close(clock.ch)
	sampler := func() (interface{}, error) {
		n := atomic.AddInt32(&calls, 1)
		switch {
		case n == 1:
			return "ready", nil
		case n == 2:
			return "working", nil
		default:
			return "done", nil
		}
	}
	states := []assert.StateMatcherFunc{
		func(state interface{}) bool { return state == "ready" },
		func(state interface{}) bool { return state == "working" },
		func(state interface{}) bool { return state == "done" },
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		EventuallyStateSequence(t, context.Background(), sampler, states, []assert.StateSequenceOption{
			assert.WithStateSequenceClock(clock),
		})
	}()
	for i := 0; i < 3; i++ {
		clock.ch <- time.Time{}
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("EventuallyStateSequence did not complete")
	}
	Equal(t, int32(3), atomic.LoadInt32(&calls))
}

func TestEventuallyStateSequenceEmptyStates(t *testing.T) {
	mockT := new(MockT)
	sampler := func() (interface{}, error) { return "ready", nil }

	EventuallyStateSequence(mockT, context.Background(), sampler, nil, nil)

	if !mockT.Failed {
		t.Fatal("FailNow must be called when no state matcher was provided")
	}
}
