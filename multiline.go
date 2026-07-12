// Package multiline aggregates log output spanning several physical lines —
// such as panic and exception stack traces — back into a single logical
// entry. Lines are fed one at a time to an [Aggregator], grouped per key, and
// completed entries are handed to an [Emitter] callback.
//
// The bundled matcher recognizes Go, Java (and Node.js), Python, .NET, Ruby,
// Rust and PHP stack traces; custom formats are declared in the patterns
// subpackage and selected with [WithMatcher].
package multiline

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/JohanLindvall/multiline/patterns"
)

// Entry is one completed log entry handed to the [Emitter].
type Entry[T any] struct {
	// Text is the entry text; for an aggregated entry the source lines are
	// joined by "\n". It is left empty when [WithoutText] is configured.
	Text string
	// Texts is the entry's retained source lines, one element per line. It is
	// a view borrowed from internal buffers, valid only until the emitter
	// returns — copy it (e.g. slices.Clone) to retain. Writing the elements
	// to an io.Writer avoids Text's joined allocation entirely (see
	// [WithoutText]).
	Texts []string
	// Key is the key the entry's lines were added under. It allows chaining
	// aggregation stages: an emitter can feed another Aggregator keyed by
	// entry.Key (see examples/cri).
	Key string
	// Match names the format that aggregated this entry (a patterns.StateSet
	// name such as "go" or "java"). It is "" when the line passed through
	// as-is.
	Match string
	// When is the time of the entry's first source line, as passed to AddAt.
	// Lines fed without a time (Add, or AddAt with a zero when) carry a zero
	// When.
	When time.Time
	// Data is the value passed to Add for the entry's first source line.
	Data T
	// Lines is the number of source lines the entry represents. It counts
	// lines dropped by WithMaxLines/WithMaxBytes, so it can exceed the number
	// of lines in Text.
	Lines int
	// Truncated is set when lines belonging to this entry were dropped or cut
	// by WithMaxLines/WithMaxBytes.
	Truncated bool
}

// Emitter receives completed entries. Returning an error aborts the Add or
// flush call that produced the entry; lines already buffered in the same
// group are not re-delivered.
type Emitter[T any] func(ctx context.Context, entry Entry[T]) error

// Matcher decides how successive lines are grouped. Implementations track
// matcher state as opaque int indices, where index 0 is the start state a new
// group begins from. The built-in implementation is [patterns.StateMachine];
// implementations must be immutable or otherwise safe for the
// (single-threaded) use an Aggregator makes of them.
type Matcher interface {
	// Step applies line to the active states and returns the new active set,
	// plus the index of an accepting state the line landed in (-1 if none).
	// An empty next means line does not continue any active state. Step must
	// not retain or modify the active slice; the aggregator retains the
	// returned slice until the group's next line, so implementations must
	// return slices they will never mutate (shared immutable slices are
	// fine).
	Step(line string, active []int) (next []int, accepted int)
	// Format returns the format name reported as [Entry].Match for a group
	// that completed in the state at index.
	Format(index int) string
}

// defaultMatcher recognizes the stack-trace formats bundled in the patterns
// subpackage.
var defaultMatcher Matcher = patterns.MustCompile(patterns.All...)

// startStates is the active set a new group is matched from.
var startStates = []int{0}

// lineAux rides alongside each retained line of a group.
type lineAux[T any] struct {
	when time.Time
	data T
}

// group buffers the pending lines of one key.
type group[T any] struct {
	prev, next *group[T]
	key        string
	when       time.Time

	lines  []string
	aux    []lineAux[T]
	bytes  int
	total  int // lines consumed, including ones dropped by the caps
	capped bool

	active []int // matcher states after the last consumed line

	// Longest accepted prefix: the group completed most recently at retained
	// line index acceptedLines (consumed line acceptedTotal) in format match.
	// acceptedLines == 0 means the group never completed.
	match         string
	acceptedLines int
	acceptedTotal int
}

