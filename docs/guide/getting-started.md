# Getting started

cogitator is a terminal (TUI) dashboard for your coding agents. It gives you a live view of
sessions and lets you manage git worktrees:

- **See status at a glance**: discovers running instances, flagging which sessions need you
  (permission requests, pending questions, errors).
- **Create git worktrees**: spin up a new worktree for a branch, or fetch, pull, and delete
  existing ones, straight from the roster.
- **Bundle multi-repo workspaces**: group several repos into a workspace and create a session
  that checks out one new branch across every member repo at once; `Tab` swaps between the
  Repos and Workspaces panes. See [Configuration](/guide/configuration#workspaces) for
  disk-cost and layout details.
- **Navigate into them**: jump to a running agent or resume a stopped one in a tmux session
  (or window) with a single keystroke.
- **Works across harnesses**: opencode, Claude Code, Codex, and omp.

## Requirements

| OS      | Support       |
| ------- | ------------- |
| macOS   | Supported     |
| Linux   | Supported     |
| Windows | Not supported |

## 1. Install

::: code-group

```sh [Homebrew]
brew install guilhermehto/tap/cogitator
```

```sh [Go]
go install github.com/guilhermehto/cogitator/cmd/cogitator@latest
```

:::

::: details macOS: unsigned binary blocked by Gatekeeper?
Current releases are unsigned. If Gatekeeper blocks the first launch, either use Finder
"Open" once, or clear quarantine:

```sh
xattr -d com.apple.quarantine cogitator
```

:::

## 2. Set up

```sh
cogitator setup
```

It finds tmux, zellij, and your coding agents (Claude Code, Codex, omp; opencode needs no
hook), prints what it found and what it would do, and asks once per multiplexer and once for
the hooks. Everything points at the same cogitator binary. It never sets anything up twice:
wiring you wrote by hand is reported with its file and line and left alone, and a re-run on
a set-up machine prints `everything is already set up` and changes no file. Then follow the
steps it prints: start a new zellij session for the sidebar, restart Claude Code and omp,
and trust the hook via `/hooks` in Codex.

`cogitator setup --check` prints the same plan and exits non-zero when something is
missing or outdated; `--yes` accepts the plan without prompting. See the
[CLI reference](/reference/cli#setup) for every flag.

## 3. Run cogitator

```sh
cogitator
```

or from source:

```sh
go run ./cmd/cogitator
```

Keep it running: the picker, the next-agent key, and the zellij sidebar read agent state
from it.

## Set up one piece at a time

`cogitator setup` runs these installers; each also works on its own:

- `cogitator hooks install`: the hooks of every harness found on this machine.
- [tmux](/guide/tmux#setup): `cogitator tmux install`.
- zellij: `cogitator zellij install` (see the [CLI reference](/reference/cli#subcommands)).
- [opencode](/guide/connect#opencode): launch with `--mdns`; discovered automatically.
- [Claude Code](/guide/connect#claude-code): `cogitator claude-hook install`.
- [Codex](/guide/connect#codex): `cogitator codex-hook install`.
- [omp](/guide/connect#omp): `cogitator omp-hook install`.

Each harness also offers an **automated** path (paste a prompt into the agent itself and let
it do the setup) and a **manual** path (do it yourself, step by step).
