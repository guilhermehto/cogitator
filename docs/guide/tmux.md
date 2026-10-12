# tmux integration

Jump to whatever needs you from any tmux window, and see agent state in your status line and
window list. cogitator publishes the state; you wire it into your own `tmux.conf`. cogitator
never edits your tmux config, bindings, or formats.

## Requirements

- cogitator's TUI running in its own tmux window. `next` and `pick` talk to the running TUI;
  with no TUI running they do nothing (`next` shows "cogitator isn't running").
- tmux 3.3 or newer for the `pick` popup (`display-popup -b`/`-T`).

## Setup

Add this block to `~/.tmux.conf`, then reload with `tmux source-file ~/.tmux.conf`:

```bash
# prefix + Space: jump to the next agent that needs you
bind Space run-shell -b 'cogitator next --client "#{client_name}" --window "#{window_id}" --session "#{session_id}"'

# prefix + a: popup list of all agents
bind a display-popup -E -w 70% -h 60% -b rounded -T ' agents ' "cogitator pick --client '#{client_name}'"

# status line counts (each part hides when its count is zero)
set -g status-right "#{?@cog_needs,#[fg=red]! #{@cog_needs} ,}#{?@cog_done,#[fg=green]✓ #{@cog_done} ,}#{?@cog_working,#[fg=yellow]● #{@cog_working} ,}#[default]%H:%M"

# window badge: appended to your existing format, not replacing it
set -ga window-status-format "#{?@cog_status, [#{@cog_status}],}"
set -ga window-status-current-format "#{?@cog_status, [#{@cog_status}],}"
```

`set -ga` appends to whatever format is already set, so put these lines after any
`window-status-format` you define yourself. Adjust `status-right` to taste; keep your own
segments alongside the cogitator ones.

## next: jump to what needs you

`prefix` + `Space` jumps to the most urgent agent. Order:

1. needs you: permission or question requests first, then errored agents
2. done: oldest finished first

Repeated presses cycle through the list. `next` always exits 0 and prints nothing; when
there is nothing to do it says so with a tmux message ("cogitator: nothing needs you",
"cogitator isn't running").

## pick: popup list

`prefix` + `a` opens a popup listing every agent.

| Key               | Action                                                              |
| ----------------- | ------------------------------------------------------------------- |
| `j` / `k`         | move down / up                                                      |
| `gg` / `G`        | first / last                                                        |
| `/`               | filter                                                              |
| `Enter`           | jump to the agent (a stopped agent is resumed, like `Enter` in the TUI) |
| `x`               | mark seen (clears a done badge)                                     |
| `Esc` / `q`       | close                                                               |

Agents that stopped are listed for 30 minutes after they stop.

## Going back

cogitator has no "back" command; use tmux built-ins, bound to whatever key you like:

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

The options refresh about every 2 seconds. A done badge clears once you are looking at the
agent (its window is active in your client, or your session belongs only to that agent) and
you have typed since it finished.

See [CLI & logging](/reference/cli#subcommands) for the `pick` and `next` flags.
