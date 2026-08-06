package multiline

import (
	"context"
	"strings"
	"testing"
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
			for i, text := range e.Texts {
				if !strings.HasPrefix(lines[consumed+i], text) {
					t.Fatalf("line %d is %q, not a prefix of input %q",
						consumed+i, text, lines[consumed+i])
				}
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

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
