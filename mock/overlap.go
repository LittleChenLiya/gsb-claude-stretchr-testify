package mock

import (
	"fmt"
	"reflect"
	"strings"
)

// OverlapLevel describes how certain a static overlap determination is.
type OverlapLevel int

const (
	// OverlapNone means the later expectation is not occluded by the earlier
	// one: there are calls the later expectation matches that the earlier one
	// can never take.
	OverlapNone OverlapLevel = iota

	// OverlapDefinite means the later expectation can never match: an unlimited
	// earlier expectation statically covers every call the later one could
	// match.
	OverlapDefinite

	// OverlapAfterExhaustion means the later expectation is shadowed by the
	// earlier one while the latter still has calls left, but becomes reachable
	// once the earlier one's finite repeatability is exhausted. An expectation
	// that is reachable after exhaustion is not reported as a permanent
	// occlusion.
	OverlapAfterExhaustion

	// OverlapUnknown means custom matchers (MatchedBy, AnythingOfType, IsType,
	// FunctionalOptions, ...) prevent a static determination. The matchers are
	// never executed, so an unknown overlap is never reported as a definite
	// conflict.
	OverlapUnknown
)

// String returns the name of the overlap level.
func (l OverlapLevel) String() string {
	switch l {
	case OverlapDefinite:
		return "definite occlusion"
	case OverlapAfterExhaustion:
		return "reachable after exhaustion"
	case OverlapUnknown:
		return "unknown (custom matcher)"
	default:
		return "none"
	}
}

// ExpectationOverlap describes the static relationship between two
// expectations registered on the same mock.
type ExpectationOverlap struct {
	// Mock is the mock the two expectations are registered on.
	Mock *Mock

	// Earlier is the expectation that was registered first.
	Earlier *Call

	// Later is the expectation that was registered after Earlier.
	Later *Call

	// Level is the certainty of the determination.
	Level OverlapLevel

	// Reason explains the occlusion condition that was found.
	Reason string
}

// String renders the full report: the two expectations, their registration
// positions, the occlusion condition and the certainty level.
func (o ExpectationOverlap) String() string {
	var b strings.Builder

	fmt.Fprintf(&b, "expectation overlap: %s\n", o.Level)
	fmt.Fprintf(&b, "\tearlier expectation: %s\n", callString(o.Earlier.Method, o.Earlier.Arguments, true))
	fmt.Fprintf(&b, "\t\tregistered at: %s\n", strings.Join(o.Earlier.callerInfo, "\n\t\t\t"))
	fmt.Fprintf(&b, "\t\trepeatability: %d (0 means unlimited)", o.Earlier.Repeatability)
	if o.Earlier.optional {
		fmt.Fprint(&b, ", optional")
	}
	b.WriteByte('\n')
	fmt.Fprintf(&b, "\tlater expectation:  %s\n", callString(o.Later.Method, o.Later.Arguments, true))
	fmt.Fprintf(&b, "\t\tregistered at: %s\n", strings.Join(o.Later.callerInfo, "\n\t\t\t"))
	fmt.Fprintf(&b, "\t\trepeatability: %d (0 means unlimited)", o.Later.Repeatability)
	if o.Later.optional {
		fmt.Fprint(&b, ", optional")
	}
	b.WriteByte('\n')
	fmt.Fprintf(&b, "\tcondition: %s\n", o.Reason)

	return b.String()
}

// CheckExpectationOverlaps performs a static analysis of all expectations
// registered on the given mocks and reports pairs where a later-registered
// expectation can never match, or can only match after an earlier expectation
// is exhausted, because of overlapping argument patterns.
//
// The analysis is purely static: it only considers method names, exact
// constant arguments, Anything and repeatability ranges. Custom matchers such
// as MatchedBy are never executed; when one is encountered the pair is
// reported as OverlapUnknown rather than as a definite conflict. Call
// selection and assertion behavior are not modified by this check.
//
// Typical usage:
//
//	mock.CheckExpectationOverlaps(t)
func CheckExpectationOverlaps(t TestingT, mocks ...*Mock) []ExpectationOverlap {
	if h, ok := t.(tHelper); ok {
		h.Helper()
	}

	var findings []ExpectationOverlap
	for _, m := range mocks {
		findings = append(findings, m.CheckExpectationOverlaps(t)...)
	}
	return findings
}

// CheckExpectationOverlaps performs the static overlap analysis for the
// expectations registered on this mock. See CheckExpectationOverlaps for a
// description of the analysis.
func (m *Mock) CheckExpectationOverlaps(t TestingT) []ExpectationOverlap {
	if h, ok := t.(tHelper); ok {
		h.Helper()
	}

	m.mutex.Lock()
	defer m.mutex.Unlock()

	var findings []ExpectationOverlap
	calls := m.expectedCalls()
	for i := 0; i < len(calls); i++ {
		for j := i + 1; j < len(calls); j++ {
			overlap := analyzeExpectationPair(calls[i], calls[j])
			if overlap.Level == OverlapNone {
				continue
			}
			overlap.Mock = m
			findings = append(findings, overlap)
		}
	}

	if t != nil {
		for _, finding := range findings {
			t.Logf("%s", finding.String())
		}
	}

	return findings
}

