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
var Ruby = StateSet{Name: "ruby", States: []State{
	{
		Name: StartState,
		Transitions: []Transition{
			{Pattern: `^\S+:\d+:in .+: .+ \([A-Z][A-Za-z0-9_:]*(Error|Exception)\)$`, Next: "error"},
		},
	},
	{Name: "error", NonTerminal: true, Transitions: rubyFrame},
	{Name: "frames", Transitions: rubyFrame},
}}
