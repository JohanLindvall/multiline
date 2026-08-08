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
			// A panic raised while another is unwinding (a deferred panic, or
			// the "[recovered]" form) repeats the chain indented one tab per
			// level before the goroutine blocks start. Without this the outer
			// panic line is emitted alone and the inner one re-opens the group.
			{Pattern: `^\t+panic: `, Next: "after_panic"},
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
