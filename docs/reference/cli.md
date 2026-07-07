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

## Hook subcommands

These are wired up during [agent setup](/guide/connect) and are not meant to be run by hand:

- `cogitator claude-hook`: receives Claude Code lifecycle events on stdin.
- `cogitator codex-hook`: receives Codex lifecycle events on stdin.
- `cogitator omp-hook`: receives omp lifecycle events; `cogitator omp-hook install` writes
  the omp extension to `~/.omp/agent/extensions/cogitator.ts`.

All hook subcommands exit 0 silently when cogitator is not running. A closed monitor is the
expected case, not a failure, so your agent is never blocked.

## Logging

Logs are written with `log/slog` text formatting.

- If `$XDG_STATE_HOME` is set: `$XDG_STATE_HOME/cogitator/cogitator.log`
- Otherwise: `/tmp/cogitator.log`
