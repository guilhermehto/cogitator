use serde::Deserialize;
use std::fmt;

#[derive(Debug, Clone, PartialEq, Eq, Default, Deserialize)]
#[serde(default)]
pub struct Agent {
    pub id: String,
    pub dir: String,
    pub kind: String,
    pub repo: String,
    pub branch: String,
    pub workspace: String,
    pub session_name: String,
    pub harness: String,
    pub title: String,
    pub attention: String,
    pub state: String,
    pub group: String,
    pub last_activity: String,
    pub mux_session: String,
    pub window_id: String,
    pub pane_id: String,
    pub session_owned: bool,
}

impl Agent {
    pub fn last_activity_secs(&self) -> Option<i64> {
        parse_rfc3339(&self.last_activity)
    }

    /// cogitator owns the attention rules; its group says whether the agent
    /// is blocked on the user.
    pub fn needs_you(&self) -> bool {
        self.group == "needs_you"
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Default)]
pub struct Feed {
    pub home: String,
    pub agents: Vec<Agent>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum ParseError {
    UnsupportedVersion(u64),
    Invalid(String),
}

impl fmt::Display for ParseError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            ParseError::UnsupportedVersion(n) => write!(f, "unsupported feed version {n}"),
            ParseError::Invalid(msg) => write!(f, "invalid feed: {msg}"),
        }
    }
}

#[derive(Deserialize)]
struct Wire {
    version: u64,
    #[serde(default)]
    home: String,
    #[serde(default)]
    agents: Vec<Agent>,
}

pub fn parse(input: &str) -> Result<Feed, ParseError> {
    let wire: Wire = serde_json::from_str(input).map_err(|e| ParseError::Invalid(e.to_string()))?;
    if wire.version != 1 {
        return Err(ParseError::UnsupportedVersion(wire.version));
    }
    Ok(Feed {
        home: wire.home,
        agents: wire.agents,
    })
}

fn parse_rfc3339(s: &str) -> Option<i64> {
    let b = s.as_bytes();
    if b.len() < 20 || b[4] != b'-' || b[7] != b'-' || !matches!(b[10], b'T' | b't') {
        return None;
    }
    if b[13] != b':' || b[16] != b':' {
        return None;
    }

    let year = num(s, 0, 4)?;
    let month = num(s, 5, 7)?;
    let day = num(s, 8, 10)?;
    let hour = num(s, 11, 13)?;
    let min = num(s, 14, 16)?;
    let sec = num(s, 17, 19)?;
    if !(1..=12).contains(&month) || !(1..=31).contains(&day) || hour > 23 || min > 59 || sec > 60 {
        return None;
    }

    let mut rest = &s[19..];
    if let Some(frac) = rest.strip_prefix('.') {
        let digits = frac.bytes().take_while(u8::is_ascii_digit).count();
        if digits == 0 {
            return None;
        }
        rest = &frac[digits..];
    }

    let offset = parse_offset(rest)?;
    let days = days_from_civil(year, month, day);
    Some(days * 86400 + hour * 3600 + min * 60 + sec - offset)
}

fn parse_offset(s: &str) -> Option<i64> {
    if s == "Z" || s == "z" {
        return Some(0);
    }

    let b = s.as_bytes();
    if b.len() != 6 || b[3] != b':' {
        return None;
    }
    let sign = match b[0] {
        b'+' => 1,
        b'-' => -1,
        _ => return None,
    };
    let h = num(s, 1, 3)?;
    let m = num(s, 4, 6)?;
    Some(sign * (h * 3600 + m * 60))
}

fn num(s: &str, from: usize, to: usize) -> Option<i64> {
    let part = s.get(from..to)?;
    if !part.bytes().all(|c| c.is_ascii_digit()) {
        return None;
    }
    part.parse().ok()
}

fn days_from_civil(y: i64, m: i64, d: i64) -> i64 {
    let y = if m <= 2 { y - 1 } else { y };
    let era = y.div_euclid(400);
    let yoe = y - era * 400;
    let mp = (m + 9) % 12;
    let doy = (153 * mp + 2) / 5 + d - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    era * 146097 + doe - 719468
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_full_agent() {
        let input = r#"{"version":1,"home":"/h","agents":[{"id":"/a/b#s1","dir":"/a/b","kind":"worktree","repo":"r","branch":"br","workspace":"w","session_name":"s","harness":"omp","title":"t","attention":"question","state":"running","group":"needs_you","last_activity":"2026-01-01T00:00:00Z","mux_session":"m","window_id":"1","pane_id":"2","session_owned":true,"extra":"x"}]}"#;
        let feed = parse(input).unwrap();
        assert_eq!(feed.home, "/h");
        assert_eq!(
            feed.agents,
            vec![Agent {
                id: "/a/b#s1".into(),
                dir: "/a/b".into(),
                kind: "worktree".into(),
                repo: "r".into(),
                branch: "br".into(),
                workspace: "w".into(),
                session_name: "s".into(),
                harness: "omp".into(),
                title: "t".into(),
                attention: "question".into(),
                state: "running".into(),
                group: "needs_you".into(),
                last_activity: "2026-01-01T00:00:00Z".into(),
                mux_session: "m".into(),
                window_id: "1".into(),
                pane_id: "2".into(),
                session_owned: true,
            }]
        );
    }

    #[test]
    fn parses_empty_agents_list() {
        assert_eq!(
            parse(r#"{"version":1,"agents":[]}"#).unwrap().agents,
            vec![]
        );
    }

    #[test]
    fn missing_fields_default_to_empty() {
        let feed = parse(r#"{"version":1,"agents":[{"id":"/x"}]}"#).unwrap();
        assert_eq!(feed.agents[0].kind, "");
        assert_eq!(feed.agents[0].last_activity_secs(), None);
    }

    #[test]
    fn rejects_unknown_version() {
        assert_eq!(
            parse(r#"{"version":2,"agents":[]}"#),
            Err(ParseError::UnsupportedVersion(2))
        );
    }

    #[test]
    fn rejects_invalid_json() {
        assert!(matches!(parse("not json"), Err(ParseError::Invalid(_))));
    }

    #[test]
    fn parses_rfc3339_utc_and_offsets() {
        const NEW_YEAR_2026: i64 = 1_767_225_600;
        assert_eq!(parse_rfc3339("1970-01-01T00:00:00Z"), Some(0));
        assert_eq!(parse_rfc3339("2026-01-01T00:00:00Z"), Some(NEW_YEAR_2026));
        assert_eq!(
            parse_rfc3339("2026-01-01T00:00:00.123456789Z"),
            Some(NEW_YEAR_2026)
        );
        assert_eq!(
            parse_rfc3339("2026-01-01T02:00:00+02:00"),
            Some(NEW_YEAR_2026)
        );
        assert_eq!(
            parse_rfc3339("2025-12-31T23:00:00-01:00"),
            Some(NEW_YEAR_2026)
        );
        assert_eq!(parse_rfc3339("2024-02-29T00:00:00Z"), Some(1_709_164_800));
        assert_eq!(parse_rfc3339(""), None);
        assert_eq!(parse_rfc3339("2026-01-01 00:00:00"), None);
        assert_eq!(parse_rfc3339("2026-13-01T00:00:00Z"), None);
    }
}
