// SPDX-License-Identifier: MIT

package multiline

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/JohanLindvall/multiline/patterns"
	"github.com/stretchr/testify/assert"
)

// TestTouchOrder verifies that continuing a group moves it to the tail of the
// last-touched list: Stop then flushes in touch order, not creation order.
func TestTouchOrder(t *testing.T) {
	var got []string
	ml := New(func(_ context.Context, e Entry[struct{}]) error {
		got = append(got, e.Key)
		return nil
	})
	ctx := context.Background()
	for _, key := range []string{"a", "b", "c"} {
		assert.NoError(t, ml.Add(ctx, key, "panic: "+key, struct{}{}))
	}
	// Touch b (middle of the list), then a (head): both continue their group
	// with the blank line of a Go panic header.
	assert.NoError(t, ml.Add(ctx, "b", "", struct{}{}))
	assert.NoError(t, ml.Add(ctx, "a", "", struct{}{}))
	assert.NoError(t, ml.Stop(ctx))
	assert.Equal(t, []string{"c", "b", "a"}, got)
}

// TestWithClock verifies that Add stamps groups with the injected clock.
func TestWithClock(t *testing.T) {
	now := time.Unix(1000, 0)
	var got []string
	ml := New(func(_ context.Context, e Entry[struct{}]) error {
		got = append(got, e.Text)
		return nil
	}, WithClock(func() time.Time { return now }))
	ctx := context.Background()

	assert.NoError(t, ml.Add(ctx, "a", "panic: a", struct{}{}))
	now = now.Add(10 * time.Second)
	assert.NoError(t, ml.Add(ctx, "b", "panic: b", struct{}{}))

	assert.NoError(t, ml.FlushBefore(ctx, time.Unix(1005, 0)))
	assert.Equal(t, []string{"panic: a"}, got)
	assert.NoError(t, ml.Stop(ctx))
	assert.Equal(t, []string{"panic: a", "panic: b"}, got)
}

// TestEmitterErrors verifies that emitter errors propagate out of every
// entry-producing call.
func TestEmitterErrors(t *testing.T) {
	boom := errors.New("boom")
	failing := func(_ context.Context, _ Entry[struct{}]) error { return boom }
	ctx := context.Background()

	t.Run("pass-through", func(t *testing.T) {
		ml := New(failing)
		assert.ErrorIs(t, ml.Add(ctx, "k", "plain", struct{}{}), boom)
		assert.ErrorIs(t, ml.Add(ctx, "", "keyless", struct{}{}), boom)
	})

	t.Run("flush of never-accepted group", func(t *testing.T) {
		ml := New(failing)
		assert.NoError(t, ml.Add(ctx, "k", "panic: x", struct{}{}))
		assert.ErrorIs(t, ml.Add(ctx, "k", "plain", struct{}{}), boom)
	})

	t.Run("flush of aggregated group", func(t *testing.T) {
		ml := New(failing)
		assert.NoError(t, ml.Add(ctx, "k", "java.lang.Exception: x", struct{}{}))
		assert.NoError(t, ml.Add(ctx, "k", "\tat a.b(C.java:1)", struct{}{}))
		assert.ErrorIs(t, ml.Flush(ctx, "k"), boom)
	})

	t.Run("stop and flush-before", func(t *testing.T) {
		ml := New(failing)
		assert.NoError(t, ml.Add(ctx, "k", "panic: x", struct{}{}))
		assert.ErrorIs(t, ml.FlushBefore(ctx, time.Now().Add(time.Hour)), boom)
		assert.NoError(t, ml.Add(ctx, "k", "panic: x", struct{}{}))
		assert.ErrorIs(t, ml.Stop(ctx), boom)
	})

	t.Run("max-groups eviction", func(t *testing.T) {
		ml := New(failing, WithMaxGroups(1))
		assert.NoError(t, ml.Add(ctx, "a", "panic: a", struct{}{}))
		assert.ErrorIs(t, ml.Add(ctx, "b", "panic: b", struct{}{}), boom)
	})
}

