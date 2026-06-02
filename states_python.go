package multiline

var statesPython = []State{
	// Python
	{
		Name: "start_state,python_error_4",
		Advance: []Advance{
			{Pattern: "^Traceback \\(most recent call last\\):$", Next: "python"},
		},
	},
	{
		Name: "python",
		Advance: []Advance{
			{Pattern: "^  File ", Next: "python"},
			{Pattern: "^    ", Next: "python"},
			{Pattern: "^([a-zA-Z0-9]+\\.)*[a-zA-Z0-9]+Error:", Next: "python_error"},
		},
	},
	{
		Name: "python_error",
		Advance: []Advance{
			{Pattern: "^$", Next: "python_error_2"},
		},
	},
	{
		Name: "python_error_2",
		Advance: []Advance{
			{Pattern: "^The above exception was the direct cause of the following exception:", Next: "python_error_3"},
		},
	},
	{
		Name: "python_error_3",
		Advance: []Advance{
			{Pattern: "^$", Next: "python_error_4"},
		},
	},
}
