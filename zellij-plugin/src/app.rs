use crate::feed::{self, Agent};
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
    Dismiss,
}

/// Where the plugin instance sits: its zellij session and, once known, the
/// `<session>:<tab_id>` address of its tab, as cogitator writes window_id.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Location {
    pub session: String,
    pub window: Option<String>,
}

#[derive(Debug)]
pub struct App {
    agents: Vec<Agent>,
    home: String,
    selected: Option<String>,
    status: Status,
    notice: Option<String>,
    floating: bool,
    here: Option<Location>,
    pub(crate) scroll: Cell<usize>,
}

impl Default for App {
    fn default() -> Self {
        App {
            agents: Vec::new(),
            home: String::new(),
            selected: None,
            status: Status::Loading,
            notice: None,
            floating: false,
            here: None,
            scroll: Cell::new(0),
        }
    }
}

impl App {
    pub fn new() -> Self {
        Self::default()
    }

    pub fn agents(&self) -> &[Agent] {
        &self.agents
    }

    pub fn home(&self) -> &str {
        &self.home
    }

    pub fn status(&self) -> &Status {
        &self.status
    }

    pub fn notice(&self) -> Option<&str> {
        self.notice.as_deref()
    }

    pub fn set_notice(&mut self, msg: impl Into<String>) {
        self.notice = Some(msg.into());
    }

    pub fn selected_index(&self) -> Option<usize> {
        let id = self.selected.as_deref()?;
        self.agents.iter().position(|a| a.id == id)
    }

    pub fn floating(&self) -> bool {
        self.floating
    }

    pub fn set_floating(&mut self, floating: bool) {
        self.floating = floating;
    }

    /// Reports whether the location changed, so the caller knows to redraw.
    pub fn set_location(&mut self, here: Location) -> bool {
        if self.here.as_ref() == Some(&here) {
            return false;
        }

        self.here = Some(here);
        true
    }

    /// Mirrors cogitator's agents.AgentAt: the agent on this tab, else the
    /// agent that owns this session.
    pub fn here_index(&self) -> Option<usize> {
        let here = self.here.as_ref()?;

        if let Some(window) = &here.window {
            if let Some(i) = self.agents.iter().position(|a| a.window_id == *window) {
                return Some(i);
            }
        }

        self.agents
            .iter()
            .position(|a| a.session_owned && a.mux_session == here.session)
    }

    pub fn set_permission_denied(&mut self) {
        self.status = Status::PermissionDenied;
    }

