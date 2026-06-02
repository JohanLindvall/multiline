package multiline

// https://github.com/fluent/fluent-bit/blob/master/src/multiline/flb_ml_parser_java.c with fixes and enhancements
var statesJava = []State{
	// Java - Start state: matches exceptions, errors, throwables
	{
		Name: "start_state,java_start_exception",
		Advance: []Advance{
			{Pattern: ".(Exception|Error|Throwable|V8 errors stack trace):", Next: "java_after_exception"},
		},
	},
	{
		Name:        "java_after_exception",
		NonTerminal: true,
		Advance: []Advance{
			{Pattern: "^[\\t ]*nested exception is:[\\t ]*", Next: "java_start_exception"},
		},
	},
	{
		Name: "java_after_exception,java",
		Advance: []Advance{
			{Pattern: "^[\\t ]+(eval )?at ", Next: "java"},
			{Pattern: "^[\\t ]*(Caused by|Suppressed):", Next: "java"},
			{Pattern: "^[\\t ]*nested exception is:", Next: "java"},
			{Pattern: "^[\\t ]*\\.\\.\\. \\d+ (more|common frames omitted)", Next: "java"},
		},
	},
}
