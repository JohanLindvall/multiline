package multiline

var statesGo = []state{
	{
		name: "start_state",
		advance: []advance{
			{pattern: "\\bpanic: ", next: "go_after_panic"},
			{pattern: "http: panic serving", next: "go_goroutine"},
		},
	},
	{
		name: "go_after_panic",
		advance: []advance{
			{pattern: "^\\[signal ", next: "go_after_signal"},
		},
	},
	{
		name: "go_after_signal, go_after_panic, go_frame_1",
		advance: []advance{
			{pattern: "^$", next: "go_goroutine"},
		},
	},
	{
		name: "go_goroutine",
		advance: []advance{
			{pattern: "^goroutine \\d+ \\[[^\\]]+\\]:$", next: "go_frame_1"},
		},
	},
	{
		name: "go_frame_1",
		advance: []advance{
			{pattern: "^(?:[^\\s.:]+\\.)*[^\\s.():]+\\(|^created by ", next: "go_frame_2"},
		},
	},
	{
		name: "go_frame_2",
		advance: []advance{
			{pattern: "^\\s", next: "go_frame_1"},
		},
	},
}
