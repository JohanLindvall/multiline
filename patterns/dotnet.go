package patterns

// dotnetBody continues a stack trace once the exception message has been
// seen. Frame and inner-exception lines land in accepting states; the
// end-of-trace markers are always followed by more frames.
var dotnetBody = []Transition{
	{Pattern: `^ ---> .+Exception\b.*:`, Next: "frame"},
	{Pattern: `^   at `, Next: "frame"},
	{Pattern: `^--- End of stack trace from previous location ---$`, Next: "mark"},
	{Pattern: `^   --- End of inner exception stack trace ---$`, Next: "mark"},
}

// DotNet matches .NET unhandled-exception stack traces. The exception
// message may span one extra line before the frames start ("cont"); the
// double transition out of the start state keeps both readings alive until
// a frame line decides.
var DotNet = StateSet{Name: "dotnet", States: []State{
	{
		Name: StartState,
		Transitions: []Transition{
			{Pattern: `^Unhandled exception\. .+Exception`, Next: "message"},
			{Pattern: `^Unhandled exception\. .+Exception`, Next: "cont"},
		},
	},
	{
		Name:        "message",
		NonTerminal: true,
		Transitions: []Transition{
			{Pattern: `.+`, Next: "cont"},
		},
	},
	{Name: "cont", NonTerminal: true, Transitions: dotnetBody},
	{Name: "frame", Transitions: dotnetBody},
	{
		Name: "mark",
		Transitions: []Transition{
			{Pattern: `^   at `, Next: "frame"},
		},
	},
}}
