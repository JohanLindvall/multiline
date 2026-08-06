package patterns

import (
	"cmp"
	"regexp/syntax"
	"slices"
	"strings"
)

// The start-state regexes dominate per-line CPU in steady state (no group in
// progress): every log line is tested against every start pattern, several of
// which are unanchored and scan the whole line ("\bpanic: ", java's
// ".(Exception|Error|...):"). Compile therefore derives a literal prefilter:
// for every start transition, a set of substrings such that any line matching
// that transition must contain at least one of them. Lines containing none of
// the (deduplicated) literals skip the regexes entirely — strings.Contains is
// SIMD-accelerated and orders of magnitude cheaper — and a line that does hit
// a literal runs only the transitions that literal implies, so a log line
// containing "Error:" pays one regex, not all of them.
//
// The literals are computed from the actual patterns, so a pattern change
// cannot silently break the implication. Degradation is per transition, not
// per machine: a start pattern with no provable literal (and any beyond the
// 64th, for which no mask bit is left) simply becomes a permanent candidate,
// so its regex runs on every line while every other pattern keeps filtering.
// Only a machine where nothing at all is provable disables the prefilter
// outright. [StateMachine.UnfilteredStarts] reports the patterns that fell
// back. Correctness is additionally covered by a differential test over the
// corpus (TestPrefilterDifferential).

// prefilter maps probe literals to the start transitions they imply.
// literals[i] hitting a line marks masks[i]'s bits of transitions[0] as
// candidates; a line with no hits can only match the always/wide transitions.
type prefilter struct {
	literals []string
	masks    []uint64
	// parents[i] is the index of a shorter kept literal that literals[i]
	// contains, or -1 for a root. The linear scan skips a child literal when
	// its parent was not found in the line: the line cannot contain the
	// longer probe either. This keeps the per-line cost at the number of
	// containment-distinct probes even when precision (see the fold rule
	// below) retains several probes sharing a stem, e.g. "Error" alongside
	// "Error:" and "Error: ". partition orders the roots first (indices
	// [0,roots)) so the common no-stem line never touches the children at
	// all; every parent is a root (fold order is shortest-first, so a chain
	// of containments bottoms out at a parentless probe, and that shortest
	// stem is always the first containment found).
	parents []int16
	// roots is the partition point, and parentHits the bitmap of roots that
	// are some child's parent: when no such root hit, the child loop is
	// skipped wholesale.
	roots      int
	parentHits uint64
	// always holds the transitions no literal could be proven for: they are
	// candidates for every line. wide records that there are start transitions
	// past bit 63, which no mask can address, so they too always run.
	always     uint64
	wide       bool
	unfiltered []string
	// ac replaces the linear Contains scan when the literal count crosses
	// acMinLiterals (see ahocorasick.go); nil otherwise. It needs no parent
	// logic: the automaton visits every literal in one pass regardless.
	ac *ahoCorasick
}

// scan returns the union of the candidate-transition masks of every probe
// literal contained in line, plus the always-candidate transitions.
func (pf *prefilter) scan(line string) uint64 {
	if pf.ac != nil {
		return pf.ac.scan(line) | pf.always
	}
	mask := pf.always
	var hits uint64 // found-root bitmap, indexed like literals (first 64)
	for i, lit := range pf.literals[:pf.roots] {
		if strings.Contains(line, lit) {
			mask |= pf.masks[i]
			if i < 64 {
				hits |= 1 << uint(i)
			}
		}
	}
	if hits&pf.parentHits != 0 {
		for i := pf.roots; i < len(pf.literals); i++ {
			if hits&(1<<uint(pf.parents[i])) == 0 {
				continue // the contained shorter probe already missed
			}
			if strings.Contains(line, pf.literals[i]) {
				mask |= pf.masks[i]
			}
		}
	}
	return mask
}

