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

// Final implements multiline.FinalMatcher. The "F" line closing a fragment run
// is definitive, so the rejoined line is handed to the [Next] stage as soon as
// it arrives instead of waiting for the stream's next line.
func (matcher) Final(state int) bool { return state == stateFull }

// Next receives each rejoined application line: the key it was added under
// (suffixed "/stdout" or "/stderr"), the line with CRI prefixes stripped, and
// the timestamp of its first fragment. The AddAt method of a
// multiline.Aggregator satisfies Next directly.
//
// A line that is not CRI-formatted is the exception on both counts: it keeps
// the bare key, unsuffixed, and carries a zero time. That is what lets a
// wholly non-CRI source degrade gracefully through this stage, but it does
// mean one source can reach the next stage under two different keys.
type Next[T any] func(ctx context.Context, key, line string, when time.Time, data T) error

// Aggregator rejoins CRI partial lines. Like multiline.Aggregator it is not
// safe for concurrent use.
type Aggregator[T any] struct {
	inner *multiline.Aggregator[T]
	next  Next[T]

	// Cached "<key>/<stream>" strings for the previous key, so the steady
	// state of tailing one container allocates nothing per line. cached
	// distinguishes "no key seen yet" from a genuine empty key.
	cached     bool
	lastKey    string
	lastStdout string
	lastStderr string
}

// New creates a CRI rejoining stage in front of next. The multiline options
// apply to the fragment buffering, measured on the raw lines: WithMaxLines and
// WithMaxBytes bound a single fragment run (an over-limit run is passed on
// silently truncated), WithMaxGroups and WithMaxTotalBytes bound the tracked
// streams. WithClock is accepted but does nothing here: every buffered
// fragment carries its own parsed log timestamp, so FlushBefore always
// compares against those. Two options are not the caller's to choose: WithMatcher is
// overridden with the CRI fragment matcher, and WithoutText is forced, since
// rejoin consumes Entry.Texts and never needs the joined form.
func New[T any](next Next[T], opts ...multiline.Option) *Aggregator[T] {
	a := &Aggregator[T]{next: next}
	// Copy rather than append in place: append on a variadic parameter writes
	// into the caller's backing array whenever it has spare capacity, which
	// would plant this stage's matcher in an option slice the caller still
	// uses elsewhere.
	inner := make([]multiline.Option, 0, len(opts)+2)
	inner = append(inner, opts...)
	inner = append(inner, multiline.WithMatcher(matcher{}), multiline.WithoutText())
	a.inner = multiline.New(a.rejoin, inner...)
	return a
}

// Add feeds one raw CRI log line. The key identifies the source (typically
// the container); fragment runs are buffered per key and stream, and rejoined
// lines are handed to the [Next] stage keyed "<key>/<stream>". A line that is
// not CRI-formatted is passed through unmodified with a zero time.
//
// An empty key means "do not buffer", matching the multiline.Aggregator
// sentinel this stage feeds: fragments are stripped and handed on one by one
// instead of being rejoined, so a logical line split into N fragments arrives
// as N lines. Pass a real key to get rejoining.
func (a *Aggregator[T]) Add(ctx context.Context, key, raw string, data T) error {
	l, ok := Parse(raw)
	return a.AddParsed(ctx, key, raw, l, ok, data)
}

// AddParsed is [Aggregator.Add] for callers that already parsed raw — for
// example to derive the key or to filter by stream. It skips the internal
// [Parse], so the line's timestamp is parsed exactly once on the whole path;
// a full line with no fragments pending skips buffering entirely and goes
// straight to the [Next] stage. line and ok must be the [Parse] results of
// raw; ok false feeds raw through unmodified as a non-CRI line with a zero
// time. As with [Aggregator.Add], an empty key disables rejoining.
func (a *Aggregator[T]) AddParsed(ctx context.Context, key, raw string, line Line, ok bool, data T) error {
	if !ok {
		return a.next(ctx, key, raw, time.Time{}, data)
	}
	if key != "" {
		key = a.streamKey(key, line.Stream)
	}
	if !line.Partial && !a.inner.Pending(key) {
		return a.next(ctx, key, line.Content, line.Time, data)
	}
	return a.inner.AddAt(ctx, key, raw, line.Time, data)
}

