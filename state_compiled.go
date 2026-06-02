package multiline

import (
	"regexp"
	"slices"
	"strings"
)

var compiledStates = [][]struct {
	pattern *regexp.Regexp
	next    int
	nextStr string
}{}

var names = []string{"start_state"}
var nonTerminal = []bool{true}

func appendStates(states ...[]state) []state {
	var result []state
	for _, st := range states {
		result = append(result, st...)
	}
	return result
}

func init() {
	allStates := appendStates(statesGo, statesNet, statesPython, statesJava)
	for _, state := range allStates {
		for _, name := range strings.Split(state.name, ",") {
			name = strings.TrimSpace(name)
			if !slices.Contains(names, name) {
				names = append(names, name)
				nonTerminal = append(nonTerminal, state.nonTerminal)
			}
		}
	}
	compiledStates = make([][]struct {
		pattern *regexp.Regexp
		next    int
		nextStr string
	}, len(names))

	for _, state := range allStates {
		for _, name := range strings.Split(state.name, ",") {
			name = strings.TrimSpace(name)
			idx := slices.Index(names, name)
			for _, st := range state.advance {
				next := slices.Index(names, st.next)
				if next == -1 {
					panic("invalid state name: " + st.next)
				}
				compiledStates[idx] = append(compiledStates[idx], struct {
					pattern *regexp.Regexp
					next    int
					nextStr string
				}{pattern: regexp.MustCompile(st.pattern), next: next, nextStr: st.next})
			}
		}
	}
}

func getNextStates(line string, states []int) (terminal bool, next []int) {
	for _, state := range states {
		for _, st := range compiledStates[state] {
			if st.pattern.MatchString(line) {
				if !slices.Contains(next, st.next) {
					next = append(next, st.next)
				}
				if !nonTerminal[state] {
					terminal = true
				}
			}
		}
	}

	return
}
