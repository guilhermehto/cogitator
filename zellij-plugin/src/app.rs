#![allow(dead_code)]

use crate::feed::{self, Agent, Group};
use std::cell::Cell;

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Status {
    Loading,
    Ready,
    NotRunning(String),
    Failed(String),
    PermissionDenied,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Key {
    Char(char),
    Up,
    Down,
    Enter,
    Esc,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Effect {
    Focus { agent_id: String },
    Refresh,
    Hide,
}

#[derive(Debug)]
pub struct App {
    agents: Vec<Agent>,
    selected: Option<String>,
    status: Status,
    pub(crate) scroll: Cell<usize>,
}

impl Default for App {
    fn default() -> Self {
        App {
            agents: Vec::new(),
            selected: None,
            status: Status::Loading,
            scroll: Cell::new(0),
        }
    }
}

pub const GROUP_ORDER: [Group; 5] = [
    Group::NeedsYou,
    Group::Done,
    Group::Working,
    Group::Idle,
    Group::Stopped,
];

fn group_rank(g: Group) -> usize {
    GROUP_ORDER
        .iter()
        .position(|o| *o == g)
        .unwrap_or(GROUP_ORDER.len())
}

impl App {
    pub fn new() -> Self {
        Self::default()
    }

    pub fn agents(&self) -> &[Agent] {
        &self.agents
    }

    pub fn status(&self) -> &Status {
        &self.status
    }

    pub fn selected_id(&self) -> Option<&str> {
        self.selected.as_deref()
    }

    pub fn selected_index(&self) -> Option<usize> {
        let id = self.selected.as_deref()?;
        self.agents.iter().position(|a| a.id == id)
    }

    pub fn set_permission_denied(&mut self) {
        self.status = Status::PermissionDenied;
    }

    pub fn apply_list_result(&mut self, exit_code: Option<i32>, stdout: &[u8], stderr: &[u8]) {
        if exit_code == Some(0) {
            match feed::parse(&String::from_utf8_lossy(stdout)) {
                Ok(feed) => {
                    self.set_agents(feed.agents);
                    self.status = Status::Ready;
                }
                Err(e) => {
                    self.status = Status::Failed(e.to_string());
                }
            }
            return;
        }

        let err = String::from_utf8_lossy(stderr);
        let err = err.trim();
        if err.contains("cogitator is not running") {
            self.status = Status::NotRunning(err.to_string());
            return;
        }

        let msg = if !err.is_empty() {
            err.to_string()
        } else {
            match exit_code {
                Some(code) => format!("exit {code}"),
                None => "exit unknown".to_string(),
            }
        };
        self.status = Status::Failed(msg);
    }

    pub fn set_agents(&mut self, mut agents: Vec<Agent>) {
        agents.sort_by_key(|a| group_rank(a.group));
        let prev_index = self.selected_index();
        let still_present = self
            .selected
            .as_deref()
            .is_some_and(|id| agents.iter().any(|a| a.id == id));

        self.agents = agents;

        if still_present {
            return;
        }

        if self.agents.is_empty() {
            self.selected = None;
            return;
        }

        let idx = prev_index.unwrap_or(0).min(self.agents.len() - 1);
        self.selected = Some(self.agents[idx].id.clone());
    }

    pub fn handle_key(&mut self, key: Key) -> Option<Effect> {
        match key {
            Key::Down | Key::Char('j') => {
                self.move_selection(1);
                None
            }
            Key::Up | Key::Char('k') => {
                self.move_selection(-1);
                None
            }
            Key::Enter => self
                .selected
                .clone()
                .map(|agent_id| Effect::Focus { agent_id }),
            Key::Char('r') => Some(Effect::Refresh),
            Key::Esc | Key::Char('q') => Some(Effect::Hide),
            Key::Char(_) => None,
        }
    }

    fn move_selection(&mut self, delta: isize) {
        if self.agents.is_empty() {
            return;
        }

        let cur = self.selected_index().unwrap_or(0) as isize;
        let max = self.agents.len() as isize - 1;
        let next = (cur + delta).clamp(0, max) as usize;
        self.selected = Some(self.agents[next].id.clone());
    }
}

#[cfg(test)]
pub(crate) mod tests {
    use super::*;

    pub(crate) fn feed_json(agents: &[(&str, &str)]) -> String {
        let items: Vec<String> = agents
            .iter()
            .map(|(id, group)| format!(r#"{{"id":"{id}","group":"{group}"}}"#))
            .collect();
        format!(r#"{{"version":1,"agents":[{}]}}"#, items.join(","))
    }

    fn app_with(agents: &[(&str, &str)]) -> App {
        let mut app = App::new();
        app.apply_list_result(Some(0), feed_json(agents).as_bytes(), b"");
        app
    }

    fn ids(app: &App) -> Vec<&str> {
        app.agents().iter().map(|a| a.id.as_str()).collect()
    }

    #[test]
    fn starts_loading_without_selection() {
        let app = App::new();
        assert_eq!(app.status(), &Status::Loading);
        assert_eq!(app.selected_id(), None);
    }

    #[test]
    fn successful_list_is_ready_and_selects_first() {
        let app = app_with(&[("/a", "needs_you"), ("/b", "idle")]);
        assert_eq!(app.status(), &Status::Ready);
        assert_eq!(app.selected_id(), Some("/a"));
    }

    #[test]
    fn keeps_cogitator_order_within_groups() {
        let app = app_with(&[("/c", "working"), ("/a", "needs_you"), ("/b", "working")]);
        assert_eq!(ids(&app), vec!["/a", "/c", "/b"]);
    }

    #[test]
    fn selection_movement_clamps_at_both_ends() {
        let mut app = app_with(&[("/a", "idle"), ("/b", "idle")]);
        assert_eq!(app.handle_key(Key::Up), None);
        assert_eq!(app.selected_id(), Some("/a"));
        app.handle_key(Key::Char('j'));
        assert_eq!(app.selected_id(), Some("/b"));
        app.handle_key(Key::Down);
        assert_eq!(app.selected_id(), Some("/b"));
        app.handle_key(Key::Char('k'));
        assert_eq!(app.selected_id(), Some("/a"));
    }

    #[test]
    fn selection_follows_id_across_refresh() {
        let mut app = app_with(&[("/a", "idle"), ("/b", "idle")]);
        app.handle_key(Key::Down);
        app.apply_list_result(
            Some(0),
            feed_json(&[("/b", "needs_you"), ("/a", "idle")]).as_bytes(),
            b"",
        );
        assert_eq!(app.selected_id(), Some("/b"));
        assert_eq!(app.selected_index(), Some(0));
    }

    #[test]
    fn removed_selection_clamps_to_same_index() {
        let mut app = app_with(&[("/a", "idle"), ("/b", "idle"), ("/c", "idle")]);
        app.handle_key(Key::Down);
        app.apply_list_result(
            Some(0),
            feed_json(&[("/a", "idle"), ("/c", "idle")]).as_bytes(),
            b"",
        );
        assert_eq!(app.selected_id(), Some("/c"));
    }

    #[test]
    fn removed_selection_clamps_to_last_row() {
        let mut app = app_with(&[("/a", "idle"), ("/b", "idle"), ("/c", "idle")]);
        app.handle_key(Key::Down);
        app.handle_key(Key::Down);
        app.apply_list_result(Some(0), feed_json(&[("/a", "idle")]).as_bytes(), b"");
        assert_eq!(app.selected_id(), Some("/a"));
    }

    #[test]
    fn empty_list_clears_selection() {
        let mut app = app_with(&[("/a", "idle")]);
        app.apply_list_result(Some(0), feed_json(&[]).as_bytes(), b"");
        assert_eq!(app.selected_id(), None);
        assert_eq!(app.handle_key(Key::Enter), None);
        assert_eq!(app.handle_key(Key::Down), None);
    }

    #[test]
    fn enter_yields_focus_with_selected_id() {
        let mut app = app_with(&[("/a", "idle"), ("/b", "idle")]);
        app.handle_key(Key::Down);
        assert_eq!(
            app.handle_key(Key::Enter),
            Some(Effect::Focus {
                agent_id: "/b".into()
            })
        );
    }

    #[test]
    fn refresh_and_hide_keys_yield_effects() {
        let mut app = App::new();
        assert_eq!(app.handle_key(Key::Char('r')), Some(Effect::Refresh));
        assert_eq!(app.handle_key(Key::Char('q')), Some(Effect::Hide));
        assert_eq!(app.handle_key(Key::Esc), Some(Effect::Hide));
        assert_eq!(app.handle_key(Key::Char('x')), None);
    }

    #[test]
    fn not_running_stderr_sets_not_running() {
        let mut app = App::new();
        app.apply_list_result(Some(1), b"", b"cogitator is not running\n");
        assert_eq!(
            app.status(),
            &Status::NotRunning("cogitator is not running".into())
        );
    }

    #[test]
    fn other_failure_uses_trimmed_stderr() {
        let mut app = App::new();
        app.apply_list_result(Some(1), b"", b"  boom\n");
        assert_eq!(app.status(), &Status::Failed("boom".into()));
    }

    #[test]
    fn failure_without_stderr_reports_exit_code() {
        let mut app = App::new();
        app.apply_list_result(Some(2), b"", b"");
        assert_eq!(app.status(), &Status::Failed("exit 2".into()));
    }

    #[test]
    fn unparseable_output_is_failed() {
        let mut app = App::new();
        app.apply_list_result(Some(0), b"nope", b"");
        assert!(matches!(app.status(), Status::Failed(m) if m.starts_with("invalid feed")));
    }

    #[test]
    fn failure_keeps_previous_agents() {
        let mut app = app_with(&[("/a", "idle")]);
        app.apply_list_result(Some(1), b"", b"boom");
        assert_eq!(ids(&app), vec!["/a"]);
        assert_eq!(app.selected_id(), Some("/a"));
    }
}
