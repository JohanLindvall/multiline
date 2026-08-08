// Package multiline aggregates log output spanning several physical lines —
// such as panic and exception stack traces — back into a single logical
// entry. Lines are fed one at a time to an [Aggregator], grouped per key, and
// completed entries are handed to an [Emitter] callback.
//
// The bundled matcher recognizes Go, Java (and Node.js), Python, .NET, Ruby,
// Rust, PHP and Elixir stack traces; custom formats are declared in the
// patterns subpackage and selected with [WithMatcher].
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
	// of lines in Text. Across the entries of one group, Lines always sums to
	// the number of lines the group consumed: lines the caps dropped are
	// attributed to the last entry the group emits.
	Lines int
	// Truncated is set when lines belonging to this entry were dropped or cut
	// by WithMaxLines/WithMaxBytes.
	Truncated bool
}

// Emitter receives completed entries. Returning an error is reported by the
// Add or flush call that produced the entry, and the first such error wins.
// It does not abort that call: an Add still accounts for its own line, which
// is buffered or emitted even when a flush it triggered failed. Lines already
// buffered in the same group are not re-delivered.
//
// An emitter may feed the same Aggregator again — that is how stages are
// chained — but one that re-enters under the key it is currently handling
// must make progress rather than add a line unconditionally: the call that
// triggered the flush drains whatever the emitter left under that key before
// claiming it, and draining runs the emitter once more. An emitter that
// answers every entry with another line for the same key therefore never
// settles. Feeding a different key, or a different Aggregator, is unaffected.
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
	// fine). accepted need not be a member of next: an implementation that
	// bounds the size of the active set may report the state the line landed
	// in without tracking it (see patterns.MaxActiveStates).
	Step(line string, active []int) (next []int, accepted int)
	// Format returns the format name reported as [Entry].Match for a group
	// that completed in the state at index.
	Format(index int) string
}

// FinalMatcher is an optional extension of [Matcher]. A matcher that can tell
// the aggregator which states are dead ends — states no line can leave, so no
// further line can extend a group sitting in one — lets such a group be
// emitted the moment it is complete, instead of waiting for the key's next
// line or a flush. [patterns.StateMachine] implements it (a state with no
// outgoing transitions), as does the cri matcher, where the "F" line closing a
// fragment run is definitive.
type FinalMatcher interface {
	Matcher
	// Final reports whether a group whose active set is exactly {index} can
	// never consume another line.
	Final(index int) bool
}

// defaultMatcher recognizes the stack-trace formats bundled in the patterns
// subpackage.
var defaultMatcher Matcher = patterns.MustCompile(patterns.All...)

// lineAux rides alongside each retained line of a group.
type lineAux[T any] struct {
	when time.Time
	data T
}

// group buffers the pending lines of one key. Flushed groups go on the
// aggregator's free list, where next chains them, so a busy stream reuses the
// line buffers instead of allocating a group per trace.
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
	final   FinalMatcher // nil unless matcher implements it
	now     func() time.Time

	groups      map[string]*group[T]
	first, last *group[T] // groups in last-touched order
	bytes       int       // text bytes retained across all groups

	free    *group[T] // recycled groups, chained through next
	freeLen int

	maxLines      int
	maxBytes      int
	maxGroups     int
	maxTotalBytes int
	noText        bool

	// Borrowed backing for single-line Entry.Texts, one slot per level of
	// emitter re-entrancy: an emitter that feeds this same aggregator must not
	// see the Texts of the entry it is still handling change under it.
	scratch []string
	depth   int

	// start is the active set a new group is matched from, held per aggregator
	// rather than in a package-level slice: a third-party Matcher that ignores
	// the "must not modify active" rule then corrupts only its own aggregator.
	start [1]int
}

// Free-list bounds: enough groups to cover the churn of a busy stream without
// pinning much, and a line-buffer ceiling so one huge trace does not leave a
// large array parked on the list.
//
// maxFreeGroups is not a concurrency limit: the streaming path releases a
// key's group and allocates the next one for that same key in the very next
// statement, so reuse is allocation-free at any number of concurrently open
// traces (measured at 1 through 256). It only bounds a bulk release —
// FlushBefore, Stop, or a Flush-per-key loop — where more than maxFreeGroups
// groups are freed back-to-back with no interleaved allocation.
const (
	maxFreeGroups = 4
	maxFreeLines  = 64
)

// Option configures an [Aggregator] at construction time.
type Option func(*config)