// TestFlushErrorKeepsLine is a regression test: the line that forced a group
// to flush used to be dropped when the flush failed, so a failing emitter lost
// input the caller could not recover.
func TestFlushErrorKeepsLine(t *testing.T) {
	boom := errors.New("boom")
	var seen []string
	ml := New(func(_ context.Context, e Entry[int]) error {
		seen = append(seen, e.Text)
		if strings.HasPrefix(e.Text, "panic: ") {
			return boom
		}
		return nil
	})
	ctx := context.Background()

	// The line passes through after the failed flush.
	assert.NoError(t, ml.Add(ctx, "k", "panic: x", 0))
	assert.ErrorIs(t, ml.Add(ctx, "k", "plain", 1), boom)
	assert.Equal(t, []string{"panic: x", "plain"}, seen)

	// The line starts a group of its own after the failed flush.
	seen = nil
	assert.NoError(t, ml.Add(ctx, "j", "panic: y", 0))
	assert.ErrorIs(t, ml.Add(ctx, "j", "panic: z", 1), boom)
	assert.Equal(t, []string{"panic: y"}, seen, "only the flush emitted")
	assert.True(t, ml.Pending("j"), "the new group is buffered, not lost")
	assert.ErrorIs(t, ml.Stop(ctx), boom)
	assert.Equal(t, []string{"panic: y", "panic: z"}, seen)
}

// TestReentrantEmitter verifies that an emitter feeding this same aggregator
// does not corrupt the Texts view of the entry it is still handling: the
// scratch backing is chosen by re-entrancy depth.
func TestReentrantEmitter(t *testing.T) {
	var ml *Aggregator[int]
	var seen []string
	depth := 0
	ml = New(func(ctx context.Context, e Entry[int]) error {
		texts := e.Texts // borrowed, held across the re-entrant Add below
		if depth == 0 {
			depth++
			assert.NoError(t, ml.Add(ctx, "other", "SECOND", 1))
			depth--
		}
		seen = append(seen, texts[0])
		return nil
	})
	assert.NoError(t, ml.Add(context.Background(), "k", "FIRST", 0))
	assert.Equal(t, []string{"SECOND", "FIRST"}, seen)
}

// TestReentrantEmitterSameKey covers the harder case: the emitter feeds this
// aggregator under the very key whose flush it is handling. The re-entrant
// line claims the key's map entry first, so the line that triggered the flush
// must drain it rather than overwrite it — otherwise two live groups share one
// key, one reachable through the map and one orphaned in the last-touched
// list, and Len/Keys/Pending/Bytes stop agreeing about which. A shipper
// draining on Len() or Pending() would then conclude the stream is empty and
// drop the tail.
func TestReentrantEmitterSameKey(t *testing.T) {
	var ml *Aggregator[int]
	var got []string
	// Re-enter on the first two entries, not just the first. Draining the key
	// runs the emitter again, which claims the key again, so a one-shot check
	// in place of the loop leaves the second group orphaned — with a single
	// re-entry it would pass and pin nothing.
	reentries := 0
	ml = New(func(ctx context.Context, e Entry[int]) error {
		got = append(got, e.Text)
		if reentries < 2 {
			reentries++
			return ml.Add(ctx, "k", fmt.Sprintf("panic: reentrant%d", reentries), 99)
		}
		return nil
	})
	ctx := context.Background()

	// "panic: y" opens a group; "panic: z" cannot continue it, so it flushes
	// it — and that flush is where the emitter re-enters under "k".
	assert.NoError(t, ml.Add(ctx, "k", "panic: y", 0))
	assert.NoError(t, ml.Add(ctx, "k", "panic: z", 1))

	assert.Equal(t, 1, ml.Len())
	assert.Equal(t, []string{"k"}, ml.Keys(nil), "the map and the last-touched list must hold the same one group")
	assert.True(t, ml.Pending("k"))

	assert.NoError(t, ml.Flush(ctx, "k"))
	assert.False(t, ml.Pending("k"))
	assert.Equal(t, 0, ml.Len())
	assert.Empty(t, ml.Keys(nil))
	assert.Equal(t, 0, ml.Bytes(), "flushing the key must leave nothing buffered under it")

	assert.NoError(t, ml.Stop(ctx))
	// Each re-entrant line arrived after the entry being handled was emitted
	// and before "panic: z" was buffered, and they come out in that order.
	assert.Equal(t, []string{
		"panic: y", "panic: reentrant1", "panic: reentrant2", "panic: z",
	}, got)
}

