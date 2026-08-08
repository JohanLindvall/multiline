package patterns

// rustFrame continues a backtrace: numbered frames and their "at" locations.
var rustFrame = []Transition{
	{Pattern: `^\s+\d+: `, Next: "frames"},
	{Pattern: `^\s+at `, Next: "frames"},
	{Pattern: `^note: Some details are omitted`, Next: "note"},
}

// Rust matches Rust panics (the Rust >= 1.73 two-line format), with or
// without a backtrace:
//
//	thread 'main' panicked at src/main.rs:5:5:
//	index out of bounds: the len is 1 but the index is 2
//	note: run with `RUST_BACKTRACE=1` environment variable to display a backtrace
var Rust = StateSet{Name: "rust", States: []State{
	{
		Name: StartState,
		Transitions: []Transition{
			{Pattern: `^thread '[^']*' panicked at .*:$`, Next: "panicked"},
		},
	},
	{
		Name:        "panicked",
		NonTerminal: true,
		Transitions: []Transition{
			{Pattern: `.+`, Next: "message"},
		},
	},
	{
		Name: "message",
		Transitions: []Transition{
			// assert_eq!/assert_ne! print their operands on two further lines,
			// which otherwise end the panic at its first message line and
			// split off the note or backtrace. Both are indented ("  left: 1"
			// / " right: 2"), so requiring the indent costs nothing and keeps
			// an unrelated column-0 "left: ..." line out of the panic.
			{Pattern: `^ +(left|right): `, Next: "message"},
			{Pattern: "^note: run with `RUST_BACKTRACE=", Next: "note"},
			{Pattern: `^stack backtrace:$`, Next: "backtrace"},
		},
	},
	{Name: "note"},
	{Name: "backtrace", NonTerminal: true, Transitions: rustFrame},
	{Name: "frames", Transitions: rustFrame},
}}
