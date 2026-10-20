# Repository guidance

`multiline` is a Go library that joins stack traces and Kubernetes CRI partial
lines into logical log entries. It has no external runtime dependencies and
performs no I/O or background work. Callers own concurrency and scheduling;
aggregators are not safe for concurrent use.

Use [CONTRIBUTING.md](CONTRIBUTING.md) for contributor workflows and
[docs/usage.md](docs/usage.md) for public behavior and limitations.

## Commands

| Command | Purpose |
| --- | --- |
| `make help` | List development commands; also the default `make` target. |
| `make test` | Run tests with coverage, including the log corpus and fuzz seeds. |
| `make check` | Check formatting, run tests and vet, and run the pinned linter. |
| `make fix` | Format Go files and tidy the module; review the resulting diff. |
| `make race` | Run tests with the race detector. |
| `make examples` | Run all example programs. |
| `make bench` | Run benchmarks with allocation counts. |
| `make fuzz` | Run each of the four conservation fuzzers for 30 seconds. |
| `make tools` | Install the pinned linter under the ignored `.tools/` directory. |

Use `FUZZTIME=5s` for shorter fuzz runs. `.golangci-version` defines the linter
version for both local development and CI.

## Layout

- [multiline.go](multiline.go): aggregation, per-key buffers, flushing, metadata,
  memory caps, and emitter callbacks.
- [patterns/](patterns): declarative format definitions, state-machine compilation,
  and literal prefiltering. The root package imports `patterns`; keep that
  dependency one-way.
- [cri/](cri): CRI parsing and fragment rejoining before stack-trace aggregation.
  `cri.Next[T]` is compatible with `(*multiline.Aggregator[T]).AddAt`.
- [tests/](tests): log fixtures that specify grouping and format attribution.
- [examples/](examples): runnable integrations referenced by the documentation.
- [.github/workflows/ci.yml](.github/workflows/ci.yml): checks and release workflow.

## Code conventions

- Keep the minimum Go version in `go.mod` at Go 1.22 unless the task explicitly
  changes compatibility. Avoid newer standard-library and testing APIs.
- Keep external dependencies confined to tests.
- Start every Go file, including tests and examples, with
  `// SPDX-License-Identifier: MIT` followed by a blank line. Keep package
  documentation separate from this header.
- Treat exported pattern definitions and `patterns.All` as read-only data.
  Build new slices when composing matchers.
- Preserve fixture whitespace and LF line endings. A trailing newline in a
  corpus file represents an additional empty input line.

## Matching and buffering invariants

- A line is accepted when it lands in a state without `NonTerminal`. On flush,
  emit the longest accepted prefix as one entry and any following lines
  individually. Aggregated entries contain at least two source lines.
- `Entry.Match` comes from the last accepting state. Preserve format ordering
  in `patterns.All`, including `DotNet` before `Java`, and verify attribution
  for overlapping formats with corpus tests.
- State names are local to their set; only `patterns.StartState` is shared.
  Transitions reference states within their own set or the start state.
- When every active state is final, emit immediately. Matchers without
  `FinalMatcher` rely on subsequent input or an explicit flush.
- An empty key bypasses aggregation. Keep independent streams isolated and
  preserve ordering within each key.
- `Entry.When` is the timestamp supplied to `AddAt`; `Add` leaves it zero.
  `FlushBefore` assumes non-decreasing input times and uses last-touched order.
- `Add` and `AddAt` consume their input even when an emitter returns an error.
  Return the first error. Flush operations stop at the first failing group;
  groups already emitted are not delivered again.
- Emitters may re-enter the aggregator. Drain groups created under the same
  key before claiming it for new input, and unlink map entries by identity.
  Re-entrant emitters must make progress for draining to finish.

## Memory and ownership

- `Entry.Lines` accounts for all consumed lines, including those dropped by
  caps. Set `Truncated` only on the entry that actually lost text.
- Retain the first source line even when a byte cap cuts it to an empty
  string. Copy cap-cut strings with `strings.Clone` so a short retained value
  does not keep an oversized backing allocation alive.
- `WithMaxTotalBytes` bounds retained text and never evicts the most recently
  touched group. Pair it with `WithMaxBytes` and `WithMaxGroups` to bound
  individual groups and group overhead.
- `Entry.Texts` is borrowed until the emitter returns. Clear retained strings
  and payloads before recycling groups, and preserve borrowed slices across
  re-entrant emitter calls.

## Performance and CRI

- Keep ordinary non-matching lines on the literal-prefilter path. Derive
  probes from patterns and preserve candidate masks when folding literals.
  Prefer anchored start patterns and selective probes without leading spaces.
- `StateMachine.UnfilteredStarts()` must report all unfilterable transitions,
  even when the prefilter is disabled. Bundled patterns should have none.
  Preserve the differential tests that compare filtered and unfiltered matching.
- Scanner selection depends on root-probe count and total-probe count. Consult
  `patterns/prefilter.go` and `patterns/ahocorasick.go` when changing thresholds;
  compare benchmarks on the same machine and Go toolchain.
- Keep CRI timestamp parsing to at most once per input line. `AddParsed` reuses
  an existing parse result; buffered lines use metadata splitting.
- The CRI full-line fast path applies only when no fragments are pending for
  that key and stream. Join fragments without a separator and emit when the
  closing `F` line arrives. Flush and stop the CRI stage before the trace stage.

## Tests and documentation

For code changes, run `make check`. For aggregation, matching, or buffering
changes, also run `make race` and `make fuzz`; include comparable benchmark
results for changes to hot paths. Documentation-only changes need link,
example, and diff checks rather than new unit tests.

Add a corpus fixture for each new log shape or matching change. The first
line lists expected entry sizes, such as `1,10,1`; the remaining lines are
input. Add surrounding ordinary lines and ambiguous cases to check for
unintended merging. Preserve a trailing newline only when an empty final line
is intentional.

Every bundled format needs a fixture directory and a matching entry in
`corpusFormat` in `multiline_test.go`. Keep that mapping aligned with
`patterns.All`. Test CRI separately in `cri/` because the root corpus uses the
default stack-trace matcher.

Keep the README focused on getting started and put detailed guidance in
`docs/usage.md`. The README's quick-start Go block must match
`examples/simple/main.go` without the SPDX header, and its output must match
`go run ./examples/simple`. Update the linked Playground demo when the example
changes. Keep citation metadata independent of release versions.

CI covers the minimum and stable Go versions, Linux amd64/arm64, macOS, and
Windows, plus lint, examples, race detection, fuzzing, and benchmark smoke runs.
The release script publishes a patch tag and generated notes only after all
checks pass for the current `main` commit. Keep publishing permissions scoped
to the release job and action references pinned to commit SHAs.
