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
// bundled sets: "go", "java", "nodejs", "python", "dotnet", "ruby", "rust",
// "php", "elixir").
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

	// singles[i] is the shared, immutable []int{i} that Step returns for
	// single-state results, so advancing a group allocates nothing.
	singles [][]int

	// pf is the literal prefilter for the start state: a line that contains
	// none of its literals cannot match any start transition, and a hit runs
	// only the transitions that literal implies. nil disables the prefilter
	// (see StartLiterals).
	pf *prefilter

	// unfiltered lists the start patterns no probe could be proven for, held
	// outside pf so it is still reported when the prefilter is disabled
	// outright — the case in which every start pattern is unfiltered.
	unfiltered []string
}

// Compile builds a [StateMachine] from the given sets. Each set contributes
// its [StartState] transitions to the shared start state (index 0); all other
// state names are scoped to their set, so sets cannot collide. Compile
// reports an error for an empty or duplicate set name, a state with an empty
// name, a transition that references an unknown state, an invalid pattern, or
// State entries that share a name but disagree on NonTerminal.
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

	pf, filtering := startPrefilter(sets)
	sm.unfiltered = pf.unfiltered
	if filtering {
		sm.pf = pf
	}
	sm.singles = make([][]int, len(sm.format))
	for i := range sm.singles {
		sm.singles[i] = []int{i}
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

// MaxActiveStates bounds how many distinct states [StateMachine.Step] tracks
// for a single line, guarding against a pathological set blowing up the active
// set. It counts the distinct successor states one line reaches from the
// current active set, not the transitions declared on a state: the bundled
// sets declare 16 transitions on the start state alone but never exceed an
// active width of 3. A set whose successors can genuinely exceed this on one
// line will have the excess dropped in declaration order — see Step.
const MaxActiveStates = 20

// Step implements the multiline.Matcher interface. It applies line to the
// transitions of the active states and returns the new active set, plus the
// index of an accepting state the line landed in (-1 if none). An empty next
// means line does not continue any active state. The returned slice may be
// shared across calls; callers must not modify it.
//
// When only the start state is active — the steady state of a log stream —
// lines that cannot possibly begin a group are rejected by the literal
// prefilter without running any regex, and lines that hit a probe literal
// run only the start transitions that literal implies (see
// [StateMachine.StartLiterals]).
//
// next holds at most [MaxActiveStates] states; a line reaching more has the
// excess dropped in transition-declaration order. accepted is reported for the
// state the line genuinely landed in even when that state was among the
// dropped ones, so callers must not assume accepted appears in next.
func (s *StateMachine) Step(line string, active []int) (next []int, accepted int) {
	if s.pf != nil && len(active) == 1 && active[0] == 0 {
		return s.stepStart(line)
	}

	accepted = -1
	var buf [MaxActiveStates]int
	n := 0
	for _, state := range active {
		for _, tr := range s.transitions[state] {
			if !tr.pattern.MatchString(line) {
				continue
			}
			if accepted < 0 && !s.nonTerminal[tr.next] {
				accepted = tr.next
			}
			if n < MaxActiveStates && !slices.Contains(buf[:n], tr.next) {
				buf[n] = tr.next
				n++
			}
		}
	}

	return s.result(buf[:n]), accepted
}

// stepStart is Step for the prefiltered start state: probe literals select
// the candidate transitions, and only those regexes run.
func (s *StateMachine) stepStart(line string) (next []int, accepted int) {
	mask := s.pf.scan(line)
	accepted = -1
	if mask == 0 && !s.pf.wide {
		return nil, accepted
	}

	var buf [MaxActiveStates]int
	n := 0
	for i, tr := range s.transitions[0] {
		// Transitions past bit 63 have no mask bit and always run; see
		// prefilter.wide.
		if i < 64 && mask&(1<<uint(i)) == 0 {
			continue
		}
		if !tr.pattern.MatchString(line) {
			continue
		}
		if accepted < 0 && !s.nonTerminal[tr.next] {
			accepted = tr.next
		}
		if n < MaxActiveStates && !slices.Contains(buf[:n], tr.next) {
			buf[n] = tr.next
			n++
		}
	}

	return s.result(buf[:n]), accepted
}

// result converts a scratch state list into the returned active set, sharing
// the interned single-state slices for the overwhelmingly common case.
func (s *StateMachine) result(states []int) []int {
	switch len(states) {
	case 0:
		return nil
	case 1:
		return s.singles[states[0]]
	}
	return append([]int(nil), states...)
}

// Format implements the multiline.Matcher interface. It returns the name of
// the [StateSet] that owns the state at index; this is what a completed
// group reports as its Match.
func (s *StateMachine) Format(index int) string {
	return s.format[index]
}

// Final implements the optional multiline.FinalMatcher interface: a state with
// no outgoing transitions is a dead end, and a group that lands in one is
// complete — the aggregator emits it right away instead of holding it until
// the key's next line. The end of a PHP report ("thrown in ... on line N") and
// a Rust panic's "note:" line are such states.
func (s *StateMachine) Final(index int) bool {
	return len(s.transitions[index]) == 0
}

// StartLiterals returns the probe literals the prefilter derived from the
// start-state patterns at Compile time: a line matching a filtered start
// transition contains at least one of them, so lines containing none skip
// those regexes entirely, and a hit runs only the transitions the literal
// implies. It returns nil only when nothing was provable anywhere and the
// prefilter is disabled outright. Since degradation is per transition, pair
// this with [StateMachine.UnfilteredStarts] when start-pattern matching is on
// your hot path.
func (s *StateMachine) StartLiterals() []string {
	if s.pf == nil {
		return nil
	}
	return slices.Clone(s.pf.literals)
}

// UnfilteredStarts returns the start patterns the prefilter could not narrow,
// whose regexes therefore run on every line: those with no provable
// case-sensitive literal of at least three bytes, plus any past the 64th start
// transition. A set worth adding to a hot path should keep this empty — it is
// the difference between roughly 100ns and several microseconds per line.
//
// It reports the patterns whether or not the prefilter ended up enabled, so a
// machine for which nothing at all was provable — every start regex running on
// every line, the worst case there is — lists all of them rather than looking
// healthy.
func (s *StateMachine) UnfilteredStarts() []string {
	return slices.Clone(s.unfiltered)
}
