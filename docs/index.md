---
layout: home
title: cogitator
---

<div class="cog-hero">
  <p class="cog-eyebrow"><i>●</i> one dashboard · every agent · your tmux</p>
  <h1 class="cog-title">every agent session.<br>one screen<span class="cog-cursor"></span></h1>
  <p class="cog-lede">
    cogitator watches <strong>opencode</strong>, <strong>Claude Code</strong>, <strong>Codex</strong>, and
    <strong>omp</strong> in a live roster: which session wants
    <span class="st-perm">permission</span>, which is holding a
    <span class="st-question">question</span>, which hit an
    <span class="st-error">error</span>, which is still
    <span class="st-active">active</span>, which just
    <span class="st-finished">finished</span>. Spin up a git worktree and jump in — one keystroke,
    straight into plain tmux in the terminal you already live in.
  </p>
  <div class="cog-actions">
    <a class="cog-btn brand" href="./guide/getting-started">get started</a>
    <a class="cog-btn ghost" href="./guide/connect">connect your agent</a>
    <a class="cog-btn ghost" href="https://github.com/guilhermehto/cogitator" target="_blank" rel="noreferrer">github ↗</a>
  </div>
</div>

<div class="cog-install">

<p class="cog-install-label">install · a single binary</p>

::: code-group

```sh [Homebrew]
brew install guilhermehto/tap/cogitator
```

```sh [Go]
go install github.com/guilhermehto/cogitator/cmd/cogitator@latest
```

:::

</div>

<div class="terminal-frame">
  <div class="bar"><i></i><i></i><i></i><em>cogitator</em></div>

<video src="/demo.mp4" poster="/tui.png" autoplay muted loop playsinline></video>

</div>

<p class="cog-caption">opencode, Claude Code, Codex, and omp in one roster — recorded live, not a mockup.</p>

<section class="cog-sec">
  <p class="cog-sec-eyebrow">why cogitator</p>
  <h2 class="cog-sec-title">the dashboard your agents report to.</h2>
  <div class="cog-grid">
    <div class="cog-card">
      <span class="cog-card-icon st-perm">●</span>
      <h3>attention at a glance</h3>
      <p>Permission requests, pending questions, errors, and finished turns — flagged live in the roster.</p>
    </div>
    <div class="cog-card">
      <span class="cog-card-icon st-active">❯</span>
      <h3>live discovery</h3>
      <p>opencode appears automatically over mDNS; Claude Code, Codex, and omp report in through lifecycle hooks.</p>
    </div>
    <div class="cog-card">
      <span class="cog-card-icon st-brand">⎇</span>
      <h3>worktree launcher</h3>
      <p>Create a worktree for a new branch, or fetch, pull, and delete existing ones — straight from the roster.</p>
    </div>
    <div class="cog-card">
      <span class="cog-card-icon st-finished">⊞</span>
      <h3>tmux native</h3>
      <p>Jump to a running agent or resume a stopped one — plain tmux sessions or windows, your config and keybinds intact.</p>
    </div>
    <div class="cog-card">
      <span class="cog-card-icon st-lilac">✓</span>
      <h3>taskwarrior pane</h3>
      <p>An optional task list beside your sessions: add, edit, start, and complete without leaving the dashboard.</p>
    </div>
    <div class="cog-card">
      <span class="cog-card-icon st-question">◇</span>
      <h3>zero config</h3>
      <p>Harnesses auto-detect. Durable settings live in one JSON file you can edit by hand.</p>
    </div>
  </div>
</section>

<section class="cog-sec">
  <p class="cog-sec-eyebrow">why different</p>
  <h2 class="cog-sec-title">no new terminal. no new muscle memory.</h2>
  <p class="cog-sec-sub">Most agent managers ship their own terminal or replace your multiplexer. cogitator drives the tools you already use — and leaves them exactly as they are when you quit.</p>
  <div class="cog-tools">
    <div class="cog-tool t-brand">
      <h3>your terminal</h3>
      <p>A TUI in whatever emulator you run — Ghostty, kitty, iTerm, Alacritty. No app, no web view, no account.</p>
    </div>
    <div class="cog-tool t-active">
      <h3>your tmux</h3>
      <p>Worktrees open as plain tmux sessions or windows: your config, your keybinds, your status bar. Quit cogitator — nothing dies. zellij is on the roadmap.</p>
    </div>
    <div class="cog-tool t-finished">
      <h3>your agents</h3>
      <p>opencode, Claude Code, Codex, and omp run unmodified, right where they already run — no wrapper, no re-hosted chat view.</p>
    </div>
  </div>
</section>

<section class="cog-sec">
  <p class="cog-sec-eyebrow">works with</p>
  <h2 class="cog-sec-title">bring your harness. setup is one step.</h2>
  <div class="cog-harnesses">
    <a class="cog-harness" href="./guide/connect#opencode">
      <span class="name">opencode</span>
      <span class="desc">discovered over mDNS — launch with <code>--mdns</code>, nothing else</span>
      <span class="go">setup →</span>
    </a>
    <a class="cog-harness" href="./guide/connect#claude-code">
      <span class="name">claude code</span>
      <span class="desc">lifecycle hooks in <code>~/.claude/settings.json</code> — paste once</span>
      <span class="go">setup →</span>
    </a>
    <a class="cog-harness" href="./guide/connect#codex">
      <span class="name">codex</span>
      <span class="desc">lifecycle hooks in <code>~/.codex/hooks.json</code> — paste once</span>
      <span class="go">setup →</span>
    </a>
    <a class="cog-harness" href="./guide/connect#omp">
      <span class="name">omp</span>
      <span class="desc">one command: <code>cogitator omp-hook install</code></span>
      <span class="go">setup →</span>
    </a>
  </div>
</section>

<section class="cog-sec">
  <p class="cog-sec-eyebrow">keyboard first</p>
  <h2 class="cog-sec-title">everything is a keystroke away.</h2>
  <div class="cog-keys">
    <div><kbd>enter</kbd><span>jump to a running agent, or resume a stopped one</span></div>
    <div><kbd>n</kbd><span>new worktree on the highlighted repo</span></div>
    <div><kbd>ctrl+p</kbd><span>fuzzy session switcher — repo/branch, jump</span></div>
    <div><kbd>T</kbd><span>toggle the Tasks pane</span></div>
    <div><kbd>A</kbd><span>fuzzy-find and add a repository</span></div>
    <div><kbd>P</kbd><span>pull latest into the highlighted branch</span></div>
  </div>
  <p class="cog-more"><a href="./guide/key-bindings">all key bindings →</a></p>
</section>

<section class="cog-cta">
  <div>
    <h2 class="cog-sec-title">your agents are already running.</h2>
    <p>Put them on one screen — in the terminal you already have open.</p>
  </div>
  <div class="cog-actions">
    <a class="cog-btn brand" href="./guide/getting-started">get started</a>
    <a class="cog-btn ghost" href="./guide/connect">connect your agent</a>
  </div>
</section>