// startPrefilter derives the prefilter from every start-state transition of
// the given sets. ok is false only when not one probe literal could be proven,
// leaving nothing to filter with.
func startPrefilter(sets []StateSet) (*prefilter, bool) {
	type probe struct {
		lit  string
		mask uint64
	}
	var probes []probe
	index := make(map[string]int)
	pf := &prefilter{}
	transition := 0
	for _, set := range sets {
		for _, st := range set.States {
			if st.Name != StartState {
				continue
			}
			for _, tr := range st.Transitions {
				ls, ok := requiredLiterals(tr.Pattern)
				if !ok || transition >= 64 {
					// Nothing provable, or no mask bit left to address this
					// transition with: it must be tried for every line.
					pf.unfiltered = append(pf.unfiltered, tr.Pattern)
					if transition < 64 {
						pf.always |= 1 << transition
					} else {
						pf.wide = true
					}
					transition++
					continue
				}
				for _, l := range ls {
					if i, ok := index[l]; ok {
						probes[i].mask |= 1 << transition
					} else {
						index[l] = len(probes)
						probes = append(probes, probe{lit: l, mask: 1 << transition})
					}
				}
				transition++
			}
		}
	}
	if len(probes) == 0 {
		return nil, false
	}

	// Fold literals that contain a shorter kept literal into it — but only when
	// the long probe implies no transition the short one does not already
	// imply. A line containing the long probe necessarily contains the short
	// one, so folding saves a Contains call; what it costs is precision, since
	// every hit on the short probe then drags in the long probe's transitions
	// too. With substring search an order of magnitude cheaper than the
	// regexes it guards, precision is the better buy: it is what keeps a line
	// carrying the bare word "Error" from running java's unanchored
	// "...(Exception|Error|Throwable):" pattern. Shortest-first order makes
	// the fold deterministic.
	slices.SortStableFunc(probes, func(a, b probe) int {
		if c := cmp.Compare(len(a.lit), len(b.lit)); c != 0 {
			return c
		}
		return cmp.Compare(a.lit, b.lit)
	})
	for _, p := range probes {
		folded := false
		parent := int16(-1)
		for i, kept := range pf.literals {
			if !strings.Contains(p.lit, kept) {
				continue
			}
			if p.mask&^pf.masks[i] == 0 {
				pf.masks[i] |= p.mask
				folded = true
				break
			}
			// Contained but not foldable: remember it as the scan-skip parent
			// (only the first 64 literals have a bit in the hits bitmap).
			if parent < 0 && i < 64 {
				parent = int16(i)
			}
		}
		if !folded {
			pf.literals = append(pf.literals, p.lit)
			pf.masks = append(pf.masks, p.mask)
			pf.parents = append(pf.parents, parent)
		}
	}
	pf.partition()
	if len(pf.literals) >= acMinLiterals {
		pf.ac = buildAhoCorasick(pf.literals, pf.masks)
	}
	return pf, true
}

// partition reorders the probes so the roots (no parent) come first, followed
// by the children, with parents remapped to the new root indices. The scan
// then walks the roots unconditionally and enters the child region only when
// a parent root hit. A child whose parent lands past bit 63 of the hits
// bitmap is promoted to a root (checked unconditionally); children are never
// parents themselves, so promotion cannot cascade.
func (pf *prefilter) partition() {
	n := len(pf.literals)
	newIndex := make([]int16, n)
	isRoot := make([]bool, n)
	roots := 0
	for i := range n {
		if pf.parents[i] < 0 {
			isRoot[i], newIndex[i] = true, int16(roots)
			roots++
		}
	}
	for i := range n {
		if !isRoot[i] && newIndex[pf.parents[i]] >= 64 {
			isRoot[i], newIndex[i] = true, int16(roots)
			roots++
		}
	}
	next := roots
	for i := range n {
		if !isRoot[i] {
			newIndex[i] = int16(next)
			next++
		}
	}

	literals := make([]string, n)
	masks := make([]uint64, n)
	parents := make([]int16, n)
	for old := range n {
		ni := newIndex[old]
		literals[ni], masks[ni] = pf.literals[old], pf.masks[old]
		if isRoot[old] {
			parents[ni] = -1
		} else {
			p := newIndex[pf.parents[old]]
			parents[ni] = p
			pf.parentHits |= 1 << uint(p)
		}
	}
	pf.literals, pf.masks, pf.parents, pf.roots = literals, masks, parents, roots
}

// requiredLiterals returns strings such that every match of pattern contains
// at least one of them.
func requiredLiterals(pattern string) ([]string, bool) {
	re, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return nil, false
	}
	return literalsOf(re.Simplify())
}

