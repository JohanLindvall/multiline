// SPDX-License-Identifier: MIT

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

// DotNet matches .NET unhandled-exception stack traces. The exception message
// may span one extra line before the frames start, so "message" keeps both
// readings alive: `.+` consumes that extra line, while the body transitions
// let a frame follow the header directly.
//
// DotNet precedes [Java] in [All] deliberately. .NET's "   at " frames also
// match Java's frame pattern, and Match reports the format of the last
// accepting line, so a trace with no .NET-only marker line (no " ---> " inner
// exception, no "--- End of ... ---") would otherwise be reported as "java".
var DotNet = StateSet{Name: "dotnet", States: []State{
	{
		Name: StartState,
		Transitions: []Transition{
			{Pattern: `^Unhandled exception\. .+Exception`, Next: "message"},
		},
	},
	{
		Name:        "message",
		NonTerminal: true,
		Transitions: append([]Transition{
			{Pattern: `.+`, Next: "cont"},
		}, dotnetBody...),
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
