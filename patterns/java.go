package patterns

// javaHeader matches an exception/error headline. It intentionally also
// matches Node.js errors with an error-class prefix ("TypeError: ..."),
// whose frame lines share the Java "at ..." shape; bare "Error:" headlines
// and the V8 marker are covered by the [NodeJS] set.
var javaHeader = []Transition{
	{Pattern: `.(Exception|Error|Throwable):`, Next: "after_exception"},
}

// javaFrames continues a stack trace body.
var javaFrames = []Transition{
	{Pattern: `^[\t ]+(eval )?at `, Next: "frames"},
	{Pattern: `^[\t ]*(Caused by|Suppressed):`, Next: "frames"},
	{Pattern: `^[\t ]*nested exception is:`, Next: "frames"},
	{Pattern: `^[\t ]*\.\.\. \d+ (more|common frames omitted)`, Next: "frames"},
}

// Java matches JVM exception stack traces (and, incidentally, Node.js ones).
// Derived from fluent-bit's java multiline parser, with fixes and
// enhancements.
var Java = StateSet{Name: "java", States: []State{
	{Name: StartState, Transitions: javaHeader},
	{
		Name: "after_exception",
		Transitions: append([]Transition{
			{Pattern: `^[\t ]*nested exception is:[\t ]*$`, Next: "nested"},
		}, javaFrames...),
	},
	{Name: "nested", NonTerminal: true, Transitions: javaHeader},
	{Name: "frames", Transitions: javaFrames},
}}