// literalsOf walks the parse tree. A concat needs any single child's literal
// set — but contiguous runs of EXACT children are product-expanded first
// ("E"+"(xception|rror)"+":" yields {"Exception:","Error:"} rather than the
// weak factored suffix {"xception","rror"}), which is what keeps ordinary
// lowercase "error ..." log lines from reaching the regexes at all. An
// alternation needs the union across every branch (a branch with no provable
// literal poisons the whole set).
func literalsOf(re *syntax.Regexp) ([]string, bool) {
	switch re.Op {
	case syntax.OpLiteral:
		if re.Flags&syntax.FoldCase != 0 || len(re.Rune) < 3 {
			// Case-folded or too short to be a useful (selective) probe.
			return nil, false
		}
		return []string{string(re.Rune)}, true
	case syntax.OpConcat:
		var best []string
		found := false
		consider := func(ls []string) {
			if len(ls) == 0 || minLen(ls) < 3 {
				return
			}
			if !found || moreSelective(ls, best) {
				best, found = ls, true
			}
		}
		// Product-expand each maximal contiguous run of exact children: a
		// match contains those children's matches concatenated, so the
		// product strings are required substrings.
		i := 0
		for i < len(re.Sub) {
			acc, j := []string{""}, i
			for j < len(re.Sub) {
				ls, ok := exactSet(re.Sub[j])
				if !ok {
					break
				}
				if acc = product(acc, ls); acc == nil {
					break // capped out; fall back to per-child literals
				}
				j++
			}
			if j > i && acc != nil {
				consider(acc)
			}
			if j == i {
				j++
			}
			i = j
		}
		// Fallback: each child's own literal set.
		for _, sub := range re.Sub {
			if ls, ok := literalsOf(sub); ok {
				consider(ls)
			}
		}
		return best, found
	case syntax.OpAlternate:
		var all []string
		for _, sub := range re.Sub {
			ls, ok := literalsOf(sub)
			if !ok {
				return nil, false
			}
			all = append(all, ls...)
		}
		return all, true
	case syntax.OpCapture, syntax.OpPlus:
		return literalsOf(re.Sub[0])
	default:
		// Quantifiers with a zero minimum, classes, anchors, etc. guarantee
		// nothing.
		return nil, false
	}
}

// exactSet returns the finite set of strings a node can match exactly (ok
// false when the node is not a small finite case-sensitive language).
func exactSet(re *syntax.Regexp) ([]string, bool) {
	switch re.Op {
	case syntax.OpLiteral:
		if re.Flags&syntax.FoldCase != 0 {
			return nil, false
		}
		return []string{string(re.Rune)}, true
	case syntax.OpEmptyMatch:
		return []string{""}, true
	case syntax.OpCapture:
		return exactSet(re.Sub[0])
	case syntax.OpAlternate:
		var all []string
		for _, sub := range re.Sub {
			ls, ok := exactSet(sub)
			if !ok {
				return nil, false
			}
			all = append(all, ls...)
			if len(all) > maxProductSet {
				return nil, false
			}
		}
		return all, true
	case syntax.OpConcat:
		acc := []string{""}
		for _, sub := range re.Sub {
			ls, ok := exactSet(sub)
			if !ok {
				return nil, false
			}
			if acc = product(acc, ls); acc == nil {
				return nil, false
			}
		}
		return acc, true
	default:
		return nil, false
	}
}

// Caps keep the product expansion tiny (the bundled patterns need a handful).
const (
	maxProductSet = 16
	maxProductLen = 64
)

// product cross-concatenates two string sets; nil when a cap is exceeded.
func product(a, b []string) []string {
	if len(a)*len(b) > maxProductSet {
		return nil
	}
	out := make([]string, 0, len(a)*len(b))
	for _, x := range a {
		for _, y := range b {
			if len(x)+len(y) > maxProductLen {
				return nil
			}
			out = append(out, x+y)
		}
	}
	return out
}

func minLen(ls []string) int {
	m := 1 << 30
	for _, l := range ls {
		m = min(m, len(l))
	}
	return m
}

// moreSelective prefers the literal set whose weakest (shortest) member is
// longest, then the one with fewer alternatives.
func moreSelective(a, b []string) bool {
	if la, lb := minLen(a), minLen(b); la != lb {
		return la > lb
	}
	return len(a) < len(b)
}
