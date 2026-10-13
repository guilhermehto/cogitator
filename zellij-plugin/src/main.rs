mod app;
mod feed;
mod render;

use app::{App, Effect, Key};
use std::collections::BTreeMap;
use std::time::{SystemTime, UNIX_EPOCH};
use zellij_tile::prelude::*;

const CALL_KEY: &str = "cogitator_call";
const LIST_CALL: &str = "list";
const FOCUS_CALL: &str = "focus";

struct State {
    app: App,
    cogitator: String,
    refresh_secs: f64,
    granted: bool,
    list_in_flight: bool,
    session_name: Option<String>,
    plugin_id: u32,
    floating: bool,
}

impl Default for State {
    fn default() -> Self {
        State {
            app: App::new(),
            cogitator: "cogitator".to_string(),
            refresh_secs: 2.0,
            granted: false,
            list_in_flight: false,
            session_name: None,
            plugin_id: 0,
            floating: false,
        }
    }
}

register_plugin!(State);

impl ZellijPlugin for State {
    fn load(&mut self, configuration: BTreeMap<String, String>) {
        if let Some(bin) = configuration.get("cogitator").filter(|b| !b.is_empty()) {
            self.cogitator = bin.clone();
        }
        if let Some(secs) = configuration
            .get("refresh_secs")
            .and_then(|s| s.parse::<f64>().ok())
            .filter(|s| *s > 0.0)
        {
            self.refresh_secs = secs;
        }
        self.plugin_id = get_plugin_ids().plugin_id;

        set_selectable(true);
        request_permission(&[
            PermissionType::ReadApplicationState,
            PermissionType::ChangeApplicationState,
            PermissionType::RunCommands,
        ]);
        subscribe(&[
            EventType::PermissionRequestResult,
            EventType::RunCommandResult,
            EventType::Timer,
            EventType::Key,
            EventType::ModeUpdate,
            EventType::PaneUpdate,
        ]);
    }

    fn update(&mut self, event: Event) -> bool {
        match event {
            Event::PermissionRequestResult(PermissionStatus::Granted) => {
                if self.granted {
                    return false;
                }
                self.granted = true;
                rename_plugin_pane(self.plugin_id, "cogitator");
                self.list();
                set_timeout(self.refresh_secs);
                false
            }
            Event::PermissionRequestResult(PermissionStatus::Denied) => {
                self.app.set_permission_denied();
                true
            }
            Event::Timer(_) => {
                self.list();
                set_timeout(self.refresh_secs);
                false
            }
            Event::RunCommandResult(exit_code, stdout, stderr, context) => {
                self.on_command_result(exit_code, &stdout, &stderr, &context)
            }
            Event::ModeUpdate(mode) => {
                if mode.session_name.is_some() {
                    self.session_name = mode.session_name;
                }
                false
            }
            Event::PaneUpdate(manifest) => {
                if let Some(pane) = manifest
                    .panes
                    .values()
                    .flatten()
                    .find(|p| p.is_plugin && p.id == self.plugin_id)
                {
                    self.floating = pane.is_floating;
                }
                false
            }
            Event::Key(key) => match map_key(&key) {
                Some(key) => {
                    let effect = self.app.handle_key(key);
                    self.apply(effect);
                    true
                }
                None => false,
            },
            _ => false,
        }
    }

    fn render(&mut self, rows: usize, cols: usize) {
        let now = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .map_or(0, |d| d.as_secs() as i64);
        print!("{}", render::render(&self.app, rows, cols, now).join("\n"));
    }
}

impl State {
    fn list(&mut self) {
        if self.list_in_flight {
            return;
        }

        self.list_in_flight = true;
        run_command(&[&self.cogitator, "agents", "list"], call(LIST_CALL));
    }

    fn on_command_result(
        &mut self,
        exit_code: Option<i32>,
        stdout: &[u8],
        stderr: &[u8],
        context: &BTreeMap<String, String>,
    ) -> bool {
        match context.get(CALL_KEY).map(String::as_str) {
            Some(LIST_CALL) => {
                self.list_in_flight = false;
                self.app.apply_list_result(exit_code, stdout, stderr);
                true
            }
            Some(FOCUS_CALL) => {
                if self.app.apply_focus_result(exit_code, stderr) && self.floating {
                    hide_self();
                }
                true
            }
            _ => false,
        }
    }

    fn apply(&mut self, effect: Option<Effect>) {
        match effect {
            Some(Effect::Focus { agent_id }) => self.focus(&agent_id),
            Some(Effect::Refresh) => self.list(),
            Some(Effect::Hide) => hide_self(),
            None => {}
        }
    }

    fn focus(&mut self, agent_id: &str) {
        let Some(session) = self.session_name.as_deref() else {
            self.app.set_notice("zellij session not known yet");
            return;
        };

        run_command(
            &[
                &self.cogitator,
                "agents",
                "focus",
                "--client",
                session,
                agent_id,
            ],
            call(FOCUS_CALL),
        );
    }
}

fn call(kind: &str) -> BTreeMap<String, String> {
    BTreeMap::from([(CALL_KEY.to_string(), kind.to_string())])
}

fn map_key(key: &KeyWithModifier) -> Option<Key> {
    if !key.has_no_modifiers() {
        return None;
    }

    match key.bare_key {
        BareKey::Char(c) => Some(Key::Char(c)),
        BareKey::Up => Some(Key::Up),
        BareKey::Down => Some(Key::Down),
        BareKey::Enter => Some(Key::Enter),
        BareKey::Esc => Some(Key::Esc),
        _ => None,
    }
}
