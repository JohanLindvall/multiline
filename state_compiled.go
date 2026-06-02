package multiline

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Matcher drives the multi-line state machine. A Multiline consults its Matcher to
// decide whether each incoming line continues, completes, or falls outside a
// multi-line group. Implementations must be safe for the (single-threaded) use a
// Multiline makes of them; the built-in [StateMachine] is immutable after Compile
// and so may be shared by several Multiline instances.
type Matcher interface {
	// NextStates returns the states reachable from the current state indices when
	// applied to line, together with whether a complete (terminable) match may end
	// here. An empty next means line does not continue any of the current states.
	NextStates(line string, current []int) (terminal bool, next []int)
	// Name returns the name of the state at index. Index 0 is always the start
	// state. The returned name is reported to the emitter as the match argument.
	Name(index int) string
}

type compiledAdvance struct {
	pattern *regexp.Regexp
	next    int
}

// StateMachine is the built-in [Matcher]. It is produced by [Compile] from one or
// more sets of [State] definitions and is immutable afterwards.
type StateMachine struct {
	states      [][]compiledAdvance
	names       []string
	nonTerminal []bool
}

// defaultMatcher recognizes the stack-trace formats bundled with this package.
var defaultMatcher = mustCompile(statesGo, statesNet, statesPython, statesJava)

func mustCompile(stateSets ...[]State) *StateMachine {
	sm, err := Compile(stateSets...)
	if err != nil {
		panic(err)
	}
	return sm
}

// Compile builds a [StateMachine] from the given state sets. The sets are merged,
// so definitions sharing a name (for example the "start_state" of each set)
// contribute their transitions to the same state. The "start_state" entry is
// always present and is the start state (index 0). Compile reports an error if a
// transition references an unknown state or contains an invalid pattern.
//
// Custom matchers are defined exactly like the bundled states_*.go files; pass the
// resulting state sets to Compile and pass the machine via [WithMatcher].
func Compile(stateSets ...[]State) (*StateMachine, error) {
	sm := &StateMachine{
		names:       []string{"start_state"},
		nonTerminal: []bool{true},
	}

	var all []State
	for _, set := range stateSets {
		all = append(all, set...)
	}

	// First pass: discover every state name so transitions can be resolved to
	// indices in the second pass.
	for _, st := range all {
		for _, name := range splitNames(st.Name) {
			if !slices.Contains(sm.names, name) {
				sm.names = append(sm.names, name)
				sm.nonTerminal = append(sm.nonTerminal, st.NonTerminal)
			}
		}
	}

	sm.states = make([][]compiledAdvance, len(sm.names))
	for _, st := range all {
		for _, name := range splitNames(st.Name) {
			idx := slices.Index(sm.names, name)
			for _, adv := range st.Advance {
				next := slices.Index(sm.names, adv.Next)
				if next == -1 {
					return nil, fmt.Errorf("multiline: state %q references unknown state %q", name, adv.Next)
				}
				re, err := regexp.Compile(adv.Pattern)
				if err != nil {
					return nil, fmt.Errorf("multiline: state %q has invalid pattern %q: %w", name, adv.Pattern, err)
				}
				sm.states[idx] = append(sm.states[idx], compiledAdvance{pattern: re, next: next})
			}
		}
	}

	return sm, nil
}

func splitNames(name string) []string {
	parts := strings.Split(name, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// NextStates implements [Matcher].
func (s *StateMachine) NextStates(line string, current []int) (terminal bool, next []int) {
	for _, state := range current {
		for _, adv := range s.states[state] {
			if adv.pattern.MatchString(line) {
				if !slices.Contains(next, adv.next) {
					next = append(next, adv.next)
				}
				if !s.nonTerminal[state] {
					terminal = true
				}
			}
		}
	}

	return
}

// Name implements [Matcher].
func (s *StateMachine) Name(index int) string {
	return s.names[index]
}
