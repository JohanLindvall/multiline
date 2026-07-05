package patterns

// PHP matches uncaught-exception reports, e.g.
//
//	PHP Fatal error:  Uncaught Exception: boom in /app/index.php:3
//	Stack trace:
//	#0 /app/index.php(7): foo()
//	#1 {main}
//	  thrown in /app/index.php on line 3
var PHP = StateSet{Name: "php", States: []State{
	{
		Name: StartState,
		Transitions: []Transition{
			{Pattern: `(PHP )?Fatal error:\s+Uncaught `, Next: "message"},
		},
	},
	{
		Name:        "message",
		NonTerminal: true,
		Transitions: []Transition{
			{Pattern: `^Stack trace:$`, Next: "trace"},
		},
	},
	{
		Name:        "trace",
		NonTerminal: true,
		Transitions: []Transition{
			{Pattern: `^#\d+ `, Next: "frames"},
		},
	},
	{
		Name: "frames",
		Transitions: []Transition{
			{Pattern: `^#\d+ `, Next: "frames"},
			{Pattern: `^\s+thrown in .+ on line \d+`, Next: "thrown"},
		},
	},
	{Name: "thrown"},
}}
