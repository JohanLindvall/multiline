package cri

import (
	"context"
	"testing"
	"time"

	"github.com/JohanLindvall/multiline"
	"github.com/stretchr/testify/assert"
)

func TestParse(t *testing.T) {
	ts := time.Date(2024, 1, 1, 10, 0, 0, 1, time.UTC)

	for _, tc := range []struct {
		raw  string
		want Line
		ok   bool
	}{
		{"2024-01-01T10:00:00.000000001Z stdout F whole line", Line{ts, "stdout", false, "whole line"}, true},
		{"2024-01-01T10:00:00.000000001Z stderr P fragment ", Line{ts, "stderr", true, "fragment "}, true},
		{"2024-01-01T10:00:00.000000001Z stdout F", Line{ts, "stdout", false, ""}, true},
		{"2024-01-01T10:00:00.000000001Z stdout F ", Line{ts, "stdout", false, ""}, true},
		{"2024-01-01T10:00:00.000000001Z stdout P:sub content", Line{ts, "stdout", true, "content"}, true},
		{"2024-01-01T10:00:00.000000001Z stdout X nope", Line{}, false},
		{"2024-01-01T10:00:00.000000001Z stdmix F nope", Line{}, false},
		{"yesterday stdout F nope", Line{}, false},
		{"plain application line", Line{}, false},
		{"", Line{}, false},
	} {
		got, ok := Parse(tc.raw)
		assert.Equal(t, tc.ok, ok, tc.raw)
		if tc.ok {
			assert.Equal(t, tc.want, got, tc.raw)
		}
	}
}

// received is one line as seen by the Next stage.
type received struct {
	key  string
	line string
	when time.Time
	data int
}

func pipeline(t *testing.T, opts ...multiline.Option) (*Aggregator[int], *[]received) {
	t.Helper()
	var got []received
	a := New(func(_ context.Context, key, line string, when time.Time, data int) error {
		got = append(got, received{key, line, when, data})
		return nil
	}, opts...)
	return a, &got
}

func TestRejoin(t *testing.T) {
	a, got := pipeline(t)
	ctx := context.Background()
	for i, raw := range []string{
		"2024-01-01T10:00:00.000000001Z stdout F whole line",
		"2024-01-01T10:00:01.000000001Z stdout P first, ",
		"2024-01-01T10:00:01.000000002Z stderr F other stream in between",
		"2024-01-01T10:00:01.000000003Z stdout P second, ",
		"2024-01-01T10:00:01.000000004Z stdout F last",
		"2024-01-01T10:00:02.000000001Z stdout F next line",
	} {
		assert.NoError(t, a.Add(ctx, "c1", raw, i))
	}
	assert.NoError(t, a.Stop(ctx))

	at := func(s string) time.Time {
		ts, err := time.Parse(time.RFC3339Nano, s)
		assert.NoError(t, err)
		return ts
	}
	assert.Equal(t, []received{
		{"c1/stdout", "whole line", at("2024-01-01T10:00:00.000000001Z"), 0},
		{"c1/stderr", "other stream in between", at("2024-01-01T10:00:01.000000002Z"), 2},
		{"c1/stdout", "first, second, last", at("2024-01-01T10:00:01.000000001Z"), 1},
		{"c1/stdout", "next line", at("2024-01-01T10:00:02.000000001Z"), 5},
	}, *got)
}

// TestDanglingFragments verifies that a run without its closing "F" line is
// passed on fragment by fragment when flushed.
func TestDanglingFragments(t *testing.T) {
	a, got := pipeline(t)
	ctx := context.Background()
	assert.NoError(t, a.Add(ctx, "c1", "2024-01-01T10:00:00.000000001Z stdout P one ", 0))
	assert.NoError(t, a.Add(ctx, "c1", "2024-01-01T10:00:00.000000002Z stdout P two", 1))
	assert.Empty(t, *got)
	assert.NoError(t, a.Flush(ctx, "c1"))
	assert.Equal(t, "one ", (*got)[0].line)
	assert.Equal(t, "two", (*got)[1].line)
	assert.Len(t, *got, 2)
}

// TestNonCRIPassThrough verifies that lines that are not CRI-formatted reach
// the next stage unmodified.
func TestNonCRIPassThrough(t *testing.T) {
	a, got := pipeline(t)
	assert.NoError(t, a.Add(context.Background(), "c1", "plain application line", 7))
	assert.Len(t, *got, 1)
	assert.Equal(t, "c1", (*got)[0].key)
	assert.Equal(t, "plain application line", (*got)[0].line)
	assert.Equal(t, 7, (*got)[0].data)
}

// TestChainsIntoMultiline verifies the documented composition: AddAt of a
// multiline.Aggregator is a valid Next, and a stack trace split into CRI
// fragments comes out as one aggregated entry.
func TestChainsIntoMultiline(t *testing.T) {
	var entries []multiline.Entry[int]
	traces := multiline.New(func(_ context.Context, e multiline.Entry[int]) error {
		entries = append(entries, e)
		return nil
	})
	logs := New(traces.AddAt)

	ctx := context.Background()
	for i, raw := range []string{
		"2024-01-01T10:00:01.000000001Z stdout P panic: runtime error: invalid memo",
		"2024-01-01T10:00:01.000000002Z stdout F ry address or nil pointer dereference",
		"2024-01-01T10:00:01.000000003Z stdout F ",
		"2024-01-01T10:00:01.000000004Z stdout F goroutine 1 [running]:",
		"2024-01-01T10:00:01.000000005Z stdout F main.handler(0x0)",
		"2024-01-01T10:00:01.000000006Z stdout F \t/app/main.go:42 +0x1d",
	} {
		assert.NoError(t, logs.Add(ctx, "c1", raw, i))
	}
	assert.NoError(t, logs.Stop(ctx))
	assert.NoError(t, traces.Stop(ctx))

	assert.Len(t, entries, 1)
	assert.Equal(t, "go", entries[0].Match)
	assert.Equal(t, "c1/stdout", entries[0].Key)
	assert.Equal(t, "panic: runtime error: invalid memory address or nil pointer dereference\n"+
		"\ngoroutine 1 [running]:\nmain.handler(0x0)\n\t/app/main.go:42 +0x1d", entries[0].Text)
	assert.Equal(t, 5, entries[0].Lines)
	assert.Equal(t, 0, entries[0].Data)
}
