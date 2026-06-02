package multiline

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"
)

type stateHolder[T any] struct {
	prev, next     *stateHolder[T]
	key            string
	when           time.Time
	states         []T
	lines          []string
	bytes          int
	capped         bool
	nextPos        []int
	terminate      bool
	last, nextLast string
}

// aggregator decides how successive lines for a given key are grouped. The
// built-in implementations are the state-machine matcher (nfaAggregator) and the
// single-pattern before/after matcher (patternAggregator). Implementations build
// groups with the buffer helpers on Multiline (newGroup, appendLine, link, unlink,
// moveLast, emit).
type aggregator[T any] interface {
	add(m *Multiline[T], ctx context.Context, line, key string, data T) error
}

// Multiline aggregates log entries that span several lines into a single line.
// Lines are grouped by key (see Add); once a group completes, the joined lines are
// passed to the emitter. Grouping is driven either by a Matcher (the default) or by
// a single before/after pattern (see NewPattern). Multiline is not safe for
// concurrent use.
type Multiline[T any] struct {
	first, last *stateHolder[T]
	emitter     func(ctx context.Context, line, match string, data T) error
	states      map[string]*stateHolder[T]
	agg         aggregator[T]

	// maxLines / maxBytes bound the size of a single group; 0 means unlimited.
	maxLines int
	maxBytes int
}

// Option configures a Multiline at construction time.
type Option func(*options)

type options struct {
	maxLines int
	maxBytes int
	matcher  Matcher
}

// WithMatcher selects a custom [Matcher] (typically a [StateMachine] built via
// [Compile]) instead of the built-in one. It has no effect on a pattern-based
// aggregator (see [NewPattern]).
func WithMatcher(matcher Matcher) Option {
	return func(o *options) { o.matcher = matcher }
}

// WithMaxLines caps the number of lines retained in a single aggregated group.
// Once the cap is reached further lines are dropped (the group's boundary is still
// detected normally, so matching continues). A value <= 0 means unlimited. This
// guards against an unterminated match growing without bound.
func WithMaxLines(n int) Option {
	return func(o *options) { o.maxLines = n }
}

// WithMaxBytes caps the total bytes retained in a single aggregated group. The line
// that crosses the limit is truncated (on a UTF-8 rune boundary) and subsequent
// lines are dropped. A value <= 0 means unlimited.
func WithMaxBytes(n int) Option {
	return func(o *options) { o.maxBytes = n }
}

func newMultiline[T any](emit func(ctx context.Context, line, match string, data T) error, opts ...Option) (*Multiline[T], options) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	m := &Multiline[T]{
		emitter:  emit,
		states:   make(map[string]*stateHolder[T]),
		maxLines: o.maxLines,
		maxBytes: o.maxBytes,
	}
	return m, o
}

// New creates a new multiline aggregator. By default it uses the built-in matcher,
// which recognizes Go, .NET, Python and Java stack traces; pass [WithMatcher] to
// supply a custom [Matcher]. The emit callback is invoked for every completed line:
// line is the aggregated text (multiple source lines joined by "\n"), match is the
// name of the terminating state ("" when the line was emitted as-is), and data is
// the value associated with the first source line of the group.
func New[T any](emit func(ctx context.Context, line, match string, data T) error, opts ...Option) *Multiline[T] {
	m, o := newMultiline(emit, opts...)
	matcher := o.matcher
	if matcher == nil {
		matcher = defaultMatcher
	}
	m.agg = &nfaAggregator[T]{matcher: matcher}
	return m
}

func (m *Multiline[T]) unlink(state *stateHolder[T], inMap bool) {
	if m.first == state {
		m.first = state.next
	}
	if m.last == state {
		m.last = state.prev
	}
	if state.prev != nil {
		state.prev.next = state.next
	}
	if state.next != nil {
		state.next.prev = state.prev
	}
	state.prev = nil
	state.next = nil

	if inMap {
		delete(m.states, state.key)
	}
}

func (m *Multiline[T]) link(state *stateHolder[T], inMap bool) {
	state.prev = m.last
	state.next = nil
	if m.first == nil {
		m.first = state
	}
	if m.last != nil {
		m.last.next = state
	}
	m.last = state
	if inMap {
		m.states[state.key] = state
	}
	state.when = time.Now()
}

func (m *Multiline[T]) moveLast(state *stateHolder[T]) {
	if state != m.last {
		m.unlink(state, false)
		m.link(state, false)
	}
}

// newGroup returns an empty group for key. Use appendLine to add its lines.
func (m *Multiline[T]) newGroup(key string) *stateHolder[T] {
	return &stateHolder[T]{key: key}
}

