package multiline

import (
	"context"
	"fmt"
	"regexp"
)

// Direction controls how a single-pattern matcher (see [NewPattern]) relates a
// continuation line to its neighbors, mirroring Beats' multiline "match" setting.
type Direction int

const (
	// After groups continuation lines after the preceding line: a line matching the
	// pattern is appended to the current group, and a non-matching line begins a new
	// group. Use this for stack traces whose continuation lines are indented.
	After Direction = iota
	// Before groups continuation lines before the following line: lines are buffered
	// until a non-matching line, which terminates the group. Use this when a line
	// signals that the next line continues it (for example a trailing "\").
	Before
)

// String returns the lower-case name of the direction. It is reported to the
// emitter as the match argument for aggregated lines.
func (d Direction) String() string {
	switch d {
	case Before:
		return "before"
	case After:
		return "after"
	default:
		return "unknown"
	}
}

// NewPattern creates a multiline aggregator that groups lines using a single
// regular expression, like Beats' pattern-based multiline. A line is a continuation
// when it matches pattern, unless negate is true, which inverts the test. match
// selects how continuations relate to their neighbors (see [Direction]). An
// aggregated group is reported to the emitter with match set to the direction name
// ("before"/"after"); a lone line is emitted as-is with an empty match. See [New]
// for the emit callback and option semantics. NewPattern returns an error if
// pattern is not a valid regular expression.
func NewPattern[T any](pattern string, negate bool, match Direction, emit func(ctx context.Context, line, match string, data T) error, opts ...Option) (*Multiline[T], error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("multiline: invalid pattern %q: %w", pattern, err)
	}
	m, _ := newMultiline(emit, opts...)
	m.agg = &patternAggregator[T]{re: re, negate: negate, dir: match}
	return m, nil
}

type patternAggregator[T any] struct {
	re     *regexp.Regexp
	negate bool
	dir    Direction
}

// continues reports whether line matches the continuation pattern.
func (a *patternAggregator[T]) continues(line string) bool {
	return a.re.MatchString(line) != a.negate
}

// mark flags a group as an aggregated multiline once it holds more than one line so
// that emit joins it and tags it with the direction name.
func (a *patternAggregator[T]) mark(state *stateHolder[T]) {
	if len(state.lines) > 1 {
		state.terminate = true
		state.last = a.dir.String()
	}
}

func (a *patternAggregator[T]) add(m *Multiline[T], ctx context.Context, line, key string, data T) error {
	state := m.states[key]
	cont := a.continues(line)

	if a.dir == Before {
		// Buffer every line; a non-continuation line is the last line of the group.
		if state == nil {
			state = m.newGroup(key)
			m.appendLine(state, line, data)
			m.link(state, true)
		} else {
			m.appendLine(state, line, data)
			m.moveLast(state)
		}
		a.mark(state)
		if !cont {
			m.unlink(state, true)
			return m.emit(ctx, state)
		}
		return nil
	}

	// After: the first line always starts a group, matching lines extend it, and a
	// non-matching line flushes the group and starts a new one.
	if state == nil {
		state = m.newGroup(key)
		m.appendLine(state, line, data)
		m.link(state, true)
		return nil
	}
	if cont {
		m.appendLine(state, line, data)
		a.mark(state)
		m.moveLast(state)
		return nil
	}

	m.unlink(state, true)
	if err := m.emit(ctx, state); err != nil {
		return err
	}
	state = m.newGroup(key)
	m.appendLine(state, line, data)
	m.link(state, true)
	return nil
}
