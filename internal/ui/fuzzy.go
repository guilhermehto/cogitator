package ui

import (
	"sort"
	"strings"
	"unicode"
)

// fuzzyRank filters candidates to those that fuzzily match query — a
// case-insensitive subsequence match — and returns them ordered best-first.
//
// An empty (or whitespace-only) query returns a copy of candidates unchanged;
// the finder relies on this to show the full, original-order list before the
// user types. Scoring favours, in order:
//
//   - matches packed close together (consecutive runes score highest), so a
//     contiguous hit beats a scattered one;
//   - matches that begin at a segment boundary ('/', '-', '_', ' ', '.', or the
//     start of the string), so typing "cog" ranks ".../cogitator" above
//     ".../incognito";
//   - shorter candidates as a tiebreak, so the most specific path wins.
//
// Remaining ties break lexicographically for a stable, predictable order.
func fuzzyRank(query string, candidates []string) []string {
	idx := fuzzyMatchIndices(query, candidates)
	out := make([]string, len(idx))
	for i, j := range idx {
		out[i] = candidates[j]
	}
	return out
}

// fuzzyMatchIndices ranks candidates exactly like fuzzyRank but returns the
// matched candidates' indices (best-first) instead of their values, so callers
// holding a parallel slice (e.g. a row per candidate) can map a match back to
// its source. An empty (or whitespace-only) query returns every index in input
// order. The scoring and tiebreaks are identical to fuzzyRank — this is the
// single shared implementation; fuzzyRank is a thin value-returning wrapper.
func fuzzyMatchIndices(query string, candidates []string) []int {
	if strings.TrimSpace(query) == "" {
		out := make([]int, len(candidates))
		for i := range candidates {
			out[i] = i
		}
		return out
	}
	q := strings.ToLower(query)

	type scored struct {
		index int
		value string
		score int
	}
	matches := make([]scored, 0, len(candidates))
	for i, c := range candidates {
		if s, ok := fuzzyScore(q, strings.ToLower(c)); ok {
			matches = append(matches, scored{index: i, value: c, score: s})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		if len(matches[i].value) != len(matches[j].value) {
			return len(matches[i].value) < len(matches[j].value)
		}
		return matches[i].value < matches[j].value
	})

	out := make([]int, len(matches))
	for i, m := range matches {
		out[i] = m.index
	}
	return out
}

// fuzzyMatchPositions returns the rune indices in target that query matches as
// a greedy, earliest-binding subsequence (case-insensitive), and whether query
// matched at all. The indices align with target's runes so callers can
// highlight the matched characters in the original (un-lowercased) string. An
// empty (or whitespace-only) query matches with no positions. The matched
// decision agrees with fuzzyScore / fuzzyMatchIndices: the same greedy walk
// decides both whether a candidate matches and where.
func fuzzyMatchPositions(query, target string) ([]int, bool) {
	if strings.TrimSpace(query) == "" {
		return nil, true
	}
	qr := []rune(strings.ToLower(query))
	tr := []rune(target)
	positions := make([]int, 0, len(qr))
	qi := 0
	for ti := 0; ti < len(tr) && qi < len(qr); ti++ {
		if unicode.ToLower(tr[ti]) == qr[qi] {
			positions = append(positions, ti)
			qi++
		}
	}
	if qi != len(qr) {
		return nil, false
	}
	return positions, true
}

// fuzzyScore reports whether query is a subsequence of target (both already
// lowercased) and, if so, a score where higher is better. Matching is greedy:
// each query rune binds to the earliest remaining target rune. Consecutive
// matches and matches at segment boundaries earn bonuses.
func fuzzyScore(query, target string) (int, bool) {
	if query == "" {
		return 0, true
	}
	qr := []rune(query)
	tr := []rune(target)

	score := 0
	qi := 0
	prevMatch := -2 // target index of the previously matched rune
	for ti := 0; ti < len(tr) && qi < len(qr); ti++ {
		if tr[ti] != qr[qi] {
			continue
		}
		score++ // base point for any match
		if ti == prevMatch+1 {
			// Consecutive-match bonus. Kept above the boundary bonus so a
			// contiguous substring ("/x/abc") outranks a scattered match that
			// merely lands on many segment boundaries ("/a/b/c").
			score += 12
		}
		if ti == 0 || isBoundary(tr[ti-1]) {
			score += 10 // segment-boundary bonus
		}
		prevMatch = ti
		qi++
	}
	if qi != len(qr) {
		return 0, false
	}
	return score, true
}

// isBoundary reports whether r is a path/word separator, used to award the
// segment-boundary bonus to the rune that follows it.
func isBoundary(r rune) bool {
	switch r {
	case '/', '-', '_', ' ', '.':
		return true
	default:
		return false
	}
}
