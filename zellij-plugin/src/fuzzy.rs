//! Ranks a query against labels as cogitator's fuzzyMatchIndices
//! (internal/ui/fuzzy.go) does, so the sidebar finds what the TUI finds.

const MATCH_SCORE: i32 = 1;
// Kept above the boundary bonus so a contiguous substring outranks a
// scattered match that lands on many segment boundaries.
const CONSECUTIVE_BONUS: i32 = 12;
const BOUNDARY_BONUS: i32 = 10;

/// Returns the indices of the labels that hold query as a case-insensitive
/// subsequence, best match first: packed matches, then matches at segment
/// starts, then shorter labels, then labels in lexical order. A blank query
/// matches every label in its own order.
pub fn rank(query: &str, labels: &[String]) -> Vec<usize> {
    if query.trim().is_empty() {
        return (0..labels.len()).collect();
    }

    let query: Vec<char> = query.to_lowercase().chars().collect();
    let mut matches: Vec<(usize, i32)> = labels
        .iter()
        .enumerate()
        .filter_map(|(i, label)| score(&query, &label.to_lowercase()).map(|s| (i, s)))
        .collect();
    matches.sort_by(|(a, a_score), (b, b_score)| {
        b_score
            .cmp(a_score)
            .then(labels[*a].len().cmp(&labels[*b].len()))
            .then(labels[*a].cmp(&labels[*b]))
    });

    matches.into_iter().map(|(i, _)| i).collect()
}

/// Binds each query char to the earliest remaining target char, so the same
/// greedy walk decides whether the target matches and how well.
fn score(query: &[char], target: &str) -> Option<i32> {
    let target: Vec<char> = target.chars().collect();
    let mut score = 0;
    let mut matched = 0;
    let mut previous: Option<usize> = None;

    for (i, &c) in target.iter().enumerate() {
        if matched == query.len() {
            break;
        }
        if c != query[matched] {
            continue;
        }

        score += MATCH_SCORE;
        if previous.is_some_and(|p| p + 1 == i) {
            score += CONSECUTIVE_BONUS;
        }
        if i == 0 || is_boundary(target[i - 1]) {
            score += BOUNDARY_BONUS;
        }
        previous = Some(i);
        matched += 1;
    }

    (matched == query.len()).then_some(score)
}

fn is_boundary(c: char) -> bool {
    matches!(c, '/' | '-' | '_' | ' ' | '.')
}

#[cfg(test)]
mod tests {
    use super::*;

    fn labels(values: &[&str]) -> Vec<String> {
        values.iter().map(|v| v.to_string()).collect()
    }

    fn ranked<'a>(query: &str, values: &[&'a str]) -> Vec<&'a str> {
        rank(query, &labels(values))
            .into_iter()
            .map(|i| values[i])
            .collect()
    }

    #[test]
    fn blank_query_keeps_every_label_in_order() {
        assert_eq!(ranked("  ", &["b", "a"]), ["b", "a"]);
    }

    #[test]
    fn matches_a_case_insensitive_subsequence() {
        assert_eq!(ranked("CoG", &["cat", "Cogitator", "dog"]), ["Cogitator"]);
    }

    #[test]
    fn contiguous_match_beats_one_on_segment_boundaries() {
        assert_eq!(ranked("abc", &["/a/b/c", "/x/abc"]), ["/x/abc", "/a/b/c"]);
    }

    #[test]
    fn segment_start_beats_a_match_inside_a_word() {
        assert_eq!(
            ranked("cog", &["incognito", "repo/cogitator"]),
            ["repo/cogitator", "incognito"]
        );
    }

    #[test]
    fn ties_go_to_the_shorter_then_lexically_first_label() {
        assert_eq!(ranked("a", &["ab", "a", "ac"]), ["a", "ab", "ac"]);
    }
}