// appendLine stores line (and its data) in state, honoring the maxLines/maxBytes
// caps. Once a cap is hit the line is dropped or truncated and state.capped is set,
// so callers may keep advancing their matching state without growing the buffer.
func (m *Multiline[T]) appendLine(state *stateHolder[T], line string, data T) {
	if state.capped {
		return
	}
	if m.maxLines > 0 && len(state.lines) >= m.maxLines {
		state.capped = true
		return
	}

	sep := 0
	if len(state.lines) > 0 {
		sep = 1 // lines are joined by a single "\n" on emit
	}

	if m.maxBytes > 0 && state.bytes+sep+len(line) > m.maxBytes {
		avail := m.maxBytes - state.bytes - sep
		// Back off to a rune boundary so truncation never yields invalid UTF-8.
		for avail > 0 && !utf8.RuneStart(line[avail]) {
			avail--
		}
		state.capped = true
		if avail <= 0 {
			return
		}
		line = line[:avail]
	}

	state.lines = append(state.lines, line)
	state.states = append(state.states, data)
	state.bytes += sep + len(line)
}

// Add feeds a single line into the aggregator. The key groups related lines and is
// typically the container id; an empty key bypasses aggregation and emits the line
// immediately. data is carried alongside the line and handed back through the
// emitter. Add returns the first error produced by the emitter, if any.
func (m *Multiline[T]) Add(ctx context.Context, line, key string, data T) error {
	if key == "" {
		// No key. Emit line as is
		return m.emitter(ctx, line, "", data)
	}
	return m.agg.add(m, ctx, line, key, data)
}

// Stop flushes every pending group through the emitter and resets the aggregator
// to its empty state, so it may be reused afterwards. It returns the first error
// produced while emitting, if any.
func (m *Multiline[T]) Stop(ctx context.Context) error {
	for _, state := range m.states {
		if err := m.emit(ctx, state); err != nil {
			return err
		}
	}

	clear(m.states)
	m.first = nil
	m.last = nil
	return nil
}

// FlushBefore emits every pending group whose most recent line arrived before t,
// freeing groups that have gone stale. Groups are tracked in arrival order, so
// flushing stops at the first group last touched at or after t. It returns the
// first error produced while emitting, if any.
func (m *Multiline[T]) FlushBefore(ctx context.Context, t time.Time) error {
	for state := m.first; state != nil && state.when.Before(t); state = m.first {
		m.unlink(state, true)
		if err := m.emit(ctx, state); err != nil {
			return err
		}
	}

	return nil
}

// emit hands state to the emitter. A group flagged terminate is emitted as one
// joined line tagged with state.last; otherwise each buffered line is emitted
// individually as an as-is line.
func (m *Multiline[T]) emit(ctx context.Context, state *stateHolder[T]) error {
	if state.terminate {
		return m.emitter(ctx, strings.Join(state.lines, "\n"), state.last, state.states[0])
	}
	for i, line := range state.lines {
		if err := m.emitter(ctx, line, "", state.states[i]); err != nil {
			return err
		}
	}

	return nil
}

// nfaAggregator groups lines using a state-machine [Matcher]. A group starts when a
// line matches a transition out of the start state and continues while subsequent
// lines advance the machine; it is emitted as a single line when it ends on a
// terminal state, otherwise its lines are emitted individually.
type nfaAggregator[T any] struct {
	matcher Matcher
}

func (a *nfaAggregator[T]) add(m *Multiline[T], ctx context.Context, line, key string, data T) error {
	state := m.states[key]
	var next []int
	var terminate bool
	if state != nil {
		if terminate, next = a.matcher.NextStates(line, state.nextPos); len(next) == 0 {
			m.unlink(state, true)
			if err := m.emit(ctx, state); err != nil {
				return err
			}
		} else {
			m.appendLine(state, line, data)

			state.terminate = terminate
			state.last = state.nextLast

			state.nextLast = a.matcher.Name(next[0])
			state.nextPos = next

			m.moveLast(state)
		}
	}

	if len(next) == 0 {
		if terminate, next = a.matcher.NextStates(line, []int{0}); len(next) == 0 {
			// No match for next. Emit line as is
			return m.emitter(ctx, line, "", data)
		}
		// Set terminate to false. Can't terminate on first elem
		state := m.newGroup(key)
		m.appendLine(state, line, data)
		state.terminate = terminate
		state.nextLast = a.matcher.Name(next[0])
		state.last = a.matcher.Name(0)
		state.nextPos = next
		m.link(state, true)
	}

	return nil
}
