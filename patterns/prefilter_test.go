package patterns

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// corpusLines returns every line of every corpus file under tests/. Both
// differential tests replay the real corpus, so they share one reader.
func corpusLines(t *testing.T) []string {
	t.Helper()
	var lines []string
	err := filepath.WalkDir("../tests", func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		file, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, line := range bytes.Split(file, []byte("\n")) {
			lines = append(lines, string(line))
		}
		return nil
	})
	assert.NoError(t, err)
	return lines
}

func TestRequiredLiterals(t *testing.T) {
	for _, tc := range []struct {
		pattern string
		want    []string
		ok      bool
	}{
		{`\bpanic: `, []string{"panic: "}, true},
		{`http: panic serving`, []string{"http: panic serving"}, true},
		// Contiguous exact runs are product-expanded, keeping the ":" probe.
		{`.(Exception|Error):`, []string{"Exception:", "Error:"}, true},
		{`^Traceback \(most recent call last\):$`, []string{"Traceback (most recent call last):"}, true},
		// The longest (most selective) child literal wins.
		{`^Unhandled exception\. .+Exception`, []string{"Unhandled exception. "}, true},
		{`^thread '[^']*' panicked at .*:$`, []string{"' panicked at "}, true},
		// An alternation branch without a provable literal poisons the set.
		{`ab|longliteral`, nil, false},
		// Case-folded and too-short literals are not useful probes.
		{`(?i)errorx`, nil, false},
		{`^ab`, nil, false},
		// No literal at all.
		{`^[A-Z]\d+`, nil, false},
		{`invalid(`, nil, false},
		// An alternation with an empty branch still product-expands.
		{`xy(abc|)def`, []string{"xyabcdef", "xydef"}, true},
		// Single-rune alternations are factored to a character class, so the
		// exact run breaks before the product cap is consulted; the trailing
		// child literal is still provable on its own.
		{`(a|b|c|d|e|f|g|h|i|j|k|l|m|n|o|p|q)xyz`, []string{"xyz"}, true},
		// A product exceeding the length cap (40+2+30 > maxProductLen) falls
		// back to the most selective single child literal. The branches must
		// be multi-rune: the parser folds a single-rune alternation into a
		// character class, which breaks the exact run before the cap is ever
		// consulted.
		{strings.Repeat("A", 40) + `(?:xx|yy)` + strings.Repeat("B", 30),
			[]string{strings.Repeat("A", 40)}, true},
		// Single-rune alternations are factored to character classes by the
		// parser, so the run breaks and the branch literals are unioned.
		{`(abc(d|e)|xyz)!`, []string{"abc", "xyz"}, true},
		// Multi-rune alternations stay exact and product-expand through
		// nested concats.
		{`(ab(cd|ef)|xyz)!`, []string{"abcd!", "abef!", "xyz!"}, true},
		// A product exceeding the set cap (5x5 > 16) restarts the run at the
		// second group.
		{`(aa|bb|cc|dd|ee)(a2|b2|c2|d2|e2)xyz`,
			[]string{"a2xyz", "b2xyz", "c2xyz", "d2xyz", "e2xyz"}, true},
		// An alternation exceeding the set cap cannot join an exact run.
		{`(aa|bb|cc|dd|ee|ff|gg|hh|ii|jj|kk|ll|mm|nn|oo|pp|qq)xyz`,
			[]string{"xyz"}, true},
		// Case-folded branches cannot join an exact run.
		{`(?i:AB|CD)efg`, []string{"efg"}, true},
		// A nested concat whose own product caps out poisons its branch, and
		// with it the whole pattern.
		{`((aa|bb|cc|dd|ee)(ff|gg|hh|ii|jj)|xyz)!`, nil, false},
	} {
		got, ok := requiredLiterals(tc.pattern)
		assert.Equal(t, tc.ok, ok, tc.pattern)
		if tc.ok {
			assert.ElementsMatch(t, tc.want, got, tc.pattern)
		}
	}
}

