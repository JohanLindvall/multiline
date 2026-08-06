package patterns

// pythonTraceback starts (or, after a chained-exception separator, restarts)
// a traceback.
var pythonTraceback = []Transition{
	{Pattern: `^Traceback \(most recent call last\):$`, Next: "frames"},
}

// Python matches CPython tracebacks, including chained exceptions
// ("The above exception was the direct cause ...").
var Python = StateSet{Name: "python", States: []State{
	{Name: StartState, Transitions: pythonTraceback},
	{
		Name: "frames",
		Transitions: []Transition{
			{Pattern: `^  File `, Next: "frames"},
			{Pattern: `^    `, Next: "frames"},
			// The line closing a traceback is the exception's qualified class
			// name, with a message after a colon or nothing at all — it is not
			// always a "...Error" (Exception, KeyboardInterrupt, SystemExit,
			// StopIteration, socket.timeout). Matching the shape rather than a
			// suffix list keeps the whole traceback in one entry. This is not a
			// start pattern, so loosening it costs the prefilter nothing.
			{Pattern: `^([A-Za-z_][A-Za-z0-9_]*\.)*[A-Za-z_][A-Za-z0-9_]*(:|$)`, Next: "error"},
		},
	},
	{
		Name: "error",
		Transitions: []Transition{
			{Pattern: `^$`, Next: "after_error"},
		},
	},
	{
		Name: "after_error",
		Transitions: []Transition{
			{Pattern: `^The above exception was the direct cause of the following exception:`, Next: "chained"},
		},
	},
	{
		Name: "chained",
		Transitions: []Transition{
			{Pattern: `^$`, Next: "restart"},
		},
	},
	{Name: "restart", Transitions: pythonTraceback},
}}
