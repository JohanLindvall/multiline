// SPDX-License-Identifier: MIT

package patterns

// javaClass is a package-qualified throwable class name ending the line, the
// shape a message-less headline finishes with.
const javaClass = `([a-zA-Z_$][a-zA-Z0-9_$]*\.)+[A-Z][A-Za-z0-9_$]*(Exception|Error|Throwable)$`

// javaHeader matches an exception/error headline. It intentionally also
// matches Node.js errors with an error-class prefix ("TypeError: ..."),
// whose frame lines share the Java "at ..." shape; bare "Error:" headlines
// and the V8 marker are covered by the [NodeJS] set.
//
// The last two transitions cover an exception thrown without a message, whose
// headline ends at the class name with no colon to anchor on. Both demand a
// dotted package qualifier, so ordinary prose ending in "...Error" does not
// open a group, and both are anchored: the equivalent unanchored pattern costs
// ~2.4us per line against ~35ns for these, because the prefilter can only
// prove the bare words "Exception"/"Error"/"Throwable" for them and every line
// carrying one of those words has to run them.
var javaHeader = []Transition{
	{Pattern: `.(Exception|Error|Throwable):`, Next: "after_exception"},
	{Pattern: `^` + javaClass, Next: "after_exception"},
	{Pattern: `^Exception in thread "[^"]*" ` + javaClass, Next: "after_exception"},
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
//
// Deliberately absent: a message-continuation state. A JVM message may span
// lines (Oracle ORA- chains, AssertJ expected-vs-actual blocks), and those
// traces aggregate nothing at all today — the first continuation matches no
// frame pattern, so the group ends at one line, below the two-line floor.
// [DotNet] solves that with its bounded "message" -> "cont" pair, and the same
// shape was measured here: it does repair the Oracle case, but it also merges
// an unrelated line sitting between a headline and its first frame into the
// trace, and collapses two consecutive headlines followed by one frame into a
// single entry — a case that aggregates correctly today. Swallowing a distinct
// log record inside another is a worse failure for a shipper than splitting a
// trace, so Java stays conservative. Anything reopening this needs to solve
// the interleaving case, not just the message case.
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
