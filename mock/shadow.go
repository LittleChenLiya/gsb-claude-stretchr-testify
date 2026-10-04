package mock

import (
	"fmt"
	"strings"

	"github.com/stretchr/testify/assert"
)

/*
	Static expectation overlap analysis

	The functions in this file perform a conservative, purely static check of
	expectations registered with [Mock.On]. They never invoke a [MatchedBy]
	matcher (or any other custom matcher) and they never change the way calls
	are dispatched or asserted; they only report expectations which an earlier
	registration can prevent from ever being selected by the mock's normal
	call-selection logic in [Mock.findExpectedCall].
*/

// Certainty describes how sure a static expectation-overlap analysis is about
// its conclusion.
type Certainty int

const (
	// CertaintyDefinite means every matcher involved was understood statically
	// and the conclusion follows without running any matcher.
	CertaintyDefinite Certainty = iota

	// CertaintyUnknown means at least one argument matcher could not be
	// evaluated statically (for example [MatchedBy]). No matcher was executed,
	// so the report must not be treated as a proven conflict.
	CertaintyUnknown
)

// String returns the name of the certainty level.
func (c Certainty) String() string {
	switch c {
	case CertaintyDefinite:
		return "definite"
	case CertaintyUnknown:
		return "unknown"
	default:
		return fmt.Sprintf("certainty(%d)", int(c))
	}
}

// ExpectationShadow describes a single later-registered expectation
// ("shadowed") that can be hidden by an earlier-registered expectation
// ("shadowing") because the earlier expectation matches every call the later
// one matches and is inspected first by call dispatch.
//
// The fields are snapshots taken at analysis time; they are not updated if the
// expectations are used afterwards.
type ExpectationShadow struct {
	// Shadowing is the earlier-registered expectation considered first during
	// call dispatch.
	Shadowing *Call

	// Shadowed is the later-registered expectation under analysis.
	Shadowed *Call

	// ShadowingIndex and ShadowedIndex are the zero-based positions of the two
	// expectations in [Mock.ExpectedCalls] registration order.
	ShadowingIndex int
	ShadowedIndex  int

	// ShadowingRegistration and ShadowedRegistration are the file:line
	// locations captured for the two [Mock.On] registrations.
	ShadowingRegistration string
	ShadowedRegistration  string

	// RemainingMatches is the number of additional calls the shadowing
	// expectation can still accept at analysis time. It is zero when
	// Unlimited is true.
	RemainingMatches int

	// Unlimited reports that the shadowing expectation can be matched an
	// unlimited number of times. Such an expectation never becomes exhausted,
	// so the shadowed expectation can never be selected.
	Unlimited bool

	// ReachableAfterExhaustion reports that the shadowing expectation accepts a
	// finite number of calls, so the shadowed expectation can be selected once
	// that budget has been spent.
	ReachableAfterExhaustion bool

	// ShadowingOptional and ShadowedOptional report the [Call.Maybe] state of
	// the two expectations.
	ShadowingOptional bool
	ShadowedOptional  bool

	// Certainty is CertaintyDefinite only when the argument overlap could be
	// proven statically; it is CertaintyUnknown when a custom matcher (such as
	// [MatchedBy]) forced a conservative conclusion without being executed.
	Certainty Certainty
}

