package multiline

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"
)

// FuzzConservation asserts that with no caps configured, aggregation neither
// loses, duplicates, nor reorders input: concatenating the emitted entries of
// a key reconstructs the input exactly, every source line is accounted for,
// and each entry carries the data of its first source line.
func FuzzConservation(f *testing.F) {
	f.Add("panic: boom\n\ngoroutine 1 [running]:\nmain.main()\n\t/app/main.go:1 +0x1d\ndone")
	f.Add("java.lang.NullPointerException: x\n\tat a.b(C.java:1)\nplain")
	f.Add("Traceback (most recent call last):\n  File \"a.py\", line 1, in <module>\nValueError: x\n")
	f.Add("no\ntraces\nhere")
	f.Add("")

	f.Fuzz(func(t *testing.T, input string) {
		lines := strings.Split(input, "\n")

		var out []string
		consumed := 0
		ml := New(func(_ context.Context, e Entry[int]) error {
			if e.Data != consumed {
				t.Fatalf("entry starting at line %d carries data %d", consumed, e.Data)
			}
			if joined := strings.Join(e.Texts, "\n"); joined != e.Text {
				t.Fatalf("Texts %q does not mirror Text %q", joined, e.Text)
			}
			if e.Truncated {
				t.Fatalf("truncated entry without caps: %q", e.Text)
			}
			out = append(out, e.Text)
			consumed += e.Lines
			return nil
		})
		ctx := context.Background()
		for i, line := range lines {
			if err := ml.Add(ctx, "key", line, i); err != nil {
				t.Fatal(err)
			}
		}
		if err := ml.Stop(ctx); err != nil {
			t.Fatal(err)
		}

		if consumed != len(lines) {
			t.Fatalf("emitted %d of %d lines", consumed, len(lines))
		}
		if got := strings.Join(out, "\n"); got != input {
			t.Fatalf("reconstructed input differs:\n got: %q\nwant: %q", got, input)
		}
	})
}

// FuzzCappedConservation asserts the accounting invariant the caps must keep:
// however much text WithMaxLines/WithMaxBytes drop, the emitted Lines still add
// up to every input line, any loss is flagged Truncated, and an entry that kept
// all of its text is byte-identical to its input.
func FuzzCappedConservation(f *testing.F) {
	f.Add("panic: boom\n\ngoroutine 1 [running]:\nmain.main()\n\t/app/main.go:1 +0x1d\ndone", 3, 12)
	f.Add("java.lang.NullPointerException: x\n\tat a.b(C.java:1)\n\tat a.b(C.java:2)\nplain", 2, 0)
	f.Add("Traceback (most recent call last):\n  File \"a.py\", line 1\nValueError: x", 0, 7)
	f.Add("no\ntraces\nhere", 1, 1)
	f.Add("", 0, 0)

	f.Fuzz(func(t *testing.T, input string, maxLines, maxBytes int) {
		// Keep the caps small but sane; 0 means unlimited.
		maxLines = abs(maxLines) % 8
		maxBytes = abs(maxBytes) % 64
		lines := strings.Split(input, "\n")

		consumed, truncated := 0, false
		ml := New(func(_ context.Context, e Entry[int]) error {
			if joined := strings.Join(e.Texts, "\n"); joined != e.Text {
				t.Fatalf("Texts %q does not mirror Text %q", joined, e.Text)
			}
			if e.Truncated {
				truncated = true
			}
			// Retained lines are always a prefix of the lines the group
			// consumed (once a cap bites, nothing more is retained), and the
			// only edit a cap makes to a line is cutting its tail. So every
			// retained line must be a prefix of the input line it stands for —
			// which also pins down alignment, catching a lost or reordered
			// line.
			//
			// intact tracks whether this entry lost anything at all: it
			// stands for more lines than it retained, or one of its lines was
			// cut. Truncated must say exactly that, on exactly this entry —
			// flagging the neighbour instead would send a consumer looking
			// for the missing text in the wrong record.
			intact := e.Lines == len(e.Texts)
			for i, text := range e.Texts {
				if !strings.HasPrefix(lines[consumed+i], text) {
					t.Fatalf("line %d is %q, not a prefix of input %q",
						consumed+i, text, lines[consumed+i])
				}
				if text != lines[consumed+i] {
					intact = false
				}
			}
			if e.Truncated == intact {
				t.Fatalf("Truncated=%v but entry %q stands for %d lines, retained %d, of input %q",
					e.Truncated, e.Text, e.Lines, len(e.Texts),
					strings.Join(lines[consumed:min(consumed+e.Lines, len(lines))], "\n"))
			}
			consumed += e.Lines
			return nil
		}, WithMaxLines(maxLines), WithMaxBytes(maxBytes))
		ctx := context.Background()
		for i, line := range lines {
			if err := ml.Add(ctx, "key", line, i); err != nil {
				t.Fatal(err)
			}
		}
		if err := ml.Stop(ctx); err != nil {
			t.Fatal(err)
		}

		if consumed != len(lines) {
			t.Fatalf("accounted for %d of %d lines (maxLines=%d maxBytes=%d, truncated=%v)",
				consumed, len(lines), maxLines, maxBytes, truncated)
		}
	})
}

