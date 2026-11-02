use crate::feed::{self, Agent, ParseError};
use crate::fuzzy;
use std::cell::Cell;

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Status {
    Loading,
    Ready,
    NotRunning(String),
    Failed(String),
    PermissionDenied,
    Outdated,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Key {
    Char(char),
    Up,
    Down,
    Enter,
    Esc,
    Backspace,
    Ctrl(char),
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Effect {
    Focus { agent_id: String },
    Refresh,
    Dismiss,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum SearchKind {
    /// `/` narrows the list in place, keeping its sections, as the TUI's
    /// Agents filter does.
    Filter,
}

#[derive(Debug, Clone, PartialEq, Eq)]
struct Search {
    kind: SearchKind,
    query: String,
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
    focused: bool,
    here: Option<Location>,
    g_pending: bool,
    search: Option<Search>,
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
            focused: false,
            here: None,
            g_pending: false,
            search: None,
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

    pub fn search(&self) -> Option<(SearchKind, &str)> {
        self.search.as_ref().map(|s| (s.kind, s.query.as_str()))
    }

    /// The agents the list shows, as indices into agents in the order shown:
    /// every agent, or the ones the open search matches.
    pub fn visible(&self) -> Vec<usize> {
        let Some(search) = &self.search else {
            return (0..self.agents.len()).collect();
        };

        let labels: Vec<String> = self.agents.iter().map(|a| a.name(&self.home)).collect();
        let mut matches = fuzzy::rank(&search.query, &labels);
        if search.kind == SearchKind::Filter {
            matches.sort_unstable();
        }
        matches
    }

    pub fn floating(&self) -> bool {
        self.floating
    }

    pub fn set_floating(&mut self, floating: bool) {
        self.floating = floating;
    }

    pub fn focused(&self) -> bool {
        self.focused
    }

    /// Reports whether focus changed, so the caller knows to redraw.
    pub fn set_focused(&mut self, focused: bool) -> bool {
        if self.focused == focused {
            return false;
        }

        self.focused = focused;
        if !focused {
            self.close_search();
        }
        self.rest_cursor();
        true
    }

    /// Reports whether the location changed, so the caller knows to redraw.
    pub fn set_location(&mut self, here: Location) -> bool {
        if self.here.as_ref() == Some(&here) {
            return false;
        }

        self.here = Some(here);
        self.rest_cursor();
        true
    }

    /// While the plugin is unfocused the cursor rests on the agent the user
    /// sits at, so focusing the plugin starts from there whatever the user
    /// moved to last time.
    fn rest_cursor(&mut self) {
        if self.focused {
            return;
        }
        if let Some(i) = self.here_index() {
            self.selected = Some(self.agents[i].id.clone());
        }
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
                Err(ParseError::NewerVersion(_)) => {
                    self.status = Status::Outdated;
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

    /// Agents run section by section, each oldest ordered_at first, so
    /// whoever has waited longest sits on top and a working agent keeps its
    /// place until it stops. Agents without a timestamp close their section
    /// in cogitator's order.
    /// The TUI's agentList.SetAgents (internal/ui/agentlist.go) mirrors this.
    fn set_agents(&mut self, mut agents: Vec<Agent>) {
        agents.sort_by_cached_key(|a| {
            let secs = a.ordered_at_secs();
            (a.section(), secs.is_none(), secs)
        });
        let prev_index = self.selected_index();
        let still_present = self
            .selected
            .as_deref()
            .is_some_and(|id| agents.iter().any(|a| a.id == id));

        self.agents = agents;

        if !still_present {
            let idx = prev_index
                .unwrap_or(0)
                .min(self.agents.len().saturating_sub(1));
            self.selected = self.agents.get(idx).map(|a| a.id.clone());
        }
        self.settle_cursor();
        self.rest_cursor();
    }

    pub fn handle_key(&mut self, key: Key) -> Option<Effect> {
        // Keys reach the plugin only while it has focus.
        self.set_focused(true);
        self.notice = None;
        if self.search.is_some() {
            return self.handle_search_key(key);
        }

        let g_pending = std::mem::take(&mut self.g_pending);
        match key {
            Key::Down | Key::Char('j') => {
                self.move_selection(1);
                None
            }
            Key::Up | Key::Char('k') => {
                self.move_selection(-1);
                None
            }
            Key::Char('G') => {
                self.select(self.visible().last().copied());
                None
            }
            Key::Char('g') => {
                if g_pending {
                    self.select(self.visible().first().copied());
                } else {
                    self.g_pending = true;
                }
                None
            }
            Key::Char('/') => {
                self.open_search(SearchKind::Filter);
                None
            }
            Key::Enter => self.focus_selected(),
            Key::Char('r') => Some(Effect::Refresh),
            Key::Esc | Key::Char('q') => Some(Effect::Dismiss),
            Key::Char(_) | Key::Ctrl(_) | Key::Backspace => None,
        }
    }

    /// While a search is open, printable keys edit its query, so only keys
    /// that type nothing move the cursor or close the search.
    fn handle_search_key(&mut self, key: Key) -> Option<Effect> {
        match key {
            Key::Enter => return self.focus_selected(),
            Key::Esc => self.close_search(),
            Key::Down | Key::Ctrl('n') => self.move_selection(1),
            Key::Up | Key::Ctrl('p') => self.move_selection(-1),
            Key::Backspace => self.edit_query(|query| {
                query.pop();
            }),
            Key::Char(c) => self.edit_query(|query| query.push(c)),
            Key::Ctrl(_) => {}
        }
        None
    }

    fn open_search(&mut self, kind: SearchKind) {
        self.search = Some(Search {
            kind,
            query: String::new(),
        });
    }

    fn close_search(&mut self) {
        if self.search.take().is_some() {
            self.settle_cursor();
        }
    }

    fn edit_query(&mut self, edit: impl FnOnce(&mut String)) {
        if let Some(search) = self.search.as_mut() {
            edit(&mut search.query);
            self.settle_cursor();
        }
    }

    fn focus_selected(&self) -> Option<Effect> {
        self.selected
            .clone()
            .map(|agent_id| Effect::Focus { agent_id })
    }

    fn move_selection(&mut self, delta: isize) {
        let visible = self.visible();
        if visible.is_empty() {
            return;
        }

        let cur = self.selected_position(&visible).unwrap_or(0) as isize;
        let max = visible.len() as isize - 1;
        let next = (cur + delta).clamp(0, max) as usize;
        self.select(Some(visible[next]));
    }

    /// Keeps the cursor on its agent while the list shows it, else moves it
    /// to the first agent shown, so keys never act on a hidden agent.
    fn settle_cursor(&mut self) {
        let visible = self.visible();
        if self.selected_position(&visible).is_none() {
            self.select(visible.first().copied());
        }
    }

    fn selected_position(&self, visible: &[usize]) -> Option<usize> {
        let selected = self.selected_index()?;
        visible.iter().position(|&i| i == selected)
    }

    fn select(&mut self, index: Option<usize>) {
        self.selected = index.map(|i| self.agents[i].id.clone());
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
    fn orders_working_then_done_then_idle_and_stopped() {
        let json = r#"{"version":1,"agents":[
            {"id":"/stopped","group":"stopped","ordered_at":"2026-01-01T07:00:00Z"},
            {"id":"/idle","group":"idle","ordered_at":"2026-01-01T08:00:00Z"},
            {"id":"/done","group":"done","ordered_at":"2026-01-01T09:00:00Z"},
            {"id":"/working","group":"working","ordered_at":"2026-01-01T10:00:00Z"}
        ]}"#;
        let mut app = App::new();
        app.apply_list_result(Some(0), json.as_bytes(), b"");
        assert_eq!(ids(&app), vec!["/working", "/done", "/stopped", "/idle"]);
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
    fn capital_g_jumps_to_the_last_agent_and_gg_to_the_first() {
        let mut app = app_with(&["/a", "/b", "/c"]);
        app.handle_key(Key::Char('G'));
        assert_eq!(selected(&app), Some("/c"));

        app.handle_key(Key::Char('g'));
        assert_eq!(selected(&app), Some("/c"));
        app.handle_key(Key::Char('g'));
        assert_eq!(selected(&app), Some("/a"));
    }

    #[test]
    fn another_key_between_the_gs_cancels_the_jump() {
        let mut app = app_with(&["/a", "/b", "/c"]);
        app.handle_key(Key::Char('G'));
        app.handle_key(Key::Char('g'));
        app.handle_key(Key::Char('k'));
        app.handle_key(Key::Char('g'));
        assert_eq!(selected(&app), Some("/b"));
    }

    fn type_keys(app: &mut App, keys: &str) {
        for c in keys.chars() {
            app.handle_key(Key::Char(c));
        }
    }

    fn visible_ids(app: &App) -> Vec<&str> {
        app.visible()
            .into_iter()
            .map(|i| app.agents()[i].id.as_str())
            .collect()
    }

    #[test]
    fn filter_shows_matching_agents_in_list_order() {
        let mut app = app_with(&["/alps", "/beta", "/alpha"]);
        type_keys(&mut app, "/al");
        assert_eq!(app.search(), Some((SearchKind::Filter, "al")));
        assert_eq!(visible_ids(&app), ["/alps", "/alpha"]);
    }

    #[test]
    fn filter_takes_list_keys_as_query_text() {
        let mut app = app_with(&["/a"]);
        app.handle_key(Key::Char('/'));
        for c in "qjkgGr/".chars() {
            assert_eq!(app.handle_key(Key::Char(c)), None);
        }
        assert_eq!(app.search(), Some((SearchKind::Filter, "qjkgGr/")));
    }

    #[test]
    fn filter_moves_only_through_matches() {
        let mut app = app_with(&["/alps", "/beta", "/alpha"]);
        type_keys(&mut app, "/al");
        app.handle_key(Key::Down);
        assert_eq!(selected(&app), Some("/alpha"));
        app.handle_key(Key::Ctrl('n'));
        assert_eq!(selected(&app), Some("/alpha"));
        app.handle_key(Key::Ctrl('p'));
        assert_eq!(selected(&app), Some("/alps"));
    }

    #[test]
    fn typing_keeps_a_matching_cursor_and_moves_a_hidden_one_to_the_first_match() {
        let mut app = app_with(&["/alps", "/beta", "/alpha"]);
        app.handle_key(Key::Char('G'));
        type_keys(&mut app, "/al");
        assert_eq!(selected(&app), Some("/alpha"));

        type_keys(&mut app, "ps");
        assert_eq!(selected(&app), Some("/alps"));
    }

    #[test]
    fn backspace_widens_the_filter() {
        let mut app = app_with(&["/alps", "/beta"]);
        type_keys(&mut app, "/alp");
        app.handle_key(Key::Backspace);
        app.handle_key(Key::Backspace);
        assert_eq!(app.search(), Some((SearchKind::Filter, "a")));
        assert_eq!(visible_ids(&app), ["/alps", "/beta"]);
    }

    #[test]
    fn esc_clears_the_filter_and_keeps_the_cursor() {
        let mut app = app_with(&["/alps", "/beta", "/alpha"]);
        type_keys(&mut app, "/alpha");
        assert_eq!(app.handle_key(Key::Esc), None);
        assert_eq!(app.search(), None);
        assert_eq!(selected(&app), Some("/alpha"));
        assert_eq!(visible_ids(&app).len(), 3);
    }

    #[test]
    fn enter_focuses_the_selected_match() {
        let mut app = app_with(&["/alps", "/beta"]);
        type_keys(&mut app, "/be");
        assert_eq!(
            app.handle_key(Key::Enter),
            Some(Effect::Focus {
                agent_id: "/beta".into()
            })
        );
    }

    #[test]
    fn filter_without_matches_has_nothing_to_focus() {
        let mut app = app_with(&["/alps"]);
        type_keys(&mut app, "/zz");
        assert_eq!(selected(&app), None);
        assert_eq!(app.handle_key(Key::Enter), None);

        app.handle_key(Key::Esc);
        assert_eq!(selected(&app), Some("/alps"));
    }

    #[test]
    fn refresh_keeps_the_filter_and_moves_a_cursor_whose_agent_left() {
        let mut app = app_with(&["/alps", "/beta", "/alpha"]);
        type_keys(&mut app, "/al");
        app.handle_key(Key::Down);
        app.apply_list_result(Some(0), feed_json(&["/alps", "/beta"]).as_bytes(), b"");
        assert_eq!(app.search(), Some((SearchKind::Filter, "al")));
        assert_eq!(selected(&app), Some("/alps"));
    }

    #[test]
    fn leaving_the_plugin_closes_the_filter() {
        let mut app = app_with(&["/alps", "/beta"]);
        type_keys(&mut app, "/be");
        app.set_focused(false);
        assert_eq!(app.search(), None);
        assert_eq!(visible_ids(&app).len(), 2);
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
    fn newer_feed_version_is_outdated() {
        let mut app = App::new();
        let json = format!(r#"{{"version":{},"agents":[]}}"#, feed::VERSION + 1);
        app.apply_list_result(Some(0), json.as_bytes(), b"");
        assert_eq!(app.status(), &Status::Outdated);
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

    fn located_feed() -> &'static str {
        r#"{"version":1,"agents":[
            {"id":"/shared-a","mux_session":"main","window_id":"main:1","session_owned":false},
            {"id":"/shared-b","mux_session":"main","window_id":"main:2","session_owned":false},
            {"id":"/own","mux_session":"wt","window_id":"wt:0","session_owned":true}
        ]}"#
    }

    fn located_app() -> App {
        let mut app = App::new();
        app.apply_list_result(Some(0), located_feed().as_bytes(), b"");
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

    #[test]
    fn set_focused_reports_only_changes() {
        let mut app = App::new();
        assert!(!app.set_focused(false));
        assert!(app.set_focused(true));
        assert!(!app.set_focused(true));
    }

    #[test]
    fn unfocused_cursor_rests_on_the_agent_here_as_the_user_moves() {
        let mut app = located_app();
        app.set_location(at("main", Some("main:2")));
        assert_eq!(selected(&app), Some("/shared-b"));

        app.set_location(at("wt", Some("wt:0")));
        assert_eq!(selected(&app), Some("/own"));
    }

    #[test]
    fn unfocused_cursor_stays_put_when_no_agent_is_here() {
        let mut app = located_app();
        app.set_location(at("main", Some("main:2")));
        app.set_location(at("main", Some("main:9")));
        assert_eq!(selected(&app), Some("/shared-b"));
    }

    #[test]
    fn leaving_the_plugin_returns_the_cursor_to_the_agent_here() {
        let mut app = located_app();
        app.set_location(at("main", Some("main:2")));
        app.set_focused(true);
        app.handle_key(Key::Down);
        assert_eq!(selected(&app), Some("/own"));

        app.set_focused(false);
        assert_eq!(selected(&app), Some("/shared-b"));
        app.set_focused(true);
        assert_eq!(selected(&app), Some("/shared-b"));
    }

    #[test]
    fn focused_cursor_keeps_its_agent_across_refresh_and_moves() {
        let mut app = located_app();
        app.set_location(at("main", Some("main:2")));
        app.handle_key(Key::Up);
        assert!(app.focused());

        app.set_location(at("wt", Some("wt:0")));
        app.apply_list_result(Some(0), located_feed().as_bytes(), b"");
        assert_eq!(selected(&app), Some("/shared-a"));
    }
}
