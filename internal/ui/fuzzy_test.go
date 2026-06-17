package ui

import (
	"reflect"
	"testing"
)

func TestFuzzyRank_EmptyQueryReturnsAllUnchanged(t *testing.T) {
	in := []string{"/c", "/a", "/b"}
	got := fuzzyRank("   ", in)
	if !reflect.DeepEqual(got, in) {
		t.Errorf("empty query: got %v, want %v (original order)", got, in)
	}
	// Must be a copy, not the same backing array.
	got[0] = "mutated"
	if in[0] == "mutated" {
		t.Errorf("fuzzyRank must not alias the input slice")
	}
}

func TestFuzzyRank_FiltersNonMatches(t *testing.T) {
	in := []string{"/home/me/cogitator", "/home/me/zzz", "/home/me/notes"}
	got := fuzzyRank("cog", in)
	if len(got) != 1 || got[0] != "/home/me/cogitator" {
		t.Errorf("cog: got %v, want only cogitator", got)
	}
}

func TestFuzzyRank_CaseInsensitive(t *testing.T) {
	in := []string{"/home/me/CogITator"}
	got := fuzzyRank("cogit", in)
	if len(got) != 1 {
		t.Errorf("expected case-insensitive match, got %v", got)
	}
}

func TestFuzzyRank_BoundaryBeatsMidword(t *testing.T) {
	// "cog" matches at the start of "cogitator" (boundary, after '/') and
	// mid-word in "incognito"; the boundary match must rank first.
	in := []string{"/src/incognito", "/src/cogitator"}
	got := fuzzyRank("cog", in)
	if len(got) != 2 {
		t.Fatalf("expected both to match, got %v", got)
	}
	if got[0] != "/src/cogitator" {
		t.Errorf("boundary match should rank first; got %v", got)
	}
}

func TestFuzzyRank_ContiguousBeatsScattered(t *testing.T) {
	// Query "abc": contiguous in "/x/abc" vs scattered in "/a/b/c".
	in := []string{"/a/b/c", "/x/abc"}
	got := fuzzyRank("abc", in)
	if len(got) != 2 {
		t.Fatalf("expected both to match, got %v", got)
	}
	if got[0] != "/x/abc" {
		t.Errorf("contiguous match should rank first; got %v", got)
	}
}

func TestFuzzyMatchIndices_EmptyQueryReturnsAllIndicesInOrder(t *testing.T) {
	in := []string{"/c", "/a", "/b"}
	got := fuzzyMatchIndices("  ", in)
	want := []int{0, 1, 2}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("empty query: got %v, want %v", got, want)
	}
}

func TestFuzzyMatchIndices_IndicesAgreeWithFuzzyRank(t *testing.T) {
	// The indices returned must map, in order, to exactly the values fuzzyRank
	// returns — they are the same ranking, one by index and one by value.
	in := []string{"/src/incognito", "/src/cogitator", "/home/notes"}
	idx := fuzzyMatchIndices("cog", in)
	ranked := fuzzyRank("cog", in)
	if len(idx) != len(ranked) {
		t.Fatalf("length mismatch: indices %v vs ranked %v", idx, ranked)
	}
	for i, j := range idx {
		if in[j] != ranked[i] {
			t.Errorf("index %d → %q, want %q", i, in[j], ranked[i])
		}
	}
}

func TestFuzzyMatchPositions_AlignsWithLabelRunes(t *testing.T) {
	// "cm" against "cogitator main": 'c' at 0, 'm' at the start of "main" (8).
	pos, ok := fuzzyMatchPositions("cm", "cogitator main")
	if !ok {
		t.Fatal("expected cm to match")
	}
	runes := []rune("cogitator main")
	if len(pos) != 2 || runes[pos[0]] != 'c' || runes[pos[1]] != 'm' {
		t.Errorf("positions = %v (runes %c,%c), want indices of 'c' then 'm'", pos, runes[pos[0]], runes[pos[1]])
	}
}

func TestFuzzyMatchPositions_CaseInsensitiveEarliestBinding(t *testing.T) {
	// Greedy earliest binding: the two 'a's bind to the first two 'A'/'a'.
	pos, ok := fuzzyMatchPositions("AA", "abracadabra")
	if !ok {
		t.Fatal("expected aa to match abracadabra")
	}
	want := []int{0, 3} // 'a' at 0, next 'a' at 3 (after 'br')
	if len(pos) != 2 || pos[0] != want[0] || pos[1] != want[1] {
		t.Errorf("positions = %v, want %v", pos, want)
	}
}

func TestFuzzyMatchPositions_NoMatchReturnsFalse(t *testing.T) {
	if _, ok := fuzzyMatchPositions("xyz", "cogitator"); ok {
		t.Error("xyz must not match cogitator")
	}
}

func TestFuzzyMatchPositions_EmptyQueryMatchesWithNoPositions(t *testing.T) {
	pos, ok := fuzzyMatchPositions("  ", "anything")
	if !ok || pos != nil {
		t.Errorf("empty query: pos=%v ok=%v, want nil,true", pos, ok)
	}
}

func TestFuzzyScore_Subsequence(t *testing.T) {
	if _, ok := fuzzyScore("abc", "xaybzc"); !ok {
		t.Errorf("abc should be a subsequence of xaybzc")
	}
	if _, ok := fuzzyScore("abc", "acb"); ok {
		t.Errorf("abc is not a subsequence of acb")
	}
	if _, ok := fuzzyScore("", "anything"); !ok {
		t.Errorf("empty query should always match")
	}
}
