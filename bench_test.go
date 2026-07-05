package multiline

import (
	"context"
	"testing"
)

func discard(_ context.Context, _ Entry[struct{}]) error { return nil }

// BenchmarkNoMatch measures the hot path of a busy log stream: ordinary lines
// that match no start pattern and pass straight through.
func BenchmarkNoMatch(b *testing.B) {
	ml := New(discard)
	ctx := context.Background()
	line := `2024-11-19 11:00:00.123 INFO [main] com.example.Application - Starting application`
	b.ReportAllocs()
	for range b.N {
		if err := ml.Add(ctx, "key", line, struct{}{}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkGoPanic measures aggregating a complete Go panic.
func BenchmarkGoPanic(b *testing.B) {
	ml := New(discard)
	ctx := context.Background()
	trace := []string{
		"panic: runtime error: index out of range [1] with length 1",
		"",
		"goroutine 1 [running]:",
		"main.handler(0x0)",
		"\t/app/main.go:42 +0x1d",
		"main.main()",
		"\t/app/main.go:17 +0x2b",
		"done",
	}
	b.ReportAllocs()
	for range b.N {
		for _, line := range trace {
			if err := ml.Add(ctx, "key", line, struct{}{}); err != nil {
				b.Fatal(err)
			}
		}
	}
}