// argumentPatternKind classifies an expectation argument for the static
// overlap analysis.
type argumentPatternKind int

const (
	// argWildcard is Anything: it matches every value.
	argWildcard argumentPatternKind = iota

	// argExact is a constant compared with reflect.DeepEqual.
	argExact

	// argCustom is a custom matcher (MatchedBy, AnythingOfType, IsType,
	// FunctionalOptions, ...): static analysis cannot decide which values it
	// matches without executing it, which the check never does.
	argCustom
)

// classifyArgument classifies an expectation argument for the static
// overlap analysis.
func classifyArgument(arg interface{}) (argumentPatternKind, interface{}) {
	if arg == Anything {
		return argWildcard, nil
	}
	switch arg.(type) {
	case argumentMatcher, anythingOfTypeArgument, *IsTypeArgument, *FunctionalOptionsArgument:
		return argCustom, nil
	}
	return argExact, arg
}

// argumentOverlap describes the static relationship between the argument
// patterns of two expectations at one position.
type argumentOverlap int

const (
	// argCovered means the earlier expectation matches every value the later
	// expectation could match at this position.
	argCovered argumentOverlap = iota

	// argReachable means the later expectation matches values the earlier one
	// can never take at this position, so the later expectation is reachable
	// regardless of repeatability.
	argReachable

	// argUnknown means a custom matcher is involved and the position cannot be
	// decided statically.
	argUnknown
)

// compareArgumentPatterns compares the argument patterns of two expectations
// at one position. earlier is the argument of the expectation registered
// first, later the argument of the expectation registered after it.
func compareArgumentPatterns(earlier, later interface{}) argumentOverlap {
	earlierKind, _ := classifyArgument(earlier)
	laterKind, _ := classifyArgument(later)

	if earlierKind == argCustom || laterKind == argCustom {
		return argUnknown
	}
	if earlierKind == argWildcard {
		return argCovered
	}
	// earlierKind == argExact from here on.
	if laterKind == argWildcard {
		return argReachable
	}
	// Both sides are exact constants.
	if reflect.DeepEqual(earlier, later) {
		return argCovered
	}
	return argReachable
}

// analyzeExpectationPair statically determines whether the later expectation
// can still match given the earlier one, which was registered before it.
func analyzeExpectationPair(earlier, later *Call) ExpectationOverlap {
	overlap := ExpectationOverlap{Earlier: earlier, Later: later}

	if earlier.Method != later.Method {
		overlap.Level = OverlapNone
		overlap.Reason = fmt.Sprintf("different method names (%q vs %q)", earlier.Method, later.Method)
		return overlap
	}

	if len(earlier.Arguments) != len(later.Arguments) {
		overlap.Level = OverlapNone
		overlap.Reason = fmt.Sprintf("expectations declare a different number of arguments (%d vs %d), so no call can satisfy both",
			len(earlier.Arguments), len(later.Arguments))
		return overlap
	}

	hasUnknown := false
	for i := range earlier.Arguments {
		switch compareArgumentPatterns(earlier.Arguments[i], later.Arguments[i]) {
		case argReachable:
			overlap.Level = OverlapNone
			overlap.Reason = fmt.Sprintf("argument %d is matched by the later expectation but not by the earlier one, so the later expectation remains reachable", i)
			return overlap
		case argUnknown:
			hasUnknown = true
		}
	}

	if hasUnknown {
		overlap.Level = OverlapUnknown
		overlap.Reason = "a custom matcher (MatchedBy/AnythingOfType/IsType/FunctionalOptions) prevents a static determination; the matcher was not executed, so this is not a definite conflict"
		if earlier.Repeatability != 0 {
			overlap.Reason += "; the earlier expectation is finite, so the later one may become reachable once it is exhausted"
		}
		return overlap
	}

	// Every argument position is statically covered by the earlier
	// expectation. The repeatability range decides whether the later
	// expectation is permanently occluded or only shadowed until exhaustion.
	switch {
	case earlier.Repeatability == 0:
		overlap.Level = OverlapDefinite
		overlap.Reason = fmt.Sprintf("the earlier expectation is unlimited (Repeatability=0) and statically covers all %d argument position(s) of the later one, so the later expectation can never match",
			len(earlier.Arguments))
	case earlier.Repeatability > 0:
		overlap.Level = OverlapAfterExhaustion
		overlap.Reason = fmt.Sprintf("the earlier expectation is finite (Repeatability=%d); the later one is shadowed while it has calls left but becomes reachable once it is exhausted",
			earlier.Repeatability)
	default:
		overlap.Level = OverlapAfterExhaustion
		overlap.Reason = "the earlier expectation is already exhausted (Repeatability<0); the later one is reachable now"
	}
	return overlap
}