// FuzzMultiKeyConservation covers the paths the other two fuzzers do not
// reach: several interleaved keys, AddAt with caller-supplied times, time-based
// flushing, and both eviction caps. None of those may drop text — they only
// force a group to be emitted sooner — so each key's entries must still
// concatenate back to exactly the lines fed under that key, and the aggregator
// must be empty afterwards.
func FuzzMultiKeyConservation(f *testing.F) {
	f.Add("panic: a\n\ngoroutine 1 [running]:\nmain.main()\n\t/x.go:1 +0x1\ndone", 2, 3, 64)
	f.Add("java.lang.NullPointerException: x\n\tat a.b(C.java:1)\nplain\npanic: z\n\ngoroutine 5 [running]:", 3, 0, 0)
	f.Add("no\ntraces\nhere\nat all", 4, 1, 8)
	f.Add("", 1, 0, 0)
	// Lines go to key i%keys, so a trace only forms if its lines are spaced
	// `keys` apart. These two seeds are what actually reach the properties this
	// fuzzer exists for: a group that aggregates, and a group cap below the
	// live key count so eviction runs.
	f.Add("panic: a\nX\n\nY\ngoroutine 1 [running]:\nZ\nmain.main()\nW\n\t/x.go:1 +0x1\nV", 5, 1, 0)
	f.Add("panic: a\nX\n\nY\ngoroutine 1 [running]:\nZ\nmain.main()\nW\n\t/x.go:1 +0x1\nV", 5, 0, 16)

	f.Fuzz(func(t *testing.T, input string, keys, maxGroups, maxTotalBytes int) {
		keys = abs(keys)%4 + 1
		maxGroups = abs(maxGroups) % 5           // 0 means unlimited
		maxTotalBytes = abs(maxTotalBytes) % 128 // 0 means unlimited
		lines := strings.Split(input, "\n")

		want := make(map[string][]string, keys)
		for i, line := range lines {
			key := fmt.Sprintf("k%d", i%keys)
			want[key] = append(want[key], line)
		}

		got := make(map[string][]string, keys)
		ml := New(func(_ context.Context, e Entry[int]) error {
			if joined := strings.Join(e.Texts, "\n"); joined != e.Text {
				t.Fatalf("Texts %q does not mirror Text %q", joined, e.Text)
			}
			if e.Truncated || e.Lines != len(e.Texts) {
				t.Fatalf("entry %q lost lines to an eviction cap: Lines=%d retained=%d truncated=%v",
					e.Text, e.Lines, len(e.Texts), e.Truncated)
			}
			got[e.Key] = append(got[e.Key], e.Texts...)
			return nil
		}, WithMaxGroups(maxGroups), WithMaxTotalBytes(maxTotalBytes))

		ctx := context.Background()
		base := time.Unix(1000, 0)
		for i, line := range lines {
			at := base.Add(time.Duration(i) * time.Millisecond)
			if err := ml.AddAt(ctx, fmt.Sprintf("k%d", i%keys), line, at, i); err != nil {
				t.Fatal(err)
			}
			// Periodically retire whatever has gone stale, so the time-based
			// path interleaves with the caps rather than only running at the end.
			if i%7 == 6 {
				if err := ml.FlushBefore(ctx, at.Add(-2*time.Millisecond)); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := ml.Stop(ctx); err != nil {
			t.Fatal(err)
		}

		if !maps.EqualFunc(want, got, slices.Equal) {
			t.Fatalf("per-key reconstruction differs:\n got: %q\nwant: %q", got, want)
		}
		if ml.Len() != 0 || ml.Bytes() != 0 || len(ml.Keys(nil)) != 0 {
			t.Fatalf("aggregator not empty after Stop: Len=%d Bytes=%d Keys=%q",
				ml.Len(), ml.Bytes(), ml.Keys(nil))
		}
	})
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
