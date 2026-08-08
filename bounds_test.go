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
	// Four continuation lines, capped at 3 retained lines (1 header + 2
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

// threeState builds a machine whose header lands in an accepting state and
// whose continuations all land in a non-terminal one, so a group's last accept
// is its first line and a cap can bite strictly after it.
func threeState(t *testing.T, emit Emitter[int], opts ...Option) *Aggregator[int] {
	t.Helper()
	sm, err := patterns.Compile(patterns.StateSet{Name: "test", States: []patterns.State{
		{Name: patterns.StartState, Transitions: []patterns.Transition{{Pattern: `^HDR`, Next: "ok"}}},
		{Name: "ok", Transitions: []patterns.Transition{{Pattern: `^cont`, Next: "tail"}}},
		{Name: "tail", NonTerminal: true, Transitions: []patterns.Transition{{Pattern: `^cont`, Next: "tail"}}},
	}})
	assert.NoError(t, err)
	return New(emit, append(opts, WithMatcher(sm))...)
}

// TestDroppedLinesAccounted is a regression test: lines the caps dropped from a
// group that never reached an accepting state used to disappear from the Lines
// accounting, so summing Lines over a stream under-counted the input.
func TestDroppedLinesAccounted(t *testing.T) {
	var got []Entry[int]
	ml := threeState(t, func(_ context.Context, e Entry[int]) error {
		e.Texts = nil
		got = append(got, e)
		return nil
	}, WithMaxLines(3))
	ctx := context.Background()
	// Five lines consumed; the group never completes (only "ok" accepts, and
	// the first line's accept does not count), so all three retained lines are
	// emitted individually and the two dropped ones are charged to the last.
	for i, line := range []string{"HDR", "cont1", "cont2", "cont3", "cont4"} {
		assert.NoError(t, ml.Add(ctx, "key", line, i))
	}
	assert.NoError(t, ml.Stop(ctx))

	assert.Equal(t, []Entry[int]{
		{Text: "HDR", Key: "key", Lines: 1, Data: 0},
		{Text: "cont1", Key: "key", Lines: 1, Data: 1},
		{Text: "cont2", Key: "key", Lines: 3, Data: 2, Truncated: true},
	}, got)

	total := 0
	for _, e := range got {
		total += e.Lines
	}
	assert.Equal(t, 5, total, "Lines must sum to the lines consumed")
}

// TestDroppedLinesAfterAccept covers the same accounting when there is an
// aggregated prefix and the cap dropped every line after it.
func TestDroppedLinesAfterAccept(t *testing.T) {
	// The second line accepts; every line after it lands in a non-terminal
	// state, so the accepted prefix stops growing while the cap drops the rest.
	sm, err := patterns.Compile(patterns.StateSet{Name: "test", States: []patterns.State{
		{Name: patterns.StartState, Transitions: []patterns.Transition{{Pattern: `^HDR`, Next: "ok"}}},
		{Name: "ok", Transitions: []patterns.Transition{{Pattern: `^cont`, Next: "mid"}}},
		{Name: "mid", Transitions: []patterns.Transition{{Pattern: `^cont`, Next: "tail"}}},
		{Name: "tail", NonTerminal: true, Transitions: []patterns.Transition{{Pattern: `^cont`, Next: "tail"}}},
	}})
	assert.NoError(t, err)

	var got []Entry[int]
	ml := New(func(_ context.Context, e Entry[int]) error {
		e.Texts = nil
		got = append(got, e)
		return nil
	}, WithMatcher(sm), WithMaxLines(2))
	ctx := context.Background()
	for i, line := range []string{"HDR", "cont1", "cont2", "cont3"} {
		assert.NoError(t, ml.Add(ctx, "key", line, i))
	}
	assert.NoError(t, ml.Stop(ctx))

	// The accepted prefix is both retained lines, so the two dropped lines are
	// charged to the aggregated entry itself.
	assert.Equal(t, []Entry[int]{
		{Text: "HDR\ncont1", Key: "key", Match: "test", Lines: 4, Data: 0, Truncated: true},
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
