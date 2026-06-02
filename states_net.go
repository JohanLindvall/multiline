package multiline

var statesNet = []State{
	// .Net
	{
		Name: "start_state",
		Advance: []Advance{
			{Pattern: "^Unhandled exception\\. .+Exception", Next: "netstart"},
			{Pattern: "^Unhandled exception\\. .+Exception", Next: "netcont"},
		},
	},
	{
		Name:        "netstart",
		NonTerminal: true,
		Advance: []Advance{
			{Pattern: ".+", Next: "netcont"},
		},
	},
	{
		Name: "netcont",
		Advance: []Advance{
			{Pattern: "^ ---> .+Exception\\b.*:", Next: "netcont"},
			{Pattern: "^   at ", Next: "netcont"},
			{Pattern: "^--- End of stack trace from previous location ---$", Next: "netcont2"},
			{Pattern: "^   --- End of inner exception stack trace ---$", Next: "netcont2"},
		},
	},
	{
		Name: "netcont2",
		Advance: []Advance{
			{Pattern: "^   at ", Next: "netcont"},
		},
	},
}