// TestKeys verifies that Keys reports pending keys in last-touched order.
func TestKeys(t *testing.T) {
	ml := New(func(_ context.Context, _ Entry[struct{}]) error { return nil })
	ctx := context.Background()
	assert.Empty(t, ml.Keys(nil))

	for _, key := range []string{"a", "b", "c"} {
		assert.NoError(t, ml.Add(ctx, key, "panic: "+key, struct{}{}))
	}
	assert.Equal(t, []string{"a", "b", "c"}, ml.Keys(nil))

	assert.NoError(t, ml.Add(ctx, "a", "", struct{}{})) // continues a's group
	assert.Equal(t, []string{"b", "c", "a"}, ml.Keys(nil))

	dst := make([]string, 0, 4)
	assert.Equal(t, []string{"b", "c", "a"}, ml.Keys(dst), "appends to a reused slice")
}

// TestMaxTotalBytes verifies that the global byte budget flushes the least
// recently touched groups, whatever the key cardinality.
func TestMaxTotalBytes(t *testing.T) {
	var got []string
	ml := New(func(_ context.Context, e Entry[struct{}]) error {
		got = append(got, e.Key)
		return nil
	}, WithMaxTotalBytes(20))
	ctx := context.Background()

	// Each group buffers 8 bytes ("panic: x").
	assert.NoError(t, ml.Add(ctx, "a", "panic: a", struct{}{}))
	assert.NoError(t, ml.Add(ctx, "b", "panic: b", struct{}{}))
	assert.Empty(t, got)
	assert.Equal(t, 16, ml.Bytes())

	assert.NoError(t, ml.Add(ctx, "c", "panic: c", struct{}{})) // 24 > 20
	assert.Equal(t, []string{"a"}, got)
	assert.Equal(t, 16, ml.Bytes())
	assert.Equal(t, []string{"b", "c"}, ml.Keys(nil))
}

// TestMaxTotalBytesOnContinuation verifies that the budget is enforced when a
// continuation line grows a group too, not only when a new key arrives.
func TestMaxTotalBytesOnContinuation(t *testing.T) {
	var got []string
	ml := continuation(t, `^HDR`, `^cont`, func(_ context.Context, e Entry[int]) error {
		got = append(got, e.Key)
		return nil
	}, WithMaxTotalBytes(20))
	ctx := context.Background()

	assert.NoError(t, ml.Add(ctx, "a", "HDR", 0))
	assert.NoError(t, ml.Add(ctx, "b", "HDR", 1))
	assert.Empty(t, got)

	assert.NoError(t, ml.Add(ctx, "b", "cont-long-line", 2)) // 3 + 1+14+3 > 20
	assert.Equal(t, []string{"a"}, got)
	assert.Equal(t, 18, ml.Bytes())
}

// TestFinalStateEmitsImmediately verifies the FinalMatcher fast path: a group
// that lands in a state no line can leave is emitted at once instead of being
// held until the key's next line or a flush. A PHP report's closing "thrown
// in ..." line is such a state.
func TestFinalStateEmitsImmediately(t *testing.T) {
	var got []Entry[int]
	ml := New(func(_ context.Context, e Entry[int]) error {
		got = append(got, e)
		return nil
	})
	ctx := context.Background()
	for i, line := range []string{
		"PHP Fatal error:  Uncaught Exception: boom in /app/index.php:3",
		"Stack trace:",
		"#0 /app/index.php(7): foo()",
		"#1 {main}",
		"  thrown in /app/index.php on line 3",
	} {
		assert.NoError(t, ml.Add(ctx, "k", line, i))
	}

	assert.Len(t, got, 1, "emitted without waiting for another line")
	assert.Equal(t, "php", got[0].Match)
	assert.Equal(t, 5, got[0].Lines)
	assert.False(t, ml.Pending("k"))
	assert.Zero(t, ml.Bytes())
}

