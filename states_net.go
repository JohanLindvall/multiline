package multiline

var statesNet = []state{
	// .Net
	{
		name: "start_state",
		advance: []advance{
			{pattern: "^Unhandled exception\\. .+Exception", next: "netstart"},
			{pattern: "^Unhandled exception\\. .+Exception", next: "netcont"},
		},
	},
	{
		name:        "netstart",
		nonTerminal: true,
		advance: []advance{
			{pattern: ".+", next: "netcont"},
		},
	},
	{
		name: "netcont",
		advance: []advance{
			{pattern: "^ ---> .+Exception\\b.*:", next: "netcont"},
			{pattern: "^   at ", next: "netcont"},
			{pattern: "^--- End of stack trace from previous location ---$", next: "netcont2"},
			{pattern: "^   --- End of inner exception stack trace ---$", next: "netcont2"},
		},
	},
	{
		name: "netcont2",
		advance: []advance{
			{pattern: "^   at ", next: "netcont"},
		},
	},
}
