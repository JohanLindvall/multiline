// Command cri shows the two-stage Kubernetes pipeline: the cri package
// rejoins CRI partial-line fragments into whole application lines and feeds
// them — prefixes stripped, keyed per stream, stamped with their log
// timestamps — into the normal stack-trace aggregation.
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/JohanLindvall/multiline"
	"github.com/JohanLindvall/multiline/cri"
)

func main() {
	ctx := context.Background()

	// Stack-trace aggregation over the rejoined lines. Data carries each
	// entry's log timestamp, handed over by the CRI stage below.
	traces := multiline.New(func(_ context.Context, e multiline.Entry[time.Time]) error {
		kind := "plain"
		if e.Match != "" {
			kind = "stacktrace " + e.Match
		}
		fmt.Printf("[%s %s %s]\n%s\n\n", e.Data.Format(time.RFC3339Nano), e.Key, kind, e.Text)
		return nil
	})

	// The CRI stage in front of it. Passing traces.AddAt directly works too;
	// the closure additionally stores each line's timestamp as its data so
	// the emitter above can print it.
	logs := cri.New(func(ctx context.Context, key, line string, when time.Time, _ any) error {
		return traces.AddAt(ctx, key, line, when, when)
	})

	for _, raw := range []string{
		"2024-01-01T10:00:00.000000001Z stdout F server started",
		"2024-01-01T10:00:01.000000001Z stdout P panic: runtime error: invalid memo",
		"2024-01-01T10:00:01.000000002Z stdout F ry address or nil pointer dereference",
		"2024-01-01T10:00:01.000000003Z stdout F ",
		"2024-01-01T10:00:01.000000004Z stdout F goroutine 1 [running]:",
		"2024-01-01T10:00:01.000000005Z stderr F unrelated stderr line",
		"2024-01-01T10:00:01.000000006Z stdout F main.handler(0x0)",
		"2024-01-01T10:00:01.000000007Z stdout F \t/app/main.go:42 +0x1d",
		"2024-01-01T10:00:02.000000001Z stdout F shutting down",
	} {
		if err := logs.Add(ctx, "container-1", raw, nil); err != nil {
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
}
