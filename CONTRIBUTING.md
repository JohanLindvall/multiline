# Contributing to multiline

Small fixes, documentation improvements, and real-world log samples are
welcome. You do not need to be a regular contributor or open an issue before
sending a small fix. For a new API or a substantial matching change, describe
the use case in an issue first so we can agree on the behavior.

Please follow the [code of conduct](CODE_OF_CONDUCT.md). Security reports go
through [private vulnerability reporting](SECURITY.md).

## Get started

Install [Go](https://go.dev/dl/) and Git. The library supports Go 1.22 and
newer; use the current stable Go release for development and linting.
GNU Make is optional for running the Go tests and examples.

Fork the repository on GitHub, clone your fork, then create a branch:

```sh
git clone https://github.com/YOUR-USERNAME/multiline.git
cd multiline
git switch -c fix/describe-your-change
go test ./...
go run ./examples/simple
```

`go test` downloads the test dependencies automatically. The library itself
has no external runtime dependencies.

## Development commands

Run `make` or `make help` for the available commands.

| Command | Purpose |
| --- | --- |
| `make test` | Run unit, corpus, example, and fuzz-seed tests with coverage. |
| `make check` | Check formatting, run tests and vet, and run the pinned linter. |
| `make fix` | Format Go files and tidy the module. Review the resulting diff. |
| `make race` | Run the suite with Go's race detector; requires a C compiler. |
| `make examples` | Run all three example programs. |
| `make bench` | Run benchmarks with allocation counts. |
| `make fuzz` | Run four conservation fuzzers for 30 seconds each. |
| `make fuzz FUZZTIME=5s` | Use shorter fuzz runs while iterating. |
| `make tools` | Install the pinned linter into the ignored `.tools/` directory. |

`make check` installs its linter automatically on first use. Its version is
recorded in [.golangci-version](.golangci-version) and shared with CI. Go may
download a newer toolchain to build that tool; this does not raise the
library's minimum version. Use a current Go toolchain for linting.

Without Make, use `go test -cover ./...`, `go vet ./...`,
`go test -race ./...`, `gofmt -w .`, and `go mod tidy` directly.
Please keep `go.mod` at Go 1.22 and avoid APIs introduced after that version.

## Find your way around

| Location | Purpose |
| --- | --- |
| `multiline.go` | Aggregation, stream keys, flushing, metadata, and memory bounds. |
| `patterns/` | Format definitions, state machines, and literal prefilter. |
| `cri/` | Kubernetes CRI parsing and partial-line rejoining. |
| `tests/<format>/` | Log samples that specify expected entry boundaries. |
| `examples/` | Runnable integrations linked from the README. |
| `docs/usage.md` | Detailed user guide, limitations, and troubleshooting. |

Start with an unclear example, a missing log sample, or an issue labeled
[good first issue](https://github.com/JohanLindvall/multiline/issues?q=is%3Aissue%20is%3Aopen%20label%3A%22good%20first%20issue%22)
or [help wanted](https://github.com/JohanLindvall/multiline/issues?q=is%3Aissue%20is%3Aopen%20label%3A%22help%20wanted%22).
If no issue fits, a small documentation fix is still useful.

## Report a matching problem

Use the [bug report form](https://github.com/JohanLindvall/multiline/issues/new?template=bug_report.yml).
Include a short input sample, the expected grouping, the actual output, the
module version, and any options or custom matcher you use. Remove secrets and
personal data while preserving indentation, blank lines, and exception names;
these can determine whether a pattern matches.

## Add or change a format

The corpus is the behavioral specification. Add a fixture under
`tests/<format>/` for each new log shape or matching fix. Its first line is a
comma-separated list of expected entry sizes; the rest is the input:

```text
1,2,1
server started
java.lang.RuntimeException: example
	at com.example.Main.main(Main.java:12)
server stopped
```

This example describes one ordinary line, a two-line Java trace, and another
ordinary line. **Do not add a trailing newline to a fixture** unless an empty
final input line is intentional. Editor settings preserve fixture whitespace.

For a new built-in format, add its definition in `patterns/`, register it in
`patterns.All`, and add its fixture directory to `corpusFormat` in
`multiline_test.go`. The suite verifies both grouping and format attribution.
CRI fixtures belong in `cri` tests because CRI rejoining is a separate stage.

Also include nearby ordinary log lines and ambiguous headlines in tests:
joining unrelated records is just as much a bug as splitting a trace.
Keep the no-match path cheap. For matcher or prefilter changes, run
`make bench` before and after on the same machine and Go version, and report
the relevant results. CI benchmark runs only check that benchmarks execute;
they are not performance measurements.

Every Go file, including examples and tests, starts with this header followed
by a blank line:

```go
// SPDX-License-Identifier: MIT

```

## Open a pull request

Keep a PR focused on one change. Explain the observable problem, what changes,
and how you checked it. Link a related issue when one exists; draft PRs are
welcome if you want early feedback.

Run `make check` before submitting code changes. Run `make race` and
`make fuzz` when changing aggregation, matching, or buffering. Documentation
changes need accurate links and runnable snippets rather than new unit tests.
If you change the quick start, keep its full program, output, and Playground
demo in sync with `examples/simple/main.go`.

CI tests the minimum and current stable Go versions, Linux on amd64 and arm64,
macOS, and Windows. Formatting, module tidiness, lint, examples, race tests, and
short fuzz runs are also checked. Follow-up commits on the same PR are fine;
there is no required commit-message format or contributor agreement.

## Versions and releases

The project is pre-1.0. Successful builds of the current `main` commit create
the next patch tag and a GitHub release with generated notes. Only the release
job has write access; pull requests run checks without publishing.

Maintainers should describe user-visible changes clearly in PR titles and
apply `bug`, `enhancement`, or `documentation` labels so release notes are
useful. Minor releases are deliberate; major versions of Go modules need a
matching module path. Tool updates should change `.golangci-version` and pass
the same checks as any other contribution.
