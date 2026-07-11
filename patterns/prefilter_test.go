package patterns

import (
	"bytes"
	"os"
	"path/filepath"
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
	} {
		got, ok := requiredLiterals(tc.pattern)
		assert.Equal(t, tc.ok, ok, tc.pattern)
		if tc.ok {
			assert.ElementsMatch(t, tc.want, got, tc.pattern)
		}
	}
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
	unfiltered.startLiterals = nil

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
		sm.startLiterals = nil
		b.ReportAllocs()
		for range b.N {
			sm.Step(line, start)
		}
	})
}