// Aggregator joins log entries that span several lines into a single entry.
// Lines are grouped per key (see [Aggregator.Add]); when a group completes,
// the joined lines are passed to the emitter. Grouping is driven by a
// [Matcher]. An Aggregator is not safe for concurrent use.
type Aggregator[T any] struct {
	emit    Emitter[T]
	matcher Matcher
	now     func() time.Time

	groups      map[string]*group[T]
	first, last *group[T] // groups in last-touched order
	bytes       int       // text bytes retained across all groups

	maxLines  int
	maxBytes  int
	maxGroups int
	noText    bool

	scratch [1]string // borrowed backing for single-line Entry.Texts
}

// Option configures an [Aggregator] at construction time.
type Option func(*config)

type config struct {
	matcher   Matcher
	now       func() time.Time
	maxLines  int
	maxBytes  int
	maxGroups int
	noText    bool
}

// WithMatcher selects a custom [Matcher] (typically a [patterns.StateMachine]
// built via [patterns.Compile]) instead of the built-in one.
func WithMatcher(matcher Matcher) Option {
	return func(c *config) { c.matcher = matcher }
}

// WithMaxLines caps the number of lines retained in a single group. Further
// lines are dropped while matching continues normally, and the resulting
// entry is flagged Truncated. A value <= 0 means unlimited. This guards
// against an unterminated match growing without bound.
func WithMaxLines(n int) Option {
	return func(c *config) { c.maxLines = n }
}

// WithMaxBytes caps the total text bytes retained in a single group. The line
// that crosses the limit is cut on a UTF-8 rune boundary, subsequent lines
// are dropped, and the resulting entry is flagged Truncated. A value <= 0
// means unlimited.
func WithMaxBytes(n int) Option {
	return func(c *config) { c.maxBytes = n }
}

// WithMaxGroups caps the number of keys with pending lines. Adding a line for
// a new key beyond the cap flushes the least recently touched group first. A
// value <= 0 means unlimited. This guards against unbounded key cardinality
// (note that Go maps keep their high-water bucket memory, so the cap also
// bounds that); time-based flushing is [Aggregator.FlushBefore].
func WithMaxGroups(n int) Option {
	return func(c *config) { c.maxGroups = n }
}

// WithoutText skips building [Entry].Text (it is left empty), for emitters
// that consume [Entry].Texts instead. This avoids joining an aggregated
// entry's lines into one string — for a large capped trace, a copy the size
// of the whole entry.
func WithoutText() Option {
	return func(c *config) { c.noText = true }
}

// WithClock replaces time.Now as the source of the arrival times that
// [Aggregator.Add] stamps groups with (used by FlushBefore). Prefer
// [Aggregator.AddAt] to supply per-line times, e.g. log timestamps.
func WithClock(now func() time.Time) Option {
	return func(c *config) { c.now = now }
}

// New creates an aggregator that hands completed entries to emit. By default
// it recognizes the stack-trace formats in [patterns.All]; pass [WithMatcher]
// to change that.
func New[T any](emit Emitter[T], opts ...Option) *Aggregator[T] {
	c := config{matcher: defaultMatcher, now: time.Now}
	for _, opt := range opts {
		opt(&c)
	}
	return &Aggregator[T]{
		emit:      emit,
		matcher:   c.matcher,
		now:       c.now,
		groups:    make(map[string]*group[T]),
		maxLines:  c.maxLines,
		maxBytes:  c.maxBytes,
		maxGroups: c.maxGroups,
		noText:    c.noText,
	}
}

// Add feeds a single line into the aggregator. The key groups related lines
// and is typically a container or stream id; an empty key bypasses
// aggregation and emits the line immediately. data rides along with the line
// and is handed back through the emitter. Add returns the first error
// produced by the emitter, if any. Entries fed via Add carry a zero
// [Entry].When; staleness for [Aggregator.FlushBefore] is tracked with the
// aggregator clock only when a line is actually buffered, keeping the
// pass-through path free of clock reads.
func (a *Aggregator[T]) Add(ctx context.Context, key, line string, data T) error {
	return a.AddAt(ctx, key, line, time.Time{}, data)
}