// Condition returns a human-readable description of why the shadowing
// expectation covers the shadowed one and whether the shadowed expectation can
// ever be selected.
func (s ExpectationShadow) Condition() string {
	var b strings.Builder
	fmt.Fprintf(&b, "expectation %d (%s)", s.ShadowingIndex, callString(s.Shadowing.Method, s.Shadowing.Arguments, false))
	fmt.Fprintf(&b, " is registered before expectation %d (%s)", s.ShadowedIndex, callString(s.Shadowed.Method, s.Shadowed.Arguments, false))
	if s.Certainty == CertaintyUnknown {
		b.WriteString(" and, as far as can be told without executing a custom matcher such as MatchedBy, it matches every argument the later expectation accepts")
	} else {
		b.WriteString(" and matches every argument the later expectation accepts")
	}
	switch {
	case s.Unlimited:
		if s.Certainty == CertaintyUnknown {
			b.WriteString("; the overlap cannot be proven statically, so the later expectation is only possibly unreachable")
		} else {
			b.WriteString("; the earlier expectation matches an unlimited number of calls, so the later expectation can never be selected")
		}
	case s.ReachableAfterExhaustion:
		fmt.Fprintf(&b, "; the earlier expectation can still match %d more call(s), after which it is exhausted and the later expectation becomes reachable", s.RemainingMatches)
	default:
		b.WriteString("; the earlier expectation is already exhausted, so the later expectation is reachable")
	}
	return b.String()
}

// String renders a full report containing both registration locations, the
// certainty level and the shadowing condition.
func (s ExpectationShadow) String() string {
	var b strings.Builder
	b.WriteString("mock: expectation overlap report\n")
	fmt.Fprintf(&b, "\tshadowing expectation (registered first): %s\n", callString(s.Shadowing.Method, s.Shadowing.Arguments, true))
	fmt.Fprintf(&b, "\t\tregistered at: %s\n", registrationSite(s.Shadowing))
	fmt.Fprintf(&b, "\tshadowed expectation  (registered later): %s\n", callString(s.Shadowed.Method, s.Shadowed.Arguments, true))
	fmt.Fprintf(&b, "\t\tregistered at: %s\n", registrationSite(s.Shadowed))
	fmt.Fprintf(&b, "\tcertainty: %s\n", s.Certainty)
	fmt.Fprintf(&b, "\tcondition: %s\n", s.Condition())
	return b.String()
}

func registrationSite(c *Call) string {
	if len(c.callerInfo) > 0 {
		return c.callerInfo[0]
	}
	return "<unknown>"
}

// staticArgKind classifies an expectation argument for the conservative static
// analysis. Only exact constant values and Anything are understood.
type staticArgKind int

const (
	argConstant staticArgKind = iota
	argAnything
	argUnknownMatcher
)

func classifyStaticArg(arg interface{}) staticArgKind {
	switch arg.(type) {
	case argumentMatcher:
		// MatchedBy matchers are functions evaluated at call time.
		return argUnknownMatcher
	case anythingOfTypeArgument, *IsTypeArgument, *FunctionalOptionsArgument:
		// Other built-in custom matchers rely on type/value inspection beyond
		// exact constants and Anything.
		return argUnknownMatcher
	}
	if assert.ObjectsAreEqual(arg, Anything) {
		return argAnything
	}
	return argConstant
}

// staticCoverage describes, for a single argument position, whether the
// earlier expectation argument matches every value the later expectation
// argument can match.
type staticCoverage int

const (
	coverageDefinite staticCoverage = iota
	coverageUnknown
	coverageNone
)

// argCovers reports whether the earlier expectation argument statically covers
// the later one. No matcher function is ever invoked.
func argCovers(earlier, later interface{}) staticCoverage {
	earlierKind := classifyStaticArg(earlier)
	laterKind := classifyStaticArg(later)

	if earlierKind == argAnything {
		// Anything matches every concrete argument value. A custom matcher on
		// the later side is not executed, so this remains unknown rather than
		// proven.
		if laterKind == argUnknownMatcher {
			return coverageUnknown
		}
		return coverageDefinite
	}

	if earlierKind == argUnknownMatcher || laterKind == argUnknownMatcher {
		return coverageUnknown
	}

	if earlierKind == argConstant && laterKind == argConstant && assert.ObjectsAreEqual(earlier, later) {
		return coverageDefinite
	}

	return coverageNone
}

