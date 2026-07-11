package patterns

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCompileErrors(t *testing.T) {
	valid := []State{
		{Name: StartState, Transitions: []Transition{{Pattern: `^X`, Next: "s"}}},
		{Name: "s"},
	}

	for _, tc := range []struct {
		name string
		sets []StateSet
		want string
	}{
		{
			"empty set name",
			[]StateSet{{Name: "", States: valid}},
			"empty name",
		},
		{
			"duplicate set name",
			[]StateSet{{Name: "a", States: valid}, {Name: "a", States: valid}},
			"duplicate state set",
		},
		{
			"empty state name",
			[]StateSet{{Name: "a", States: []State{{Name: ""}}}},
			"state with empty name",
		},
		{
			"conflicting NonTerminal",
			[]StateSet{{Name: "a", States: []State{
				{Name: "s", NonTerminal: true},
				{Name: "s"},
			}}},
			"conflicting NonTerminal",
		},
		{
			"unknown next state",
			[]StateSet{{Name: "a", States: []State{
				{Name: StartState, Transitions: []Transition{{Pattern: `^X`, Next: "missing"}}},
			}}},
			"unknown state",
		},
		{
			"invalid pattern",
			[]StateSet{{Name: "a", States: []State{
				{Name: StartState, Transitions: []Transition{{Pattern: `(`, Next: "s"}}},
				{Name: "s"},
			}}},
			"invalid pattern",
		},
		{
			"cross-set state reference",
			[]StateSet{
				{Name: "a", States: valid},
				{Name: "b", States: []State{
					{Name: StartState, Transitions: []Transition{{Pattern: `^Y`, Next: "s"}}},
				}},
			},
			"unknown state",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sm, err := Compile(tc.sets...)
			assert.Nil(t, sm)
			assert.ErrorContains(t, err, tc.want)
		})
	}
}

func TestMustCompilePanics(t *testing.T) {
	assert.Panics(t, func() {
		MustCompile(StateSet{Name: "", States: nil})
	})
}

// TestMergedStateDeclarations verifies that several State entries sharing a
// name (with agreeing NonTerminal flags) merge their transitions.
func TestMergedStateDeclarations(t *testing.T) {
	sm, err := Compile(StateSet{Name: "a", States: []State{
		{Name: StartState, Transitions: []Transition{{Pattern: `^X`, Next: "body"}}},
		{Name: "body", Transitions: []Transition{{Pattern: `^1`, Next: "body"}}},
		{Name: "body", Transitions: []Transition{{Pattern: `^2`, Next: "body"}}},
	}})
	assert.NoError(t, err)

	next, _ := sm.Step("X start", []int{0})
	for _, line := range []string{"1 one", "2 two"} {
		cont, accepted := sm.Step(line, next)
		assert.Len(t, cont, 1, line)
		assert.Equal(t, "a", sm.Format(accepted), line)
	}
}

// TestStateNamespacing verifies that same-named states in different sets stay
// independent while StartState is shared.
func TestStateNamespacing(t *testing.T) {
	sm, err := Compile(
		StateSet{Name: "a", States: []State{
			{Name: StartState, Transitions: []Transition{{Pattern: `^A`, Next: "body"}}},
			{Name: "body", Transitions: []Transition{{Pattern: `^a`, Next: "body"}}},
		}},
		StateSet{Name: "b", States: []State{
			{Name: StartState, Transitions: []Transition{{Pattern: `^B`, Next: "body"}}},
			{Name: "body", Transitions: []Transition{{Pattern: `^b`, Next: "body"}}},
		}},
	)
	assert.NoError(t, err)

	next, accepted := sm.Step("A start", []int{0})
	assert.Len(t, next, 1)
	assert.Equal(t, "a", sm.Format(accepted))
	// Set a's body must not accept set b's continuation.
	cont, _ := sm.Step("b cont", next)
	assert.Empty(t, cont)
	cont, _ = sm.Step("a cont", next)
	assert.Len(t, cont, 1)
}

// TestMaxActiveStates verifies the active-set cap: a line matching many
// transitions tracks at most maxActiveStates distinct states.
func TestMaxActiveStates(t *testing.T) {
	states := []State{{Name: StartState}}
	for i := range maxActiveStates + 5 {
		name := string(rune('a' + i))
		states[0].Transitions = append(states[0].Transitions, Transition{Pattern: `^match`, Next: name})
		states = append(states, State{Name: name})
	}
	sm, err := Compile(StateSet{Name: "many", States: states})
	assert.NoError(t, err)

	next, _ := sm.Step("match this", []int{0})
	assert.Len(t, next, maxActiveStates)
}
