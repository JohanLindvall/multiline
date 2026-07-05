// Package cri rejoins Kubernetes CRI log lines back into whole application
// lines, as a stage in front of stack-trace aggregation.
//
// Container runtimes such as containerd and CRI-O write logs in the CRI
// format ("<timestamp> <stream> P|F <content>") and split long application
// lines into "P" (partial) fragments closed by an "F" (full) line. An
// [Aggregator] parses raw CRI lines, buffers fragment runs per key and
// stream, and hands every rejoined line — CRI prefixes stripped, fragments
// concatenated without a separator — to the [Next] stage, typically the AddAt
// method of a multiline.Aggregator:
//
//	traces := multiline.New(emitEntries)
//	logs := cri.New(traces.AddAt)
//	err := logs.Add(ctx, containerID, rawLine, data)
//
// Docker's json-file driver is a different format and needs JSON unwrapping
// instead.
package cri

import (
	"context"
	"strings"
	"time"

	"github.com/JohanLindvall/multiline"
)

// Line is a parsed CRI log line ("<timestamp> <stream> <tag> <content>").
type Line struct {
	Time    time.Time
	Stream  string // "stdout" or "stderr"
	Partial bool   // true for a "P" fragment, false for a full "F" line
	Content string
}

// Parse splits a raw CRI log line into its parts. It reports false when raw
// is not CRI-formatted.
func Parse(raw string) (Line, bool) {
	var l Line

	sp := strings.IndexByte(raw, ' ')
	if sp <= 0 {
		return l, false
	}
	t, err := time.Parse(time.RFC3339Nano, raw[:sp])
	if err != nil {
		return l, false
	}
	rest := raw[sp+1:]

	sp = strings.IndexByte(rest, ' ')
	if sp <= 0 {
		return l, false
	}
	stream := rest[:sp]
	if stream != "stdout" && stream != "stderr" {
		return l, false
	}
	rest = rest[sp+1:]

	tag := rest
	if sp = strings.IndexByte(rest, ' '); sp >= 0 {
		tag, rest = rest[:sp], rest[sp+1:]
	} else {
		rest = ""
	}
	// The tag may carry future ":"-delimited sub-tags; the first one is the
	// partial/full flag.
	if i := strings.IndexByte(tag, ':'); i >= 0 {
		tag = tag[:i]
	}
	switch tag {
	case "P":
		l.Partial = true
	case "F":
	default:
		return l, false
	}

	l.Time = t
	l.Stream = stream
	l.Content = rest
	return l, true
}

// Matcher state indices: a group opens on a "P" fragment and completes on the
// "F" line that closes the run.
const (
	stateStart = iota
	statePartial
	stateFull
)

var (
	partialNext = []int{statePartial}
	fullNext    = []int{stateFull}
)

// matcher implements multiline.Matcher for CRI fragment runs by parsing each
// raw line instead of pattern matching.
type matcher struct{}

func (matcher) Step(line string, active []int) (next []int, accepted int) {
	if active[0] == stateFull {
		return nil, -1 // the "F" line completed the entry
	}
	l, ok := Parse(line)
	if !ok {
		return nil, -1
	}
	if l.Partial {
		return partialNext, -1
	}
	if active[0] == statePartial {
		return fullNext, stateFull
	}
	return nil, -1 // a full line with no pending fragments never groups
}

func (matcher) Format(int) string { return "cri" }

// Next receives each rejoined application line: the key it was added under
// (suffixed "/stdout" or "/stderr"), the line with CRI prefixes stripped, and
// the timestamp of its first fragment. The AddAt method of a
// multiline.Aggregator satisfies Next directly.
type Next[T any] func(ctx context.Context, key, line string, when time.Time, data T) error

// Aggregator rejoins CRI partial lines. Like multiline.Aggregator it is not
// safe for concurrent use.
type Aggregator[T any] struct {
	inner *multiline.Aggregator[T]
	next  Next[T]
}

// New creates a CRI rejoining stage in front of next. The multiline options
// apply to the fragment buffering: WithMaxLines/WithMaxBytes bound a fragment
// run (measured on the raw lines; an over-limit run is passed on silently
// truncated), WithMaxGroups bounds the tracked streams. WithMatcher is
// ignored.
func New[T any](next Next[T], opts ...multiline.Option) *Aggregator[T] {
	a := &Aggregator[T]{next: next}
	a.inner = multiline.New(a.rejoin, append(opts, multiline.WithMatcher(matcher{}))...)
	return a
}

// Add feeds one raw CRI log line. The key identifies the source (typically
// the container); fragment runs are buffered per key and stream, and rejoined
// lines are handed to the [Next] stage keyed "<key>/<stream>". A line that is
// not CRI-formatted is passed through unmodified, stamped with the current
// time.
func (a *Aggregator[T]) Add(ctx context.Context, key, raw string, data T) error {
	l, ok := Parse(raw)
	if !ok {
		return a.next(ctx, key, raw, time.Now(), data)
	}
	if key != "" {
		key += "/" + l.Stream
	}
	return a.inner.AddAt(ctx, key, raw, l.Time, data)
}

// rejoin receives buffered raw lines from the inner aggregator — a completed
// fragment run, or a single line — and forwards the stripped, concatenated
// content.
func (a *Aggregator[T]) rejoin(ctx context.Context, e multiline.Entry[T]) error {
	if !strings.Contains(e.Text, "\n") {
		l, ok := Parse(e.Text)
		if !ok {
			return a.next(ctx, e.Key, e.Text, time.Now(), e.Data)
		}
		return a.next(ctx, e.Key, l.Content, l.Time, e.Data)
	}

	var text strings.Builder
	var when time.Time
	for i, fragment := range strings.Split(e.Text, "\n") {
		l, ok := Parse(fragment)
		if !ok {
			// Cannot happen for lines admitted by the matcher; keep the
			// fragment rather than dropping it.
			text.WriteString(fragment)
			continue
		}
		if i == 0 {
			when = l.Time
		}
		text.WriteString(l.Content)
	}
	return a.next(ctx, e.Key, text.String(), when, e.Data)
}

// Flush hands any pending fragments of key's streams to the next stage. Call
// it when the source ends, e.g. when the container terminates; note that a
// run flushed without its closing "F" line is passed on line by line.
func (a *Aggregator[T]) Flush(ctx context.Context, key string) error {
	for _, k := range []string{key + "/stdout", key + "/stderr"} {
		if err := a.inner.Flush(ctx, k); err != nil {
			return err
		}
	}
	return nil
}

// FlushBefore hands pending fragment runs whose last fragment carries a
// timestamp before t to the next stage, freeing runs whose closing "F" line
// never arrived.
func (a *Aggregator[T]) FlushBefore(ctx context.Context, t time.Time) error {
	return a.inner.FlushBefore(ctx, t)
}

// Stop flushes all pending fragments, leaving the aggregator empty and
// reusable. Stop any downstream stage afterwards.
func (a *Aggregator[T]) Stop(ctx context.Context) error {
	return a.inner.Stop(ctx)
}