type config struct {
	matcher       Matcher
	now           func() time.Time
	maxLines      int
	maxBytes      int
	maxGroups     int
	maxTotalBytes int
	noText        bool
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

// WithMaxTotalBytes caps the text bytes retained across all groups (the gauge
// [Aggregator.Bytes] reports). When a line pushes the total over the cap, the
// least recently touched groups are flushed until it fits again. A value <= 0
// means unlimited. Unlike WithMaxGroups x WithMaxBytes, it bounds retained
// text whatever the key cardinality. The most recently touched group is never
// evicted, so pair this with [WithMaxBytes] to bound a single group too.
//
// It bounds retained text only: the per-group overhead (the group itself, its
// key, its map entry and slice backing — on the order of a few hundred bytes)
// is not charged against the cap, so a workload of very short lines spread
// over very many keys can hold considerably more than the configured budget.
// Pair it with [WithMaxGroups] to bound that too.
func WithMaxTotalBytes(n int) Option {
	return func(c *config) { c.maxTotalBytes = n }
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
	a := &Aggregator[T]{
		emit:          emit,
		matcher:       c.matcher,
		now:           c.now,
		groups:        make(map[string]*group[T]),
		maxLines:      c.maxLines,
		maxBytes:      c.maxBytes,
		maxGroups:     c.maxGroups,
		maxTotalBytes: c.maxTotalBytes,
		noText:        c.noText,
		scratch:       make([]string, 1),
	}
	if fm, ok := c.matcher.(FinalMatcher); ok {
		a.final = fm
	}
	return a
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
//
// The line is always accounted for, even when an emitter error is on its way
// out: an error raised by a flush this line triggered is held back until the
// line itself has been buffered or emitted, so no input is lost to it.
func (a *Aggregator[T]) AddAt(ctx context.Context, key, line string, when time.Time, data T) error {
	if key == "" {
		return a.emitLine(ctx, Entry[T]{When: when, Lines: 1, Data: data}, line)
	}

	var held error
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
			if a.allFinal(next) {
				// No line can extend the group: emit it now rather than
				// holding it until the key's next line or a flush.
				a.unlink(g)
				return a.flush(ctx, g)
			}
			if a.maxTotalBytes > 0 {
				return a.evict(ctx, nil)
			}
			return nil
		}
		// The line does not continue the group: flush it, then let the line
		// start a new group or pass through below.
		a.unlink(g)
		held = a.flush(ctx, g)
	}

	next, _ := a.matcher.Step(line, a.start[:])
	if len(next) == 0 {
		return firstErr(held, a.emitLine(ctx, Entry[T]{Key: key, When: when, Lines: 1, Data: data}, line))
	}

	// The flush above ran the emitter, which may have re-entered this
	// aggregator under this same key and left a group behind. Drain it before
	// claiming the map entry, or the two groups would share a key: one
	// reachable through the map, one orphaned in the last-touched list. Each
	// drain re-enters the emitter in turn, so re-read until the key is free.
	for old := a.groups[key]; old != nil; old = a.groups[key] {
		a.unlink(old)
		held = firstErr(held, a.flush(ctx, old))
	}

	// The accepted result is deliberately ignored here: an aggregated entry
	// must span at least two source lines.
	g := a.newGroup(key, a.stamp(when), next)
	a.append(g, line, when, data)
	a.groups[key] = g
	a.link(g)
	if a.allFinal(next) {
		// A first line landing in a dead end can never grow into an
		// aggregate; flush emits it as the single line it is.
		a.unlink(g)
		return firstErr(held, a.flush(ctx, g))
	}

	return a.evict(ctx, held)
}

// allFinal reports whether no further line can extend a group whose active set
// is next, which makes the group complete the moment it is buffered.
func (a *Aggregator[T]) allFinal(next []int) bool {
	if a.final == nil {
		return false
	}
	for _, state := range next {
		if !a.final.Final(state) {
			return false
		}
	}
	return true
}

// evict flushes least recently touched groups until the group-count and
// total-byte caps are met, keeping the most recently touched group so the
// current line always has somewhere to accumulate. held, the error of an
// earlier flush in the same call, wins over any error raised here.
func (a *Aggregator[T]) evict(ctx context.Context, held error) error {
	for a.first != a.last &&
		(a.maxGroups > 0 && len(a.groups) > a.maxGroups ||
			a.maxTotalBytes > 0 && a.bytes > a.maxTotalBytes) {
		g := a.first
		a.unlink(g)
		held = firstErr(held, a.flush(ctx, g))
	}
	return held
}

// firstErr keeps the earlier of two emitter errors.
func firstErr(held, err error) error {
	if held != nil {
		return held
	}
	return err
}