// AddAt is [Aggregator.Add] with an explicit time for the line, which
// [Aggregator.FlushBefore] compares against and [Entry].When reports — pass
// the log's own timestamp to make time-based flushing robust when replaying
// old logs. Times are assumed to be non-decreasing across calls. A zero when
// is allowed (Add uses one): staleness falls back to the aggregator clock and
// Entry.When stays zero.
func (a *Aggregator[T]) AddAt(ctx context.Context, key, line string, when time.Time, data T) error {
	if key == "" {
		return a.emitLine(ctx, Entry[T]{When: when, Lines: 1, Data: data}, line)
	}

	if g := a.groups[key]; g != nil {
		if next, accepted := a.matcher.Step(line, g.active); len(next) > 0 {
			a.append(g, line, when, data)
			g.active = next
			if accepted >= 0 {
				g.match = a.matcher.Format(accepted)
				g.acceptedLines = len(g.lines)
				g.acceptedTotal = g.total
			}
			g.when = a.stamp(when)
			a.moveLast(g)
			return nil
		}
		// The line does not continue the group: flush it, then let the line
		// start a new group or pass through below.
		a.unlink(g)
		if err := a.flush(ctx, g); err != nil {
			return err
		}
	}

	next, _ := a.matcher.Step(line, startStates)
	if len(next) == 0 {
		return a.emitLine(ctx, Entry[T]{Key: key, When: when, Lines: 1, Data: data}, line)
	}

	// The accepted result is deliberately ignored here: an aggregated entry
	// must span at least two source lines.
	g := &group[T]{key: key, when: a.stamp(when), active: next}
	a.append(g, line, when, data)
	a.groups[key] = g
	a.link(g)

	for a.maxGroups > 0 && len(a.groups) > a.maxGroups {
		oldest := a.first
		a.unlink(oldest)
		if err := a.flush(ctx, oldest); err != nil {
			return err
		}
	}

	return nil
}

// emitLine hands a single-line entry to the emitter, lending the aggregator's
// scratch slot as the Texts backing.
func (a *Aggregator[T]) emitLine(ctx context.Context, e Entry[T], line string) error {
	a.scratch[0] = line
	e.Texts = a.scratch[:]
	if !a.noText {
		e.Text = line
	}
	return a.emit(ctx, e)
}

// stamp resolves the staleness timestamp for a buffered line: the caller's
// time, or the aggregator clock when none was supplied.
func (a *Aggregator[T]) stamp(when time.Time) time.Time {
	if when.IsZero() {
		return a.now()
	}
	return when
}

// Pending reports whether key has buffered lines.
func (a *Aggregator[T]) Pending(key string) bool {
	_, ok := a.groups[key]
	return ok
}

// Len returns the number of keys with buffered lines.
func (a *Aggregator[T]) Len() int {
	return len(a.groups)
}

// Bytes returns the total text bytes currently buffered across all groups —
// a cheap gauge for memory monitoring.
func (a *Aggregator[T]) Bytes() int {
	return a.bytes
}

// Flush emits the pending group for key, if any. Use it when a stream ends,
// for example when its container terminates.
func (a *Aggregator[T]) Flush(ctx context.Context, key string) error {
	g := a.groups[key]
	if g == nil {
		return nil
	}
	a.unlink(g)
	return a.flush(ctx, g)
}

// FlushBefore emits every pending group last touched before t, freeing groups
// that have gone stale. Call it periodically, e.g. from a ticker:
//
//	ml.FlushBefore(ctx, time.Now().Add(-5*time.Second))
//
// Groups are kept in last-touched order, so flushing stops at the first group
// touched at or after t. It returns the first error produced while emitting.
func (a *Aggregator[T]) FlushBefore(ctx context.Context, t time.Time) error {
	for g := a.first; g != nil && g.when.Before(t); g = a.first {
		a.unlink(g)
		if err := a.flush(ctx, g); err != nil {
			return err
		}
	}

	return nil
}

