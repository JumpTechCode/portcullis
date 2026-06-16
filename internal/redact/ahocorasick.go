package redact

// ahoCorasick is an immutable Aho-Corasick automaton over a fixed set of byte
// patterns (the secret values). It supports single-pass matching of all patterns
// simultaneously: one scan of the input reports every occurrence of every
// pattern, with cost linear in the input length plus the number of matches and
// independent of the number of patterns.
//
// The automaton is built once by newAhoCorasick and is never mutated afterward,
// so findSpans is safe for concurrent use.
type ahoCorasick struct {
	nodes []acNode
}

// acNode is a single automaton state.
//
// next maps an input byte to the child state reached by the goto function. A
// byte-keyed map (rather than a dense 256-entry array per node) keeps memory
// proportional to the trie's real fan-out, which matters when there are many
// long secrets. fail is the failure link: the state to fall back to when no goto
// edge exists for the current byte. patLen is the length of the pattern ending at
// this state, or 0 if no pattern ends here. outLen is the length of the longest
// pattern reachable from this state through failure links (a "dictionary suffix
// link" collapsed to a length), or 0 if none; it lets findSpans report the
// longest match ending at each position without walking the failure chain.
type acNode struct {
	next   map[byte]int
	fail   int
	patLen int
	outLen int
}

// newAhoCorasick builds the automaton from the given patterns. Empty patterns
// are ignored. When no non-empty patterns remain, the returned automaton has a
// single root state and never reports a match.
func newAhoCorasick(patterns []string) *ahoCorasick {
	ac := &ahoCorasick{
		// Index 0 is the root state.
		nodes: []acNode{{next: map[byte]int{}}},
	}
	for _, p := range patterns {
		ac.add(p)
	}
	ac.build()
	return ac
}

// add inserts a single pattern into the goto trie. Empty patterns are ignored so
// they can never produce zero-length matches.
func (ac *ahoCorasick) add(pattern string) {
	if pattern == "" {
		return
	}
	cur := 0
	for i := 0; i < len(pattern); i++ {
		b := pattern[i]
		next, ok := ac.nodes[cur].next[b]
		if !ok {
			next = len(ac.nodes)
			ac.nodes = append(ac.nodes, acNode{next: map[byte]int{}})
			ac.nodes[cur].next[b] = next
		}
		cur = next
	}
	// Record the pattern length at its terminal state. If the same pattern (or a
	// pattern of the same terminal node) is added twice, keep the longer; here
	// the path is identical so the length is the same.
	ac.nodes[cur].patLen = len(pattern)
}

// build computes failure and output links with a breadth-first sweep, the
// standard Aho-Corasick construction. After build, each state's outLen is the
// length of the longest pattern that ends at that state or at any state reachable
// from it via failure links.
func (ac *ahoCorasick) build() {
	// Depth-1 states fail to the root; seed the BFS queue with them.
	queue := make([]int, 0, len(ac.nodes))
	for _, child := range ac.nodes[0].next {
		ac.nodes[child].fail = 0
		queue = append(queue, child)
	}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		// Collapse this state's dictionary-suffix length: the longest pattern
		// ending here is either a pattern terminating at this state or the
		// longest one inherited along the failure link.
		failOut := ac.nodes[ac.nodes[cur].fail].outLen
		ac.nodes[cur].outLen = max(ac.nodes[cur].patLen, failOut)

		for b, child := range ac.nodes[cur].next {
			// Follow failure links from cur's parent failure state to find the
			// longest proper suffix that has a goto edge on b.
			f := ac.nodes[cur].fail
			for {
				if next, ok := ac.nodes[f].next[b]; ok {
					ac.nodes[child].fail = next
					break
				}
				if f == 0 {
					ac.nodes[child].fail = 0
					break
				}
				f = ac.nodes[f].fail
			}
			queue = append(queue, child)
		}
	}
}

// findSpans scans data once and returns one span per match occurrence. To avoid
// corrupting the output, only the longest pattern ending at each position is
// reported (leftmost-longest at each end position); shorter patterns sharing the
// same end are subsumed by the surrounding merge step. Spans are returned in
// non-decreasing end order, which is also non-decreasing start order for the
// per-position-longest set, so the caller may merge them directly.
func (ac *ahoCorasick) findSpans(data []byte) []span {
	var spans []span
	state := 0
	for i := 0; i < len(data); i++ {
		b := data[i]
		// Goto with failure fallback: follow failure links until a transition on
		// b exists or we are back at the root with none.
		for {
			if next, ok := ac.nodes[state].next[b]; ok {
				state = next
				break
			}
			if state == 0 {
				break
			}
			state = ac.nodes[state].fail
		}
		// outLen is the longest pattern ending at position i (inclusive); if any
		// pattern ends here, emit exactly that one span.
		if l := ac.nodes[state].outLen; l > 0 {
			end := i + 1
			spans = append(spans, span{start: end - l, end: end})
		}
	}
	return spans
}
