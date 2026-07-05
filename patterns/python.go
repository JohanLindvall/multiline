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
			{Pattern: `^([a-zA-Z0-9]+\.)*[a-zA-Z0-9]+Error:`, Next: "error"},
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
