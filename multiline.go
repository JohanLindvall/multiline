package multiline

import (
	"context"
	"strings"
	"time"
)

type stateHolder[T any] struct {
	prev, next     *stateHolder[T]
	key            string
	when           time.Time
	states         []T
	lines          []string
	nextPos        []int
	terminate      bool
	last, nextLast string
}

// Multiline aggregates log entries that span several lines into a single line.
// Lines are grouped by key (see Add) and matched against the configured states;
// once a complete match terminates, the joined lines are passed to the emitter.
// Multiline is not safe for concurrent use.
type Multiline[T any] struct {
	first, last *stateHolder[T]
	emitter     func(ctx context.Context, line, match string, data T) error
	states      map[string]*stateHolder[T]
}

// New creates a new multiline aggregator. The emit callback is invoked for every
// completed line: line is the aggregated text (multiple source lines joined by
// "\n"), match is the name of the terminating state ("" when the line was emitted
// as-is), and data is the value associated with the first source line of the group.
func New[T any](emit func(ctx context.Context, line, match string, data T) error) *Multiline[T] {
	return &Multiline[T]{emitter: emit, states: make(map[string]*stateHolder[T])}
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

// Add feeds a single line into the aggregator. The key groups related lines and
// is typically the container id; an empty key bypasses aggregation and emits the
// line immediately. data is carried alongside the line and handed back through the
// emitter. Lines that extend a pending group are buffered; a line that neither
// extends nor starts a group is emitted as-is. Add returns the first error
// produced by the emitter, if any.
func (m *Multiline[T]) Add(ctx context.Context, line, key string, data T) error {
	if key == "" {
		// No key. Emit line as is
		return m.emitter(ctx, line, "", data)
	}
	state := m.states[key]
	var next []int
	var terminate bool
	if state != nil {
		if terminate, next = getNextStates(line, state.nextPos); len(next) == 0 {
			m.unlink(state, true)
			if err := m.emit(ctx, state); err != nil {
				return err
			}
		} else {
			state.states = append(state.states, data)
			state.lines = append(state.lines, line)

			state.terminate = terminate
			state.last = state.nextLast

			state.nextLast = names[next[0]]
			state.nextPos = next

			m.moveLast(state)
		}
	}

	if len(next) == 0 {
		if terminate, next = getNextStates(line, []int{0}); len(next) == 0 {
			// No match for next. Emit line as is
			return m.emitter(ctx, line, "", data)
		} else {
			// Set terminate to false. Can't terminate on first elem
			nextLast := names[next[0]]
			state := &stateHolder[T]{nextPos: next, lines: []string{line}, states: []T{data}, key: key, terminate: terminate, nextLast: nextLast, last: names[0]}
			m.link(state, true)
		}
	}

	return nil
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

func (m *Multiline[T]) emit(ctx context.Context, state *stateHolder[T]) error {
	// Cannot end on non-terminal
	if state.terminate {
		return m.emitter(ctx, strings.Join(state.lines, "\n"), state.last, state.states[0])
	} else {
		for i, line := range state.lines {
			if err := m.emitter(ctx, line, "", state.states[i]); err != nil {
				return err
			}
		}
	}

	return nil
}
