# Getting started

cogitator is a terminal (TUI) dashboard for your coding agents. It gives you a live view of
sessions and lets you manage git worktrees:

- **See status at a glance**: discovers running instances, flagging which sessions need you
  (permission requests, pending questions, errors).
- **Create git worktrees**: spin up a new worktree for a branch, or fetch, pull, and delete
  existing ones, straight from the roster.
- **Navigate into them**: jump to a running agent or resume a stopped one in a tmux session
  (or window) with a single keystroke.
- **Works across harnesses**: opencode, Claude Code, Codex, and omp, with an optional
  [Taskwarrior](https://taskwarrior.org) pane for your task list.

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

## 2. Run cogitator

```sh
cogitator
```

or from source:

```sh
go run ./cmd/cogitator
```

## 3. Connect your coding agent

Pick the harness you use; setup is a one-time step per machine:

- [opencode](/guide/connect#opencode): launch with `--mdns`; discovered automatically.
- [Claude Code](/guide/connect#claude-code): wire lifecycle hooks in `~/.claude/settings.json`.
- [Codex](/guide/connect#codex): wire lifecycle hooks in `~/.codex/hooks.json`.
- [omp](/guide/connect#omp): a single command, `cogitator omp-hook install`.

Each harness offers an **automated** path (paste a prompt into the agent itself and let it do
the setup) and a **manual** path (do it yourself, step by step).
