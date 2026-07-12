package patterns

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

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
		// Too many alternation branches exceed the product cap; the trailing
		// child literal is still provable on its own.
		{`(a|b|c|d|e|f|g|h|i|j|k|l|m|n|o|p|q)xyz`, []string{"xyz"}, true},
		// A product exceeding the length cap falls back to the most selective
		// single child literal.
		{strings.Repeat("A", 40) + `(x|y)` + strings.Repeat("B", 30),
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

// TestStartLiteralsDedupe verifies that duplicate probes and probes containing
// another probe are dropped.
func TestStartLiteralsDedupe(t *testing.T) {
	sm := MustCompile(StateSet{Name: "a", States: []State{
		{Name: StartState, Transitions: []Transition{
			{Pattern: `foobar`, Next: "s"},
			{Pattern: `xxfoobarxx`, Next: "s"}, // contains "foobar": redundant
			{Pattern: `foobar!`, Next: "s"},    // contains "foobar": redundant
			{Pattern: `foobar`, Next: "t"},     // exact duplicate
		}},
		{Name: "s"},
		{Name: "t"},
	}})
	assert.Equal(t, []string{"foobar"}, sm.StartLiterals())
}

// TestBundledPrefilterEnabled guards the bundled sets: a new start pattern
// without a provable literal would silently disable the prefilter for
// everyone.
func TestBundledPrefilterEnabled(t *testing.T) {
	lits := MustCompile(All...).StartLiterals()
	assert.NotEmpty(t, lits)
	for _, l := range lits {
		assert.GreaterOrEqual(t, len(l), 3, "weak probe %q", l)
	}
	// The product expansion must keep the trailing colon: without it, every
	// lowercase "error" log line would fall through to the regexes.
	assert.Contains(t, lits, "Error:")
	assert.Contains(t, lits, "panic: ")
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
		"thread 'main' panicked at src/main.rs:5:5:",
		"PHP Fatal error:  Uncaught Exception: x in /a.php:1",
		"main.rb:4:in `foo': boom (NoMethodError)",
		"é ünicode Ërror: line",
	}
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

	// Start-transition order follows the set order in All: go declares
	// transitions 0-1, java 2, python 3, dotnet 4-5, ruby 6, rust 7, php 8.
	assert.Equal(t, uint64(1<<0), maskOf("panic: "))
	assert.Equal(t, uint64(1<<1), maskOf("http: panic serving"))
	assert.Equal(t, uint64(1<<2), maskOf("Error:"))
	assert.Equal(t, uint64(1<<3), maskOf("Traceback (most recent call last):"))
	assert.Equal(t, uint64(1<<4|1<<5), maskOf("Unhandled exception. "))
	assert.Equal(t, uint64(1<<6), maskOf("Error)"))
}

// TestPrefilterTooManyTransitions verifies that more than 64 start
// transitions disable the prefilter (the candidate masks are 64-bit).
func TestPrefilterTooManyTransitions(t *testing.T) {
	start := State{Name: StartState}
	for range 65 {
		start.Transitions = append(start.Transitions, Transition{Pattern: `longliteral`, Next: "s"})
	}
	sm, err := Compile(StateSet{Name: "big", States: []State{start, {Name: "s"}}})
	assert.NoError(t, err)
	assert.Nil(t, sm.StartLiterals())
}
