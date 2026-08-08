# Multiline

[![Go Reference](https://pkg.go.dev/badge/github.com/JohanLindvall/multiline.svg)](https://pkg.go.dev/github.com/JohanLindvall/multiline)
[![CI](https://github.com/JohanLindvall/multiline/actions/workflows/ci.yml/badge.svg)](https://github.com/JohanLindvall/multiline/actions/workflows/ci.yml)

`multiline` is a small, dependency-free Go library that aggregates log output
spanning several physical lines — such as panic and exception stack traces —
back into a single logical entry.

Many log shippers treat each newline as a separate record, which scatters a
single stack trace across many entries. `multiline` recognizes the start and
continuation patterns of common stack traces and re-joins them, while passing
ordinary single-line logs straight through untouched.

## Supported formats

- Go (`panic:`, runtime `fatal error:` crashes, `SIGQUIT:` dumps, nested and
  deferred panics, and their goroutine blocks)
- Java / JVM (with or without an exception message, `Caused by:`,
  `Suppressed:` and `... N more`; also claims Node.js traces with an
  error-class headline like `TypeError:`, which share the `at ...` frame
  shape. A message spanning several lines is *not* joined — see
  [Known limits](#known-limits))
- Node.js (bare `Error:` headlines and V8 stack-trace markers)
- Python (both chaining separators — the explicit `raise ... from ...` one and
  the implicit `During handling of the above exception ...` — the
  `[Previous line repeated N more times]` elision marker, and non-`Error`
  terminators such as `KeyboardInterrupt`)
- .NET (reported as `dotnet` even when its `   at ` frames would also satisfy
  Java's)
- Ruby (both the `` `method' `` and 3.4+ `'method'` quotings, and the
  `Errno::*`, `*Timeout` and `*NotFound` families alongside `*Error` /
  `*Exception` — see [Known limits](#known-limits) for the classes left out)
- Rust (panics with or without backtrace, including the `assert_eq!`
  left/right payload)
- PHP
- Elixir
- Kubernetes CRI partial lines (via the [cri](cri) subpackage, see
  [CRI partial lines](#kubernetes-cri-partial-lines))

## Install

```sh
go get github.com/JohanLindvall/multiline
```

## How it works

You create an `Aggregator[T]` with an emitter callback and feed it lines one
at a time with `Add`. Lines are grouped by a key (typically a container or
stream id) so interleaved streams stay separate. When a multi-line entry
completes — or you call `Flush`, `FlushBefore` or `Stop` — the emitter
receives an `Entry`:

| Field       | Meaning |
| ----------- | ------- |
| `Text`      | The entry text; aggregated source lines are joined by `"\n"` (empty with `WithoutText`). |
| `Texts`     | The retained source lines, one element per line — a view borrowed until the emitter returns; copy to retain. |
| `Key`       | The key the lines were added under. |
| `Match`     | Name of the format that aggregated the entry (`"go"`, `"java"`, …), or `""` for a line passed through as-is. |
| `When`      | Time of the entry's first source line, as passed to `AddAt` (zero for lines fed via `Add`). |
| `Data`      | The `T` value passed to `Add` with the entry's first source line. |
| `Lines`     | Number of source lines the entry represents (including lines dropped by the size caps). |
| `Truncated` | Set when the size caps dropped or cut lines belonging to this entry. |

`T` is a generic payload you attach to each line — a log timestamp, a file
offset for checkpointing, or `struct{}` if you don't need one. An
`Aggregator` is not safe for concurrent use.

```go
package main

import (
	"context"
	"fmt"

	"github.com/JohanLindvall/multiline"
)

func main() {
	ml := multiline.New(func(_ context.Context, e multiline.Entry[any]) error {
		if e.Match != "" {
			fmt.Printf("[stacktrace %s]\n%s\n\n", e.Match, e.Text)
		} else {
			fmt.Printf("[plain] %s\n", e.Text)
		}
		return nil
	})

	ctx := context.Background()
	for _, line := range []string{
		"server started",
		"panic: runtime error: invalid memory address or nil pointer dereference",
		"",
		"goroutine 1 [running]:",
		"main.handler(0x0)",
		"\t/app/main.go:42 +0x1d",
		"shutting down",
	} {
		if err := ml.Add(ctx, "key", line, nil); err != nil {
			panic(err)
		}
	}
	if err := ml.Stop(ctx); err != nil {
		panic(err)
	}
}
```

The runnable version lives in [examples/simple](examples/simple/main.go)
(`go run ./examples/simple`).

### Methods

- `New[T](emit, opts...)` — create an aggregator. Defaults to the built-in
  matcher covering `patterns.All`; pass `WithMatcher` to change it.
- `Add(ctx, key, line, data)` — feed one line. An empty key bypasses
  aggregation and emits immediately.
- `AddAt(ctx, key, line, when, data)` — like `Add` with an explicit time,
  which `FlushBefore` compares against and `Entry.When` reports. Pass the
  log's own timestamp to make time-based flushing robust when replaying old
  logs.
- `Flush(ctx, key)` — emit the pending group for one key. Call it when a
  stream ends, e.g. when its container terminates.
- `FlushBefore(ctx, t)` — emit pending groups last touched before `t`.
- `Stop(ctx)` — flush everything (oldest first) and reset for reuse.
- `Pending(key)`, `Len()`, `Bytes()`, `Keys(dst)` — cheap gauges for
  monitoring: whether a key has buffered lines, how many keys do, the total
  buffered text bytes, and the pending keys in last-touched order.

### Buffering latency

A line that matches any start pattern is buffered until the next line for its
key arrives, so the last entry of an idle stream stays pending. The exception
is a group that provably cannot continue: when every active state is a dead
end — PHP's closing `thrown in ... on line N`, a Rust panic ended by its
`note:` line, a CRI fragment run closed by its `F` line — the entry is
emitted immediately (see `multiline.FinalMatcher`, which
`patterns.StateMachine` implements). Everything else needs a periodic flush
of stale groups:

```go
ticker := time.NewTicker(time.Second)
defer ticker.Stop()
for range ticker.C {
	if err := ml.FlushBefore(ctx, time.Now().Add(-5*time.Second)); err != nil {
		...
	}
}
```

### Bounding memory

By default a group grows until its match completes. Four options bound the
aggregator; entries that lost lines to a cap are flagged `Truncated`, and
`Entry.Lines` still counts the dropped lines, so summing it over a stream
always accounts for every input line (`0` means unlimited):

- `WithMaxLines(n)` — retain at most `n` lines per group; further lines are
  dropped while matching continues normally.
- `WithMaxBytes(n)` — retain at most `n` text bytes per group; the crossing
  line is cut on a UTF-8 rune boundary and later lines are dropped.
- `WithMaxGroups(n)` — track at most `n` keys with pending lines; beyond it
  the least recently touched group is flushed. This guards against key
  cardinality explosions.
- `WithMaxTotalBytes(n)` — cap the text bytes buffered across *all* groups
  (the `Bytes()` gauge); least recently touched groups are flushed until the
  total fits. This bounds retained text whatever the key cardinality; pair it
  with `WithMaxBytes`, since the most recently touched group itself is never
  evicted, and with `WithMaxGroups`, since the per-group overhead (the group,
  its key, its map entry — a few hundred bytes) is not charged against the
  cap.

```go
ml := multiline.New(emit,
	multiline.WithMaxLines(500),
	multiline.WithMaxBytes(64*1024),
	multiline.WithMaxGroups(10_000),
	multiline.WithMaxTotalBytes(64*1024*1024))
```

An emitter that writes lines to an `io.Writer` can consume `Entry.Texts` and
pass `WithoutText()`, which skips joining aggregated lines into `Entry.Text`
entirely — for a large capped trace, that saves a copy the size of the whole
entry.

## Custom formats

Matching is driven by declarative state machines in the
[patterns](patterns) subpackage. The bundled definitions are exported
(`patterns.Go`, `patterns.DotNet`, `patterns.Java`, `patterns.NodeJS`,
`patterns.Python`, `patterns.Ruby`, `patterns.Rust`, `patterns.PHP`,
`patterns.Elixir`, collected in `patterns.All`), so you can compile a subset,
or add your own set alongside them — its `Name` is what completed entries
report as `Match`:

```go
set := patterns.StateSet{Name: "tx", States: []patterns.State{
	{Name: patterns.StartState, Transitions: []patterns.Transition{
		{Pattern: `^BEGIN TX`, Next: "body"},
	}},
	{Name: "body", Transitions: []patterns.Transition{
		{Pattern: `^\s`, Next: "body"},
		{Pattern: `^(COMMIT|ROLLBACK)`, Next: "body"},
	}},
}}

matcher, err := patterns.Compile(append(patterns.All, set)...)
if err != nil {
	// invalid pattern, unknown state reference, ...
}
ml := multiline.New(emit, multiline.WithMatcher(matcher))
```

Notes:

- Every group begins at `patterns.StartState`; each set's transitions on it
  are merged into the shared start state, while all other state names are
  private to their set.
- A state is *accepting* unless `NonTerminal` is set. A group completes at
  the most recent line that landed in an accepting state; when it is flushed,
  those lines are emitted as one aggregated entry and any lines consumed
  after them are re-emitted individually. An aggregated entry always spans at
  least two source lines. Use `NonTerminal` for intermediate states that are
  not a valid stopping point.
- Non-matching lines are rejected by a literal prefilter derived from the
  start patterns at compile time, at roughly 100ns per line instead of
  running every start regex. The filter degrades per pattern: a start
  pattern with no provable case-sensitive literal of at least 3 bytes simply
  runs on every line while the rest keep filtering —
  `StateMachine.UnfilteredStarts()` reports such patterns, worth asserting
  empty in a test when adding a set to a hot path. It reports them whether or
  not the prefilter ended up enabled, so the check is meaningful even for a
  machine where nothing at all was provable.
- `Step` tracks at most `patterns.MaxActiveStates` distinct states for one
  line. That counts the successors a single line reaches, not the transitions
  declared on a state — the bundled sets declare 15 transitions on the start
  state but never exceed an active width of 3 — so only an unusually
  ambiguous set can reach it.
- For full control you can implement the `multiline.Matcher` interface
  directly instead of compiling state sets (plus `multiline.FinalMatcher` to
  get completed groups emitted without waiting for the next line).

A runnable example lives in [examples/custom](examples/custom/main.go)
(`go run ./examples/custom`).

## Kubernetes CRI partial lines

Container runtimes (containerd, CRI-O) write logs in the CRI format
(`<timestamp> <stream> P|F <content>`) and split long application lines into
`P` (partial) fragments closed by an `F` (full) line. Those fragments must be
rejoined **before** stack-trace aggregation, and the fragments of one line
are concatenated without a separator — so this is a separate stage, provided
by the [cri](cri) subpackage:

```go
// Stack-trace aggregation over the rejoined lines...
traces := multiline.New(emitEntries)

// ...with CRI rejoining in front of it. Fragment runs are buffered per key
// and stream; rejoined lines reach the next stage with prefixes stripped,
// keyed "<key>/<stream>", stamped with their log timestamps.
logs := cri.New(traces.AddAt)

err := logs.Add(ctx, containerID, rawLine, data)
```

`cri.New` accepts the same `WithMaxLines` / `WithMaxBytes` / `WithMaxGroups` /
`WithMaxTotalBytes` / `WithClock` options to bound fragment buffering
(`WithMatcher` and `WithoutText` are this stage's to set, not yours), and has
its own `Flush`, `FlushBefore`,
`Stop` (stop the upstream stage first) and `Pending` / `Len` / `Bytes`
gauges. A fragment run is handed on the moment its closing `F` line arrives —
nothing waits for the stream's next line. Lines that
are not CRI-formatted pass through unmodified with a zero time, and
`cri.Parse` is exported for callers that need the pieces. A tailer that already parses each line (to derive the key, or to
route by stream) should feed the parse result to `AddParsed` instead of
`Add` — the timestamp is then parsed exactly once per line on the whole
path, roughly halving the per-line cost. Its `(line, ok)` parameters mirror
`Parse`'s results, so the non-CRI passthrough needs no separate call:

```go
l, ok := cri.Parse(raw)
// ...derive key from l...
err := logs.AddParsed(ctx, key, raw, l, ok, data)
```

An empty key means "do not buffer" here too, so fragments are passed through
one by one instead of being rejoined; pass a real key to get rejoining. Lines
that are not CRI-formatted keep the bare key, unsuffixed, alongside the
`<key>/<stream>` keys the rejoined lines arrive under.

A runnable pipeline lives in [examples/cri](examples/cri/main.go)
(`go run ./examples/cri`). Docker's json-file driver is a different format and
needs JSON unwrapping instead.

## Known limits

Deliberate trade-offs, so you can tell them from bugs:

- **Java messages spanning several lines are not joined.** When a JVM message
  runs past one line (Oracle `ORA-` chains, AssertJ expected-vs-actual blocks),
  the trace is emitted line by line rather than as one entry. The obvious fix —
  a bounded message-continuation state, which the `dotnet` set does use — was
  measured and rejected: it merges an unrelated line sitting between a headline
  and its first frame into the trace, and collapses two consecutive headlines
  into one entry. Swallowing a distinct log record inside another is a worse
  failure for a shipper than splitting a trace.
- **A Ruby exception whose class is outside the recognized families is not
  joined**, nor is a message-less one that prints no parenthesised class at all
  (`Interrupt`). The headline is anchored on that trailing class precisely
  because it is what gives the prefilter a rare literal to gate on; anchoring
  on the `:in 'method':` shape instead would cover every class but make every
  Rails backtrace line and every JSON log carrying a caller field run an
  expensive regex — measured 8-22x slower at the matcher.
- **Node.js traces with an error-class headline are reported as `java`.**
  `TypeError: ...` and a JVM exception headline share the `at ...` frame shape
  and cannot be told apart by line shape alone; only bare `Error:` headlines
  and the V8 marker report `nodejs`.
- **An empty key bypasses aggregation** in both stages — it is the "pass this
  through untouched" sentinel, not a real key. In the `cri` stage that means
  fragments are handed on individually rather than rejoined.
- **`WithMaxTotalBytes` bounds retained text, not total memory.** Per-group
  overhead is not charged against it; see [Bounding memory](#bounding-memory).
- **`FlushBefore` assumes non-decreasing times** across `Add`/`AddAt` calls,
  since it walks groups in last-touched order and stops at the first one that
  is new enough.

## License

MIT — see [LICENSE](LICENSE).
