# Multiline

`multiline` is a small, dependency-free Go library that aggregates log output
spanning several physical lines — such as panic and exception stack traces —
back into a single logical entry.

Many log shippers treat each newline as a separate record, which scatters a
single stack trace across many entries. `multiline` recognizes the start and
continuation patterns of common stack traces and re-joins them, while passing
ordinary single-line logs straight through untouched.

## Supported stack traces

- Go (`panic:` / goroutine dumps)
- .NET
- Python
- Java

## Install

```sh
go get github.com/JohanLindvall/multiline
```

## How it works

You create a `Multiline[T]` with an emitter callback. Feed it lines one at a
time with `Add`. Lines are grouped by a `key` (typically a container or stream
id) so interleaved streams stay separate. When a multi-line entry completes — or
you call `FlushBefore` / `Stop` — the emitter is invoked with the aggregated
text.

The emitter receives:

| Argument | Meaning |
| -------- | ------- |
| `line`   | The aggregated text; multiple source lines joined by `"\n"`. |
| `match`  | Name of the terminating state, or `""` when the line was emitted as-is. |
| `data`   | The `T` value associated with the first source line of the group. |

`T` is a generic payload you attach to each line (use `any` or `struct{}` if you
don't need one). `Multiline` is not safe for concurrent use.

### Key methods

- `New[T](emit, opts...)` — create an aggregator. Defaults to the built-in
  matcher; pass `WithMatcher(m)` for a custom `Matcher` (see below).
- `NewPattern[T](pattern, negate, match, emit, opts...)` — create an aggregator
  driven by a single regex (see [Pattern matching](#pattern-matching)).
- `Add(ctx, line, key, data)` — feed one line. An empty `key` bypasses
  aggregation and emits immediately.
- `FlushBefore(ctx, t)` — emit pending groups last touched before `t` (useful
  for time-based flushing of stale entries).
- `Stop(ctx)` — flush everything and reset for reuse.

### Bounding group size

By default a group grows until its match completes, so a malformed or
never-terminating match could accumulate without bound. Two options cap it
(both apply to any matcher; `0` means unlimited):

- `WithMaxLines(n)` — keep at most `n` lines per group; further lines are
  dropped while matching continues normally.
- `WithMaxBytes(n)` — keep at most `n` bytes per group; the crossing line is
  truncated on a UTF-8 rune boundary and later lines are dropped.

```go
ml := multiline.New(emit, multiline.WithMaxLines(500), multiline.WithMaxBytes(64*1024))
```

## Pattern matching

For simple formats you don't need a full state machine — `NewPattern` aggregates
using a single regular expression plus a direction, like Beats' pattern-based
multiline. A line is a *continuation* when it matches `pattern` (set `negate` to
invert), and `match` controls how continuations attach:

- `multiline.After` — continuation lines are appended to the preceding line; a
  non-matching line starts a new group. Use for traces with indented frames.
- `multiline.Before` — lines are buffered until a non-matching line, which ends
  the group. Use when a line signals that the *next* line continues it.

```go
// Indented lines continue the entry above them.
ml, err := multiline.NewPattern("^\\s", false, multiline.After, emit)

// A trailing backslash means the next line is a continuation.
ml, err := multiline.NewPattern("\\\\$", false, multiline.Before, emit)
```

Aggregated lines are reported to the emitter with `match` set to `"before"` or
`"after"`; a standalone line is emitted as-is with `match == ""`.

## Custom matchers

## Custom matchers

Line matching is driven by the `Matcher` interface, so you can recognize your own
multi-line formats. The simplest way is to declare states — exactly like the
bundled `states_*.go` files — and compile them:

```go
states := []multiline.State{
	{
		Name:    "start_state", // index 0; every group starts here
		Advance: []multiline.Advance{{Pattern: "^BEGIN TX", Next: "tx_body"}},
	},
	{
		Name: "tx_body",
		Advance: []multiline.Advance{
			{Pattern: "^\\s", Next: "tx_body"},
			{Pattern: "^(COMMIT|ROLLBACK)", Next: "tx_body"},
		},
	},
}

matcher, err := multiline.Compile(states)
if err != nil {
	// invalid pattern or a transition to an unknown state
}
ml := multiline.New(emit, multiline.WithMatcher(matcher))
```

Notes:

- The state named `start_state` is the entry point where every group begins.
- A `State` is *terminal* unless `NonTerminal` is set. A group is emitted as a
  single aggregated line only when its most recent line was matched from a
  terminal state — otherwise its lines are flushed individually. Use
  `NonTerminal` for intermediate states that must not be a valid stopping point.
- A `State.Name` may list several comma-separated names to share transitions.

For full control you can also implement the `Matcher` interface directly. A
runnable example lives in [examples/custom](examples/custom/main.go):

```sh
go run ./examples/custom
```

## Example

```go
package main

import (
	"context"
	"fmt"

	"github.com/JohanLindvall/multiline"
)

func main() {
	// The emitter is called once per aggregated entry.
	ml := multiline.New(func(_ context.Context, line, match string, _ any) error {
		if match != "" {
			fmt.Printf("[stacktrace %s]\n%s\n\n", match, line)
		} else {
			fmt.Printf("[plain] %s\n", line)
		}
		return nil
	})

	log := []string{
		"server started",
		"panic: runtime error: invalid memory address or nil pointer dereference",
		"[signal SIGSEGV: segmentation violation code=0x1 addr=0x0 pc=0x123456]",
		"",
		"goroutine 1 [running]:",
		"main.handler(0x0)",
		"\t/app/main.go:42 +0x1d",
		"main.main()",
		"\t/app/main.go:17 +0x2b",
		"shutting down",
	}

	ctx := context.Background()
	// "key" groups related lines together; use e.g. a container id in real use.
	for _, line := range log {
		if err := ml.Add(ctx, line, "key", any(nil)); err != nil {
			panic(err)
		}
	}

	// Flush anything still buffered.
	if err := ml.Stop(ctx); err != nil {
		panic(err)
	}
}
```

Output:

```
[plain] server started
[stacktrace go_frame_2]
panic: runtime error: invalid memory address or nil pointer dereference
[signal SIGSEGV: segmentation violation code=0x1 addr=0x0 pc=0x123456]

goroutine 1 [running]:
main.handler(0x0)
	/app/main.go:42 +0x1d
main.main()
	/app/main.go:17 +0x2b

[plain] shutting down
```

The runnable version lives in [examples/simple](examples/simple/main.go):

```sh
go run ./examples/simple
```
