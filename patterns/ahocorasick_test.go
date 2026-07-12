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

// linearScan is the reference the automaton must agree with.
func linearScan(literals []string, masks []uint64, line string) uint64 {
	var mask uint64
	for i, lit := range literals {
		if strings.Contains(line, lit) {
			mask |= masks[i]
		}
	}
	return mask
}

func TestAhoCorasickScan(t *testing.T) {
	// The classic overlapping set: suffix relationships ("hers"/"ers"→"she"
	// via fail links) exercise mask propagation along failure chains.
	literals := []string{"he", "she", "his", "hers"}
	masks := []uint64{1 << 0, 1 << 1, 1 << 2, 1 << 3}
	ac := buildAhoCorasick(literals, masks)

	for _, tc := range []struct {
		line string
		want uint64
	}{
		{"", 0},
		{"nothing here at all... well", 1 << 0}, // "here" contains "he"
		{"xxx", 0},
		{"ushers", 1<<0 | 1<<1 | 1<<3}, // "she", "he", "hers" all end inside
		{"his hers", 1<<0 | 1<<2 | 1<<3},
		{"hi hishe", 1<<0 | 1<<1 | 1<<2},
		{"h", 0},
		{"hehehe", 1 << 0},
	} {
		assert.Equal(t, tc.want, ac.scan(tc.line), tc.line)
		assert.Equal(t, linearScan(literals, masks, tc.line), ac.scan(tc.line), tc.line)
	}
}

// TestAhoCorasickPrefixLiterals verifies that a literal that is a strict
// prefix of another is reported when the walk passes through it.
func TestAhoCorasickPrefixLiterals(t *testing.T) {
	literals := []string{"abc", "abcdef"}
	masks := []uint64{1, 2}
	ac := buildAhoCorasick(literals, masks)
	assert.Equal(t, uint64(1), ac.scan("xx abcde xx"))
	assert.Equal(t, uint64(3), ac.scan("xx abcdef xx"))
}

// TestAhoCorasickDifferential runs the bundled prefilter's literal set
// through both scanners over every corpus line plus adversarial near-misses
// and requires identical masks.
func TestAhoCorasickDifferential(t *testing.T) {
	pf := MustCompile(All...).pf
	assert.NotNil(t, pf)
	assert.Nil(t, pf.ac, "bundled literal count below threshold uses the linear scan")
	ac := buildAhoCorasick(pf.literals, pf.masks)

	lines := []string{
		"", " ", "Error: bare", "eErrors:", "panic: mid panic: twice",
		"Traceback (most recent call last):", "é ünicode Ërror: line",
		strings.Repeat("Error", 100) + ":",
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
		assert.Equal(t, linearScan(pf.literals, pf.masks, line), ac.scan(line), "line %q", line)
	}
}

// TestPrefilterUsesAhoCorasickPastThreshold verifies Compile switches scanner
// implementations on the literal count without changing decisions.
func TestPrefilterUsesAhoCorasickPastThreshold(t *testing.T) {
	start := State{Name: StartState}
	var sets []StateSet
	for i := range acMinLiterals {
		start.Transitions = append(start.Transitions,
			Transition{Pattern: fmt.Sprintf("startmarker%02d: ", i), Next: "body"})
	}
	sets = append(sets, StateSet{Name: "many", States: []State{start, {Name: "body"}}})
	sm := MustCompile(sets...)
	assert.NotNil(t, sm.pf.ac)
	assert.Len(t, sm.pf.literals, acMinLiterals)

	next, accepted := sm.Step("prefix startmarker07: suffix", []int{0})
	assert.Len(t, next, 1)
	assert.Equal(t, "many", sm.Format(accepted))
	next, _ = sm.Step("no probe here", []int{0})
	assert.Empty(t, next)
}

// BenchmarkPrefilterScan compares the linear Contains loop against
// Aho-Corasick across literal counts, on a typical no-match log line. This is
// the measurement behind acMinLiterals.
func BenchmarkPrefilterScan(b *testing.B) {
	line := `2024-11-19 11:00:00.123 INFO [main] com.example.Application - Starting application`

	for _, n := range []int{13, 24, 48} {
		var literals []string
		var masks []uint64
		bundled := MustCompile(All...).pf
		literals = append(literals, bundled.literals...)
		masks = append(masks, bundled.masks...)
		for i := len(literals); i < n; i++ {
			literals = append(literals, fmt.Sprintf("startmarker%02d: ", i))
			masks = append(masks, 1<<uint(i%64))
		}
		literals = literals[:n]
		masks = masks[:n]

		b.Run(fmt.Sprintf("linear/%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				linearScan(literals, masks, line)
			}
		})
		b.Run(fmt.Sprintf("ahocorasick/%d", n), func(b *testing.B) {
			ac := buildAhoCorasick(literals, masks)
			b.ReportAllocs()
			for range b.N {
				ac.scan(line)
			}
		})
	}
}
