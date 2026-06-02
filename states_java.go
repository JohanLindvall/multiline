package multiline

// https://github.com/fluent/fluent-bit/blob/master/src/multiline/flb_ml_parser_java.c with fixes and enhancements
var statesJava = []state{
	// Java - Start state: matches exceptions, errors, throwables
	{
		name: "start_state,java_start_exception",
		advance: []advance{
			{pattern: ".(Exception|Error|Throwable|V8 errors stack trace):", next: "java_after_exception"},
		},
	},
	{
		name:        "java_after_exception",
		nonTerminal: true,
		advance: []advance{
			{pattern: "^[\\t ]*nested exception is:[\\t ]*", next: "java_start_exception"},
		},
	},
	{
		name: "java_after_exception,java",
		advance: []advance{
			{pattern: "^[\\t ]+(eval )?at ", next: "java"},
			{pattern: "^[\\t ]*(Caused by|Suppressed):", next: "java"},
			{pattern: "^[\\t ]*nested exception is:", next: "java"},
			{pattern: "^[\\t ]*\\.\\.\\. \\d+ (more|common frames omitted)", next: "java"},
		},
	},
}
