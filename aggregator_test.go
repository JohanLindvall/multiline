package multiline

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

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

// TestAcceptedPrefixTruncatedTail verifies the emitter-error path of the tail
// loop after an aggregated prefix, and that tail lines carry their own data.
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
