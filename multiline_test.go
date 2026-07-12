package multiline

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// Test_Unit_Multiline runs every file under tests/ through the default
// matcher. A corpus file's first line lists the expected entry sizes (in
// source lines, comma-separated); the rest is the log to feed.
func Test_Unit_Multiline(t *testing.T) {
	err := filepath.WalkDir("tests", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		t.Run(path, func(t *testing.T) {
			file, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			firstSplit := bytes.SplitN(file, []byte("\n"), 2)
			if len(firstSplit) < 2 {
				t.Fatal("Invalid test file, must have at least two lines")
			}
			file = firstSplit[1]
			var expected []int
			for _, part := range bytes.Split(firstSplit[0], []byte{','}) {
				val, err := strconv.Atoi(strings.TrimSpace(string(part)))
				if err != nil {
					t.Fatal(err)
				}
				expected = append(expected, val)
			}

			var expectedStr []string
			split := bytes.Split(file, []byte("\n"))
			for _, lines := range expected {
				tmp := min(lines, len(split))
				expectedStr = append(expectedStr, string(bytes.Join(split[:tmp], []byte("\n"))))
				split = split[tmp:]
			}

			var actualLineCounts []int
			var actualLines []string
			ml := New(func(_ context.Context, e Entry[struct{}]) error {
				actualLines = append(actualLines, e.Text)
				actualLineCounts = append(actualLineCounts, e.Lines)
				return nil
			})
			for _, line := range bytes.Split(file, []byte("\n")) {
				assert.NoError(t, ml.Add(context.Background(), "key", string(line), struct{}{}))
			}
			assert.NoError(t, ml.Stop(context.Background()))
			var msg strings.Builder
			for _, line := range actualLines {
				fmt.Fprintf(&msg, "==========================================================\n%s\n==========================================================\n", line)
			}
			assert.Equal(t, expected, actualLineCounts, msg.String())
			assert.Equal(t, expectedStr, actualLines, msg.String())
		})

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}
}

// collect feeds lines for key into a default-matcher aggregator and returns
// the emitted entries.
func collect(t *testing.T, key string, lines []string, opts ...Option) []Entry[int] {
	t.Helper()
	var got []Entry[int]
	ml := New(func(_ context.Context, e Entry[int]) error {
		// Texts always mirrors Text; drop the borrowed slice before retaining.
		assert.Equal(t, e.Text, strings.Join(e.Texts, "\n"))
		e.Texts = nil
		got = append(got, e)
		return nil
	}, opts...)
	for i, line := range lines {
		assert.NoError(t, ml.Add(context.Background(), key, line, i))
	}
	assert.NoError(t, ml.Stop(context.Background()))
	return got
}

// TestSingleFrameJava verifies that an exception with a single frame is
// aggregated (regression: the old matched-from-terminal semantics required
// two frames).
func TestSingleFrameJava(t *testing.T) {
	got := collect(t, "k", []string{
		"java.lang.NullPointerException: boom",
		"\tat com.example.Foo.bar(Foo.java:12)",
		"plain",
	})
	assert.Equal(t, []Entry[int]{
		{Text: "java.lang.NullPointerException: boom\n\tat com.example.Foo.bar(Foo.java:12)", Key: "k", Match: "java", Lines: 2, Data: 0},
		{Text: "plain", Key: "k", Lines: 1, Data: 2},
	}, got)
}

// TestAcceptedPrefix verifies that trailing lines consumed after the last
// accepting line are re-emitted individually rather than glued to the trace
// or demoting it.
func TestAcceptedPrefix(t *testing.T) {
	got := collect(t, "k", []string{
		"java.lang.NullPointerException: boom",
		"\tat com.example.Foo.bar(Foo.java:12)",
		"\tnested exception is:",
		"plain",
	})
	assert.Equal(t, []Entry[int]{
		{Text: "java.lang.NullPointerException: boom\n\tat com.example.Foo.bar(Foo.java:12)\n\tnested exception is:", Key: "k", Match: "java", Lines: 3, Data: 0},
		{Text: "plain", Key: "k", Lines: 1, Data: 3},
	}, got)

	// A group that never reaches an accepting state passes through untouched.
	got = collect(t, "k", []string{
		"java.lang.NullPointerException: boom",
		"plain",
	})
	assert.Equal(t, []Entry[int]{
		{Text: "java.lang.NullPointerException: boom", Key: "k", Lines: 1, Data: 0},
		{Text: "plain", Key: "k", Lines: 1, Data: 1},
	}, got)
}

// TestStopOrder verifies that Stop flushes groups oldest-first, not in map
// order.
func TestStopOrder(t *testing.T) {
	var got []string
	ml := New(func(_ context.Context, e Entry[struct{}]) error {
		got = append(got, e.Text)
		return nil
	})
	ctx := context.Background()
	for _, key := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		assert.NoError(t, ml.Add(ctx, key, "panic: "+key, struct{}{}))
	}
	assert.NoError(t, ml.Stop(ctx))
	assert.Equal(t, []string{
		"panic: a", "panic: b", "panic: c", "panic: d",
		"panic: e", "panic: f", "panic: g", "panic: h",
	}, got)
}

// TestFlushKey verifies per-key flushing.
func TestFlushKey(t *testing.T) {
	var got []string
	ml := New(func(_ context.Context, e Entry[struct{}]) error {
		got = append(got, e.Text)
		return nil
	})
	ctx := context.Background()
	assert.NoError(t, ml.Add(ctx, "a", "panic: a", struct{}{}))
	assert.NoError(t, ml.Add(ctx, "b", "panic: b", struct{}{}))
	assert.NoError(t, ml.Flush(ctx, "b"))
	assert.Equal(t, []string{"panic: b"}, got)
	assert.NoError(t, ml.Flush(ctx, "missing"))
	assert.NoError(t, ml.Stop(ctx))
	assert.Equal(t, []string{"panic: b", "panic: a"}, got)
}

// TestMaxGroups verifies that exceeding the group cap flushes the least
// recently touched group.
func TestMaxGroups(t *testing.T) {
	var got []string
	ml := New(func(_ context.Context, e Entry[struct{}]) error {
		got = append(got, e.Text)
		return nil
	}, WithMaxGroups(2))
	ctx := context.Background()
	assert.NoError(t, ml.Add(ctx, "a", "panic: a", struct{}{}))
	assert.NoError(t, ml.Add(ctx, "b", "panic: b", struct{}{}))
	assert.Empty(t, got)
	assert.NoError(t, ml.Add(ctx, "c", "panic: c", struct{}{}))
	assert.Equal(t, []string{"panic: a"}, got)
	assert.NoError(t, ml.Stop(ctx))
	assert.Equal(t, []string{"panic: a", "panic: b", "panic: c"}, got)
}

// TestFlushBefore verifies time-based flushing with caller-supplied times.
func TestFlushBefore(t *testing.T) {
	var got []string
	ml := New(func(_ context.Context, e Entry[struct{}]) error {
		got = append(got, e.Text)
		return nil
	})
	ctx := context.Background()
	t0 := time.Unix(1000, 0)
	assert.NoError(t, ml.AddAt(ctx, "a", "panic: a", t0, struct{}{}))
	assert.NoError(t, ml.AddAt(ctx, "b", "panic: b", t0.Add(10*time.Second), struct{}{}))
	assert.NoError(t, ml.FlushBefore(ctx, t0.Add(5*time.Second)))
	assert.Equal(t, []string{"panic: a"}, got)
	assert.NoError(t, ml.FlushBefore(ctx, t0.Add(15*time.Second)))
	assert.Equal(t, []string{"panic: a", "panic: b"}, got)
}
