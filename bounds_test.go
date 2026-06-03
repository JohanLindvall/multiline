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

// mustContinuation builds a Multiline whose matcher groups an "after"-style entry:
// a line matching header starts a group and every subsequent line matching cont
// extends it. It is just enough of a matcher to exercise the WithMaxLines /
// WithMaxBytes bounds below.
func mustContinuation(t *testing.T, header, cont string, emit func(ctx context.Context, line, match string, _ struct{}) error, opts ...Option) *Multiline[struct{}] {
	t.Helper()
	sm, err := Compile([]State{
		{Name: "start_state", Advance: []Advance{{Pattern: header, Next: "after"}}},
		{Name: "after", Advance: []Advance{{Pattern: cont, Next: "after"}}},
	})
	assert.NoError(t, err)
	return New(emit, append(opts, WithMatcher(sm))...)
}

func TestMaxLines(t *testing.T) {
	// Five continuation lines, capped at 3 stored lines (1 header + 2 continuations).
	lines := []string{"ERROR boom", "  1", "  2", "  3", "  4"}
	got := collect(t, lines, func(emit func(ctx context.Context, line, match string, _ struct{}) error) *Multiline[struct{}] {
		return mustContinuation(t, `^\S`, `^\s`, emit, WithMaxLines(3))
	})
	assert.Equal(t, [][2]string{{"ERROR boom\n  1\n  2", "after"}}, got)
}

func TestMaxBytes(t *testing.T) {
	// Header is 10 bytes; cap retains the header plus 4 bytes of the next line
	// ("\n" + "abc"), truncating it.
	lines := []string{"HEADER____", "abcdefgh"}
	got := collect(t, lines, func(emit func(ctx context.Context, line, match string, _ struct{}) error) *Multiline[struct{}] {
		return mustContinuation(t, `^[A-Z]`, `^[a-z]`, emit, WithMaxBytes(14))
	})
	assert.Equal(t, [][2]string{{"HEADER____\nabc", "after"}}, got)
}

func TestMaxBytesRuneBoundary(t *testing.T) {
	// "é" is two bytes; the cap must not split it, so the second line is dropped
	// entirely (no room for a whole rune after the separator).
	lines := []string{"HEAD", "é"}
	got := collect(t, lines, func(emit func(ctx context.Context, line, match string, _ struct{}) error) *Multiline[struct{}] {
		return mustContinuation(t, `.`, `.`, emit, WithMaxBytes(6))
	})
	// Header retained (4 bytes), separator would be byte 5, only 1 byte left for the
	// 2-byte rune -> dropped, leaving just the header in the group.
	assert.Equal(t, [][2]string{{"HEAD", "after"}}, got)
}