    pub fn apply_list_result(&mut self, exit_code: Option<i32>, stdout: &[u8], stderr: &[u8]) {
        if exit_code == Some(0) {
            match feed::parse(&String::from_utf8_lossy(stdout)) {
                Ok(feed) => {
                    self.home = feed.home;
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

        self.status = Status::Failed(failure_message(exit_code, err));
    }

    pub fn apply_focus_result(&mut self, exit_code: Option<i32>, stderr: &[u8]) -> bool {
        if exit_code == Some(0) {
            return true;
        }

        let err = String::from_utf8_lossy(stderr);
        self.notice = Some(failure_message(exit_code, err.trim()));
        false
    }

    /// Agents that need the user come first, then the rest; each part runs
    /// oldest ordered_at first, so whoever has waited longest sits on top and
    /// a working agent keeps its place until it stops. Agents without a
    /// timestamp close their part in cogitator's order.
    /// The TUI's agentList.SetAgents (internal/ui/agentlist.go) mirrors this.
    fn set_agents(&mut self, mut agents: Vec<Agent>) {
        agents.sort_by_cached_key(|a| {
            let secs = a.ordered_at_secs();
            (!a.needs_you(), secs.is_none(), secs)
        });
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
        self.notice = None;
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
            Key::Esc | Key::Char('q') => Some(Effect::Dismiss),
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

fn failure_message(exit_code: Option<i32>, err: &str) -> String {
    if !err.is_empty() {
        return err.to_string();
    }

    match exit_code {
        Some(code) => format!("exit {code}"),
        None => "exit unknown".to_string(),
    }
}

#[cfg(test)]
pub(crate) mod tests {
    use super::*;

    pub(crate) fn feed_json(ids: &[&str]) -> String {
        let items: Vec<String> = ids
            .iter()
            .map(|id| format!(r#"{{"id":"{id}","dir":"{id}"}}"#))
            .collect();
        format!(r#"{{"version":1,"agents":[{}]}}"#, items.join(","))
    }

    fn app_with(ids: &[&str]) -> App {
        let mut app = App::new();
        app.apply_list_result(Some(0), feed_json(ids).as_bytes(), b"");
        app
    }

    fn ids(app: &App) -> Vec<&str> {
        app.agents().iter().map(|a| a.id.as_str()).collect()
    }

    fn selected(app: &App) -> Option<&str> {
        app.selected.as_deref()
    }

    #[test]
    fn starts_loading_without_selection() {
        let app = App::new();
        assert_eq!(app.status(), &Status::Loading);
        assert_eq!(selected(&app), None);
    }

    #[test]
    fn successful_list_is_ready_and_selects_first() {
        let app = app_with(&["/a", "/b"]);
        assert_eq!(app.status(), &Status::Ready);
        assert_eq!(selected(&app), Some("/a"));
    }

    #[test]
    fn orders_oldest_first_and_unknown_last() {
        let json = r#"{"version":1,"agents":[
            {"id":"/unknown-1"},
            {"id":"/newest","ordered_at":"2026-01-01T12:00:00Z"},
            {"id":"/unknown-2","ordered_at":"garbage"},
            {"id":"/oldest","ordered_at":"2026-01-01T13:00:00+02:00"},
            {"id":"/middle","ordered_at":"2026-01-01T11:30:00Z","last_activity":"2026-01-01T13:00:00Z"}
        ]}"#;
        let mut app = App::new();
        app.apply_list_result(Some(0), json.as_bytes(), b"");
        assert_eq!(
            ids(&app),
            vec!["/oldest", "/middle", "/newest", "/unknown-1", "/unknown-2"]
        );
    }

    #[test]
    fn needs_you_agents_lead_oldest_first() {
        let json = r#"{"version":1,"agents":[
            {"id":"/old-idle","group":"idle","ordered_at":"2026-01-01T08:00:00Z"},
            {"id":"/new-ask","group":"needs_you","ordered_at":"2026-01-01T12:00:00Z"},
            {"id":"/old-ask","group":"needs_you","ordered_at":"2026-01-01T09:00:00Z"}
        ]}"#;
        let mut app = App::new();
        app.apply_list_result(Some(0), json.as_bytes(), b"");
        assert_eq!(ids(&app), vec!["/old-ask", "/new-ask", "/old-idle"]);
    }

    #[test]
    fn selection_movement_clamps_at_both_ends() {
        let mut app = app_with(&["/a", "/b"]);
        assert_eq!(app.handle_key(Key::Up), None);
        assert_eq!(selected(&app), Some("/a"));
        app.handle_key(Key::Char('j'));
        assert_eq!(selected(&app), Some("/b"));
        app.handle_key(Key::Down);
        assert_eq!(selected(&app), Some("/b"));
        app.handle_key(Key::Char('k'));
        assert_eq!(selected(&app), Some("/a"));
    }

    #[test]
    fn selection_follows_id_across_refresh() {
        let mut app = app_with(&["/a", "/b"]);
        app.handle_key(Key::Down);
        app.apply_list_result(Some(0), feed_json(&["/b", "/a"]).as_bytes(), b"");
        assert_eq!(selected(&app), Some("/b"));
        assert_eq!(app.selected_index(), Some(0));
    }

    #[test]
    fn removed_selection_clamps_to_same_index() {
        let mut app = app_with(&["/a", "/b", "/c"]);
        app.handle_key(Key::Down);
        app.apply_list_result(Some(0), feed_json(&["/a", "/c"]).as_bytes(), b"");
        assert_eq!(selected(&app), Some("/c"));
    }

    #[test]
    fn removed_selection_clamps_to_last_row() {
        let mut app = app_with(&["/a", "/b", "/c"]);
        app.handle_key(Key::Down);
        app.handle_key(Key::Down);
        app.apply_list_result(Some(0), feed_json(&["/a"]).as_bytes(), b"");
        assert_eq!(selected(&app), Some("/a"));
    }

    #[test]
    fn empty_list_clears_selection() {
        let mut app = app_with(&["/a"]);
        app.apply_list_result(Some(0), feed_json(&[]).as_bytes(), b"");
        assert_eq!(selected(&app), None);
        assert_eq!(app.handle_key(Key::Enter), None);
        assert_eq!(app.handle_key(Key::Down), None);
    }

    #[test]
    fn enter_yields_focus_with_selected_id() {
        let mut app = app_with(&["/a", "/b"]);
        app.handle_key(Key::Down);
        assert_eq!(
            app.handle_key(Key::Enter),
            Some(Effect::Focus {
                agent_id: "/b".into()
            })
        );
    }

    #[test]
    fn refresh_and_dismiss_keys_yield_effects() {
        let mut app = App::new();
        assert_eq!(app.handle_key(Key::Char('r')), Some(Effect::Refresh));
        assert_eq!(app.handle_key(Key::Char('q')), Some(Effect::Dismiss));
        assert_eq!(app.handle_key(Key::Esc), Some(Effect::Dismiss));
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
        let mut app = app_with(&["/a"]);
        app.apply_list_result(Some(1), b"", b"boom");
        assert_eq!(ids(&app), vec!["/a"]);
        assert_eq!(selected(&app), Some("/a"));
    }

    #[test]
    fn failed_focus_sets_notice_and_keeps_list() {
        let mut app = app_with(&["/a"]);
        assert!(!app.apply_focus_result(Some(1), b"no such agent\n"));
        assert_eq!(app.notice(), Some("no such agent"));
        assert_eq!(app.status(), &Status::Ready);
        assert_eq!(ids(&app), vec!["/a"]);
    }

    #[test]
    fn failed_focus_without_stderr_reports_exit_code() {
        let mut app = App::new();
        assert!(!app.apply_focus_result(None, b""));
        assert_eq!(app.notice(), Some("exit unknown"));
    }

    #[test]
    fn successful_focus_leaves_notice_untouched() {
        let mut app = App::new();
        assert!(app.apply_focus_result(Some(0), b""));
        assert_eq!(app.notice(), None);
    }

    #[test]
    fn any_key_clears_notice() {
        let mut app = app_with(&["/a"]);
        app.set_notice("boom");
        app.handle_key(Key::Char('x'));
        assert_eq!(app.notice(), None);
    }

    fn located_app() -> App {
        let mut app = App::new();
        let json = r#"{"version":1,"agents":[
            {"id":"/shared-a","mux_session":"main","window_id":"main:1","session_owned":false},
            {"id":"/shared-b","mux_session":"main","window_id":"main:2","session_owned":false},
            {"id":"/own","mux_session":"wt","window_id":"wt:0","session_owned":true}
        ]}"#;
        app.apply_list_result(Some(0), json.as_bytes(), b"");
        app
    }

    fn at(session: &str, window: Option<&str>) -> Location {
        Location {
            session: session.into(),
            window: window.map(Into::into),
        }
    }

    #[test]
    fn nothing_is_here_until_location_is_known() {
        assert_eq!(located_app().here_index(), None);
    }

    #[test]
    fn here_is_the_agent_on_this_tab() {
        let mut app = located_app();
        app.set_location(at("main", Some("main:2")));
        assert_eq!(app.here_index(), Some(1));
    }

    #[test]
    fn any_tab_of_an_owned_session_is_at_its_agent() {
        let mut app = located_app();
        app.set_location(at("wt", Some("wt:7")));
        assert_eq!(app.here_index(), Some(2));

        app.set_location(at("wt", None));
        assert_eq!(app.here_index(), Some(2));
    }

    #[test]
    fn agent_free_tab_of_a_shared_session_is_nowhere() {
        let mut app = located_app();
        app.set_location(at("main", Some("main:9")));
        assert_eq!(app.here_index(), None);
    }

    #[test]
    fn set_location_reports_only_changes() {
        let mut app = App::new();
        assert!(app.set_location(at("s", None)));
        assert!(!app.set_location(at("s", None)));
        assert!(app.set_location(at("s", Some("s:1"))));
    }
}
