# multiline

**Keep every stack trace in one log entry.**

[![CI](https://github.com/JohanLindvall/multiline/actions/workflows/ci.yml/badge.svg)](https://github.com/JohanLindvall/multiline/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/JohanLindvall/multiline.svg)](https://pkg.go.dev/github.com/JohanLindvall/multiline)
[![Latest version](https://img.shields.io/github/v/tag/JohanLindvall/multiline?sort=semver&label=version)](https://github.com/JohanLindvall/multiline/tags)
[![Go version](https://img.shields.io/github/go-mod/go-version/JohanLindvall/multiline)](go.mod)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

A Go library that rejoins multiline logs: Go panics, stack traces from nine
language runtimes, and Kubernetes CRI partial lines. Feed it lines; get complete
entries with the detected format, line count, and your metadata. Ordinary log
lines pass straight through.

[Quick start](#quick-start) · [Formats](#supported-formats) · [Usage guide](docs/usage.md) · [API reference](https://pkg.go.dev/github.com/JohanLindvall/multiline) · [Contributing](CONTRIBUTING.md)

- **No runtime dependencies.** Go 1.22 or newer; `testify` is used only in tests.
- **Fits your pipeline.** No background goroutines or I/O. You own the reader,
  flush schedule, and destination; independent stream keys keep logs separate.
- **Fast on ordinary lines.** A literal prefilter skips unnecessary regular
  expressions with no allocations on the no-match path. Run `make bench` to
  measure your workload and machine.
- **Configurable bounds.** Limit lines, bytes, pending streams, and total
  buffered text; truncated entries are explicitly flagged.
- **Tested for conservation.** Fuzz tests check line order and conservation
  across interleaved streams, and verify accounting and truncation under caps.

Use it inside a log shipper, file-tail processor, or Kubernetes collector when
one stack trace should become one searchable event. Matching expects the raw
application line: unwrap JSON and remove per-line logging prefixes first.

## Quick start

In a Go module, install the library:

```sh
go get github.com/JohanLindvall/multiline
```

Save this complete program as `main.go` and run `go run .`. For a new project,
run `go mod init example.com/logdemo` before `go get`.

```go
package main

import (
	"context"
	"fmt"

	"github.com/JohanLindvall/multiline"
)

func main() {
	// The emitter is called once per completed entry.
	ml := multiline.New(func(_ context.Context, e multiline.Entry[any]) error {
		if e.Match != "" {
			fmt.Printf("[stacktrace %s, %d lines]\n%s\n\n", e.Match, e.Lines, e.Text)
		} else {
			fmt.Printf("[plain] %s\n", e.Text)
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
	// The key groups related lines together; use e.g. a container id in real use.
	for _, line := range log {
		if err := ml.Add(ctx, "key", line, nil); err != nil {
			panic(err)
		}
	}

	// Flush anything still buffered.
	if err := ml.Stop(ctx); err != nil {
		panic(err)
	}
}
```

Ten input lines become three entries:

```text
[plain] server started
[stacktrace go, 8 lines]
panic: runtime error: invalid memory address or nil pointer dereference
[signal SIGSEGV: segmentation violation code=0x1 addr=0x0 pc=0x123456]

goroutine 1 [running]:
main.handler(0x0)
	/app/main.go:42 +0x1d
main.main()
	/app/main.go:17 +0x2b

[plain] shutting down
```

[Run it in the Go Playground](https://go.dev/play/p/ri6EXc_NCqj) without installing
anything, or clone this repository and run `go run ./examples/simple`.
The Playground demo is pinned to v0.0.13; local examples use your checkout.

## Supported formats

| Format | Recognizes | `Entry.Match` |
| --- | --- | --- |
| Go | Panics, fatal runtime errors, SIGQUIT, goroutine dumps | `go` |
| Java / JVM | Exceptions, causes, suppressed exceptions, elided frames | `java` |
| .NET | Exceptions and stack frames | `dotnet` |
| Node.js | Bare `Error:` headlines and V8 stack-trace markers | `nodejs` |
| Python | Tracebacks, chained exceptions, recursion elision | `python` |
| Ruby | Backtraces with supported exception-class headlines | `ruby` |
| Rust | Panics, backtraces, assertion payloads | `rust` |
| PHP | Stack traces and closing `thrown in` lines | `php` |
| Elixir | Exception stack traces | `elixir` |
| Kubernetes CRI | Partial fragments rejoined before trace matching | Separate `cri` stage |

Node.js headlines such as `TypeError:` share Java's frame shape and report
`java`. See [format details](docs/usage.md#supported-formats) and
[known limits](#known-limits) before choosing a matcher.

## Examples

Run these commands from a clone of the repository:

| Goal | Command | Source |
| --- | --- | --- |
| Join a Go panic into one entry | `go run ./examples/simple` | [Simple example](examples/simple/main.go) |
| Add a transaction-log format | `go run ./examples/custom` | [Custom matcher](examples/custom/main.go) |
| Rejoin CRI fragments, then stack traces | `go run ./examples/cri` | [Kubernetes pipeline](examples/cri/main.go) |

### How it works

Create an `Aggregator[T]` with an emitter callback, then call `Add` for each
line without its trailing newline. Use a non-empty key per independent source,
such as a file or container/stream pair. `T` carries metadata from the entry's
first line; use `struct{}` when you do not need metadata.

Call `Flush` when a stream closes and `Stop` at shutdown. Completed entries
include `Text`, `Key`, `Match`, `Lines`, and `Truncated`.
[Read the entry and method reference](docs/usage.md#how-it-works).

### Buffering latency

Schedule `FlushBefore` to release the last trace of an idle stream. An
aggregator is **not safe for concurrent use**: serialize input, timer, and
shutdown calls, for example in one event loop.
[See flushing and error handling](docs/usage.md#buffering-latency).

### Bounding memory

Buffers are unlimited by default. Configure `WithMaxLines`, `WithMaxBytes`,
`WithMaxGroups`, and `WithMaxTotalBytes` for long-running collectors.
[See the memory configuration example](docs/usage.md#bounding-memory).

### Custom formats

Compile a subset of the built-in formats or add a declarative state machine
using `patterns.Compile` and `WithMatcher`.
[Build a custom matcher](docs/usage.md#custom-formats).

### Kubernetes CRI partial lines

Use `cri.New(traces.AddAt)` to rejoin container-runtime fragments before
stack-trace matching. Flush and stop the CRI stage before the trace stage.
[Read the pipeline guide](docs/usage.md#kubernetes-cri-partial-lines).

## Known limits

- Java exception messages spanning several lines are not joined.
- Ruby exception classes outside the supported families, and message-less
  exceptions without a parenthesized class, are not joined.
- Ambiguous Node.js traces can report `java`.
- An empty key bypasses aggregation.
- `WithMaxTotalBytes` limits retained text, not total process memory; pair it
  with per-entry and group-count caps.
- `FlushBefore` assumes non-decreasing timestamps across input calls.

[The usage guide explains these trade-offs](docs/usage.md#known-limits), along
with [common integration problems](docs/usage.md#troubleshooting).

## Get help and contribute

[Ask a question or report a bug](https://github.com/JohanLindvall/multiline/issues/new/choose).
For a matching problem, include a small, sanitized log sample and the entry
boundaries you expected. Report security vulnerabilities privately through
[the security policy](SECURITY.md).

Documentation fixes, real-world log samples, tests, and matcher improvements
are welcome. Start with [CONTRIBUTING.md](CONTRIBUTING.md) or the
[good first issues](https://github.com/JohanLindvall/multiline/issues?q=is%3Aissue%20is%3Aopen%20label%3A%22good%20first%20issue%22).
Contributors follow the [code of conduct](CODE_OF_CONDUCT.md).

```sh
git clone https://github.com/JohanLindvall/multiline.git
cd multiline
make help
make test
```

Maintained by [Johan Lindvall](https://github.com/JohanLindvall). If `multiline`
helps your project, a star makes it easier to find again and lets others know
it is useful. Sharing a concrete integration example helps future users too.

## Releases

The library is pre-1.0. Successful CI builds of `main` receive a patch tag;
review [versions](https://github.com/JohanLindvall/multiline/tags) and
[release notes](https://github.com/JohanLindvall/multiline/releases) when updating.
Pin the version you use in `go.mod`.

## License

MIT — see [LICENSE](LICENSE).