// TestStartLiteralsDedupe verifies probe consolidation: exact duplicates share
// one probe, a longer probe folds into a shorter contained one only when that
// costs no precision (no new transitions implied), and a containing probe that
// is kept for precision is scan-skipped via its parent when the stem misses.
func TestStartLiteralsDedupe(t *testing.T) {
	sm := MustCompile(StateSet{Name: "a", States: []State{
		{Name: StartState, Transitions: []Transition{
			// Two literals for one transition, the longer containing the
			// shorter: "xxabcdxx" implies nothing "abcd" does not already
			// imply, so it folds away and is never scanned for. A line
			// carrying it still hits, since it necessarily contains "abcd".
			{Pattern: `abcd|xxabcdxx`, Next: "s"},
			{Pattern: `abcd`, Next: "t"},     // exact duplicate literal: same probe, mask union
			{Pattern: `zzabcdzz`, Next: "u"}, // contains "abcd" but implies a new transition: kept
		}},
		{Name: "s"},
		{Name: "t"},
		{Name: "u"},
	}})
	assert.Equal(t, []string{"abcd", "zzabcdzz"}, sm.StartLiterals(),
		`"xxabcdxx" folded into "abcd"; "zzabcdzz" was kept because folding it would drag transition 2 in`)
	assert.Equal(t, []uint64{1<<0 | 1<<1, 1 << 2}, sm.pf.masks)
	assert.Equal(t, []int16{-1, 0}, sm.pf.parents, `"zzabcdzz" is skipped when "abcd" misses`)

	// The fold must not cost a decision: the folded-away literal still selects
	// its transition, via the stem it folded into.
	assert.Equal(t, uint64(1<<0|1<<1), sm.pf.scan("... xxabcdxx ..."))
}

// TestBundledPrefilterEnabled guards the bundled sets: a new start pattern
// without a provable literal falls back to running on every line, and one
// that cannot be probed at all would disable the prefilter for everyone.
func TestBundledPrefilterEnabled(t *testing.T) {
	sm := MustCompile(All...)
	lits := sm.StartLiterals()
	assert.NotEmpty(t, lits)
	for _, l := range lits {
		assert.GreaterOrEqual(t, len(l), 3, "weak probe %q", l)
	}
	// Every bundled start pattern must stay narrowable; a fallback here costs
	// every line of every stream a regex.
	assert.Empty(t, sm.UnfilteredStarts())
	assert.Contains(t, lits, "panic: ")
	assert.Contains(t, lits, "fatal error: ")
	// Java's message-less headline cannot reach a colon, so the bare word
	// "Error" is the strongest probe provable for it — but it must imply only
	// that cheap anchored pattern, not absorb the "Error:" probes of the
	// expensive unanchored headline: precision is what keeps a line merely
	// containing "Error" from running that regex.
	assert.Contains(t, lits, "Error")
	assert.Contains(t, lits, "Error:")
}

// TestCompileWithoutProvableLiterals verifies that an unprovable start
// pattern disables the prefilter without changing behavior.
func TestCompileWithoutProvableLiterals(t *testing.T) {
	sm, err := Compile(StateSet{Name: "test", States: []State{
		{Name: StartState, Transitions: []Transition{{Pattern: `^[A-Z]+$`, Next: "after"}}},
		{Name: "after", Transitions: []Transition{{Pattern: `^\s`, Next: "after"}}},
	}})
	assert.NoError(t, err)
	assert.Nil(t, sm.StartLiterals())
	assert.Equal(t, []string{`^[A-Z]+$`}, sm.UnfilteredStarts(),
		"a machine with no prefilter at all runs every start regex on every line — the worst case, so it must report every pattern rather than look healthy")

	next, accepted := sm.Step("HEADER", []int{0})
	assert.NotEmpty(t, next)
	assert.Equal(t, 1, accepted) // landed in the accepting "after" state
}

// TestPrefilterDifferential replays every corpus line — plus adversarial
// near-misses — against the prefiltered and unfiltered machines and requires
// identical start-state decisions.
func TestPrefilterDifferential(t *testing.T) {
	filtered := MustCompile(All...)
	assert.NotNil(t, filtered.StartLiterals())
	unfiltered := *filtered
	unfiltered.pf = nil

	lines := []string{
		"",
		" ",
		"Error: at start of line",
		"error: lowercase",
		"ERROR: uppercase",
		"an Exception occurred (no colon)",
		"panic:no space",
		"the word panic: mid-line",
		"http: panic serving 1.2.3.4: boom",
		"Unhandled exception happened",
		"java.lang.NullPointerException",
		`Exception in thread "main" java.lang.NullPointerException`,
		"an Error without any colon after it",
		"fatal error: concurrent map writes",
		"** (RuntimeError) boom",
		"thread 'main' panicked at src/main.rs:5:5:",
		"PHP Fatal error:  Uncaught Exception: x in /a.php:1",
		"main.rb:4:in `foo': boom (NoMethodError)",
		"é ünicode Ërror: line",
	}
	lines = append(lines, corpusLines(t)...)

	for _, line := range lines {
		gotNext, gotAccepted := filtered.Step(line, []int{0})
		wantNext, wantAccepted := unfiltered.Step(line, []int{0})
		assert.Equal(t, wantNext, gotNext, "line %q", line)
		assert.Equal(t, wantAccepted, gotAccepted, "line %q", line)
	}
}

