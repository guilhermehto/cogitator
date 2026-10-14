# CLI & logging

## Flags

| Flag          | Effect                                                                                                                                                                 |
| ------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `--status`    | print a one-shot icons-only status line and exit                                                                                                                          |
| `--demo`      | launch the TUI with a curated synthetic snapshot (mixed session states, tasks, a running task). No mDNS, no `task` shell-outs; intended for screenshots and walkthroughs |
| `--debug`     | show diagnostic UI elements that are noisy during normal use (e.g. the unreachable-instance footer)                                                                       |
| `--log-level` | set log verbosity (`debug\|info\|warn\|error`). Default is `info`                                                                                                         |
| `--version`   | print module version, commit, and build date                                                                                                                              |

## Status mode

`--status` runs discovery/supervision without opening the TUI and prints a compact status
line. It exits when either:

- a non-empty snapshot arrives, or
- the status deadline is reached (default: 3s).

When the cogitator TUI is running, `--status` reads its live feed instead of running
discovery itself; otherwise it falls back to mDNS discovery as above.

## Subcommands

These drive the [tmux integration](/guide/tmux) and talk to the running TUI:

- `cogitator pick [--client X]`: open the agent picker (meant for a tmux popup).
- `cogitator next [--client X --window W --session S]`: jump to the next agent that needs
  you. Always exits 0 and prints nothing, reporting through a tmux message; exits 2 only on
  usage errors.

- `cogitator tmux install [--next-key KEY] [--pick-key KEY] [--config PATH] [--binary PATH] [--dry-run] [--no-reload] [--force]`:
  write the key bindings to a cogitator-owned file and add one `source-file -q` line to your
  tmux config. Defaults: `--next-key Space`, `--pick-key a`.
- `cogitator tmux uninstall [--config PATH] [--dry-run] [--no-reload]`: undo the install.
- `cogitator zellij install [--next-key KEY] [--pick-key KEY] [--plugin WASM] [--sidebar-key KEY] [--config PATH] [--binary PATH] [--dry-run] [--force]`:
  add the next/pick keybinds to a cogitator-owned block in your zellij `config.kdl`. With
  `--plugin`, also bind the sidebar key and dock the plugin in new sessions through a
  cogitator-owned layout.
- `cogitator zellij uninstall [--config PATH] [--dry-run]`: remove the owned blocks and layout.

The installers never set anything up twice. If cogitator's own wiring is already current,
a re-run prints `already set up` and writes nothing. If it points at an older binary,
plugin, or keys, the re-run updates it in place. If you wired cogitator yourself (a
hand-written bind, a layout that loads the plugin, an edited omp extension, a hook that runs
a wrapper script), the installer names the file and line, leaves it alone, and exits
non-zero. Pass `--force` to install anyway.

See [tmux integration](/guide/tmux#setup) for details. All `pick` and `next` flags are optional and fall back to tmux's current client, window, and session.

## Hook subcommands

These are wired up during [agent setup](/guide/connect) and are not meant to be run by hand:

- `cogitator claude-hook`: receives Claude Code lifecycle events on stdin;
  `cogitator claude-hook install [--binary PATH] [--force]` merges the hooks into
  `~/.claude/settings.json` (`$CLAUDE_HOME/settings.json` when set).
- `cogitator codex-hook`: receives Codex lifecycle events on stdin.
- `cogitator omp-hook`: receives omp lifecycle events; `cogitator omp-hook install [--force]`
  writes the omp extension to `~/.omp/agent/extensions/cogitator.ts`.

All hook subcommands exit 0 silently when cogitator is not running. A closed monitor is the
expected case, not a failure, so your agent is never blocked.

Each hook also records the multiplexer pane it ran in (`$ZELLIJ_PANE_ID` or `$TMUX_PANE`).
That is how several sessions in one directory each get their own Agents row on their own
tab. A hook still pointing at an older cogitator binary cannot record its pane, so rebuild
or reinstall after upgrading.

The Agents list shows only agents in the multiplexer cogitator runs in. A session whose
recorded pane is in another multiplexer, or has closed, is left out. So is a session with no
recorded pane yet when every pane in its directory already has an agent.

## Logging

Logs are written with `log/slog` text formatting.

- If `$XDG_STATE_HOME` is set: `$XDG_STATE_HOME/cogitator/cogitator.log`
- Otherwise: `/tmp/cogitator.log`
