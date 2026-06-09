package ui

import (
	"sort"
	"strings"
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
	if strings.TrimSpace(query) == "" {
		out := make([]string, len(candidates))
		copy(out, candidates)
		return out
	}
	q := strings.ToLower(query)

	type scored struct {
		value string
		score int
	}
	matches := make([]scored, 0, len(candidates))
	for _, c := range candidates {
		if s, ok := fuzzyScore(q, strings.ToLower(c)); ok {
			matches = append(matches, scored{value: c, score: s})
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

	out := make([]string, len(matches))
	for i, m := range matches {
		out[i] = m.value
	}
	return out
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