// BenchmarkStepNoMatch measures the steady-state cost of a line that starts
// no group, with and without the literal prefilter.
func BenchmarkStepNoMatch(b *testing.B) {
	line := `2024-11-19 11:00:00.123 INFO [main] com.example.Application - Starting application`
	start := []int{0}

	b.Run("prefiltered", func(b *testing.B) {
		sm := MustCompile(All...)
		b.ReportAllocs()
		for range b.N {
			sm.Step(line, start)
		}
	})
	b.Run("unfiltered", func(b *testing.B) {
		sm := MustCompile(All...)
		sm.pf = nil
		b.ReportAllocs()
		for range b.N {
			sm.Step(line, start)
		}
	})
}

// BenchmarkStepNearMiss measures the worst case after the prefilter: a line
// that contains a probe literal ("Error)") but matches no start pattern, so
// every regex still runs.
func BenchmarkStepNearMiss(b *testing.B) {
	sm := MustCompile(All...)
	line := `2024-11-19 11:00:00.123 WARN retry callback(Error) invoked for request 12345`
	start := []int{0}
	if next, _ := sm.Step(line, start); next != nil {
		b.Fatal("line unexpectedly starts a group")
	}
	b.ReportAllocs()
	for range b.N {
		sm.Step(line, start)
	}
}

// TestPrefilterMasks verifies (white-box) that probe literals select only the
// start transitions they were derived from, so a near-miss line runs one
// regex instead of all of them.
func TestPrefilterMasks(t *testing.T) {
	sm := MustCompile(All...)
	assert.NotNil(t, sm.pf)

	maskOf := func(lit string) uint64 {
		t.Helper()
		for i, l := range sm.pf.literals {
			if l == lit {
				return sm.pf.masks[i]
			}
		}
		t.Fatalf("literal %q not found in %q", lit, sm.pf.literals)
		return 0
	}

	parentOf := func(lit string) string {
		t.Helper()
		for i, l := range sm.pf.literals {
			if l == lit {
				if p := sm.pf.parents[i]; p >= 0 {
					return sm.pf.literals[p]
				}
				return ""
			}
		}
		t.Fatalf("literal %q not found in %q", lit, sm.pf.literals)
		return ""
	}

	// Start-transition order follows the set order in All: go declares
	// transitions 0-3, dotnet 4, java 5-7, nodejs 8-9, python 10, ruby 11-12,
	// rust 13, php 14, elixir 15. Folding is subset-only, so the probes of
	// java's expensive unanchored headline ("Error:") stay separate from the
	// bare-word probes of its anchored message-less headline ("Error"); the
	// scan instead skips the longer probe via its parent when the stem misses.
	assert.Equal(t, uint64(1<<0), maskOf("panic: "))
	assert.Equal(t, uint64(1<<1), maskOf("fatal error: "))
	assert.Equal(t, uint64(1<<2), maskOf("http: panic serving"))
	assert.Equal(t, uint64(1<<3), maskOf("SIGQUIT: "))
	assert.Equal(t, uint64(1<<4), maskOf("Unhandled exception. "))
	assert.Equal(t, uint64(1<<5), maskOf("Error:"))
	assert.Equal(t, uint64(1<<5), maskOf("Exception:"))
	assert.Equal(t, uint64(1<<5), maskOf("Throwable:"))
	assert.Equal(t, uint64(1<<6), maskOf("Error"))
	assert.Equal(t, uint64(1<<6), maskOf("Exception"))
	assert.Equal(t, uint64(1<<6), maskOf("Throwable"))
	assert.Equal(t, uint64(1<<7), maskOf(`Exception in thread "`))
	assert.Equal(t, uint64(1<<8), maskOf("Error: "))
	assert.Equal(t, uint64(1<<9), maskOf("V8 errors stack trace:"))
	assert.Equal(t, uint64(1<<10), maskOf("Traceback (most recent call last):"))
	assert.Equal(t, uint64(1<<11), maskOf("Error)"))
	assert.Equal(t, uint64(1<<11), maskOf("Exception)"))
	assert.Equal(t, uint64(1<<11), maskOf("Timeout)"))
	assert.Equal(t, uint64(1<<11), maskOf("NotFound)"))
	assert.Equal(t, uint64(1<<12), maskOf(" (Errno::"))
	assert.Equal(t, uint64(1<<13), maskOf("' panicked at "))
	assert.Equal(t, uint64(1<<14), maskOf("Fatal error:"))
	assert.Equal(t, uint64(1<<15), maskOf("** ("))
	assert.Zero(t, sm.pf.always)
	assert.False(t, sm.pf.wide)

	// The parent chain restores the effective probe count: every "Error"-,
	// "Exception"- and "Throwable"-stemmed probe is skipped when its stem is
	// absent, so a typical line pays for the stems only.
	assert.Equal(t, "Error", parentOf("Error:"))
	assert.Equal(t, "Error", parentOf("Error: "))
	assert.Equal(t, "Error", parentOf("Error)"))
	assert.Equal(t, "Exception", parentOf("Exception:"))
	assert.Equal(t, "Exception", parentOf("Exception)"))
	assert.Equal(t, "Exception", parentOf(`Exception in thread "`))
	assert.Equal(t, "Throwable", parentOf("Throwable:"))
	assert.Equal(t, "", parentOf("panic: "))
	assert.Equal(t, "", parentOf("Unhandled exception. "), "case-sensitive: no parent")

	// Ruby's headline is what keeps ordinary Ruby-adjacent lines off the
	// regexes: its probes are the rare class suffixes, not the ":in " that a
	// Rails backtrace line or a JSON caller field carries constantly. Anchoring
	// the pattern on ":in " instead measured 8-22x slower at Step for those
	// lines, because this regex is expensive to fail.
	const rubyBit = uint64(1<<11 | 1<<12)
	for _, line := range []string{
		`{"level":"info","caller":"app/models/user.rb:42:in 'find_by_id'","msg":"ok"}`,
		`app/controllers/x.rb:15:in 'show'`,
		"\tfrom main.rb:8:in `baz'",
	} {
		assert.Zero(t, sm.pf.scan(line)&rubyBit, "line must not be a ruby candidate: %s", line)
	}
}

