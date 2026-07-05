package patterns_test

import (
	"context"
	"fmt"

	"github.com/JohanLindvall/multiline"
	"github.com/JohanLindvall/multiline/patterns"
)

func ExampleCompile() {
	// A custom format, declared exactly like the bundled sets: "BEGIN" opens
	// a group, indented lines and "COMMIT" extend it. Compiling it alongside
	// patterns.All keeps the bundled stack-trace formats working too.
	set := patterns.StateSet{Name: "tx", States: []patterns.State{
		{Name: patterns.StartState, Transitions: []patterns.Transition{
			{Pattern: `^BEGIN`, Next: "body"},
		}},
		{Name: "body", Transitions: []patterns.Transition{
			{Pattern: `^\s`, Next: "body"},
			{Pattern: `^COMMIT`, Next: "body"},
		}},
	}}
	matcher, err := patterns.Compile(append(patterns.All, set)...)
	if err != nil {
		panic(err)
	}

	ml := multiline.New(func(_ context.Context, e multiline.Entry[struct{}]) error {
		match := e.Match
		if match == "" {
			match = "plain"
		}
		fmt.Printf("%s: %d line(s)\n", match, e.Lines)
		return nil
	}, multiline.WithMatcher(matcher))

	ctx := context.Background()
	for _, line := range []string{
		"BEGIN 42",
		"  UPDATE accounts",
		"COMMIT 42",
		"idle",
	} {
		if err := ml.Add(ctx, "session-1", line, struct{}{}); err != nil {
			panic(err)
		}
	}
	if err := ml.Stop(ctx); err != nil {
		panic(err)
	}

	// Output:
	// tx: 3 line(s)
	// plain: 1 line(s)
}