// TestFinalFirstLine verifies that a first line landing in a dead end is
// emitted straight away as the single line it is: it can never grow into an
// aggregate, so there is nothing to wait for.
func TestFinalFirstLine(t *testing.T) {
	sm, err := patterns.Compile(patterns.StateSet{Name: "test", States: []patterns.State{
		{Name: patterns.StartState, Transitions: []patterns.Transition{{Pattern: `^ONLY`, Next: "done"}}},
		{Name: "done"},
	}})
	assert.NoError(t, err)

	var got []Entry[int]
	ml := New(func(_ context.Context, e Entry[int]) error {
		assert.Equal(t, []string{"ONLY this"}, e.Texts)
		e.Texts = nil // borrowed only for the duration of this call
		got = append(got, e)
		return nil
	}, WithMatcher(sm))
	assert.NoError(t, ml.Add(context.Background(), "k", "ONLY this", 0))

	assert.Equal(t, []Entry[int]{{Text: "ONLY this", Key: "k", Lines: 1}}, got)
	assert.False(t, ml.Pending("k"))
}

// plainMatcher is a Matcher that deliberately does not implement
// FinalMatcher, so the aggregator has no way to know a state is a dead end.
type plainMatcher struct{ sm *patterns.StateMachine }

func (m plainMatcher) Step(line string, active []int) ([]int, int) { return m.sm.Step(line, active) }
func (m plainMatcher) Format(index int) string                     { return m.sm.Format(index) }

// TestMatcherWithoutFinal verifies that a matcher not implementing
// FinalMatcher keeps the original behavior: the completed group waits.
func TestMatcherWithoutFinal(t *testing.T) {
	var got []Entry[int]
	ml := New(func(_ context.Context, e Entry[int]) error {
		got = append(got, e)
		return nil
	}, WithMatcher(plainMatcher{patterns.MustCompile(patterns.All...)}))
	ctx := context.Background()
	for i, line := range []string{
		"thread 'main' panicked at src/main.rs:5:5:",
		"boom",
		"note: run with `RUST_BACKTRACE=1` environment variable to display a backtrace",
	} {
		assert.NoError(t, ml.Add(ctx, "k", line, i))
	}
	assert.Empty(t, got, "held until flushed")
	assert.True(t, ml.Pending("k"))

	assert.NoError(t, ml.Stop(ctx))
	assert.Len(t, got, 1)
	assert.Equal(t, "rust", got[0].Match)
}

// TestAcceptedPrefixTailError verifies the emitter-error path of the tail loop
// after an aggregated prefix: the aggregated entry emits fine, the tail line's
// emission fails, and that error reaches the caller.
func TestAcceptedPrefixTailError(t *testing.T) {
	boom := errors.New("boom")
	var texts []string
	ml := New(func(_ context.Context, e Entry[int]) error {
		texts = append(texts, e.Text)
		if e.Match == "" {
			return boom
		}
		return nil
	})
	ctx := context.Background()
	for i, line := range []string{
		"thread 'main' panicked at src/main.rs:5:5:",
		"boom message",
		"stack backtrace:", // consumed after the last accept, never accepted
	} {
		assert.NoError(t, ml.Add(ctx, "k", line, i))
	}
	// The aggregated prefix emits fine; the tail line's emission fails.
	assert.ErrorIs(t, ml.Stop(ctx), boom)
	assert.Equal(t, []string{
		"thread 'main' panicked at src/main.rs:5:5:\nboom message",
		"stack backtrace:",
	}, texts)
}

