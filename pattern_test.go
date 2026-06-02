package multiline

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

// collect feeds lines into ml and returns the emitted (line, match) pairs.
func collect(t *testing.T, lines []string, build func(emit func(ctx context.Context, line, match string, _ struct{}) error) *Multiline[struct{}]) [][2]string {
	t.Helper()
	var got [][2]string
	ml := build(func(_ context.Context, line, match string, _ struct{}) error {
		got = append(got, [2]string{line, match})
		return nil
	})
	for _, line := range lines {
		assert.NoError(t, ml.Add(context.Background(), line, "key", struct{}{}))
	}
	assert.NoError(t, ml.Stop(context.Background()))
	return got
}

func mustPattern(t *testing.T, pattern string, negate bool, dir Direction, emit func(ctx context.Context, line, match string, _ struct{}) error, opts ...Option) *Multiline[struct{}] {
	t.Helper()
	ml, err := NewPattern(pattern, negate, dir, emit, opts...)
	assert.NoError(t, err)
	return ml
}

func TestPatternAfter(t *testing.T) {
	// Continuation lines are indented; each non-indented line starts a new event.
	lines := []string{
		"ERROR boom",
		"  at foo",
		"  at bar",
		"INFO ok",
	}
	got := collect(t, lines, func(emit func(ctx context.Context, line, match string, _ struct{}) error) *Multiline[struct{}] {
		return mustPattern(t, `^\s`, false, After, emit)
	})
	assert.Equal(t, [][2]string{
		{"ERROR boom\n  at foo\n  at bar", "after"},
		{"INFO ok", ""},
	}, got)
}

func TestPatternBefore(t *testing.T) {
	// A trailing backslash means the next line continues the current one.
	lines := []string{
		`line a \`,
		`line b \`,
		"line c",
		"standalone",
	}
	got := collect(t, lines, func(emit func(ctx context.Context, line, match string, _ struct{}) error) *Multiline[struct{}] {
		return mustPattern(t, `\\$`, false, Before, emit)
	})
	assert.Equal(t, [][2]string{
		{"line a \\\nline b \\\nline c", "before"},
		{"standalone", ""},
	}, got)
}

func TestPatternNegate(t *testing.T) {
	// negate inverts the match: here non-empty lines are continuations and a blank
	// line terminates the group (before direction).
	lines := []string{
		"header",
		"body 1",
		"",
		"next",
	}
	got := collect(t, lines, func(emit func(ctx context.Context, line, match string, _ struct{}) error) *Multiline[struct{}] {
		return mustPattern(t, `^$`, true, Before, emit)
	})
	assert.Equal(t, [][2]string{
		{"header\nbody 1\n", "before"},
		{"next", ""},
	}, got)
}

func TestNewPatternInvalidRegexp(t *testing.T) {
	_, err := NewPattern[struct{}]("(", false, After, nil)
	assert.Error(t, err)
}

func TestMaxLines(t *testing.T) {
	// Five continuation lines, capped at 3 stored lines (1 header + 2 continuations).
	lines := []string{"ERROR boom", "  1", "  2", "  3", "  4"}
	got := collect(t, lines, func(emit func(ctx context.Context, line, match string, _ struct{}) error) *Multiline[struct{}] {
		return mustPattern(t, `^\s`, false, After, emit, WithMaxLines(3))
	})
	assert.Equal(t, [][2]string{{"ERROR boom\n  1\n  2", "after"}}, got)
}

func TestMaxBytes(t *testing.T) {
	// Header is 10 bytes; cap retains the header plus 4 bytes of the next line
	// ("\n" + "abc"), truncating it.
	lines := []string{"HEADER____", "abcdefgh"}
	got := collect(t, lines, func(emit func(ctx context.Context, line, match string, _ struct{}) error) *Multiline[struct{}] {
		return mustPattern(t, `^[a-z]`, false, After, emit, WithMaxBytes(14))
	})
	assert.Equal(t, [][2]string{{"HEADER____\nabc", "after"}}, got)
}

func TestMaxBytesRuneBoundary(t *testing.T) {
	// "é" is two bytes; the cap must not split it, so the second line is dropped
	// entirely (no room for a whole rune after the separator).
	lines := []string{"HEAD", "é"}
	got := collect(t, lines, func(emit func(ctx context.Context, line, match string, _ struct{}) error) *Multiline[struct{}] {
		return mustPattern(t, `.`, false, After, emit, WithMaxBytes(6))
	})
	// Header retained (4 bytes), separator would be byte 5, only 1 byte left for the
	// 2-byte rune -> dropped, leaving a single-line (as-is) emit.
	assert.Equal(t, [][2]string{{"HEAD", ""}}, got)
}
