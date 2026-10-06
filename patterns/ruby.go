// SPDX-License-Identifier: MIT

package patterns

// rubyFrame continues a backtrace ("\tfrom file.rb:8:in `baz'").
var rubyFrame = []Transition{
	{Pattern: `^\s+from .+:\d+:in `, Next: "frames"},
}

// Ruby matches uncaught-exception reports from the Ruby CLI, e.g.
//
//	main.rb:4:in `foo': undefined method `bar' for nil:NilClass (NoMethodError)
//		from main.rb:8:in `baz'
//		from main.rb:12:in `<main>'
//
// The headline must end in a parenthesised exception class, and the class
// alternation is what the prefilter derives its probes from — that is the
// whole reason this pattern is shaped the way it is. Anchoring on the
// "<file>:<line>:in <method>:" shape instead would cover more classes, but its
// only provable literal is ":in ", which ordinary Ruby-adjacent log lines carry
// constantly (a Rails backtrace line, a JSON log whose caller field embeds
// one). Every such line would then run this regex, and this regex is expensive
// to *fail* — measured at 1.8-4.4us against the ~105ns/line the no-match path
// budgets, an 8-22x regression at the matcher. The class suffixes below are
// rare literals instead, so the regex runs essentially only on real Ruby
// error headlines.
//
// The alternation therefore lists the families worth buying: everything ending
// in Error or Exception (the bulk), plus Errno::* (ENOENT, ECONNREFUSED — the
// most common production failures), *Timeout (Net::ReadTimeout) and *NotFound
// (ActiveRecord::RecordNotFound). Both method quotings are accepted, since Ruby
// 3.4 switched `foo' to 'foo'. A class outside these families, and a
// message-less exception with no parenthesised class at all (Interrupt), do not
// aggregate — see the Known limits section of the README.
var Ruby = StateSet{Name: "ruby", States: []State{
	{
		Name: StartState,
		// Two transitions rather than one alternation, because the prefilter
		// derives its probes from contiguous exact runs: keeping the class
		// alternation directly adjacent to the closing paren yields "Error)",
		// "Exception)", "Timeout)", "NotFound)", and the Errno form yields
		// "(Errno::". Folding both into one group would break the run and
		// leave only the bare words, which are far weaker probes.
		//
		// The "\s" before "\(" in the Errno pattern is load-bearing and must
		// not be simplified back to a literal space. A space would join the
		// exact run and make the probe " (Errno::", and a probe whose first
		// byte is a space is pathological: strings.Contains only brute-forces
		// while the line is short (bytealg.MaxBruteForce, 64 bytes on amd64
		// but 16 on arm64, which CI also builds), and above that it scans for
		// the probe's first byte — so every space in every log line becomes a
		// false positive. Measured on a 94-byte access-log line: 153ns for
		// " (Errno::" against 14ns for "(Errno::", against a ~105ns/line
		// budget for the whole matcher. TestBundledPrefilterEnabled rejects
		// any space-leading probe so this cannot come back.
		Transitions: []Transition{
			{Pattern: `^\S+:\d+:in .+: .+ \([A-Z][A-Za-z0-9_:]*(Error|Exception|Timeout|NotFound)\)$`, Next: "error"},
			// [A-Z0-9] because five errno constants carry a digit: E2BIG,
			// EL2HLT, EL2NSYNC, EL3HLT, EL3RST.
			{Pattern: `^\S+:\d+:in .+: .+\s\(Errno::[A-Z0-9]+\)$`, Next: "error"},
		},
	},
	{Name: "error", NonTerminal: true, Transitions: rubyFrame},
	{Name: "frames", Transitions: rubyFrame},
}}
