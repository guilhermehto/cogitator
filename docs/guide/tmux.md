# tmux integration

Jump to whatever needs you from any tmux window, and see agent state in your status line and
window list. cogitator publishes the state. It edits your tmux config only when you run
`cogitator tmux install`, and never touches your themes or status and window formats.

## Requirements

- cogitator's TUI running in its own tmux window. `next` and `pick` talk to the running TUI;
  with no TUI running they do nothing (`next` shows "cogitator isn't running").
- tmux 3.3 or newer for the `pick` popup (`display-popup -b`/`-T`).

## Setup

```bash
cogitator tmux install
```

This writes a cogitator-owned file (`tmux.conf` in cogitator's config dir, typically
`~/.config/cogitator/tmux.conf`) with the two key bindings, and adds one
`source-file -q` line to your tmux config. It is idempotent, and the bindings are live
immediately in a running tmux server. The bindings use the absolute path of the cogitator
binary, so tmux's `PATH` does not matter.

Your tmux config is found in tmux's lookup order (`~/.tmux.conf`,
`$XDG_CONFIG_HOME/tmux/tmux.conf`, `~/.config/tmux/tmux.conf`); if none exists,
`~/.tmux.conf` is created. Symlinks are resolved, so dotfiles setups keep working.

| Flag               | Default             | Meaning                                                   |
| ------------------ | ------------------- | --------------------------------------------------------- |
| `--next-key KEY`   | `Space`             | prefix key that jumps to the next agent needing attention |
| `--pick-key KEY`   | `a`                 | prefix key that opens the agent picker                    |
| `--config PATH`    | tmux's lookup order | tmux config file to edit                                  |
| `--binary PATH`    | `PATH` lookup, then this executable | cogitator binary to bind                  |
| `--dry-run`        | off                 | print the changes without writing anything                |
| `--no-reload`      | off                 | do not touch the running tmux server                      |

Install warns when a key is already bound and still installs. Without `--no-reload` it
reloads into the running tmux server, or prints "no running tmux server" when there is none.

To remove everything:

```bash
cogitator tmux uninstall
```

It removes the `source-file` line and the owned file, and unbinds the keys in the running
server only if they still run cogitator. Pass the same `--config` if you used a custom one at
install; `--dry-run` and `--no-reload` work here too.

### Status line and window badges

The installer does not touch `status-right` or window formats. Add them yourself, merged into
your own formats. These use plain `set -g`, so reloading your config repeatedly does not
duplicate the badge. The window formats below are tmux's defaults plus the badge:

```bash
# status line counts (each part hides when its count is zero)
set -g status-right "#{?@cog_needs,#[fg=red]! #{@cog_needs} ,}#{?@cog_done,#[fg=green]✓ #{@cog_done} ,}#{?@cog_working,#[fg=yellow]● #{@cog_working} ,}#[default]%H:%M"

# window badge: tmux's default formats plus the badge
set -g window-status-format "#I:#W#{?window_flags,#{window_flags}, }#{?@cog_status, [#{@cog_status}],}"
set -g window-status-current-format "#I:#W#{?window_flags,#{window_flags}, }#{?@cog_status, [#{@cog_status}],}"
```

If you already customise these options, add `#{?@cog_status, [#{@cog_status}],}` to your own
format instead of copying the lines above, and keep your own `status-right` segments next to
the cogitator ones.

### Themes

Themes usually own the window formats, so a `set -g window-status-format` in your config may
be overwritten. Put `#{@cog_status}` in whatever window-title option the theme exposes. For
PowerKit, set these before `run tpm`:

```bash
set -g @powerkit_active_window_title '#W#{?@cog_status, [#{@cog_status}],}'
set -g @powerkit_inactive_window_title '#W#{?@cog_status, [#{@cog_status}],}'
```

### Manual bindings

To skip the installer, add the bindings to your tmux config yourself:

```bash
# prefix + Space: jump to the next agent that needs you
bind Space run-shell -b 'cogitator next --client "#{client_name}" --window "#{window_id}" --session "#{session_id}"'

# prefix + a: popup list of all agents
bind a display-popup -E -w 70% -h 60% -b rounded -T ' agents ' "cogitator pick --client '#{client_name}'"
```

## next: jump to what needs you

`prefix` + `Space` jumps to the most urgent agent. Order:

1. needs you: permission or question requests first, then errored agents
2. done: oldest finished first

Repeated presses cycle through the list. `next` always exits 0 and prints nothing; when
there is nothing to do it says so with a tmux message ("cogitator: nothing needs you",
"cogitator isn't running").

## pick: popup list

`prefix` + `a` opens a popup listing every agent with the same status icons as the TUI: a
"Needs you" section on top, then everyone else under "Agents", each oldest activity first so
whoever has waited longest leads. A working agent keeps its place until it stops working, so
its tool calls do not move it around the list. The TUI's Agents tab and the zellij sidebar list
them the same way. On a popup at least 100 columns wide, the right half shows a live preview of
the highlighted agent's pane: its status, harness, session title and the last lines of output,
refreshed every second. The output keeps the pane's text colours and attributes but drops
background colours, so zellij's painted pane background does not turn the preview into a solid
block. On zellij the colours come from `dump-screen --ansi`, which needs zellij 0.44.0 or later.

Rows lead with the name you gave the agent's window, e.g. `LEX-123 · ~/notes stray`, here, in the
TUI's Agents tab and in the zellij sidebar. Names tmux or zellij generated (automatic rename,
`Tab #N`) and the names cogitator gives windows it launches are skipped. Several agents sharing a
directory always get a label: the window name, else the session title, else the window number
(`tab 3`). Renames show up within a couple of seconds.

| Key               | Action                                                              |
| ----------------- | ------------------------------------------------------------------- |
| `j` / `k`         | move down / up                                                      |
| `gg` / `G`        | first / last                                                        |
| `/`               | filter                                                              |
| `Enter`           | jump to the agent (a stopped agent is resumed, like `Enter` in the TUI) |
| `x`               | mark seen (clears a done badge)                                     |
| `p`               | toggle the preview                                                  |
| `Esc` / `q`       | close                                                               |

A done (✓) agent stays done until you have seen it, even after its session goes quiet. Agents
with nothing waiting stay listed while they have a tmux window; without one they move to
stopped and are listed for 30 minutes.

## Going back

cogitator has no "back" command, and the installer adds no back key (many users already bind
`switch-client -l`). Use tmux built-ins, bound to whatever key you like:

- `session` launch mode (jumps cross sessions): `bind b switch-client -l`
- `window` launch mode (jumps stay in one session): `bind b last-window`

See [Configuration](/guide/configuration#tmux-window-vs-session) for launch modes.

## Published tmux options

cogitator owns these `@cog_*` user options and unsets them when it exits cleanly.

Per window:

| Option         | Values                                                                |
| -------------- | --------------------------------------------------------------------- |
| `@cog_status`  | `needs-you`, `errored`, `done`, `working`, `idle`; unset for stopped agents |
| `@cog_reason`  | `permission`, `question`, `errored`, `finished`                       |
| `@cog_since`   | unix seconds when the current state began                             |

Global (unset when zero): `@cog_needs`, `@cog_done`, `@cog_working`, counted per agent.

The options refresh about every 2 seconds. A done badge stays while you are looking at the
agent (its window is active in your client, or your session belongs only to that agent) and
clears once you switch away, so the agent drops to idle. If you prompt it instead, it moves to
working.

See [CLI & logging](/reference/cli#subcommands) for the `pick` and `next` flags.
