package multiline

import (
	"context"
	"strings"
	"testing"

	"github.com/JohanLindvall/multiline/patterns"
	"github.com/stretchr/testify/assert"
)

// continuation builds an aggregator whose matcher groups an "after"-style
// entry: a line matching header starts a group and every subsequent line
// matching cont extends it. Just enough to exercise the bounds options.
func continuation(t *testing.T, header, cont string, emit Emitter[int], opts ...Option) *Aggregator[int] {
	t.Helper()
	sm, err := patterns.Compile(patterns.StateSet{Name: "test", States: []patterns.State{
		{Name: patterns.StartState, Transitions: []patterns.Transition{{Pattern: header, Next: "after"}}},
		{Name: "after", Transitions: []patterns.Transition{{Pattern: cont, Next: "after"}}},
	}})
	assert.NoError(t, err)
	return New(emit, append(opts, WithMatcher(sm))...)
}

func collectBounded(t *testing.T, header, cont string, lines []string, opts ...Option) []Entry[int] {
	t.Helper()
	var got []Entry[int]
	ml := continuation(t, header, cont, func(_ context.Context, e Entry[int]) error {
		// Texts always mirrors Text; drop the borrowed slice before retaining.
		assert.Equal(t, e.Text, strings.Join(e.Texts, "\n"))
		e.Texts = nil
		got = append(got, e)
		return nil
	}, opts...)
	for i, line := range lines {
		assert.NoError(t, ml.Add(context.Background(), "key", line, i))
	}
	assert.NoError(t, ml.Stop(context.Background()))
	return got
}

func TestMaxLines(t *testing.T) {
	// Five continuation lines, capped at 3 retained lines (1 header + 2
	// continuations); Lines still counts all 5 consumed lines.
	lines := []string{"ERROR boom", "  1", "  2", "  3", "  4"}
	got := collectBounded(t, `^\S`, `^\s`, lines, WithMaxLines(3))
	assert.Equal(t, []Entry[int]{
		{Text: "ERROR boom\n  1\n  2", Key: "key", Match: "test", Lines: 5, Data: 0, Truncated: true},
	}, got)
}

func TestMaxBytes(t *testing.T) {
	// Header is 10 bytes; the cap retains the header plus 4 bytes of the next
	// line ("\n" + "abc"), cutting it.
	lines := []string{"HEADER____", "abcdefgh"}
	got := collectBounded(t, `^[A-Z]`, `^[a-z]`, lines, WithMaxBytes(14))
	assert.Equal(t, []Entry[int]{
		{Text: "HEADER____\nabc", Key: "key", Match: "test", Lines: 2, Data: 0, Truncated: true},
	}, got)
}

func TestMaxBytesRuneBoundary(t *testing.T) {
	// "é" is two bytes; the cap must not split it, so the second line is
	// dropped entirely (no room for a whole rune after the separator).
	lines := []string{"HEAD", "é"}
	got := collectBounded(t, `.`, `.`, lines, WithMaxBytes(6))
	assert.Equal(t, []Entry[int]{
		{Text: "HEAD", Key: "key", Match: "test", Lines: 2, Data: 0, Truncated: true},
	}, got)
}

// TestMaxBytesFirstLineMultibyte is a regression test: a first line whose
// leading rune does not fit used to leave the group empty and panic on emit.
// The first line is retained cut-to-empty instead.
func TestMaxBytesFirstLineMultibyte(t *testing.T) {
	lines := []string{"é first", "second", "third"}
	got := collectBounded(t, `.`, `.`, lines, WithMaxBytes(1))
	assert.Equal(t, []Entry[int]{
		{Text: "", Key: "key", Match: "test", Lines: 3, Data: 0, Truncated: true},
	}, got)
}

// TestMaxBytesNeverAccepted verifies the Truncated flag lands on the last
// individually emitted line when a capped group never completes.
func TestMaxBytesNeverAccepted(t *testing.T) {
	// The header matches but nothing continues it, and it is cut by the cap.
	lines := []string{"HEADER____", "HEADER____"}
	got := collectBounded(t, `^[A-Z]`, `^[a-z]`, lines, WithMaxBytes(4))
	assert.Equal(t, []Entry[int]{
		{Text: "HEAD", Key: "key", Lines: 1, Data: 0, Truncated: true},
		{Text: "HEAD", Key: "key", Lines: 1, Data: 1, Truncated: true},
	}, got)
}
