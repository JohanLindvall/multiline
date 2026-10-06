// SPDX-License-Identifier: MIT

package patterns

// pythonTraceback starts (or, after a chained-exception separator, restarts)
// a traceback.
var pythonTraceback = []Transition{
	{Pattern: `^Traceback \(most recent call last\):$`, Next: "frames"},
}

// Python matches CPython tracebacks, including both chained-exception
// separators — the explicit "The above exception was the direct cause ..."
// produced by "raise ... from ...", and the implicit "During handling of the
// above exception, another exception occurred:" that any raise inside an
// except block produces — and the "[Previous line repeated N more times]"
// marker CPython elides repeated frames with.
var Python = StateSet{Name: "python", States: []State{
	{Name: StartState, Transitions: pythonTraceback},
	{
		Name: "frames",
		Transitions: []Transition{
			{Pattern: `^  File `, Next: "frames"},
			{Pattern: `^    `, Next: "frames"},
			// CPython elides runs of identical frames with this marker. It
			// fires wherever more than three repeat, so it lands mid-traceback
			// as often as just before the exception line — without it a
			// RecursionError, the trace that most needs joining, is split and
			// its exception line detached from its stack. The singular
			// "1 more time" is real output.
			{Pattern: `^  \[Previous line repeated \d+ more times?\]$`, Next: "frames"},
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
			// CPython has two chaining separators. The first is explicit
			// ("raise ... from ..."); the second is implicit and needs no
			// deliberate act at all — any raise inside an except block
			// produces it — so it is at least as common in the wild.
			{Pattern: `^The above exception was the direct cause of the following exception:`, Next: "chained"},
			{Pattern: `^During handling of the above exception, another exception occurred:`, Next: "chained"},
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