// TestEntryWhen verifies When reporting: aggregated entries carry the first
// line's AddAt time, tail lines their own, pass-through lines the supplied
// time, and Add-fed entries a zero When.
func TestEntryWhen(t *testing.T) {
	var got []Entry[struct{}]
	ml := New(func(_ context.Context, e Entry[struct{}]) error {
		got = append(got, e)
		return nil
	})
	ctx := context.Background()
	t0 := time.Unix(1000, 0)

	assert.NoError(t, ml.AddAt(ctx, "k", "plain", t0, struct{}{}))
	assert.NoError(t, ml.AddAt(ctx, "k", "thread 'main' panicked at src/main.rs:5:5:", t0.Add(time.Second), struct{}{}))
	assert.NoError(t, ml.AddAt(ctx, "k", "boom message", t0.Add(2*time.Second), struct{}{}))
	assert.NoError(t, ml.AddAt(ctx, "k", "stack backtrace:", t0.Add(3*time.Second), struct{}{}))
	assert.NoError(t, ml.Stop(ctx))
	assert.NoError(t, ml.Add(ctx, "k", "added without a time", struct{}{}))

	assert.Len(t, got, 4)
	assert.Equal(t, t0, got[0].When)                  // pass-through
	assert.Equal(t, t0.Add(time.Second), got[1].When) // aggregate: first line's
	assert.Equal(t, "rust", got[1].Match)
	assert.Equal(t, t0.Add(3*time.Second), got[2].When) // tail line: its own
	assert.True(t, got[3].When.IsZero(), "Add carries zero When")
}

// TestIntrospection verifies the Pending/Len/Bytes gauges.
func TestIntrospection(t *testing.T) {
	ml := New(func(_ context.Context, _ Entry[struct{}]) error { return nil })
	ctx := context.Background()

	assert.False(t, ml.Pending("a"))
	assert.Zero(t, ml.Len())
	assert.Zero(t, ml.Bytes())

	assert.NoError(t, ml.Add(ctx, "a", "panic: a", struct{}{}))
	assert.NoError(t, ml.Add(ctx, "b", "panic: bb", struct{}{}))
	assert.NoError(t, ml.Add(ctx, "b", "", struct{}{})) // continuation: +1 separator byte
	assert.True(t, ml.Pending("a"))
	assert.True(t, ml.Pending("b"))
	assert.False(t, ml.Pending("c"))
	assert.Equal(t, 2, ml.Len())
	assert.Equal(t, len("panic: a")+len("panic: bb")+1, ml.Bytes())

	assert.NoError(t, ml.Flush(ctx, "a"))
	assert.False(t, ml.Pending("a"))
	assert.Equal(t, 1, ml.Len())
	assert.Equal(t, len("panic: bb")+1, ml.Bytes())

	assert.NoError(t, ml.Stop(ctx))
	assert.Zero(t, ml.Len())
	assert.Zero(t, ml.Bytes())
}

// TestTexts verifies the zero-copy line view: Texts carries the retained
// source lines for every entry shape, and WithoutText leaves Text empty while
// Texts stays complete.
func TestTexts(t *testing.T) {
	trace := []string{
		"java.lang.NullPointerException: boom",
		"\tat com.example.Foo.bar(Foo.java:12)",
		"plain",
	}

	var texts [][]string
	var joined []string
	ml := New(func(_ context.Context, e Entry[struct{}]) error {
		texts = append(texts, slices.Clone(e.Texts))
		joined = append(joined, e.Text)
		return nil
	}, WithoutText())
	ctx := context.Background()
	for _, line := range trace {
		assert.NoError(t, ml.Add(ctx, "k", line, struct{}{}))
	}
	assert.NoError(t, ml.Stop(ctx))

	assert.Equal(t, [][]string{
		{"java.lang.NullPointerException: boom", "\tat com.example.Foo.bar(Foo.java:12)"},
		{"plain"},
	}, texts)
	assert.Equal(t, []string{"", ""}, joined, "WithoutText leaves Text empty")
}
