package main

import (
	"context"
	"fmt"

	"github.com/JohanLindvall/multiline"
	"github.com/JohanLindvall/multiline/patterns"
)

// txSet defines a tiny custom format, exactly like the bundled sets in the
// patterns package. It groups a "BEGIN TX" line with the indented body lines
// that follow and the terminating COMMIT/ROLLBACK.
//
// A group completes only on lines that land in an accepting state, so "body"
// is accepting while the start state never is (a lone "BEGIN TX" line should
// not aggregate on its own).
var txSet = patterns.StateSet{Name: "tx", States: []patterns.State{
	{
		Name: patterns.StartState,
		Transitions: []patterns.Transition{
			{Pattern: `^BEGIN TX`, Next: "body"},
		},
	},
	{
		Name: "body",
		Transitions: []patterns.Transition{
			{Pattern: `^\s`, Next: "body"},
			{Pattern: `^(COMMIT|ROLLBACK)`, Next: "body"},
		},
	},
}}

func main() {
	// Compile the custom set alongside the bundled ones; compile only txSet
	// to recognize nothing else.
	matcher, err := patterns.Compile(append(patterns.All, txSet)...)
	if err != nil {
		panic(err)
	}

	ml := multiline.New(func(_ context.Context, e multiline.Entry[any]) error {
		if e.Match != "" {
			fmt.Printf("[%s]\n%s\n\n", e.Match, e.Text)
		} else {
			fmt.Printf("[plain] %s\n", e.Text)
		}
		return nil
	}, multiline.WithMatcher(matcher))

	log := []string{
		"listening on :5432",
		"BEGIN TX 42",
		"  UPDATE accounts SET balance = balance - 10",
		"  UPDATE accounts SET balance = balance + 10",
		"COMMIT 42",
		"idle",
	}

	ctx := context.Background()
	for _, line := range log {
		if err := ml.Add(ctx, "session-1", line, nil); err != nil {
			panic(err)
		}
	}
	if err := ml.Stop(ctx); err != nil {
		panic(err)
	}
}
