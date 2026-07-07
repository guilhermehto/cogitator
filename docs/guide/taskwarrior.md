# Taskwarrior

cogitator displays a live Tasks pane alongside the Sessions pane when a `task` binary is found
on the cogitator process's `$PATH`.

## Requirements

- A `task` ([Taskwarrior](https://taskwarrior.org)) binary must be reachable on the `$PATH` of
  the process that runs cogitator. No configuration flag is needed.

## Auto-detection

- cogitator checks for `task` at startup. If the binary is present the Tasks pane is shown by
  default; if not, the pane is hidden and no error is surfaced.
- Press `T` to hide or show the Tasks pane while cogitator is running. There is no
  `--no-tasks` flag.

## Visual indicators

- The `ST` column shows a priority glyph (high / medium / low) for idle tasks.
- A running task (one started via `s` or `task <id> start`) is rendered bold green with a play
  glyph (`󰐊`) in the `ST` column, replacing the priority glyph for that row. Press `s` again
  to stop it. The legend at the bottom of the TUI lists each glyph.

## Environment variables

cogitator inherits the full environment of the process that launched it. Taskwarrior respects
the following variables from that environment:

| Variable    | Effect                                                          |
| ----------- | --------------------------------------------------------------- |
| `$PATH`     | must include the directory containing the `task` binary         |
| `$TASKDATA` | overrides the Taskwarrior data directory (`~/.task` by default) |
| `$TASKRC`   | overrides the Taskwarrior config file (`~/.taskrc` by default)  |

See [Key bindings](/guide/key-bindings) for the Tasks pane keys (`a`, `e`, `s`, `d`, `D`, `U`).
