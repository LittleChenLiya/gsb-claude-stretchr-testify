package mock

import "fmt"

// stageExpectation tracks a single expectation registered in a stage. All
// fields are guarded by the parent Mock's mutex.
type stageExpectation struct {
	call     *Call
	stageIdx int
	min      int // completions required before the stage may advance
	max      int // maximum number of times the expectation may be consumed
	consumed int
}

// scenarioStage is a named group of expectations. Calls belonging to the same
// stage may be made in any order, but the next stage only becomes active once
// every required expectation of the current stage has been satisfied.
type scenarioStage struct {
	name         string
	expectations []*stageExpectation
}

func (s *scenarioStage) complete() bool {
	for _, exp := range s.expectations {
		if exp.consumed < exp.min {
			return false
		}
	}
	return true
}

// StageScenario enforces explicit execution stages on top of a Mock.
//
// A scenario is built from named stages, each holding a number of
// expectations with their own call counts. Within a stage, calls may happen
// in any order, but a call belonging to a later stage is rejected until every
// required expectation of the current stage has been consumed. Optional
// expectations (see [Call.Maybe]) never block stage advancement, and once a
// stage is closed its remaining optional counts can no longer be consumed.
//
// The scenario must be committed with [StageScenario.Freeze] after
// registration; it is only enforced once frozen. All scenario state is
// guarded by the parent Mock's mutex, so concurrent calls are counted
// consistently. Scenario diagnostics only mention the method name and the
// actual call sequence number; argument formatting is left to the regular
// Mock machinery.
//
//	scenario := mock.NewStageScenario()
//	scenario.Stage("prepare")
//	scenario.Expect("A").Return(nil).Times(2)
//	scenario.Expect("B").Return(nil).Once()
//	scenario.Stage("commit")
//	scenario.Expect("Commit").Return(nil).Once()
//	scenario.Freeze()
type StageScenario struct {
	mock    *Mock
	stages  []*scenarioStage
	frozen  bool
	current int // index of the active stage; == len(stages) when finished
	seq     int // actual number of calls observed since Freeze
}

// NewStageScenario creates a stage scenario bound to the mock. It panics if
// the mock already has a scenario.
func (m *Mock) NewStageScenario() *StageScenario {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	if m.scenario != nil {
		panic("mock: a StageScenario is already registered on this Mock")
	}
	s := &StageScenario{mock: m}
	m.scenario = s
	return s
}

// Stage appends a new named stage to the scenario. Expectations registered
// with [StageScenario.Expect] are added to the most recently declared stage.
// It panics if the scenario is already frozen or the name is empty.
func (s *StageScenario) Stage(name string) *StageScenario {
	s.mock.mutex.Lock()
	defer s.mock.mutex.Unlock()
	if s.frozen {
		panic("mock: cannot add a stage to a frozen StageScenario")
	}
	if name == "" {
		panic("mock: stage name must not be empty")
	}
	s.stages = append(s.stages, &scenarioStage{name: name})
	return s
}

// Expect registers an expectation in the current stage and returns the
// regular [Call], so Return, Times, Once, Maybe, NotBefore and the other
// chainable modifiers keep working. The call count defaults to 1 and is
// taken from [Call.Times] (or [Call.Once]/[Call.Twice]) when the scenario is
// frozen. An expectation marked with [Call.Maybe] is optional: it does not
// block stage advancement and can be consumed at most its declared count of
// times, only while its stage is active.
//
// Expect panics if no stage has been declared yet or the scenario is frozen.
func (s *StageScenario) Expect(methodName string, arguments ...interface{}) *Call {
	s.mock.mutex.Lock()
	defer s.mock.mutex.Unlock()
	if s.frozen {
		panic("mock: cannot add an expectation to a frozen StageScenario")
	}
	if len(s.stages) == 0 {
		panic("mock: declare a stage with Stage() before calling Expect()")
	}
	// On re-locks the mutex, so register the call while not holding it.
	s.mock.mutex.Unlock()
	call := s.mock.On(methodName, arguments...)
	s.mock.mutex.Lock()

	stage := s.stages[len(s.stages)-1]
	exp := &stageExpectation{call: call, stageIdx: len(s.stages) - 1}
	stage.expectations = append(stage.expectations, exp)
	call.stageExp = exp
	return call
}

// Freeze commits the scenario. No stages or expectations may be registered
// afterwards, and from this point on every call to the mock is checked
// against the scenario. Freezing an already frozen scenario panics.
func (s *StageScenario) Freeze() {
	s.mock.mutex.Lock()
	defer s.mock.mutex.Unlock()
	if s.frozen {
		panic("mock: StageScenario is already frozen")
	}
	if len(s.stages) == 0 {
		panic("mock: cannot freeze a StageScenario without stages")
	}
	for _, stage := range s.stages {
		for _, exp := range stage.expectations {
			count := exp.call.Repeatability
			if count <= 0 {
				count = 1
			}
			if exp.call.optional {
				exp.min, exp.max = 0, count
			} else {
				exp.min, exp.max = count, count
			}
			// The scenario enforces the counts itself, so hand the call
			// back to the regular machinery as unlimited and let the
			// scenario be the single source of diagnostics.
			exp.call.Repeatability = 0
			exp.call.optional = true
		}
	}
	s.frozen = true
	// Skip stages that are complete from the start (all expectations
	// optional).
	s.advance()
}

// advance moves past every completed stage. Must be called with the mock
// mutex held.
func (s *StageScenario) advance() {
	for s.current < len(s.stages) && s.stages[s.current].complete() {
		s.current++
	}
}

func (s *StageScenario) currentStageName() string {
	if s.current >= len(s.stages) {
		return "<finished>"
	}
	return s.stages[s.current].name
}

// checkCall validates a matched call against the scenario. It returns an
// empty string when the call is allowed, or a diagnostic message describing
// the violation. Must be called with the mock mutex held.
func (s *StageScenario) checkCall(call *Call) string {
	s.seq++
	seq := s.seq
	exp := call.stageExp
	if exp == nil {
		// Not governed by the scenario.
		return ""
	}
	stage := s.stages[exp.stageIdx]
	switch {
	case exp.stageIdx < s.current:
		return fmt.Sprintf("mock: stage scenario violation at call #%d: method %q belongs to stage %q, which is already closed (current stage: %q)",
			seq, call.Method, stage.name, s.currentStageName())
	case exp.stageIdx > s.current:
		return fmt.Sprintf("mock: stage scenario violation at call #%d: method %q of stage %q was called before stage %q was completed",
			seq, call.Method, stage.name, s.currentStageName())
	case exp.consumed >= exp.max:
		return fmt.Sprintf("mock: stage scenario violation at call #%d: method %q of stage %q exceeded its allowed %d call(s)",
			seq, call.Method, stage.name, exp.max)
	}
	exp.consumed++
	s.advance()
	return ""
}

// unfinished returns a diagnostic line for every required expectation that
// was not fully consumed. Must be called with the mock mutex held.
func (s *StageScenario) unfinished() []string {
	var reasons []string
	for _, stage := range s.stages {
		for _, exp := range stage.expectations {
			if exp.consumed < exp.min {
				reasons = append(reasons, fmt.Sprintf("FAIL: stage %q incomplete after call #%d: method %q was called %d of %d required time(s)",
					stage.name, s.seq, exp.call.Method, exp.consumed, exp.min))
			}
		}
	}
	return reasons
}
