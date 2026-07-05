package cri_test

import (
	"context"
	"fmt"

	"github.com/JohanLindvall/multiline"
	"github.com/JohanLindvall/multiline/cri"
)

func ExampleNew() {
	// Stack-trace aggregation over the rejoined lines...
	traces := multiline.New(func(_ context.Context, e multiline.Entry[struct{}]) error {
		fmt.Printf("%s %q\n", e.Key, e.Text)
		return nil
	})
	// ...with CRI partial-line rejoining in front of it: AddAt is a valid
	// Next stage as-is.
	logs := cri.New(traces.AddAt)

	ctx := context.Background()
	for _, raw := range []string{
		"2024-01-01T10:00:00.000000001Z stdout P Hello, ",
		"2024-01-01T10:00:00.000000002Z stdout F world",
		"2024-01-01T10:00:01.000000001Z stdout F Goodbye",
	} {
		if err := logs.Add(ctx, "container-1", raw, struct{}{}); err != nil {
			panic(err)
		}
	}
	// Flush both stages, upstream first.
	if err := logs.Stop(ctx); err != nil {
		panic(err)
	}
	if err := traces.Stop(ctx); err != nil {
		panic(err)
	}

	// Output:
	// container-1/stdout "Hello, world"
	// container-1/stdout "Goodbye"
}
