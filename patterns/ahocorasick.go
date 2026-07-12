package patterns

// The prefilter scans each line for its probe literals. With a handful of
// probes, a linear strings.Contains loop is fastest — SIMD substring search
// over ~a dozen probes beats walking a table for every byte. Past
// acMinLiterals probes the balance flips, and Compile builds an Aho-Corasick
// automaton instead: one pass over the line regardless of how many literals
// there are, collecting the union of the candidate-transition masks of every
// literal present.
//
// The automaton is a dense goto table (256 entries per node, one node per
// distinct literal-prefix byte), so scanning is a single indexed load per
// input byte with no branching on failure links; the memory cost is ~1 KiB
// per literal byte and is only paid by machines that cross the threshold.

// acMinLiterals is the literal count at which Compile switches the prefilter
// scan from the linear Contains loop to Aho-Corasick. Chosen by benchmark
// (BenchmarkPrefilterScan): the linear loop wins clearly at the bundled ~13
// literals, Aho-Corasick wins from roughly twice that.
const acMinLiterals = 24

// ahoCorasick is a dense-table Aho-Corasick automaton over the probe
// literals. mask[s] is the union of the masks of every literal that ends at
// state s or along its failure chain.
type ahoCorasick struct {
	next [][256]int32
	mask []uint64
}

// buildAhoCorasick constructs the automaton for literals with their parallel
// candidate masks.
func buildAhoCorasick(literals []string, masks []uint64) *ahoCorasick {
	ac := &ahoCorasick{next: make([][256]int32, 1), mask: make([]uint64, 1)}

	// Trie phase. 0 is the root; no trie edge ever targets it, so 0 doubles
	// as the "unset" marker.
	for i, lit := range literals {
		s := int32(0)
		for j := 0; j < len(lit); j++ {
			c := lit[j]
			if ac.next[s][c] == 0 {
				ac.next = append(ac.next, [256]int32{})
				ac.mask = append(ac.mask, 0)
				ac.next[s][c] = int32(len(ac.next) - 1)
			}
			s = ac.next[s][c]
		}
		ac.mask[s] |= masks[i]
	}

	// BFS phase: convert to a goto automaton. Missing edges are redirected
	// along the failure link, and each state's mask absorbs its failure
	// chain's (parents are processed before children, so one hop suffices).
	fail := make([]int32, len(ac.next))
	queue := make([]int32, 0, len(ac.next))
	for c := range 256 {
		if u := ac.next[0][c]; u != 0 {
			queue = append(queue, u) // fail(u) = root
		}
	}
	for qi := 0; qi < len(queue); qi++ {
		u := queue[qi]
		f := fail[u]
		ac.mask[u] |= ac.mask[f]
		for c := range 256 {
			if v := ac.next[u][c]; v != 0 {
				fail[v] = ac.next[f][c]
				queue = append(queue, v)
			} else {
				ac.next[u][c] = ac.next[f][c]
			}
		}
	}

	return ac
}

// scan returns the union of the masks of every literal contained in line.
func (a *ahoCorasick) scan(line string) uint64 {
	var mask uint64
	s := int32(0)
	for i := 0; i < len(line); i++ {
		s = a.next[s][line[i]]
		mask |= a.mask[s]
	}
	return mask
}
