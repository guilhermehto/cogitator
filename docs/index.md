---
layout: home

hero:
  name: cogitator
  text: Every agent session, one screen.
  tagline: A terminal dashboard that monitors your coding agents, spins up git worktrees, and jumps you between them. Works with opencode, Claude Code, Codex, and omp.
  image:
    src: /logo.svg
    alt: cogitator
  actions:
    - theme: brand
      text: Get started
      link: /guide/getting-started
    - theme: alt
      text: Connect your agent
      link: /guide/connect
    - theme: alt
      text: GitHub
      link: https://github.com/guilhermehto/cogitator

features:
  - icon: "●"
    title: Attention at a glance
    details: Sessions that need you are flagged live, from permission requests and pending questions to errors and finished turns.
  - icon: "❯"
    title: Live discovery
    details: opencode instances appear automatically over mDNS; Claude Code, Codex, and omp report in through lightweight lifecycle hooks.
  - icon: "⎇"
    title: Worktree launcher
    details: Spin up a worktree for a new branch, or fetch, pull, and delete existing ones, straight from the roster.
  - icon: "⊞"
    title: tmux native
    details: Jump to a running agent or resume a stopped one in a tmux session or window with a single keystroke.
  - icon: "✓"
    title: Taskwarrior pane
    details: An optional task list lives beside your sessions. Add, edit, start, and complete tasks without leaving the dashboard.
  - icon: "◇"
    title: Zero config
    details: Harnesses are auto-detected. Durable settings live in one JSON file you can edit by hand.
---

<div class="terminal-frame">
  <div class="bar"><i></i><i></i><i></i><em>cogitator</em></div>

<video src="/demo.mp4" poster="/tui.png" autoplay muted loop playsinline></video>

</div>

<div class="attn-legend">
  <span class="perm">permission</span>
  <span class="question">question</span>
  <span class="error">error</span>
  <span class="active">active</span>
  <span class="finished">finished</span>
</div>

## Up and running in seconds

::: code-group

```sh [Homebrew]
brew install guilhermehto/tap/cogitator
```

```sh [Go]
go install github.com/guilhermehto/cogitator/cmd/cogitator@latest
```

:::

Then run `cogitator` and [connect your coding agent](/guide/connect). You only do it once.