// Stop flushes every pending group, oldest first, leaving the aggregator
// empty and reusable. It returns the first error produced while emitting;
// groups emitted before the error are not re-delivered by a retry.
func (a *Aggregator[T]) Stop(ctx context.Context) error {
	for g := a.first; g != nil; g = a.first {
		a.unlink(g)
		if err := a.flush(ctx, g); err != nil {
			return err
		}
	}

	return nil
}

// append stores line (and its data) in g, honoring the maxLines/maxBytes
// caps. Once a cap is hit the line is cut or dropped and g.capped is set;
// matching still advances, so the group's boundary is detected normally. The
// first line of a group is always retained (possibly cut to ""), so a group
// is never empty.
func (a *Aggregator[T]) append(g *group[T], line string, when time.Time, data T) {
	g.total++
	if g.capped {
		return
	}
	if a.maxLines > 0 && len(g.lines) >= a.maxLines {
		g.capped = true
		return
	}

	sep := 0
	if len(g.lines) > 0 {
		sep = 1 // lines are joined by a single "\n" on emit
	}
	if a.maxBytes > 0 && g.bytes+sep+len(line) > a.maxBytes {
		avail := a.maxBytes - g.bytes - sep
		// Back off to a rune boundary so a cut never yields invalid UTF-8.
		for avail > 0 && !utf8.RuneStart(line[avail]) {
			avail--
		}
		g.capped = true
		if avail <= 0 {
			if len(g.lines) > 0 {
				return
			}
			avail = 0
		}
		line = line[:avail]
	}

	g.lines = append(g.lines, line)
	g.aux = append(g.aux, lineAux[T]{when: when, data: data})
	g.bytes += sep + len(line)
	a.bytes += sep + len(line)
}

// flush emits g's longest accepted prefix as one aggregated entry and any
// retained lines after it individually. A group that never completed has all
// its lines emitted individually.
func (a *Aggregator[T]) flush(ctx context.Context, g *group[T]) error {
	tail := 0
	if k := g.acceptedLines; k > 0 {
		tail = k
		e := Entry[T]{
			Texts:     g.lines[:k],
			Key:       g.key,
			Match:     g.match,
			When:      g.aux[0].when,
			Data:      g.aux[0].data,
			Lines:     g.acceptedTotal,
			Truncated: g.capped,
		}
		if !a.noText {
			e.Text = strings.Join(g.lines[:k], "\n")
		}
		if err := a.emit(ctx, e); err != nil {
			return err
		}
	}

	for i := tail; i < len(g.lines); i++ {
		e := Entry[T]{Texts: g.lines[i : i+1], Key: g.key, When: g.aux[i].when, Lines: 1, Data: g.aux[i].data}
		if !a.noText {
			e.Text = g.lines[i]
		}
		if tail == 0 && g.capped && i == len(g.lines)-1 {
			e.Truncated = true
		}
		if err := a.emit(ctx, e); err != nil {
			return err
		}
	}

	return nil
}

// unlink removes g from the last-touched list and the key map.
func (a *Aggregator[T]) unlink(g *group[T]) {
	if a.first == g {
		a.first = g.next
	}
	if a.last == g {
		a.last = g.prev
	}
	if g.prev != nil {
		g.prev.next = g.next
	}
	if g.next != nil {
		g.next.prev = g.prev
	}
	g.prev = nil
	g.next = nil
	a.bytes -= g.bytes
	delete(a.groups, g.key)
}

// link appends g to the tail of the last-touched list.
func (a *Aggregator[T]) link(g *group[T]) {
	g.prev = a.last
	g.next = nil
	if a.first == nil {
		a.first = g
	}
	if a.last != nil {
		a.last.next = g
	}
	a.last = g
}

// moveLast moves g to the tail of the last-touched list.
func (a *Aggregator[T]) moveLast(g *group[T]) {
	if g == a.last {
		return
	}
	if a.first == g {
		a.first = g.next
	}
	if g.prev != nil {
		g.prev.next = g.next
	}
	if g.next != nil {
		g.next.prev = g.prev
	}
	a.link(g)
}
