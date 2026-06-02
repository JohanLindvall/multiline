package multiline

var statesPython = []state{
	// Python
	{
		name: "start_state,python_error_4",
		advance: []advance{
			{pattern: "^Traceback \\(most recent call last\\):$", next: "python"},
		},
	},
	{
		name: "python",
		advance: []advance{
			{pattern: "^  File ", next: "python"},
			{pattern: "^    ", next: "python"},
			{pattern: "^([a-zA-Z0-9]+\\.)*[a-zA-Z0-9]+Error:", next: "python_error"},
		},
	},
	{
		name: "python_error",
		advance: []advance{
			{pattern: "^$", next: "python_error_2"},
		},
	},
	{
		name: "python_error_2",
		advance: []advance{
			{pattern: "^The above exception was the direct cause of the following exception:", next: "python_error_3"},
		},
	},
	{
		name: "python_error_3",
		advance: []advance{
			{pattern: "^$", next: "python_error_4"},
		},
	},
}
