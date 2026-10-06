// SPDX-License-Identifier: MIT

package cri

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/JohanLindvall/multiline"
	"github.com/stretchr/testify/assert"
)

// capture runs a two-fragment run through a capped stage and returns the
// lines the next stage received.
func capture(t *testing.T, opts ...multiline.Option) []string {
	t.Helper()
	var got []string
	a := New(func(_ context.Context, _, line string, _ time.Time, _ int) error {
		got = append(got, line)
		return nil
	}, opts...)
	ctx := context.Background()
	assert.NoError(t, a.Add(ctx, "c1", "2024-01-01T10:00:00.000000001Z stdout P AAAA", 0))
	assert.NoError(t, a.Add(ctx, "c1", "2024-01-01T10:00:00.000000002Z stdout F BBBB", 1))
	assert.NoError(t, a.Stop(ctx))
	return got
}

// TestCapsNeverLeakMetadata is the property the caps must hold on this stage:
// they bound how much of a fragment run survives, and the survivor is always a
// prefix of the run's true content. They must never turn the run into
// something it never said.
//
// The failure mode this guards is specific to CRI. WithMaxBytes cuts the
// *raw* line at an arbitrary offset, prefix included, so a cut landing inside
// the ~40-byte "<ts> stdout P " header leaves bytes that no longer parse. Those
// bytes are metadata, not application text, and shipping them verbatim would
// put a partial timestamp and a stream name into the log record with nothing
// downstream able to tell.
func TestCapsNeverLeakMetadata(t *testing.T) {
	const full = "AAAABBBB" // what an uncapped run rejoins to

	assert.Equal(t, []string{full}, capture(t), "uncapped baseline")

	// 44 is the raw fragment length, so this sweep cuts at every interesting
	// offset: inside the timestamp, inside the stream name, at the tag, and
	// inside the content of either fragment.
	for maxBytes := 1; maxBytes <= 96; maxBytes++ {
		got := capture(t, multiline.WithMaxBytes(maxBytes))
		joined := strings.Join(got, "")
		assert.True(t, strings.HasPrefix(full, joined),
			"WithMaxBytes(%d) produced %q, which is not a prefix of %q", maxBytes, joined, full)
		for _, line := range got {
			assert.NotContains(t, line, "2024-01-01",
				"WithMaxBytes(%d) leaked a CRI timestamp into %q", maxBytes, line)
			assert.NotContains(t, line, "stdout",
				"WithMaxBytes(%d) leaked the stream name into %q", maxBytes, line)
		}
	}

	// WithMaxLines drops whole raw lines, so it can only ever shorten the
	// content — never corrupt it.
	for maxLines := 1; maxLines <= 3; maxLines++ {
		got := capture(t, multiline.WithMaxLines(maxLines))
		joined := strings.Join(got, "")
		assert.True(t, strings.HasPrefix(full, joined),
			"WithMaxLines(%d) produced %q, which is not a prefix of %q", maxLines, joined, full)
	}
}

// TestAddParsedDoesNotReparse pins the parse-once discipline: no internal path
// may re-derive a timestamp from the raw line. Feeding AddParsed a Line whose
// Time deliberately disagrees with the raw text proves it — anything that
// re-parsed would surface the raw value instead of the supplied one, both on
// the buffered fragment-run path and on the full-line fast path.
func TestAddParsedDoesNotReparse(t *testing.T) {
	var got []time.Time
	a := New(func(_ context.Context, _, _ string, when time.Time, _ int) error {
		got = append(got, when)
		return nil
	})
	ctx := context.Background()

	raw1 := "2024-01-01T10:00:00.000000001Z stdout P one "
	raw2 := "2024-01-01T10:00:00.000000002Z stdout F two"
	raw3 := "2024-01-01T10:00:00.000000003Z stdout F alone"

	l1, ok := Parse(raw1)
	assert.True(t, ok)
	l2, ok := Parse(raw2)
	assert.True(t, ok)
	l3, ok := Parse(raw3)
	assert.True(t, ok)

	// Times no re-parse of the raw text could ever produce.
	first := time.Unix(0, 0).UTC()
	l1.Time, l2.Time, l3.Time = first, first.Add(time.Hour), first.Add(2*time.Hour)

	assert.NoError(t, a.AddParsed(ctx, "c1", raw1, l1, true, 0))
	assert.NoError(t, a.AddParsed(ctx, "c1", raw2, l2, true, 1))
	assert.NoError(t, a.AddParsed(ctx, "c1", raw3, l3, true, 2))

	assert.Equal(t, []time.Time{first, l3.Time}, got,
		"the rejoined run must carry its first fragment's supplied time, and the fast-path line its own")
}
