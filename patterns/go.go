package patterns

import "slices"

// goBlank restarts goroutine matching after the blank lines that separate the
// panic header and each goroutine block.
var goBlank = []Transition{
	{Pattern: `^$`, Next: "goroutine"},
}

// goGoroutine opens a goroutine block.
var goGoroutine = []Transition{
	{Pattern: `^goroutine \d+ \[[^\]]+\]:$`, Next: "function"},
}

// goResume enters a goroutine block either directly or after the usual blank
// separator: a runtime crash dump does not always leave the blank line in.
var goResume = slices.Concat(goGoroutine, goBlank)

// Go matches Go runtime panics and goroutine dumps: panic(), the runtime's own
// "fatal error:" crashes (deadlock, concurrent map access, out of memory) and
// the SIGQUIT stack dump, all of which share the goroutine-block shape.
var Go = StateSet{Name: "go", States: []State{
	{
		Name: StartState,
		Transitions: []Transition{
			{Pattern: `\bpanic: `, Next: "after_panic"},
			{Pattern: `^fatal error: `, Next: "after_panic"},
			{Pattern: `http: panic serving`, Next: "goroutine"},
			{Pattern: `^SIGQUIT: `, Next: "after_sigquit"},
		},
	},
	{
		Name: "after_panic",
		Transitions: append([]Transition{
			{Pattern: `^\[signal `, Next: "after_signal"},
		}, goResume...),
	},
	{
		Name:        "after_sigquit",
		NonTerminal: true,
		Transitions: []Transition{
			{Pattern: `^PC=`, Next: "after_signal"},
		},
	},
	{Name: "after_signal", Transitions: goResume},
	{Name: "goroutine", Transitions: goGoroutine},
	{
		Name: "function",
		Transitions: append([]Transition{
			{Pattern: `^(?:[^\s.:]+\.)*[^\s.():]+\(|^created by `, Next: "location"},
		}, goBlank...),
	},
	{
		Name: "location",
		Transitions: []Transition{
			{Pattern: `^\s`, Next: "function"},
		},
	},
}}
