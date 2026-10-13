mod app;
mod feed;
mod render;

use std::collections::BTreeMap;
use zellij_tile::prelude::*;

#[derive(Default)]
struct State;

register_plugin!(State);

impl ZellijPlugin for State {
    fn load(&mut self, _configuration: BTreeMap<String, String>) {
        request_permission(&[
            PermissionType::ReadApplicationState,
            PermissionType::ChangeApplicationState,
            PermissionType::RunCommands,
        ]);
    }

    fn update(&mut self, _event: Event) -> bool {
        false
    }

    fn render(&mut self, _rows: usize, _cols: usize) {
        println!("cogitator");
    }
}
