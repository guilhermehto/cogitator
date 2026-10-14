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

## Setup

`cogitator setup [--yes] [--dry-run] [--check] [--binary PATH]` wires everything cogitator
supports on this machine with one binary: tmux keybinds, zellij keybinds and sidebar, and
the Claude Code, Codex, and omp hooks. It finds tmux and zellij on your `PATH` (warning when
zellij is not 0.45.x, the version the sidebar plugin is built for) and the harnesses you
have, then prints one plan:

| Found                         | Action                                            |
| ----------------------------- | ------------------------------------------------- |
| cogitator's wiring, current   | `already set up, skip`                            |
| cogitator's wiring, outdated  | `update: OLD -> NEW` (binary or plugin path)      |
| wiring you wrote by hand      | `yours, left alone`, with the `file:line`         |
| nothing                       | `install`                                         |
| the tool is not installed     | `not installed, skip`                             |

opencode is listed too, but needs no hook. It then asks once for tmux, once for zellij, and
once for the hooks (`y/N`, default no), only for the groups with something to install or
update, and offers each key with a default: the key cogitator's wiring already binds, else
the installer's default. A re-run therefore never reverts keys you chose. Hand-written
wiring is never touched: zellij keeps everything in one `config.kdl`, so a hand-written
zellij bind or sidebar skips the whole zellij install, while hooks skip only that
harness. It ends with the follow-ups that apply (start a new zellij session for the
sidebar, restart Claude Code and omp, trust the hook via `/hooks` in Codex, keep the TUI
running). On a machine that is already set up it prints `everything is already set up`
and writes nothing. It reloads the tmux server only when run inside tmux.

- `--yes`: accept the plan with the default keys, without prompting.
- `--dry-run`: print the plan and write nothing.
- `--check`: print the plan, write nothing, and exit non-zero if anything is missing or
  outdated. Use it as a health check.
- `--binary PATH`: the cogitator binary every component runs (default: `cogitator` on
  `PATH`, then the running executable).

A build without the bundled plugin (`go install`) lists the sidebar as `plugin not
bundled in this build` and sets up the keybinds alone.

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
- `cogitator zellij install [--next-key KEY] [--pick-key KEY] [--plugin WASM] [--no-sidebar] [--sidebar-key KEY] [--config PATH] [--binary PATH] [--dry-run] [--force]`:
  add the next/pick/sidebar keybinds to a cogitator-owned block in your zellij `config.kdl`
  and dock the sidebar plugin in new sessions through a cogitator-owned layout. Release
  binaries (`brew install guilhermehto/tap/cogitator`) carry the plugin, so
  `cogitator zellij install` is the whole setup, with no Rust toolchain: it writes the plugin
  to `~/.local/share/cogitator/cogitator-zellij-VERSION.wasm` (`$XDG_DATA_HOME/cogitator`
  when set). After upgrading cogitator, re-run it: new sessions load the new plugin, since
  zellij has never cached that file, and older copies are removed. A plugin older than the
  running cogitator says `cogitator plugin outdated, run cogitator zellij install`. A source
  build (`go install`) has no plugin inside: run `make plugin` in a checkout and pass
  `--plugin zellij-plugin/target/wasm32-wasip1/release/cogitator-zellij.wasm`, or pass
  `--no-sidebar` for the keybinds alone. When zellij rewrites `config.kdl` (its header reads
  `THIS FILE WAS AUTOGENERATED BY ZELLIJ`), it drops every comment, cogitator's block markers
  included. In such a file with no marker left, the installer still recognizes its own binds
  and a `default_layout` naming its own layout: `setup` reports them as outdated, a re-run of
  `install` replaces them, and `uninstall` removes them.
- `cogitator zellij uninstall [--config PATH] [--dry-run]`: remove the owned blocks, layout,
  and plugin copies.

The installers never set anything up twice. If cogitator's own wiring is already current,
a re-run prints `already set up` and writes nothing. If it points at an older binary,
plugin, or keys, the re-run updates it in place. If you wired cogitator yourself (a
hand-written bind, a layout that loads the plugin, an edited omp extension, a hook that runs
a wrapper script), the installer names the file and line, leaves it alone, and exits
non-zero. Pass `--force` to install anyway.

See [tmux integration](/guide/tmux#setup) for details. All `pick` and `next` flags are optional and fall back to tmux's current client, window, and session.

## Hook subcommands

The bare hook commands are wired up during [agent setup](/guide/connect) and are not meant to
be run by hand; the `install` forms do that wiring:

- `cogitator claude-hook`: receives Claude Code lifecycle events on stdin;
  `cogitator claude-hook install [--binary PATH] [--force]` merges the hooks into
  `~/.claude/settings.json` (`$CLAUDE_HOME/settings.json` when set).
- `cogitator codex-hook`: receives Codex lifecycle events on stdin;
  `cogitator codex-hook install [--binary PATH] [--force]` merges the hooks into
  `~/.codex/hooks.json` (`$CODEX_HOME/hooks.json` when set). Trust them via `/hooks` in Codex.
- `cogitator omp-hook`: receives omp lifecycle events; `cogitator omp-hook install [--force]`
  writes the omp extension to `~/.omp/agent/extensions/cogitator.ts`.
- `cogitator hooks install [--binary PATH] [--force]`: runs the installers above for every
  harness found on this machine, all with the same binary, and skips the rest without
  creating their directories. It prints one line per harness (installed, updated, already
  set up, yours and left alone, skipped, or the error), notes that opencode needs no hook,
  and lists what to do next. It keeps going past a failing harness, then exits non-zero.

All hook subcommands exit 0 silently when cogitator is not running. A closed monitor is the
expected case, not a failure, so your agent is never blocked.

Each hook also records the multiplexer pane it ran in (`$ZELLIJ_PANE_ID` or `$TMUX_PANE`).
That is how several sessions in one directory each get their own Agents row on their own
tab. A hook still pointing at an older cogitator binary cannot record its pane, so re-run
`cogitator hooks install` after upgrading.

The Agents list shows only agents in the multiplexer cogitator runs in. A session whose
recorded pane is in another multiplexer, or has closed, is left out. So is a session with no
recorded pane yet when every pane in its directory already has an agent.

## Logging

Logs are written with `log/slog` text formatting.

- If `$XDG_STATE_HOME` is set: `$XDG_STATE_HOME/cogitator/cogitator.log`
- Otherwise: `/tmp/cogitator.log`