// emitLine hands a single-line entry to the emitter, lending a scratch slot as
// the Texts backing. The slot is chosen by re-entrancy depth: an emitter that
// calls back into this aggregator gets its own, leaving the Texts view of the
// entry still in flight intact.
func (a *Aggregator[T]) emitLine(ctx context.Context, e Entry[T], line string) error {
	depth := a.depth
	if depth == len(a.scratch) {
		a.scratch = append(a.scratch, "")
	}
	a.scratch[depth] = line
	e.Texts = a.scratch[depth : depth+1 : depth+1]
	if !a.noText {
		e.Text = line
	}
	a.depth = depth + 1
	err := a.emit(ctx, e)
	a.depth = depth
	return err
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

// Keys appends the keys with buffered lines to dst, least recently touched
// first — the order [Aggregator.Stop] and the [WithMaxGroups] eviction use.
// Pass a reused slice (dst[:0]) to keep the call allocation-free. It is for
// inspection only: flushing while iterating the result is fine, since the keys
// are copies.
func (a *Aggregator[T]) Keys(dst []string) []string {
	for g := a.first; g != nil; g = g.next {
		dst = append(dst, g.key)
	}
	return dst
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
		// Clone rather than reslice: a Go substring shares its backing array,
		// so retaining line[:avail] would pin the whole original line while
		// only avail bytes are charged to g.bytes/a.bytes — the cap would
		// report a bound it does not hold, and evict would never see the
		// difference. This is the cold path, taken at most once per group.
		line = strings.Clone(line[:avail])
	}

	g.lines = append(g.lines, line)
	g.aux = append(g.aux, lineAux[T]{when: when, data: data})
	g.bytes += sep + len(line)
	a.bytes += sep + len(line)
}

// flush emits g's longest accepted prefix as one aggregated entry and any
// retained lines after it individually. A group that never completed has all
// its lines emitted individually. The group is recycled afterwards, so the
// Texts views lent to the emitter must not outlive their emit call.
func (a *Aggregator[T]) flush(ctx context.Context, g *group[T]) error {
	tail := g.acceptedLines
	// Lines the caps consumed but never retained, and that the aggregated
	// entry's Lines does not already cover. They are charged to the last entry
	// this flush emits, so Lines sums back to the lines the group consumed.
	dropped := g.total - g.acceptedTotal - (len(g.lines) - tail)

	if tail > 0 {
		e := Entry[T]{
			Texts: g.lines[:tail:tail],
			Key:   g.key,
			Match: g.match,
			When:  g.aux[0].when,
			Data:  g.aux[0].data,
			Lines: g.acceptedTotal,
			// append retains nothing once g.capped is set, so the cut line is
			// always the last retained one and every dropped line follows it.
			// The aggregated prefix therefore only lost text when it runs to
			// the end of the retained lines; otherwise the flag belongs to the
			// last individually emitted line below, which is also the entry
			// charged with the dropped count.
			Truncated: g.capped && tail == len(g.lines),
		}
		if tail == len(g.lines) {
			e.Lines += dropped
		}
		if !a.noText {
			e.Text = strings.Join(g.lines[:tail], "\n")
		}
		if err := a.emit(ctx, e); err != nil {
			return err
		}
	}

	for i := tail; i < len(g.lines); i++ {
		e := Entry[T]{Texts: g.lines[i : i+1 : i+1], Key: g.key, When: g.aux[i].when, Lines: 1, Data: g.aux[i].data}
		if !a.noText {
			e.Text = g.lines[i]
		}
		if i == len(g.lines)-1 {
			e.Lines += dropped
			e.Truncated = g.capped
		}
		if err := a.emit(ctx, e); err != nil {
			return err
		}
	}

	a.release(g)
	return nil
}

// newGroup returns a group for key, reusing a recycled one (and its line
// buffers) when the free list is not empty.
func (a *Aggregator[T]) newGroup(key string, when time.Time, active []int) *group[T] {
	g := a.free
	if g == nil {
		return &group[T]{key: key, when: when, active: active}
	}
	a.free, a.freeLen = g.next, a.freeLen-1
	*g = group[T]{key: key, when: when, active: active, lines: g.lines, aux: g.aux}
	return g
}

// release parks a flushed group on the free list so the next group can reuse
// its line buffers. The buffers are cleared to their full capacity first: a
// recycled group must not keep the strings (or the T values) of the entry it
// just emitted alive.
func (a *Aggregator[T]) release(g *group[T]) {
	if a.freeLen >= maxFreeGroups {
		return
	}
	lines, aux := g.lines[:cap(g.lines)], g.aux[:cap(g.aux)]
	if cap(lines) > maxFreeLines {
		lines, aux = nil, nil
	}
	clear(lines)
	clear(aux)
	*g = group[T]{lines: lines[:0], aux: aux[:0], next: a.free}
	a.free, a.freeLen = g, a.freeLen+1
}

// detach removes g from the last-touched list only.
func (a *Aggregator[T]) detach(g *group[T]) {
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
}

// unlink removes g from the last-touched list and the key map. The map entry
// is dropped by identity: under emitter re-entrancy another group may already
// have claimed the key, and deleting by name alone would strand it.
func (a *Aggregator[T]) unlink(g *group[T]) {
	a.detach(g)
	a.bytes -= g.bytes
	if a.groups[g.key] == g {
		delete(a.groups, g.key)
	}
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
	a.detach(g)
	a.link(g)
}
