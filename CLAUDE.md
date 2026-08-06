# CLAUDE.md

Go library that rejoins multi-line log output (stack traces, panics) into
single logical entries, for use in log shippers. Dependency-free (testify is
test-only). Not safe for concurrent use by design; callers own goroutines and
I/O ("sans-IO").

## Commands

Use the Makefile (same shape as JohanLindvall/lightning):

- `make test` — full suite with coverage, including the corpus tests under
  `tests/`
- `make check` — golangci-lint (installed on demand) + test; keep it at 0
  issues
- `make fix` — gofmt + go mod tidy
- `make bench` — benchmarks (the no-match path must stay on the prefilter
  fast path: ~105ns/line at the matcher, ~130ns/line through the
  aggregator; see below)
- `make fuzz` — 30s bursts of both fuzzers: uncapped conservation (no line
  lost/duplicated/reordered) and capped conservation (`Entry.Lines` sums to
  the lines consumed under `WithMaxLines`/`WithMaxBytes`)
- CI (`.github/workflows/ci.yml`) mirrors lightning: one check job per arch
  (amd64+arm64) running `make test`, lint once on amd64 via
  golangci-lint-action pinned to v2.12.2, and auto patch-tagging on green main

## Layout

- `multiline.go` — the engine: `Aggregator[T]`, per-key `group` buffers, an
  intrusive linked list in last-touched order (drives `FlushBefore`,
  `WithMaxGroups` eviction, and deterministic `Stop`), size caps, and
  longest-accepted-prefix emission
- `patterns/` — declarative state machines: `Compile(StateSet...)` builds the
  `StateMachine` that implements `multiline.Matcher` (structurally; patterns
  must not import the root package — the root imports patterns for the
  default matcher). One file per bundled format
- `cri/` — Kubernetes CRI partial-line rejoining as a stage in front of the
  root aggregator. Has its own hand-written `Matcher` and `Parse`; does not
  use regex. `cri.Next[T]` is deliberately signature-compatible with
  `(*multiline.Aggregator[T]).AddAt`. The timestamp is parsed at most once
  per line (zero times via `AddParsed`): internal paths use the cheap `meta`
  split and recover the first fragment's time from `Entry.When` — don't
  reintroduce `Parse` calls on buffered lines
- `tests/<format>/*.txt` — corpus files, the behavioral spec

## Matcher semantics (the part worth re-reading)

- A group completes on a line that *lands in* an accepting state (a state
  without `NonTerminal`). Not "matched from" — that older semantics had an
  off-by-one that broke single-frame Java traces.
- On flush, the longest accepted prefix is emitted as one aggregated entry;
  lines consumed after it are re-emitted individually. A group never emits an
  aggregated entry spanning fewer than two source lines (first-line accepts
  are deliberately ignored).
