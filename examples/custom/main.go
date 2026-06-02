package main

import (
	"context"
	"fmt"

	"github.com/JohanLindvall/multiline"
)

// myStates defines a tiny custom matcher, exactly like the bundled states_*.go
// files. It groups a "BEGIN TX" line with the indented body lines that follow and
// the terminating COMMIT/ROLLBACK.
//
// A group is emitted as a single aggregated line only when its most recent line
// was matched from a *terminal* state, so the body/commit lines live in a terminal
// state while "start_state" stays non-terminal (a lone "BEGIN TX" should not
// aggregate on its own).
var myStates = []multiline.State{
	{
		Name: "start_state",
		Advance: []multiline.Advance{
			{Pattern: "^BEGIN TX", Next: "tx_body"},
		},
	},
	{
		Name: "tx_body",
		Advance: []multiline.Advance{
			{Pattern: "^\\s", Next: "tx_body"},
			{Pattern: "^(COMMIT|ROLLBACK)", Next: "tx_body"},
		},
	},
}

func main() {
	matcher, err := multiline.Compile(myStates)
	if err != nil {
		panic(err)
	}

	ml := multiline.New(func(_ context.Context, line, match string, _ any) error {
		if match != "" {
			fmt.Printf("[transaction]\n%s\n\n", line)
		} else {
			fmt.Printf("[plain] %s\n", line)
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
		if err := ml.Add(ctx, line, "session-1", any(nil)); err != nil {
			panic(err)
		}
	}
	if err := ml.Stop(ctx); err != nil {
		panic(err)
	}
}
