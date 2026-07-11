package multiline

import (
	"context"
	"errors"
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