- The emitted `Match` is the `StateSet.Name`, resolved via
  `Matcher.Format(acceptedStateIndex)`. It is the format of the *last*
  accepting line, so where two sets overlap, the one that survives longest
  wins ties — this is why `DotNet` precedes `Java` in `patterns.All` (.NET's
  `   at ` frames also satisfy Java's frame pattern).
- State names are namespaced per set; only `patterns.StartState` is shared.
  Transitions may only reference states within the same set (or the start
  state).
- `multiline.FinalMatcher` (optional): when every active state is a dead end
  (no outgoing transitions — `StateMachine.Final`), the group is emitted
  immediately instead of waiting for the key's next line. PHP's `thrown`,
  Rust's `note`, and cri's `stateFull` rely on this; a custom `Matcher` that
  doesn't implement it just keeps the old hold-until-next-line behavior.

## Corpus test format

First line: comma-separated expected entry sizes in source lines (e.g.
`1,10,1`). Rest: the log to feed, one group per expected size, in order.
Files must NOT end with a trailing newline unless the trailing empty line is
intentionally part of the last group (python.txt relies on this: the blank
after the error line is absorbed by the trace). Every file under `tests/` is
run through the *default* matcher, so corpora for non-default sets (CRI)
don't belong there — test those with `WithMatcher` unit tests instead.
Every corpus directory must have an entry in `corpusFormat`
(multiline_test.go): aggregated entries are asserted to report that format
(this is what catches one set silently claiming another's traces), and a new
directory without an entry fails fast.

## Gotchas

- Start-pattern prefilter (`patterns/prefilter.go`): Compile derives literal
  substrings from the start patterns (via regexp/syntax) so non-matching
  lines skip the regexes entirely, and each literal carries a bitmask of the
  start transitions it implies, so a near-miss line (contains "Error:" but
  matches nothing) runs one candidate regex instead of all of them.
  Degradation is per transition: a start pattern with no provable
  case-sensitive literal of >= 3 bytes (or past the 64th start transition)
  becomes a permanent candidate — its regex runs on every line — while the
  rest keep filtering; `StateMachine.UnfilteredStarts()` reports these, and
  `TestBundledPrefilterEnabled` asserts it stays empty for the bundled sets.
  `TestPrefilterDifferential` proves the filter never changes a decision.
- Prefilter probe folding is subset-only: a longer literal folds into a
  shorter contained one only when that implies no new transitions, because
  precision is what keeps a line containing the bare word "Error" (probe of
  java's cheap anchored no-message headline) from running the expensive
  unanchored `.(Exception|Error|Throwable):` (probe "Error:"). The scan cost
  of the extra probes is reclaimed by the root/child partition: children are
  only probed when their parent stem hit. Keep start patterns anchored where
  possible — the unanchored java headline costs ~1.9us when it runs, the
  anchored ones ~35ns.
- `Entry.When` is "the time you gave AddAt": lines fed via `Add` carry a
  zero When by design — that keeps the pass-through path free of clock
  reads (the staleness stamp for `FlushBefore` is taken lazily, only when a
  line is actually buffered).
- `cri.AddParsed` fast path: a full line with no pending fragments goes
  straight to the next stage — it must stay behind the `Pending(key)` check
  or fragment runs would be reordered against interleaving full lines.

- `Truncated` reporting: when a capped group flushes, the flag is set on the
  aggregated entry if there is one, else on the last individually emitted
  line — and the cap-dropped lines are charged to that same last entry's
  `Lines`, so `sum(Lines)` always equals the lines consumed
  (`FuzzCappedConservation` holds this). The first line of a group is always
  retained (cut to `""` at worst) — this is what prevents the historical
  empty-group panic; don't "optimize" it away.
- A line that triggers a flush (by not continuing its group) is processed
  even when that flush's emitter errors: the error is held, the line is
  buffered or emitted, and the first error is returned. Don't reintroduce the
  early return — it silently destroyed the triggering line.
- Flushed `group`s are recycled through a small free list (`release`
  clears the retained strings/`T`s so nothing leaks through reuse). This is
  legal only because `Entry.Texts` is borrowed until the emitter returns —
  never hand out group-backed slices with a longer lifetime. Single-line
  entries lend a depth-indexed scratch slot instead, so emitters that
  re-enter the same aggregator see stable `Texts`.
- `WithMaxTotalBytes` eviction never touches the most recently touched group
  (the current line must have somewhere to accumulate); a single oversized
  group is bounded by `WithMaxBytes`, not by the total.
- `FlushBefore` assumes non-decreasing times across `Add`/`AddAt` calls (the
  linked list is only sorted if times are).
- Java's header pattern intentionally matches Node.js errors with an
  error-class prefix ("TypeError:"); only bare "Error:" headlines and the V8
  marker report `nodejs`. The two formats share the "at" frame shape and
  cannot be told apart reliably, so ambiguous traces stay `java` by design.
- Prefilter scanning is linear strings.Contains below `acMinLiterals` probe
  literals and a dense Aho-Corasick automaton at or above it (crossover
  measured by `BenchmarkPrefilterScan`); the bundled sets stay linear. Both
  scanners are differentially tested against each other.
- go.mod declares `go 1.22` (needs range-over-int); don't let tooling bump it
  to the local toolchain version, and don't use newer stdlib/testing APIs
  (e.g. `b.Loop`, `strings.SplitSeq`) without raising it deliberately.
