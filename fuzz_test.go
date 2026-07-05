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
