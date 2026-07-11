// Package patterns contains the declarative state-machine matcher used by the
// multiline aggregator, together with the bundled stack-trace definitions for
// common languages (see [All]).
//
// A matcher is described as one or more [StateSet] values and compiled with
// [Compile] into an immutable [StateMachine], which implements the
// multiline.Matcher interface. Custom formats are declared exactly like the
// bundled sets in this package:
//
//	set := patterns.StateSet{Name: "tx", States: []patterns.State{
//		{Name: patterns.StartState, Transitions: []patterns.Transition{
//			{Pattern: `^BEGIN TX`, Next: "body"},
//		}},
//		{Name: "body", Transitions: []patterns.Transition{
//			{Pattern: `^\s`, Next: "body"},
//			{Pattern: `^(COMMIT|ROLLBACK)`, Next: "body"},
//		}},
//	}}
//	m, err := patterns.Compile(append(patterns.All, set)...)
package patterns

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// StartState is the name of the entry state where every group begins. Each
// set's transitions declared on StartState are merged into the single, shared
// start state; all other state names are private to their set.
const StartState = "start_state"

// Transition is a single edge in a [State]: when Pattern (a regular
// expression) matches the current line, the machine moves to the state named
// Next. Next must name a state in the same [StateSet] (or [StartState]).
type Transition struct {
	Pattern string
	Next    string
}

// State is one node of a [StateSet]. A state is accepting unless NonTerminal
// is set: a group may only complete on a line that lands in an accepting
// state. Use NonTerminal for intermediate states that are not a valid
// stopping point. Several State entries may share a Name; their transitions
// are merged, but their NonTerminal flags must agree. The state named
// [StartState] is always non-terminal.
type State struct {
	Name        string
	NonTerminal bool
	Transitions []Transition
}

// StateSet is a named group of states describing one multi-line format. The
// set's Name is reported as the Match of entries it aggregated (for the
// bundled sets: "go", "java", "python", "dotnet", "ruby", "rust", "php").
type StateSet struct {
	Name   string
	States []State
}

type compiledTransition struct {
	pattern *regexp.Regexp
	next    int
}

// StateMachine is the compiled form of one or more [StateSet] values,
// produced by [Compile]. It is immutable and safe to share between several
// aggregators. It implements the multiline.Matcher interface.
type StateMachine struct {
	transitions [][]compiledTransition
	format      []string
	nonTerminal []bool

	// startLiterals is the literal prefilter for the start state: a line that
	// contains none of them cannot match any start transition, so Step skips
	// the regexes. nil disables the prefilter (see StartLiterals).
	startLiterals []string
}

// Compile builds a [StateMachine] from the given sets. Each set contributes
// its [StartState] transitions to the shared start state (index 0); all other
// state names are scoped to their set, so sets cannot collide. Compile
// reports an error for an empty or duplicate set name, a transition that
// references an unknown state, an invalid pattern, or State entries that
// share a name but disagree on NonTerminal.
func Compile(sets ...StateSet) (*StateMachine, error) {
	sm := &StateMachine{
		transitions: make([][]compiledTransition, 1),
		format:      []string{""},
		nonTerminal: []bool{true},
	}

	seen := make(map[string]bool, len(sets))
	for _, set := range sets {
		if set.Name == "" {
			return nil, fmt.Errorf("patterns: state set with empty name")
		}
		if seen[set.Name] {
			return nil, fmt.Errorf("patterns: duplicate state set %q", set.Name)
		}
		seen[set.Name] = true

		// First pass: allocate every state of the set so transitions can be
		// resolved to indices in the second pass.
		index := map[string]int{StartState: 0}
		for _, st := range set.States {
			if st.Name == "" {
				return nil, fmt.Errorf("patterns: set %q contains a state with empty name", set.Name)
			}
			if st.Name == StartState {
				continue // the start state is shared and always non-terminal
			}
			if idx, ok := index[st.Name]; ok {
				if sm.nonTerminal[idx] != st.NonTerminal {
					return nil, fmt.Errorf("patterns: set %q declares state %q with conflicting NonTerminal flags", set.Name, st.Name)
				}
				continue
			}
			index[st.Name] = len(sm.format)
			sm.format = append(sm.format, set.Name)
			sm.nonTerminal = append(sm.nonTerminal, st.NonTerminal)
			sm.transitions = append(sm.transitions, nil)
		}

		for _, st := range set.States {
			idx := index[st.Name]
			for _, tr := range st.Transitions {
				next, ok := index[tr.Next]
				if !ok {
					return nil, fmt.Errorf("patterns: set %q state %q references unknown state %q", set.Name, st.Name, tr.Next)
				}
				re, err := regexp.Compile(tr.Pattern)
				if err != nil {
					return nil, fmt.Errorf("patterns: set %q state %q has invalid pattern %q: %w", set.Name, st.Name, tr.Pattern, err)
				}
				sm.transitions[idx] = append(sm.transitions[idx], compiledTransition{pattern: re, next: next})
			}
		}
	}

	if lits, ok := startLiterals(sets); ok {
		sm.startLiterals = lits
	}

	return sm, nil
}

// MustCompile is like [Compile] but panics on error. Use it for state sets
// that are known valid, such as the bundled ones.
func MustCompile(sets ...StateSet) *StateMachine {
	sm, err := Compile(sets...)
	if err != nil {
		panic(err)
	}
	return sm
}

// maxActiveStates bounds how many distinct states Step tracks for a single
// line, guarding against a pathological matcher blowing up the active set.
const maxActiveStates = 20

// Step implements the multiline.Matcher interface. It applies line to the
// transitions of the active states and returns the new active set, plus the
// index of an accepting state the line landed in (-1 if none). An empty next
// means line does not continue any active state.
//
// When only the start state is active — the steady state of a log stream —
// lines that cannot possibly begin a group are rejected by the literal
// prefilter without running any regex (see [StateMachine.StartLiterals]).
func (s *StateMachine) Step(line string, active []int) (next []int, accepted int) {
	accepted = -1
	if s.startLiterals != nil && len(active) == 1 && active[0] == 0 && !s.maybeStart(line) {
		return nil, accepted
	}
	for _, state := range active {
		for _, tr := range s.transitions[state] {
			if !tr.pattern.MatchString(line) {
				continue
			}
			if accepted < 0 && !s.nonTerminal[tr.next] {
				accepted = tr.next
			}
			if len(next) < maxActiveStates && !slices.Contains(next, tr.next) {
				next = append(next, tr.next)
			}
		}
	}

	return
}

// Format implements the multiline.Matcher interface. It returns the name of
// the [StateSet] that owns the state at index; this is what a completed
// group reports as its Match.
func (s *StateMachine) Format(index int) string {
	return s.format[index]
}

func (s *StateMachine) maybeStart(line string) bool {
	for _, lit := range s.startLiterals {
		if strings.Contains(line, lit) {
			return true
		}
	}
	return false
}

// StartLiterals returns the literal prefilter derived from the start-state
// patterns at Compile time: every line that matches any start transition
// contains at least one of the returned substrings, so lines containing none
// skip the start regexes entirely. It returns nil when no such set could be
// proven for every start pattern and the prefilter is disabled — worth
// checking in a test when start-pattern matching is on your hot path.
func (s *StateMachine) StartLiterals() []string {
	return slices.Clone(s.startLiterals)
}
