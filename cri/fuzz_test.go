package cri

import (
	"context"
	"maps"
	"strings"
	"testing"
	"time"
)

// FuzzRejoinConservation asserts the byte- and order-conservation property of
// the CRI stage: rejoining changes how content is packaged into lines, never
// what it is. For every key the next stage sees, concatenating what it
// received must reproduce, exactly and in order, the concatenation of the
// content the input carried for that key — whether that content arrived as a
// closed fragment run, a dangling run flushed by Stop, or a non-CRI line
// passed through under the bare key.
func FuzzRejoinConservation(f *testing.F) {
	f.Add("2024-01-01T10:00:00.000000001Z stdout P one \n" +
		"2024-01-01T10:00:00.000000002Z stdout F two")
	f.Add("2024-01-01T10:00:00.000000001Z stdout P dangling \nplain line")
	f.Add("2024-01-01T10:00:00.000000001Z stdout P a\n" +
		"2024-01-01T10:00:00.000000002Z stderr P b\n" +
		"2024-01-01T10:00:00.000000003Z stdout F c\n" +
		"2024-01-01T10:00:00.000000004Z stderr F d")
	f.Add("2024-01-01T10:00:00.000000001Z stdout F \nnot cri at all\n")
	f.Add("")

	f.Fuzz(func(t *testing.T, input string) {
		lines := strings.Split(input, "\n")

		// What each downstream key must end up holding, derived straight from
		// Parse rather than from the stage under test.
		want := map[string]string{}
		for _, raw := range lines {
			if l, ok := Parse(raw); ok {
				want["c1/"+l.Stream] += l.Content
			} else {
				want["c1"] += raw
			}
		}

		got := map[string]string{}
		a := New(func(_ context.Context, key, line string, _ time.Time, _ int) error {
			got[key] += line
			return nil
		})
		ctx := context.Background()
		for i, raw := range lines {
			if err := a.Add(ctx, "c1", raw, i); err != nil {
				t.Fatal(err)
			}
		}
		if err := a.Stop(ctx); err != nil {
			t.Fatal(err)
		}

		if !maps.Equal(want, got) {
			t.Fatalf("content per key differs:\n got: %q\nwant: %q", got, want)
		}
		if a.Len() != 0 || a.Bytes() != 0 {
			t.Fatalf("fragments still buffered after Stop: Len=%d Bytes=%d", a.Len(), a.Bytes())
		}
	})
}
