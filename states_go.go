package multiline

var statesGo = []State{
	{
		Name: "start_state",
		Advance: []Advance{
			{Pattern: "\\bpanic: ", Next: "go_after_panic"},
			{Pattern: "http: panic serving", Next: "go_goroutine"},
		},
	},
	{
		Name: "go_after_panic",
		Advance: []Advance{
			{Pattern: "^\\[signal ", Next: "go_after_signal"},
		},
	},
	{
		Name: "go_after_signal, go_after_panic, go_frame_1",
		Advance: []Advance{
			{Pattern: "^$", Next: "go_goroutine"},
		},
	},
	{
		Name: "go_goroutine",
		Advance: []Advance{
			{Pattern: "^goroutine \\d+ \\[[^\\]]+\\]:$", Next: "go_frame_1"},
		},
	},
	{
		Name: "go_frame_1",
		Advance: []Advance{
			{Pattern: "^(?:[^\\s.:]+\\.)*[^\\s.():]+\\(|^created by ", Next: "go_frame_2"},
		},
	},
	{
		Name: "go_frame_2",
		Advance: []Advance{
			{Pattern: "^\\s", Next: "go_frame_1"},
		},
	},
}
