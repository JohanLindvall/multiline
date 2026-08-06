package patterns

// elixirFrame continues a stack trace. Elixir frames are indented and carry
// the owning application and version in parentheses, followed by a
// "file:line: Module.fun/arity" location:
//
//	(my_app 0.1.0) lib/my_app.ex:7: MyApp.work/1
var elixirFrame = []Transition{
	{Pattern: `^\s+\([a-z_][a-zA-Z0-9_]* [^)]*\) .+:\d+: `, Next: "frames"},
	{Pattern: `^\s+\((for|anonymous fn|fn)\b`, Next: "frames"},
}

// Elixir matches uncaught-exception reports from the Elixir CLI, e.g.
//
//	** (RuntimeError) boom
//	    (my_app 0.1.0) lib/my_app.ex:7: MyApp.work/1
//	    (elixir 1.15.0) lib/task/supervised.ex:101: Task.Supervised.reply/4
var Elixir = StateSet{Name: "elixir", States: []State{
	{
		Name: StartState,
		Transitions: []Transition{
			{Pattern: `^\*\* \(\w`, Next: "error"},
		},
	},
	// The report may carry extra message lines between the headline and the
	// first frame, so "error" is not a stopping point on its own.
	{Name: "error", NonTerminal: true, Transitions: elixirFrame},
	{Name: "frames", Transitions: elixirFrame},
}}
