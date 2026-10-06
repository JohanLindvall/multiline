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
- `make fuzz` — 30s bursts of all four fuzzers, one per property: uncapped
  conservation (no line lost/duplicated/reordered), capped conservation
  (`Entry.Lines` sums to the lines consumed under `WithMaxLines`/`WithMaxBytes`,
  and `Truncated` lands on exactly the entry that lost text), multi-key
  conservation (interleaved keys, `AddAt`, `FlushBefore` and both eviction
  caps, none of which may drop text), and cri rejoin conservation (the CRI
  stage repackages content without changing it)
- CI (`.github/workflows/ci.yml`) mirrors lightning: one check job per arch
  (amd64+arm64) running `make test`, lint once on amd64 via
  golangci-lint-action pinned to v2.12.2, a `race-and-fuzz` job (`go test
  -race`, `make fuzz`, benchmarks at `-benchtime=1x`, uploading any crasher
  under `testdata/fuzz/` on failure), and auto patch-tagging on green main
  once both jobs pass

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
- `SECURITY.md`, `CITATION.cff` — adoption files. `SECURITY.md` links GitHub's
  private vulnerability reporting form, which is enabled in the repo settings.
  `CITATION.cff` deliberately has no `version`/`date-released`: CI tags every
  green main, so a pinned version would always be stale

## README quick start

The quick start is the README's five-second pitch, so it must not rot: its Go
block is a verbatim excerpt of `examples/simple/main.go`, its output block is
the verbatim output of `go run ./examples/simple`, and its Go Playground link
(go.dev/play/p/ri6EXc_NCqj) runs that program pinned to v0.0.13 by a
`-- go.mod --` section. Changing the example means updating all three. To
re-share, POST the program to `https://go.dev/_/share` with
`Content-Type: text/plain` — curl's default form type stores an empty snippet.

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
directory without an entry fails fast. `TestCorpusCoversEveryBundledSet` closes
the loop the other way — `corpusFormat` and `patterns.All` must name exactly
the same formats, so a new set cannot ship without a corpus, and a stale entry
cannot outlive its set. Every pattern-set change belongs with a corpus file:
the gaps found in the go/python/ruby/rust sets all survived because no corpus
described the shape.

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
  The unfiltered list lives on the `StateMachine`, not inside `pf`, so it is
  still reported when nothing at all was provable and the prefilter is off
  outright — that case is every start regex on every line, the worst there is,
  and it must not read as healthy.
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

- `Truncated` reporting: the flag marks the one entry that actually lost text,
  which is always the last entry the flush emits — `append` retains nothing
  once `capped` is set, so the cut line is the last retained line and every
  dropped line follows it. So it goes on the aggregated entry only when that
  entry runs to the end of the retained lines (`tail == len(g.lines)`),
  otherwise on the last individually emitted line, which is also the entry
  charged with the dropped count. `sum(Lines)` therefore always equals the
  lines consumed, and `Truncated` is true exactly when an entry stands for
  more lines than it retained or one of its lines was cut
  (`FuzzCappedConservation` holds both). Don't move the flag onto the
  aggregated entry unconditionally — the intact prefix would claim a loss that
  happened in its neighbour. The first line of a group is always retained (cut
  to `""` at worst) — this is what prevents the historical empty-group panic;
  don't "optimize" it away.
- A cap-cut line is `strings.Clone`d, never resliced: a Go substring shares its
  backing array, so `line[:avail]` would pin the whole original line while only
  `avail` bytes are charged to `g.bytes`/`a.bytes` — `WithMaxTotalBytes` would
  then bound a number unrelated to the memory actually held (measured 65,000x
  on 4 MiB lines). This is the cold path, at most once per group; don't move it
  onto the hot path or drop it.
