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
// cannot silently break the implication: if no literal set can be proven for
// every start transition, the prefilter is disabled and Step always runs the
// regexes. Correctness is additionally covered by a differential test over
// the corpus (TestPrefilterDifferential).

// prefilter maps probe literals to the start transitions they imply.
// literals[i] hitting a line marks masks[i]'s bits of transitions[0] as
// candidates; a line with no hits cannot match any start transition.
type prefilter struct {
	literals []string
	masks    []uint64
	// ac replaces the linear Contains scan when the literal count crosses
	// acMinLiterals (see ahocorasick.go); nil otherwise.
	ac *ahoCorasick
}

// scan returns the union of the candidate-transition masks of every probe
// literal contained in line.
func (pf *prefilter) scan(line string) uint64 {
	if pf.ac != nil {
		return pf.ac.scan(line)
	}
	var mask uint64
	for i, lit := range pf.literals {
		if strings.Contains(line, lit) {
			mask |= pf.masks[i]
		}
	}
	return mask
}

// startPrefilter derives the prefilter from every start-state transition of
// the given sets. ok is false when any pattern's required literals cannot be
// proven, or there are more than 64 start transitions (the prefilter must
// then be disabled).
func startPrefilter(sets []StateSet) (*prefilter, bool) {
	type probe struct {
		lit  string
		mask uint64
	}
	var probes []probe
	index := make(map[string]int)
	transition := 0
	for _, set := range sets {
		for _, st := range set.States {
			if st.Name != StartState {
				continue
			}
			for _, tr := range st.Transitions {
				if transition >= 64 {
					return nil, false
				}
				ls, ok := requiredLiterals(tr.Pattern)
				if !ok {
					return nil, false
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

	// Fold literals that contain a shorter kept literal into it: a line
	// containing the long probe necessarily contains the short one, so the
	// long check is redundant — its transitions just become candidates of
	// the short probe. Shortest-first order makes the fold deterministic.
	slices.SortStableFunc(probes, func(a, b probe) int {
		if c := cmp.Compare(len(a.lit), len(b.lit)); c != 0 {
			return c
		}
		return cmp.Compare(a.lit, b.lit)
	})
	pf := &prefilter{}
	for _, p := range probes {
		folded := false
		for i, kept := range pf.literals {
			if strings.Contains(p.lit, kept) {
				pf.masks[i] |= p.mask
				folded = true
				break
			}
		}
		if !folded {
			pf.literals = append(pf.literals, p.lit)
			pf.masks = append(pf.masks, p.mask)
		}
	}
	if len(pf.literals) >= acMinLiterals {
		pf.ac = buildAhoCorasick(pf.literals, pf.masks)
	}
	return pf, true
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
