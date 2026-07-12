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
// A caller that has already parsed the line (for example to derive the key)
// can pass the parse result to [Aggregator.AddParsed] instead; the timestamp
// is then parsed exactly once per line on the whole path.
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
	stream, partial, content, ok := splitMeta(raw[sp+1:])
	if !ok {
		return l, false
	}

	l.Time = t
	l.Stream = stream
	l.Partial = partial
	l.Content = content
	return l, true
}

// splitMeta splits "<stream> <tag> <content>" — a raw CRI line after its
// timestamp token.
func splitMeta(rest string) (stream string, partial bool, content string, ok bool) {
	sp := strings.IndexByte(rest, ' ')
	if sp <= 0 {
		return "", false, "", false
	}
	stream = rest[:sp]
	if stream != "stdout" && stream != "stderr" {
		return "", false, "", false
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
		partial = true
	case "F":
	default:
		return "", false, "", false
	}

	return stream, partial, rest, true
}

// meta is [splitMeta] with the timestamp token skipped unvalidated: the
// internal paths only ever see lines whose timestamp was parsed on entry, so
// re-parsing it (the expensive part of Parse) would be wasted work.
func meta(raw string) (stream string, partial bool, content string, ok bool) {
	sp := strings.IndexByte(raw, ' ')
	if sp <= 0 {
		return "", false, "", false
	}
	return splitMeta(raw[sp+1:])
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

// matcher implements multiline.Matcher for CRI fragment runs by splitting
// each raw line instead of pattern matching.
type matcher struct{}

func (matcher) Step(line string, active []int) (next []int, accepted int) {
	if active[0] == stateFull {
		return nil, -1 // the "F" line completed the entry
	}
	_, partial, _, ok := meta(line)
	if !ok {
		return nil, -1
	}
	if partial {
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

// lineData rides through the internal aggregator alongside each buffered
// line, so rejoin recovers the first fragment's timestamp without re-parsing.
type lineData[T any] struct {
	when time.Time
	data T
}

// Aggregator rejoins CRI partial lines. Like multiline.Aggregator it is not
// safe for concurrent use.
type Aggregator[T any] struct {
	inner *multiline.Aggregator[lineData[T]]
	next  Next[T]

	// Cached "<key>/<stream>" strings for the previous key, so the steady
	// state of tailing one container allocates nothing per line.
	lastKey    string
	lastStdout string
	lastStderr string
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
	return a.AddParsed(ctx, key, raw, l, ok, data)
}

// AddParsed is [Aggregator.Add] for callers that already parsed raw — for
// example to derive the key or to filter by stream. It skips the internal
// [Parse], so the line's timestamp is parsed exactly once on the whole path
// (the buffering, grouping and rejoining stages use a cheap structural split
// that never re-parses it). line and ok must be the [Parse] results of raw;
// ok false feeds raw through unmodified as a non-CRI line, stamped with the
// current time.
func (a *Aggregator[T]) AddParsed(ctx context.Context, key, raw string, line Line, ok bool, data T) error {
	if !ok {
		return a.next(ctx, key, raw, time.Now(), data)
	}
	if key != "" {
		key = a.streamKey(key, line.Stream)
	}
	return a.inner.AddAt(ctx, key, raw, line.Time, lineData[T]{when: line.Time, data: data})
}

// streamKey returns "<key>/<stream>", cached for the previous key so repeated
// lines from one source do not allocate.
func (a *Aggregator[T]) streamKey(key, stream string) string {
	if key != a.lastKey {
		a.lastKey = key
		a.lastStdout = key + "/stdout"
		a.lastStderr = key + "/stderr"
	}
	switch stream {
	case "stdout":
		return a.lastStdout
	case "stderr":
		return a.lastStderr
	default: // only reachable via AddParsed with a non-CRI stream
		return key + "/" + stream
	}
}

// rejoin receives buffered raw lines from the inner aggregator — a completed
// fragment run, or a single line — and forwards the stripped, concatenated
// content, stamped with the first line's timestamp.
func (a *Aggregator[T]) rejoin(ctx context.Context, e multiline.Entry[lineData[T]]) error {
	if !strings.Contains(e.Text, "\n") {
		if _, _, content, ok := meta(e.Text); ok {
			return a.next(ctx, e.Key, content, e.Data.when, e.Data.data)
		}
		// Cannot happen for lines admitted through Add/AddParsed; keep the
		// text rather than dropping it.
		return a.next(ctx, e.Key, e.Text, e.Data.when, e.Data.data)
	}

	var text strings.Builder
	for _, fragment := range strings.Split(e.Text, "\n") {
		if _, _, content, ok := meta(fragment); ok {
			text.WriteString(content)
		} else {
			text.WriteString(fragment)
		}
	}
	return a.next(ctx, e.Key, text.String(), e.Data.when, e.Data.data)
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
