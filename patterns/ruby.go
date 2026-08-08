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
		Transitions: []Transition{
			{Pattern: `^\S+:\d+:in .+: .+ \([A-Z][A-Za-z0-9_:]*(Error|Exception|Timeout|NotFound)\)$`, Next: "error"},
			{Pattern: `^\S+:\d+:in .+: .+ \(Errno::[A-Z]+\)$`, Next: "error"},
		},
	},
	{Name: "error", NonTerminal: true, Transitions: rubyFrame},
	{Name: "frames", Transitions: rubyFrame},
}}
