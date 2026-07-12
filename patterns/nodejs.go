package patterns

// NodeJS matches Node.js / V8 error stack traces whose headline the java set
// does not claim: a bare "Error:" at the start of the line (the most common
// Node headline), and the V8 "errors stack trace" marker. Headlines with an
// error-class prefix ("TypeError: ...") share their frame shape with the
// java set and are matched — and reported — as "java"; the two formats
// cannot be told apart reliably by line shape alone.
var NodeJS = StateSet{Name: "nodejs", States: []State{
	{
		Name: StartState,
		Transitions: []Transition{
			{Pattern: `^Error: `, Next: "frames"},
			{Pattern: `V8 errors stack trace:`, Next: "frames"},
		},
	},
	{
		Name: "frames",
		Transitions: []Transition{
			{Pattern: `^\s+(eval )?at `, Next: "frames"},
		},
	},
}}
