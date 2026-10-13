#![allow(dead_code)]

use serde::Deserialize;
use std::fmt;

#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
pub enum Group {
    NeedsYou,
    Done,
    Working,
    Idle,
    #[default]
    Stopped,
}

impl<'de> Deserialize<'de> for Group {
    fn deserialize<D: serde::Deserializer<'de>>(d: D) -> Result<Self, D::Error> {
        let s = String::deserialize(d)?;
        Ok(match s.as_str() {
            "needs_you" => Group::NeedsYou,
            "done" => Group::Done,
            "working" => Group::Working,
            "idle" => Group::Idle,
            _ => Group::Stopped,
        })
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Default, Deserialize)]
#[serde(default)]
pub struct Agent {
    pub id: String,
    pub kind: String,
    pub repo: String,
    pub branch: String,
    pub workspace: String,
    pub session_name: String,
    pub harness: String,
    pub title: String,
    pub attention: String,
    pub group: Group,
    pub last_activity: String,
    pub mux_session: String,
    pub window_id: String,
    pub pane_id: String,
}

impl Agent {
    pub fn label(&self) -> String {
        if self.kind == "worktree" && !self.repo.is_empty() && !self.branch.is_empty() {
            return format!("{}/{}", self.repo, self.branch);
        }
        if self.kind == "workspace" && !self.workspace.is_empty() && !self.session_name.is_empty() {
            return format!("{}/{}", self.workspace, self.session_name);
        }
        if !self.title.is_empty() {
            return self.title.clone();
        }
        self.id
            .trim_end_matches('/')
            .rsplit('/')
            .next()
            .unwrap_or_default()
            .to_string()
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Default)]
pub struct Feed {
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
    agents: Vec<Agent>,
}

pub fn parse(input: &str) -> Result<Feed, ParseError> {
    let wire: Wire = serde_json::from_str(input).map_err(|e| ParseError::Invalid(e.to_string()))?;
    if wire.version != 1 {
        return Err(ParseError::UnsupportedVersion(wire.version));
    }
    Ok(Feed {
        agents: wire.agents,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_full_agent() {
        let input = r#"{"version":1,"agents":[{"id":"/a/b","kind":"worktree","repo":"r","branch":"br","workspace":"w","session_name":"s","harness":"omp","title":"t","attention":"question","group":"needs_you","last_activity":"2026-01-01T00:00:00Z","mux_session":"m","window_id":"1","pane_id":"2","extra":"x"}]}"#;
        let feed = parse(input).unwrap();
        assert_eq!(
            feed.agents,
            vec![Agent {
                id: "/a/b".into(),
                kind: "worktree".into(),
                repo: "r".into(),
                branch: "br".into(),
                workspace: "w".into(),
                session_name: "s".into(),
                harness: "omp".into(),
                title: "t".into(),
                attention: "question".into(),
                group: Group::NeedsYou,
                last_activity: "2026-01-01T00:00:00Z".into(),
                mux_session: "m".into(),
                window_id: "1".into(),
                pane_id: "2".into(),
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
        assert_eq!(feed.agents[0].group, Group::Stopped);
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
    fn maps_known_and_unknown_groups() {
        let input = r#"{"version":1,"agents":[{"group":"done"},{"group":"working"},{"group":"idle"},{"group":"stopped"},{"group":"weird"}]}"#;
        let groups: Vec<Group> = parse(input)
            .unwrap()
            .agents
            .iter()
            .map(|a| a.group)
            .collect();
        assert_eq!(
            groups,
            vec![
                Group::Done,
                Group::Working,
                Group::Idle,
                Group::Stopped,
                Group::Stopped
            ]
        );
    }

    fn agent(kind: &str) -> Agent {
        Agent {
            id: "/home/me/proj".into(),
            kind: kind.into(),
            ..Agent::default()
        }
    }

    #[test]
    fn label_uses_repo_branch_for_worktree() {
        let a = Agent {
            repo: "r".into(),
            branch: "b".into(),
            ..agent("worktree")
        };
        assert_eq!(a.label(), "r/b");
    }

    #[test]
    fn label_uses_workspace_session_for_workspace() {
        let a = Agent {
            workspace: "w".into(),
            session_name: "s".into(),
            ..agent("workspace")
        };
        assert_eq!(a.label(), "w/s");
    }

    #[test]
    fn label_falls_back_to_title_when_parts_missing() {
        let a = Agent {
            repo: "r".into(),
            title: "T".into(),
            ..agent("worktree")
        };
        assert_eq!(a.label(), "T");
    }

    #[test]
    fn label_falls_back_to_last_path_component() {
        assert_eq!(agent("stray").label(), "proj");
    }
}