// streamKeys returns key's "<key>/stdout" and "<key>/stderr" forms, cached for
// the previous key so repeated lines from one source do not allocate.
func (a *Aggregator[T]) streamKeys(key string) (stdout, stderr string) {
	if !a.cached || key != a.lastKey {
		a.cached = true
		a.lastKey = key
		a.lastStdout = key + "/stdout"
		a.lastStderr = key + "/stderr"
	}
	return a.lastStdout, a.lastStderr
}

// streamKey returns "<key>/<stream>".
func (a *Aggregator[T]) streamKey(key, stream string) string {
	stdout, stderr := a.streamKeys(key)
	switch stream {
	case "stdout":
		return stdout
	case "stderr":
		return stderr
	default: // only reachable via AddParsed with a non-CRI stream
		return key + "/" + stream
	}
}

// rejoin receives buffered raw lines from the inner aggregator — a completed
// fragment run, or a flushed dangling fragment — and forwards the stripped,
// concatenated content, stamped with the first line's timestamp (Entry.When).
// It consumes Entry.Texts, so the fragments are never joined and re-split.
func (a *Aggregator[T]) rejoin(ctx context.Context, e multiline.Entry[T]) error {
	if len(e.Texts) == 1 {
		return a.next(ctx, e.Key, fragmentContent(e, 0), e.When, e.Data)
	}

	// Size the builder on the content, not on the raw bytes: Builder.String
	// hands out the whole backing array, so growing to the raw size would
	// leave every stripped CRI prefix permanently attached to the rejoined
	// line. Recomputing the content is one extra prefix split per fragment
	// (four IndexByte calls and two short compares), nothing against the copy
	// the single allocation saves.
	n := 0
	for i := range e.Texts {
		n += len(fragmentContent(e, i))
	}
	var text strings.Builder
	text.Grow(n)
	for i := range e.Texts {
		text.WriteString(fragmentContent(e, i))
	}
	return a.next(ctx, e.Key, text.String(), e.When, e.Data)
}

// fragmentContent strips the CRI prefix from e's i'th retained fragment.
//
// A fragment that no longer parses was cut inside its own CRI prefix by
// WithMaxBytes — only reachable on the last retained line of a truncated
// entry, since append retains nothing after the cut. Its remaining bytes are
// raw metadata (a partial timestamp, the stream name), not application text,
// so they are dropped rather than shipped as log content. Any other parse
// failure cannot have arrived through Add/AddParsed, so the text is kept
// rather than silently lost.
func fragmentContent[T any](e multiline.Entry[T], i int) string {
	if _, _, content, ok := meta(e.Texts[i]); ok {
		return content
	}
	if e.Truncated && i == len(e.Texts)-1 {
		return ""
	}
	return e.Texts[i]
}

// Flush hands any pending fragments of key's stdout and stderr streams to
// the next stage. Call it when the source ends, e.g. when the container
// terminates; note that a run flushed without its closing "F" line is passed
// on line by line. Runs fed via [Aggregator.AddParsed] with a non-standard
// Line.Stream are not covered — flush those with [Aggregator.FlushBefore] or
// [Aggregator.Stop].
func (a *Aggregator[T]) Flush(ctx context.Context, key string) error {
	stdout, stderr := a.streamKeys(key)
	if err := a.inner.Flush(ctx, stdout); err != nil {
		return err
	}
	return a.inner.Flush(ctx, stderr)
}

// Pending reports whether key has buffered fragments on either stream. Runs fed
// via [Aggregator.AddParsed] with a non-standard Line.Stream are not covered.
func (a *Aggregator[T]) Pending(key string) bool {
	stdout, stderr := a.streamKeys(key)
	return a.inner.Pending(stdout) || a.inner.Pending(stderr)
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

// Len returns the number of streams with buffered fragments.
func (a *Aggregator[T]) Len() int {
	return a.inner.Len()
}

// Bytes returns the total raw bytes currently buffered — a cheap gauge for
// memory monitoring.
func (a *Aggregator[T]) Bytes() int {
	return a.inner.Bytes()
}
