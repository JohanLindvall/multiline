package cri

import (
	"context"
	"testing"
	"time"
)

// BenchmarkAddFull measures the hot path of a CRI log stream: ordinary full
// ("F") lines that pass straight through to the next stage.
func BenchmarkAddFull(b *testing.B) {
	a := New(func(_ context.Context, _, _ string, _ time.Time, _ struct{}) error { return nil })
	ctx := context.Background()
	line := "2024-01-01T10:00:00.000000001Z stdout F GET /healthz 200 in 1.2ms"
	b.ReportAllocs()
	for range b.N {
		if err := a.Add(ctx, "container-1", line, struct{}{}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkAddParsedFull is BenchmarkAddFull for a caller that already parsed
// the line (e.g. to derive the key): the timestamp parse is skipped entirely.
func BenchmarkAddParsedFull(b *testing.B) {
	a := New(func(_ context.Context, _, _ string, _ time.Time, _ struct{}) error { return nil })
	ctx := context.Background()
	line := "2024-01-01T10:00:00.000000001Z stdout F GET /healthz 200 in 1.2ms"
	l, ok := Parse(line)
	if !ok {
		b.Fatal("parse failed")
	}
	b.ReportAllocs()
	for range b.N {
		if err := a.AddParsed(ctx, "container-1", line, l, ok, struct{}{}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkAddFragments measures rejoining a partial-line run.
func BenchmarkAddFragments(b *testing.B) {
	a := New(func(_ context.Context, _, _ string, _ time.Time, _ struct{}) error { return nil })
	ctx := context.Background()
	run := []string{
		"2024-01-01T10:00:00.000000001Z stdout P first fragment of a long line ",
		"2024-01-01T10:00:00.000000002Z stdout P second fragment ",
		"2024-01-01T10:00:00.000000003Z stdout F last fragment",
		"2024-01-01T10:00:00.000000004Z stdout F next line",
	}
	b.ReportAllocs()
	for range b.N {
		for _, line := range run {
			if err := a.Add(ctx, "container-1", line, struct{}{}); err != nil {
				b.Fatal(err)
			}
		}
	}
}
