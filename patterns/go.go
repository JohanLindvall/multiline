package patterns

// goBlank restarts goroutine matching after the blank lines that separate the
// panic header and each goroutine block.
var goBlank = []Transition{
	{Pattern: `^$`, Next: "goroutine"},
}

// Go matches Go runtime panics and goroutine dumps.
var Go = StateSet{Name: "go", States: []State{
	{
		Name: StartState,
		Transitions: []Transition{
			{Pattern: `\bpanic: `, Next: "after_panic"},
			{Pattern: `http: panic serving`, Next: "goroutine"},
		},
	},
	{
		Name: "after_panic",
		Transitions: append([]Transition{
			{Pattern: `^\[signal `, Next: "after_signal"},
		}, goBlank...),
	},
	{Name: "after_signal", Transitions: goBlank},
	{
		Name: "goroutine",
		Transitions: []Transition{
			{Pattern: `^goroutine \d+ \[[^\]]+\]:$`, Next: "function"},
		},
	},
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