// TestPrefilterDegrades verifies that a start pattern with no provable literal
// costs only itself: it becomes a permanent candidate while every other
// pattern keeps filtering, and decisions are unchanged.
func TestPrefilterDegrades(t *testing.T) {
	mine := StateSet{Name: "mine", States: []State{
		{Name: StartState, Transitions: []Transition{{Pattern: `^[A-Z]+\d+ `, Next: "a"}}},
		{Name: "a", Transitions: []Transition{{Pattern: `^\s`, Next: "a"}}},
	}}
	sm := MustCompile(append(append([]StateSet(nil), All...), mine)...)

	assert.Equal(t, []string{`^[A-Z]+\d+ `}, sm.UnfilteredStarts())
	assert.Subset(t, sm.StartLiterals(), []string{"panic: "}, "bundled probes survive")
	assert.NotZero(t, sm.pf.always, "the unprovable transition is always a candidate")

	unfiltered := *sm
	unfiltered.pf = nil
	for _, line := range []string{
		"", "HEADER1 boom", "HEADER boom", "panic: x", "ordinary log line", "ABC12 x",
	} {
		gotNext, gotAccepted := sm.Step(line, []int{0})
		wantNext, wantAccepted := unfiltered.Step(line, []int{0})
		assert.Equal(t, wantNext, gotNext, "line %q", line)
		assert.Equal(t, wantAccepted, gotAccepted, "line %q", line)
	}
}

// TestPrefilterTooManyTransitions verifies that start transitions past the
// 64th — which no candidate mask can address — always run, while the first 64
// keep filtering.
func TestPrefilterTooManyTransitions(t *testing.T) {
	start := State{Name: StartState}
	for i := range 66 {
		start.Transitions = append(start.Transitions,
			Transition{Pattern: fmt.Sprintf(`literal%02d`, i), Next: "s"})
	}
	sm, err := Compile(StateSet{Name: "big", States: []State{start, {Name: "s"}}})
	assert.NoError(t, err)
	assert.Len(t, sm.StartLiterals(), 64)
	assert.Equal(t, []string{`literal64`, `literal65`}, sm.UnfilteredStarts())
	assert.True(t, sm.pf.wide)

	unfiltered := *sm
	unfiltered.pf = nil
	for _, line := range []string{"", "nothing here", "literal00 x", "x literal64", "literal65"} {
		gotNext, gotAccepted := sm.Step(line, []int{0})
		wantNext, wantAccepted := unfiltered.Step(line, []int{0})
		assert.Equal(t, wantNext, gotNext, "line %q", line)
		assert.Equal(t, wantAccepted, gotAccepted, "line %q", line)
	}
}