// callsOverlap reports whether the earlier expectation statically matches
// every call the later expectation can match. Expectations with different
// methods or argument counts can never overlap.
func callsOverlap(earlier, later *Call) staticCoverage {
	if earlier.Method != later.Method {
		return coverageNone
	}
	if len(earlier.Arguments) != len(later.Arguments) {
		return coverageNone
	}

	result := coverageDefinite
	for i := range later.Arguments {
		switch argCovers(earlier.Arguments[i], later.Arguments[i]) {
		case coverageNone:
			return coverageNone
		case coverageUnknown:
			result = coverageUnknown
		}
	}
	return result
}

// remainingMatches reports how many more calls an expectation can accept and
// whether it matches an unlimited number of calls. Repeatability 0 means
// unlimited, positive values are the remaining finite budget and -1 (or any
// negative value) means the expectation is exhausted.
func remainingMatches(c *Call) (remaining int, unlimited bool) {
	switch {
	case c.Repeatability == 0:
		return 0, true
	case c.Repeatability > 0:
		return c.Repeatability, false
	default:
		return 0, false
	}
}

// FindExpectationShadows performs a purely static analysis of expectations
// registered on the mock and reports any later-registered expectation that can
// be hidden by an earlier one.
//
// The check is deliberately conservative: only method names, exact constant
// arguments, [Anything] and the call-count range (Once/Times/unlimited) are
// interpreted. Expectations involving [MatchedBy] or any other custom matcher
// are reported with [CertaintyUnknown] and the matcher is never executed.
//
// A shadowing expectation with an unlimited match budget yields a definite
// report that the later expectation can never be selected. A shadowing
// expectation with a finite budget (for example [Call.Once]) is reported as
// reachable after that budget is spent; this is not flagged as a permanent
// conflict. Expectations which are already exhausted are not reported.
//
// Call dispatch and assertion behavior are never modified by this function.
func (m *Mock) FindExpectationShadows() []ExpectationShadow {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	var shadows []ExpectationShadow

	for laterIndex, later := range m.ExpectedCalls {
		for earlierIndex := 0; earlierIndex < laterIndex; earlierIndex++ {
			earlier := m.ExpectedCalls[earlierIndex]

			coverage := callsOverlap(earlier, later)
			if coverage == coverageNone {
				continue
			}

			// An already exhausted earlier expectation no longer stands in the
			// way: findExpectedCall skips expectations with Repeatability < 0.
			if !unlimitedBudget(earlier) && earlier.Repeatability < 0 {
				continue
			}

			remaining, unlimited := remainingMatches(earlier)

			shadow := ExpectationShadow{
				Shadowing:             earlier,
				Shadowed:              later,
				ShadowingIndex:        earlierIndex,
				ShadowedIndex:         laterIndex,
				ShadowingRegistration: registrationSite(earlier),
				ShadowedRegistration:  registrationSite(later),
				RemainingMatches:      remaining,
				Unlimited:             unlimited,
				ShadowingOptional:     earlier.optional,
				ShadowedOptional:      later.optional,
				Certainty:             CertaintyDefinite,
			}
			if !unlimited {
				shadow.ReachableAfterExhaustion = true
			}
			if coverage == coverageUnknown {
				shadow.Certainty = CertaintyUnknown
			}

			shadows = append(shadows, shadow)
		}
	}

	return shadows
}

func unlimitedBudget(c *Call) bool {
	return c.Repeatability == 0
}

// AssertNoPermanentlyShadowedExpectations fails the test when the static
// analysis finds a definitely permanent shadow, i.e. a later-registered
// expectation that an unlimited earlier expectation can prevent from ever
// being selected. Reports with [CertaintyUnknown] never fail the test because
// custom matchers were not executed.
//
// This is an explicit, opt-in check; ordinary call dispatch and
// [Mock.AssertExpectations] behavior are unchanged.
func (m *Mock) AssertNoPermanentlyShadowedExpectations(t TestingT) bool {
	if h, ok := t.(tHelper); ok {
		h.Helper()
	}

	for _, shadow := range m.FindExpectationShadows() {
		if shadow.Certainty == CertaintyDefinite && shadow.Unlimited {
			t.Errorf("%s", shadow.String())
			return false
		}
	}
	return true
}
