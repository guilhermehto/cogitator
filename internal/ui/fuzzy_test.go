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