- Emitter re-entrancy under the *same* key: the flush in `AddAt`'s
  flush-then-restart path runs the emitter, which may re-enter and claim
  `a.groups[key]` before the triggering line does. `AddAt` therefore drains the
  key in a loop before claiming it (each drain re-enters the emitter in turn),
  and `unlink` deletes the map entry by identity. Without the loop two live
  groups share one key — one in the map, one orphaned in the last-touched list
  — and `Len`/`Keys`/`Pending`/`Bytes` disagree, so a shipper draining on
  `Len()` drops the tail. A one-shot check is not enough; `TestReentrantEmitterSameKey`
  pins it. The drain converges only if the emitter makes progress — one that
  answers every entry with another line for the *same* key never settles, which
  is documented on `Emitter`. Any alternative that avoids the loop (including
  `goto restart`) has the same property, because the regress is the emitter's,
  not the drain's.
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
- Java has no message-continuation state, deliberately — multi-line JVM
  messages (Oracle `ORA-` chains, AssertJ blocks) do not aggregate at all. The
  .NET-style bounded continuation was measured and rejected: it merges an
  unrelated line between a headline and its first frame into the trace, and
  collapses two consecutive headlines plus one frame into a single entry (a
  case that works today). Swallowing a distinct record inside another is worse
  for a shipper than splitting a trace. See the comment on `Java` in
  patterns/java.go before reopening this.
- The ruby headline must end in a parenthesised class, and its class
  alternation is deliberately kept *adjacent to the closing paren* so the
  prefilter derives `"Error)"`, `"Exception)"`, `"Timeout)"`, `"NotFound)"` and
  `"(Errno::"` — rare literals, the first two of which fold in as children of
  the existing `Error`/`Exception` roots. The `\s` before `\(` in the Errno
  pattern is load-bearing: a literal space would join the exact run and make the
  probe `" (Errno::"`, and a space-leading probe is pathological, because
  `strings.Contains` only brute-forces below `bytealg.MaxBruteForce` (64 bytes
  on amd64, **16 on arm64**, which CI also builds) and above it scans for the
  probe's first byte — so every space in every line becomes a false positive.
  Measured 153ns vs 14ns per line on a 94-byte access-log line, against a
  ~105ns/line budget for the whole matcher. `TestBundledPrefilterEnabled`
  rejects any space-leading probe. Anchoring on the
  `<file>:<line>:in <method>:` shape instead covers more classes but its only
  provable literal is `":in "`, which ordinary Ruby-adjacent lines carry
  constantly (a Rails backtrace line, a JSON log with a caller field); every
  one would then run this regex, which is expensive to *fail* — measured
  1.8-4.4us, an 8-22x regression at the matcher. That was tried and reverted;
  don't reintroduce it. `TestPrefilterMasks` pins both the probes and the
  negative cases, and no benchmark catches this on its own (BenchmarkNoMatch's
  line has no `":in "`). Splitting the Errno form into its own transition is
  also load-bearing: one alternation covering both would break the exact run
  and leave only the bare words as probes.
- Prefilter scanning is linear strings.Contains below `acMinLiterals` probe
  literals and a dense Aho-Corasick automaton at or above it (crossover
  measured by `BenchmarkPrefilterScan`); the bundled sets stay linear. Both
  scanners are differentially tested against each other. The threshold is
  compared against `pf.roots`, **not** `len(pf.literals)`: the linear scan
  walks only the roots and reaches a child solely when its parent stem hit, so
  the roots are what a line actually pays for. The bundled sets carry 23 probes
  over 16 roots — comparing the total would hand a set that merely grew a few
  contained probes to the slower scanner (measured +39% to +71% through
  `Matcher.Step`).
- go.mod declares `go 1.22` (needs range-over-int); don't let tooling bump it
  to the local toolchain version, and don't use newer stdlib/testing APIs
  (e.g. `b.Loop`, `strings.SplitSeq`) without raising it deliberately.
- Every `.go` file, tests and examples included, starts with
  `// SPDX-License-Identifier: MIT` and a blank line; `TestSPDXHeaders`
  enforces both. The blank line keeps the header out of the package doc
  comment, whose first sentence is the package synopsis on pkg.go.dev.
