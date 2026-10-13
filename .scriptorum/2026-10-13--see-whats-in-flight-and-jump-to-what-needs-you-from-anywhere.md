---
created: 2026-10-13
updated: 2026-10-13
slug: see-whats-in-flight-and-jump-to-what-needs-you-from-anywhere
goal: See what's in flight and jump to what needs you from anywhere in tmux, keyboard only.
status: in-progress
weight: heavy
supersedes: []
---

# tmux-Native Agents Popup, Next Key, and Badges

## Summary
Add a tmux-native layer over cogitator's live state: one attention-ordered agents list (Repos worktrees + workspace sessions + stray agents in tmux panes) served by the running TUI over a local socket, `@cog_*` tmux options for status-line/window badges, a `cogitator next` jump key, and a `cogitator pick` popup. Later phases add an optional popup preview and a prototype attention-first Agents tab in the main TUI (option C). A zellij plugin (option E) is captured as a deferred idea only.

## Scope
- New pure `internal/agents` projection: unified ordered list, groups, "next" selection, badge diffs, focus-seen decisions.
- Request/response transport in `internal/hookipc`; new `internal/agentfeed` protocol/server/client on `cogitator-feed.sock`.
- `internal/tmuxctl` additions: client-targeted select, pane/client listing, batched `@cog_*` option writes, display-message.
- TUI wiring: serve the feed, execute focus/seen requests via the existing launch path, publish diffed badges, clear `finished` when any client focuses an agent, unset options on exit.
- CLI: `cogitator pick` (display-popup UI), `cogitator next`; `--status` reads the feed when the TUI is running.
- Docs: tmux guide (bindings, status-line snippet, tmux's built-in `switch-client -l` as "back"), CLI reference.
- Nice-to-have: popup preview (wait reason + capture-pane tail).
- Prototype (option C): "Agents" tab in the main TUI reusing the popup list; default view unchanged.
- Deferred idea, not planned (option E): zellij WASM plugin sidebar consuming the same JSON feed. Costs a Rust/WASM codebase; zellij has no `@user` options so panes map by cwd. Revisit after living with the tmux layer.
- Opt-in installer (amended 2026-10-13, user reversed publish-only for bindings): `cogitator tmux install` writes a cogitator-owned bindings file and adds one `source-file -q` line to the user's tmux.conf; `uninstall` reverses it. Badges/status formats stay a documented snippet because themes own them.
- Excluded: persistent sidebar pane, approving/answering permissions from the popup, editing the user's theme or status/window formats, any runtime tmux.conf edits, screen scraping.

## Steps

### Phase 1 — Foundations · parallel
1. [x] Create `internal/agents`: the unified, attention-ordered agent projection
   Outcome: From the TUI's Repos rows, workspace statuses, live sessions, and tmux panes, one function returns every agent the user cares about, grouped and ordered by urgency, and picks the "next" one to jump to.
   Context: Two parallel joins exist today (`settings.Merge` → `[]settings.Row`, `workspace.MergeStatus` → `[]workspace.WorkspaceStatus`); the only unified list is the MRU-ordered ctrl+P palette. Consume their outputs — don't re-join the snapshot or reload the roster. No `internal/ui`/bubbletea imports. Define:
     - `Agent`: ID (canonical dir — the identity everywhere), Kind (worktree|workspace|stray), Repo/Branch, Workspace/SessionName, Harness, Provider, SessionID, Title, State (`settings.RowState`), Attention (`state.Attention`), LastActivity, Group, and tmux location joined from pane input: WindowID, PaneID, TmuxSessionID, and `SessionOwned` (true when the agent's tmux session holds no other `@cog_dir` window — the only case where "client is in that session" may count as "at this agent").
     - `Group`, in order: NeedsYou (permission, question, errored), Done (finished), Working (active), Idle (running + inactive, and StateUnknown), Stopped.
     - `Pane` — the single pane type for this package (step 6 reuses it; the UI converts from `tmuxctl.Pane`, so this step doesn't depend on step 3): pane id, window id, session id, session name, canonical pane cwd, `pane_current_command`, and window options `@cog_dir`, `@cog_status`, `@cog_reason`, `@cog_since`.
     - `Build(Input) []Agent`, where Input = rows, workspace statuses, live top-level sessions, panes, WorkspaceRoot, Now, RecentWindow (`cfg.RecentWindow`). Managed worktree rows + workspace sessions (skip pending sessions with empty `Session.Dir`; skip rows with no SessionID and zero LastActivity, e.g. repo-header placeholders); managed agents locate their window by `@cog_dir`. Strays = live sessions with `Source == SourceLive` (recent-history sessions from `SyncRecent` must never become strays) whose canonical dir is neither managed nor under WorkspaceRoot (same rule as `excludeWorkspaceOwnedSessions`) and matches a pane cwd; collapse per dir like `buildLiveByDir`; when several panes share the cwd prefer one whose `pane_current_command` matches the harness, else the lowest pane id; drop strays with no pane. Stopped included only while `Now - LastActivity <= RecentWindow`; Missing dropped.
     - Ordering: group rank → within NeedsYou permission/question before errored → oldest LastActivity first (proxy for "waiting since"; the store has no attention-since timestamp and a waiting/finished session has no newer activity) → ID.
     - `AgentAt(list, windowID, sessionID) (id string, ok bool)`: the agent on that window; else the `SessionOwned` agent whose TmuxSessionID == sessionID (session launch mode restores the session's last-active window, which may be a sibling).
     - `Next(list, currentID) (Agent, bool)`: first NeedsYou/Done agent after currentID in list order, wrapping; first actionable when currentID is empty or not actionable; never Working/Idle/Stopped.
     - `Counts(list)` per group and per NeedsYou attention.
     Compare paths only via `pathnorm.Canonical`.
   Read first: `internal/settings/merge.go:45`, `internal/settings/merge.go:126`, `internal/workspace/status.go:15`, `internal/workspace/status.go:117`, `internal/workspace/status.go:170`, `internal/state/attention.go:24`, `internal/state/store.go:34`, `internal/ui/model.go:2802`, `internal/ui/model.go:3278`, `internal/ui/visibility.go:9`, `internal/tmuxctl/tmux.go:134`, `internal/pathnorm/pathnorm.go`
   Done when:
     - GIVEN rows with permission/finished/active/inactive/stopped agents, Build orders them NeedsYou → Done → Working → Idle → Stopped, oldest first within a group.
     - GIVEN a stopped agent last active 31m ago (window 30m) it is absent; at 29m it is present at the bottom.
     - GIVEN a live (SourceLive) top-level session in an unmanaged dir matching a pane cwd, Build returns a stray with that PaneID/WindowID; with no matching pane, with `SourceRecent`, or under WorkspaceRoot, it is absent.
     - GIVEN two panes sharing a stray's cwd, the one running the harness command is chosen.
     - GIVEN NeedsYou agents A (older) and B and currentID = A, Next returns B; from B it wraps to A; with only Working agents it returns false.
     - GIVEN a client on a sibling window of a `SessionOwned` agent's session, AgentAt returns that agent; GIVEN the session holds two tagged windows, it returns false.
   Touchpoints: `internal/agents/agents.go`, `internal/agents/agents_test.go`
   Anti-touch: `internal/settings/merge.go`, `internal/workspace/status.go` (consume outputs; don't refactor the joins here)
   Verification: `go vet ./internal/agents/ && go test -race ./internal/agents/`
   Independent Test: `go test -race ./internal/agents/` — table tests for ordering, stopped window, strays, AgentAt, Next wrap/none.
   > note: follow-up from manual test — commits 9af4c28 + 1b0573d: managed agents count as running only with a `SourceLive` view (Merge/MergeStatus mark recent-only dirs running); recent-only → Stopped (≤30m), incl. stale finished badges (rule change surfaced to user). Labels fixed in 9cd4ff5 (`repo·branch` / `~` dir).
   > note: follow-up 2 (user: "same statuses as the others") — commit 6720047 replaces the 9af4c28 rule: needs-you/finished keep their badge until seen regardless of liveness (as Repos/Workspaces show them); idle/working count as in flight with a live view OR a cogitator-tagged tmux window, else Stopped (≤30m).
   > note: done — commit cafd913. Next never returns currentID (sole actionable agent = current → false). Creating/Missing dropped. Harness↔pane_current_command match is case-insensitive substring.
2. [x] Add request/response IPC to `internal/hookipc`
   Outcome: A client process can send one framed request to a cogitator-owned Unix socket and get one framed reply, with the same safety guarantees as the hook sockets.
   Context: `Listen` is one-way (handler gets bytes, no reply) and `SendHook` reads stdin. Add `ListenRPC(ctx, sockPath, handler func(req []byte) []byte, logger)` and `Call(ctx, sockPath, req []byte) ([]byte, error)`, extracting a shared helper (not copy-paste) for bind / EADDRINUSE live-vs-stale detection / chmod 0600 / cleanup-once, and reusing `WriteFrame`/`ReadFrame` (1 MiB cap). Serve each RPC connection in its own goroutine (ordering doesn't matter; a slow client must not block others) with read/write deadlines. `Call` wraps `ErrListenerUnavailable` on dial failure. Existing `Listen`/`SendHook` behaviour and tests unchanged.
   Read first: `internal/hookipc/hookipc.go:54`, `internal/hookipc/hookipc.go:82`, `internal/hookipc/hookipc.go:190`, `internal/hookipc/hookipc_test.go`
   Done when:
     - GIVEN a ListenRPC echo handler, Call returns the handler's reply.
     - GIVEN no listener, Call returns an error satisfying `errors.Is(err, ErrListenerUnavailable)` within the dial timeout.
     - GIVEN a stale socket file, ListenRPC replaces it; GIVEN a live owner, it returns ErrListenerOwned.
     - The socket file mode is 0600.
   Touchpoints: `internal/hookipc/hookipc.go`, `internal/hookipc/hookipc_test.go`
   Verification: `go test -race ./internal/hookipc/`
   Independent Test: `go test -race ./internal/hookipc/` (new RPC tests + unchanged existing ones).
   > note: done — commit 5642f4d. Socket tests need an unsandboxed run (sandbox blocks unix bind).
3. [x] Extend `internal/tmuxctl` with client-targeted select, listings, batched options, and messages
   Outcome: cogitator can move a specific tmux client to a window/session/pane, see which window each client is on, list every pane with its cwd, and set/unset many `@cog_*` options in one tmux call.
   Context: All tmux access goes through the `Runner` seam (tests use `RunnerFunc`). `Select`/`SelectSession` run `switch-client` without `-c`, so a popup request could move the wrong client when several are attached. Add, keeping existing behaviour (empty client = today):
     - `SelectForClient` / `SelectSessionForClient` (existing `SelectWith`/`SelectSessionWith` delegate with "").
     - `SelectPaneForClient(r, paneID, client)` for strays (select-window, select-pane, switch-client -c).
     - `ListPanes(r) ([]Pane, error)`: one `list-panes -a -F` call → pane id, window id, session id, session name, `pane_current_path` (canonicalised; raw kept on error), `pane_current_command`, window `@cog_dir`, `@cog_status`, `@cog_reason`, `@cog_since`.
     - `ListClientFocus(r) ([]ClientFocus, error)`: `list-clients -F` → client name, session id, window id, `client_activity` (unix seconds).
     - `ReadGlobalOptions(r, names []string) (map[string]string, error)`: current values of global `@cog_*` options (absent = unset), so callers diff against what tmux actually holds.
     - `OptionWrite{Global bool, Target, Name, Value, Unset}` + `ApplyOptions(r, []OptionWrite)`: chains commands into one invocation with `;` separator args; window writes are `set-option -w [-u] -t <window_id> <name> [value]` (without `-w`, `@` options land on the session and badge every window — the existing `setOptionWith` already uses `-w`), globals are `set-option -g [-u]`. No-op on empty. Note tmux aborts the rest of a `;` chain when one command fails, so callers must only target windows that exist.
     - `DisplayMessage(r, client, text)`.
     Tab-separated formats (paths may contain spaces). Gate on `Available()` like existing functions.
   Read first: `internal/tmuxctl/tmux.go:68`, `internal/tmuxctl/tmux.go:104`, `internal/tmuxctl/tmux.go:383`, `internal/tmuxctl/tmux.go:414`, `internal/tmuxctl/tmux.go:517`, `internal/tmuxctl/tmux.go:545`, `internal/tmuxctl/tmux.go:621`, `internal/tmuxctl/tmux_test.go`, `internal/tmuxctl/live_test.go`
   Done when:
     - GIVEN client "/dev/ttys003", SelectForClient issues `switch-client -c /dev/ttys003 -t <target>`; GIVEN "", argv equals today's.
     - GIVEN three OptionWrites, ApplyOptions makes exactly one Runner call with three `set-option` commands separated by `;`; window writes carry `-w -t <window_id>`, globals `-g`, unsets `-u`.
     - GIVEN list-panes output containing a path with spaces, ListPanes parses every field.
     - Existing tmuxctl tests pass unchanged.
   Touchpoints: `internal/tmuxctl/tmux.go`, `internal/tmuxctl/tmux_test.go`
   Verification: `go test -race ./internal/tmuxctl/`
   Independent Test: `go test -race ./internal/tmuxctl/` (fake Runner argv assertions).
   > note: done — commit 7e9fdda. ReadGlobalOptions chains `show-options -gqv` per name (assumes single-line values).

### Phase 2 — Feed protocol and decisions · sequential
4. [x] Create `internal/agentfeed`: protocol, server, and client on `cogitator-feed.sock`
   Outcome: Any process can ask the running cogitator for the agent list, ask it to focus an agent for a given tmux client, or mark one seen.
   Context: Built on step 2's `ListenRPC`/`Call` and step 1's `agents.Agent`; no bubbletea/ui imports. `SocketPath()` = `hookipc.RuntimePath("cogitator-feed.sock")` (mirror the claude hook socket naming). JSON request `{version, op: list|focus|seen, id, client}`, response `{version, agents, error}`; a version mismatch fails explicitly. `Holder`: mutex-guarded latest `[]agents.Agent`; `Set` cheap and non-blocking (called from Update). `Serve(ctx, holder, actions chan<- Action, logger)`: `list` answers from Holder directly (no Bubble Tea round-trip); `focus`/`seen` validate the id against Holder, enqueue `Action{Kind, AgentID, Client}` non-blockingly (reply "busy" if full), reply ok — execution is async in the TUI. Client: `List`, `Focus`, `Seen` returning typed `ErrNotRunning` when unreachable. Short timeouts: popup and next must feel instant.
   Read first: `internal/hookipc/hookipc.go`, `internal/agents/agents.go`, `internal/claudecode/hook.go:16`
   Done when:
     - GIVEN a Holder with two agents and Serve running, List returns both in order.
     - GIVEN Focus with a known id, an Action with that id and client arrives on the channel; with an unknown id, Focus errors and nothing is enqueued.
     - GIVEN no server, List returns ErrNotRunning.
     - GIVEN a mismatched version, the reply is an explicit version error.
   Touchpoints: `internal/agentfeed/feed.go`, `internal/agentfeed/feed_test.go`
   Pre-conditions: Phase 1 landed.
   Verification: `go test -race ./internal/agentfeed/`
   Independent Test: `go test -race ./internal/agentfeed/` with a temp-dir socket.
   > note: done — commit 4059835. API: `Serve(ctx, sockPath, holder, actions, logger) (cleanup, error)`; client struct `NewClient()`/`NewClientAt(path)` with `List/Focus/Seen`; errors `ErrNotRunning`, `ErrBusy`, `ErrUnknownAgent`; 1.5s call timeout; wire = `agents.Agent` default JSON field names.
5. [x] Implement the `next` client logic in `internal/agentfeed`
   Outcome: One call jumps a tmux client to the most urgent agent and cycles oldest-first on repeat, telling the user when nothing needs them or cogitator isn't running.
   Context: `RunNext(ctx, client, windowID, sessionID string) error`: `List` → `agents.AgentAt(list, windowID, sessionID)` → `agents.Next(list, currentID)` → `Focus`. Session id matters: in session launch mode the jump only switches session (`internal/ui/model.go:913`) and tmux restores that session's last window, so a window-only match would make Next return the same agent forever. Nothing actionable → `tmuxctl.DisplayMessage(client, "cogitator: nothing needs you")`. ErrNotRunning → display-message "cogitator isn't running". Both are user-facing outcomes, not failures: return nil and write nothing to stdout/stderr, because `run-shell` overlays any output and shows "'cmd' returned 1" on a non-zero exit. Unexpected errors: display-message + log, still nil. Inject feed client + tmux messenger behind small interfaces for tests. Must not touch the single-instance lock.
   Read first: `internal/agentfeed/feed.go`, `internal/agents/agents.go`, `internal/tmuxctl/tmux.go`, `internal/ui/model.go:913`, `internal/singleinstance/singleinstance.go:25`
   Done when:
     - GIVEN two NeedsYou agents and windowID of the first, RunNext focuses the second.
     - GIVEN a client on a sibling window of a SessionOwned NeedsYou agent's session, RunNext moves to the next actionable agent instead of re-focusing it.
     - GIVEN only Working agents, RunNext shows "nothing needs you" and returns nil.
     - GIVEN no feed, RunNext shows "isn't running", returns nil, and prints nothing.
   Touchpoints: `internal/agentfeed/next.go`, `internal/agentfeed/next_test.go`
   Verification: `go test -race ./internal/agentfeed/`
   Independent Test: `go test -race -run Next ./internal/agentfeed/`
   > note: done — commit 69e2ac9. Signature `RunNext(ctx, client, windowID, sessionID, logger) error`; core `runNext` over `nextFeed` + `messenger` interfaces; uses `tmuxctl.DefaultRunner`.
6. [x] Add badge diff and focus-seen decisions to `internal/agents`
   Outcome: From the agent list and what tmux shows, cogitator knows exactly which `@cog_*` options to change and which finished agents the user is now looking at.
   Context: Pure; the TUI does the shell-outs (step 8). Per window: `@cog_status` (needs-you|errored|done|working|idle; unset for stopped), `@cog_reason` (permission|question|errored|finished), `@cog_since` (unix seconds). Globals: `@cog_needs`, `@cog_done`, `@cog_working` (unset when zero, so `#{?@cog_needs,…}` works).
     - `DiffOptions(list []Agent, panes []Pane, globals map[string]string) []tmuxctl.OptionWrite` diffs *desired vs what tmux actually holds* (the pane listing carries the window `@cog_*` values; globals come from `ReadGlobalOptions`) — no in-memory "last published" state. Window writes target only window ids present in `panes` (tmux aborts the rest of a `;` chain on the first failure, so writing to a closed window would freeze all badges). Any listed window carrying `@cog_*` that isn't desired gets unset — this also covers startup after a crash, so there is no separate sweep. Steady state = zero writes.
     - One window can host several agents (split-pane strays, a stray beside a managed agent): reduce per WindowID so the most urgent group wins, tiebreak by agent ID.
     - `@cog_since` changes only when that window's `@cog_status` changes (a Working agent's LastActivity moves on every message; writing it would issue a write every tick).
     - `ClearAll(panes, globals) []tmuxctl.OptionWrite`: unsets every `@cog_*` window option present and every global — used on shutdown.
     - `SeenOnFocus(list, []tmuxctl.ClientFocus) []Agent`: Done agents for which some client is "at" the agent (`AgentAt` with that client's window and session ids — the SessionOwned rule keeps a user's main multi-window session from clearing everything) AND that client's `client_activity` is after the agent's LastActivity (an idle second client parked on a window must not silently clear new Done badges). Never returns NeedsYou (permission/question clear only via real events).
   Read first: `internal/agents/agents.go`, `internal/tmuxctl/tmux.go`, `internal/tmuxctl/tmux.go:384`, `internal/state/store.go:826`
   Done when:
     - GIVEN tmux already holding exactly the desired options, DiffOptions returns nothing.
     - GIVEN an agent going permission → working, DiffOptions sets `@cog_status working` and a new `@cog_since`, unsets `@cog_reason`, updates `@cog_needs`/`@cog_working`.
     - GIVEN a Working agent whose LastActivity advanced but status didn't change, DiffOptions returns nothing.
     - GIVEN a window carrying `@cog_status` with no agent (crashed predecessor), it is unset; GIVEN a desired agent whose window is not in `panes`, no write targets it.
     - GIVEN a stray (Working) and a NeedsYou agent sharing a window, the window shows needs-you.
     - GIVEN a Done agent on a client's current window with client_activity after the agent's LastActivity, SeenOnFocus returns it; with older client_activity, or for a NeedsYou agent, it does not.
   Touchpoints: `internal/agents/badges.go`, `internal/agents/badges_test.go`
   Verification: `go test -race ./internal/agents/`
   Independent Test: `go test -race -run 'Diff|Clear|Seen' ./internal/agents/`
   > note: done — commits 94eb07f + de7e17c (globals now per agent via `Counts`, matching popup/`--status`). `@cog_since` = winning agent's LastActivity. Exported `OptStatus/OptReason/OptSince/OptNeeds/OptDone/OptWorking`.

### Phase 3 — TUI wiring · sequential
7. [x] Serve the feed from the TUI and execute focus/seen requests
   Outcome: While the TUI runs, the feed returns the live agent list, and a focus request jumps the requesting tmux client to the agent — resuming it if stopped — exactly like Enter in cogitator.
   Context:
     - `RunTUI`: start `agentfeed.Serve` beside the roster recorder (non-fatal on failure, like the workspace store); give the model the Holder + actions channel; add `waitFeedAction` mirroring `waitSnapshot`.
     - Model gets `panes` (converted once from `tmuxctl.Pane` to `agents.Pane`) from a background `tmuxctl.ListPanes` cmd, dispatched with the snapshot-driven builds and coalesced like `rowsBuilding`/`wsBuilding`. Recompute `agents.Build` in Update when rows, wsStatuses, panes, or snap change (pure) and `Holder.Set` it. Live input = top-level sessions filtered with `shouldHideSubagent` + `ParentID == ""`, as `buildWorkspaceRows` does; pass `m.workspaceRoot` and `cfg.RecentWindow`.
     - Focus, managed: resolve the `launchTarget` by dir (`rowLaunchTarget` over `m.workspaceRows`, `wsSessionLaunchTarget` over `m.wsStatuses`) and dispatch `launchCmd` with the client threaded to selection (extend `tmuxOps` with step 3's client-targeted selects; update fakes in `actions_test.go`). Stray: `SelectPaneForClient`, then MarkViewed. Record `switchOrder` like the palette so ctrl+P MRU stays truthful.
     - Seen → `viewMarker.MarkViewed(provider, "", sessionID)`.
     - Add a `client` field to `launchResultMsg`; when set and `err != nil`, the handler also returns a `DisplayMessage` tea.Cmd to that client (today it only sets hints, which the popup/next user can't see), plus `m.tmuxHint`.
     - Every tmux call is a tea.Cmd; nothing shells out in Update. `--demo` never starts the feed.
   Read first: `internal/ui/tui.go:21`, `internal/ui/model.go:206`, `internal/ui/model.go:276`, `internal/ui/model.go:770`, `internal/ui/model.go:853`, `internal/ui/model.go:896`, `internal/ui/model.go:1323`, `internal/ui/model.go:1342`, `internal/ui/model.go:2157`, `internal/ui/model.go:2454`, `internal/ui/model.go:2492`, `internal/ui/model.go:2514`, `internal/ui/model.go:2802`, `internal/ui/model.go:3197`, `internal/ui/actions_test.go`
   Done when:
     - GIVEN a finished worktree agent, feed List returns it in Done.
     - GIVEN Focus(client C) on a running managed agent, the fake tmuxOps records a select of its window for client C and the finished badge clears.
     - GIVEN Focus on a stopped managed agent, the outcome is exactly what Enter does today (window mode: dead pane respawned; session mode: the surviving shell's session is selected — see `internal/tmuxctl/tmux.go:231`), but for client C.
     - GIVEN Focus on a stray, `SelectPaneForClient` gets its pane id.
     - GIVEN a feed-initiated jump that fails, client C receives a tmux display-message with the error.
     - GIVEN the feed socket can't bind, the TUI still starts and logs a warning.
   Touchpoints: `internal/ui/tui.go`, `internal/ui/model.go`, `internal/ui/agents_feed.go`, `internal/ui/agents_feed_test.go`, `internal/ui/actions_test.go`
   Anti-touch: `internal/settings/merge.go`, `internal/workspace/status.go`
   Verification: `make test`
   Independent Test: `go test -race -run Feed ./internal/ui/`; manual: run `cogitator` in tmux, then `go test`-style client or (after step 12) `cogitator pick` from another window lists agents.
   Pre-conditions: Phases 1-2 landed.
   > note: done — commit e96c197. Reuse `requestPanes`/`applyPanes`/`refreshAgents` in step 8. New `launchForClientCmd`/`launchInnerForClient`; `tmuxOps` gained client selects + `DisplayMessage`. List-over-socket asserted via `m.agents`, not a socket round-trip. `make test`: all green except pre-existing `internal/git` `TestPull_AutostashesDirtyTrackedFiles` (local `merge.conflictstyle=diff3`; package untouched since 76b1c7d).
8. [x] Publish `@cog_*` badges and clear "done" when a client focuses an agent
   Outcome: Status lines and window lists can show live agent state via `#{@cog_*}`, and a finished agent stops being "done" as soon as the user looks at it, however they got there.
   Context:
     - Add `FocusPollEvery` (default 2s) to `config.Config` beside the other timings. A tea.Tick at that interval dispatches one background tmux read (panes + client focus + global `@cog_*`, ideally one invocation) → msg → Update refreshes panes/agents (step 7), calls `agents.SeenOnFocus` → `MarkViewed` each, and dispatches `agents.DiffOptions(agents, panes, globals)` → `ApplyOptions` only when non-empty. Because the diff is against what tmux reports, a failed write simply retries next tick against fresh state, and a crashed predecessor's badges/globals are cleared by the first diff.
     - Shutdown: after `Run()` returns in `RunTUI`, synchronously read panes + globals and apply `agents.ClearAll`, bounded by a short timeout (outside Update, so direct is fine; no need for the final model, which `internal/ui/tui.go:104` discards).
     - Killing the TUI's tmux window sends SIGHUP, which Bubble Tea does not handle (only SIGINT/SIGTERM), so cleanup would never run: in `RunTUI` add `signal.Notify` for SIGHUP → `p.Quit()`.
     - Polling over tmux hooks because cogitator is publish-only (never edits tmux config); over a control-mode client because an extra attached client confuses `switch-client` current-client resolution.
     - `--demo` never shells out.
   Read first: `internal/config/config.go:12`, `internal/config/config.go:144`, `internal/ui/model.go:74`, `internal/ui/model.go:2454`, `internal/ui/demo.go:22`, `internal/ui/tui.go:104`, `internal/agents/badges.go`
   Done when:
     - GIVEN an agent turning finished, within one poll its window has `@cog_status done` and `@cog_done` increments.
     - GIVEN a client focusing that window and generating input (client_activity newer than the agent's LastActivity), within one poll the agent shows Idle in the feed and `@cog_status idle`; GIVEN an idle client merely parked there, the agent stays Done.
     - GIVEN no state change across ticks, no `set-option` is issued (fake Runner).
     - GIVEN a clean exit, or SIGHUP from killing the TUI's window, no window retains `@cog_status` and no `@cog_needs/@cog_done/@cog_working` global remains.
   Touchpoints: `internal/config/config.go`, `internal/ui/model.go`, `internal/ui/tui.go`, `internal/ui/agents_badges.go`, `internal/ui/agents_badges_test.go`
   Verification: `make test`
   Independent Test: `go test -race -run 'Badge|Focus' ./internal/ui/`; manual: `tmux list-windows -a -F '#{window_name} #{@cog_status}'` while an agent finishes, then after visiting it.
   > note: done — commit 1299cc6. Poll = 3 sequential tmux reads in one background cmd, gated on `badgeRunner` (set only when feed binds). Shutdown `clearBadges` 2s timeout; SIGHUP → `p.Quit()` (handler not unit-tested). Manual tmux check not yet run.
   > note: follow-up (user picked "clear when you leave") — commit f81ce54: `SeenOnFocus` → `SeenOnLeave`; per-client visits of Done agents, marked seen once the client switches away or detaches while the agent stays on the same finished turn; prompting drops the visit. Jumps no longer MarkViewed while the focus poll runs.

### Phase 4 — Popup and docs · parallel
9. [x] Build the agents list component and `cogitator pick` popup UI
   Outcome: `cogitator pick --client <name>` inside a tmux popup shows every agent grouped Needs you / Done / Working / Idle / Stopped; Enter jumps, `x` marks seen, `/` filters, and the list refreshes live.
   Context:
     - Reusable component `agentlist.go` (step 14 embeds it in the main TUI): rows from `[]agents.Agent`, group headers, cursor pinned to agent ID across re-sorts, `/` fuzzy filter via `fuzzyMatchIndices`, j/k/gg/G, glyphs via `attnLabel`/`worktreeStatusCell` and palette styles so the vocabulary matches the main TUI. Thin program `pick.go`: `RunPick(cfg, logger, client)`.
     - Row: worktree `repo·branch`, workspace `workspace/session`, stray dir basename + `stray`; then harness, reason, relative age.
     - Data: `agentfeed.List` at start (render first reply immediately) and every ~1s via tea.Tick; feed client behind an interface for tests. Enter → `Focus(id, client)` then quit (display-popup -E closes). `x` → `Seen(id)` + refresh. `esc`/`q`/`ctrl+c` quit.
     - ErrNotRunning: show "cogitator isn't running — start it in a tmux window" and quit on any key (exiting at once would close the popup unread).
     - Never acquire the single-instance lock (it would evict the TUI). Lives in `internal/ui` (only bubbletea importer).
   Read first: `internal/ui/model.go:2776`, `internal/ui/model.go:2802`, `internal/ui/render.go:148`, `internal/ui/render.go:971`, `internal/ui/render.go:1228`, `internal/ui/fuzzy.go:39`, `internal/ui/sessionpalette_test.go`, `internal/singleinstance/singleinstance.go:25`, `internal/agentfeed/feed.go`
   Done when:
     - GIVEN a fake feed with agents in every group, the view shows group headers in order with matching glyphs.
     - GIVEN a re-sort while the cursor is on agent X, the cursor stays on X.
     - GIVEN `/` + "api", only matches remain; Enter calls Focus with that id and the client name.
     - GIVEN ErrNotRunning, the not-running message shows and the program waits for a key.
   Touchpoints: `internal/ui/agentlist.go`, `internal/ui/agentlist_test.go`, `internal/ui/pick.go`, `internal/ui/pick_test.go`
   Verification: `go test -race ./internal/ui/`
   Independent Test: `go test -race -run 'AgentList|Pick' ./internal/ui/`; manual after step 12: `tmux display-popup -E -w 70% -h 60% "cogitator pick --client '#{client_name}'"`.
   Pre-conditions: Phases 1-3 landed.
   > note: done — commit a8584b1. `agentList` API: `SetAgents`, `HandleKey` (returns intent + id), `View(now, width, height)`. Reason column derived from attention/group (Agent has no reason field). Local `fitCell`/`renderAgentRow`/`agentName`. Manual popup check pending step 12.
10. [x] Document the tmux integration and new commands
   Outcome: A user copies one tmux.conf block and gets prefix+Space "next", prefix+a popup, a "back" key, status-line counts, and window badges.
   Context: Publish-only — docs carry the snippet. Include: `bind Space run-shell -b 'cogitator next --client "#{client_name}" --window "#{window_id}" --session "#{session_id}"'`; `bind a display-popup -E -w 70% -h 60% -b rounded -T ' agents ' "cogitator pick --client '#{client_name}'"` (tmux ≥ 3.3); "back" = tmux built-ins, no cogitator command: `switch-client -l` in session launch mode (cross-session) and `last-window` in window launch mode — document both; status-right with `#{?@cog_needs,…}` / `#{@cog_done}` / `#{@cog_working}`; a window-status badge from `#{@cog_status}` that appends to the user's existing format rather than replacing it. Requirement: cogitator TUI running in its own tmux window. CLI reference: `pick`, `next`, `--status` reads the feed when the TUI runs. Sidebar entry. Add `internal/agents` and `internal/agentfeed` to the AGENTS.md Layout list.
   Read first: `docs/reference/cli.md`, `docs/guide/key-bindings.md`, `docs/.vitepress/config.mts`, `README.md`, `AGENTS.md`
   Done when:
     - `npm run build` in `docs/` succeeds and the page shows in the Guide sidebar.
     - The snippet loads into a scratch tmux 3.6a server without errors.
   Touchpoints: `docs/guide/tmux.md`, `docs/reference/cli.md`, `docs/.vitepress/config.mts`, `README.md`, `AGENTS.md`
   Verification: `cd docs && npm run build`
   Independent Test: `tmux -L cogtest -f /dev/null new-session -d \; source-file <snippet> ; echo $?` → 0, then `tmux -L cogtest kill-server`.
   > note: done — commit 02e5931. Docs build + scratch-server snippet load both pass (unsandboxed). `pick`/`next`/`--status` documented as planned in steps 11-12; back-key binds not sourced.

### Phase 5 — Entry points · sequential
11. [x] Make `--status` read the feed when the TUI is running
   Outcome: `cogitator --status` prints the same counts the popup shows, instantly and for every harness, whenever the TUI is up.
   Context: Try `agentfeed.List` first and render from `agents.Counts` in the existing glyph order (question, permission, error, finished); on ErrNotRunning fall back to today's mDNS path unchanged. Fixes the opencode-only and cold-start gaps when the TUI runs. `formatStatusLine` has no tests today — add them.
   Read first: `internal/ui/status.go:17`, `internal/ui/status.go:61`, `internal/agents/agents.go`, `internal/agentfeed/feed.go`
   Done when:
     - GIVEN a running feed with one permission + one finished agent, `--status` prints both glyph counts without starting mDNS.
     - GIVEN no feed, output matches today's behaviour.
   Touchpoints: `internal/ui/status.go`, `internal/ui/status_test.go`
   Verification: `go test -race ./internal/ui/`
   Independent Test: `go test -race -run Status ./internal/ui/`
   > note: done — commit 8a7e2f4. Shared `statusCounts.render()` for both paths; non-ErrNotRunning feed errors fall back to mDNS + warn log; empty feed list prints empty line (no fallback).
12. [x] Route `pick` and `next` subcommands in `cmd/cogitator`
   Outcome: `cogitator pick --client X` and `cogitator next --client X --window W --session S` work from tmux bindings.
   Context: Subcommands route before `flag.Parse()` (see the `*-hook` branches); use a per-subcommand `flag.FlagSet`. Log to the file like the TUI — never to the popup. `--client` optional: when empty, resolve via `tmux display-message -p '#{client_name}'` (likewise `--window`/`--session` from `#{window_id}`/`#{session_id}`); verify on 3.6a that `display-popup` expands `#{client_name}` in its command and keep the fallback regardless. Exit codes: `next` exits 0 and prints nothing for jumped / nothing to do / not running (it reports via display-message; `run-shell` would otherwise overlay "'cmd' returned 1" and any output); 2 only for usage errors, which should be visible. `pick` exit code is irrelevant to `display-popup -E`.
   Read first: `cmd/cogitator/main.go:26`, `cmd/cogitator/main.go:90`, `internal/ui/pick.go`, `internal/agentfeed/next.go`
   Done when:
     - GIVEN the TUI running and an agent waiting, `cogitator next --client X --window @3 --session '$1'` lands client X on it.
     - GIVEN the TUI not running, `cogitator next …` shows a tmux message, prints nothing, and exits 0.
     - GIVEN `cogitator pick` without `--client` inside a popup, the jump targets the popup's client.
     - GIVEN `cogitator next --bogus`, usage prints and exit code is 2.
     - `--status`, `--demo`, and hook subcommands are unchanged.
   Touchpoints: `cmd/cogitator/main.go`
   Verification: `make build && go vet ./cmd/...`
   Independent Test: inside tmux with the TUI running: `./cogitator next --client "$(tmux display -p '#{client_name}')" --window "$(tmux display -p '#{window_id}')" --session "$(tmux display -p '#{session_id}')"; echo $?` and `tmux display-popup -E "./cogitator pick"`; paste results into the commit message.
   > note: done — commit cda52fa. Verified `next --bogus` → usage + exit 2; `next` (no flags) → silent exit 0. NOT verified: real jump with TUI running, popup `pick`, `#{client_name}` expansion in display-popup. `tmuxDisplayValue` in main.go could move to tmuxctl later.

### Phase 6 — Popup preview (nice-to-have) · sequential
13. [x] Add a preview pane to `cogitator pick`
   Outcome: The popup shows why the selected agent is waiting and the last lines of its pane, so the user can often decide without jumping.
   Context: Optional — if it grows past a capture helper + one render region, add `> note: deferred — <reason>` and skip. `tmuxctl.CapturePane(r, target, lines)` via `capture-pane -p -J -S -<n> -t`; target = PaneID (strays) or WindowID (active pane). Fetch async on cursor move with a short debounce; never block render. `p` toggles; default on when popup width ≥ 100 cols. Reason line from Attention. Captured text is display-only — never used to infer state.
   Read first: `internal/ui/pick.go`, `internal/ui/agentlist.go`, `internal/tmuxctl/tmux.go`
   Done when:
     - GIVEN the cursor on a permission agent, the preview shows "needs permission" and the pane tail.
     - GIVEN rapid cursor moves, at most one capture is in flight and the final selection wins.
     - GIVEN `p`, the preview hides and the list widens.
   Touchpoints: `internal/ui/pick.go`, `internal/ui/pick_test.go`, `internal/tmuxctl/tmux.go`, `internal/tmuxctl/tmux_test.go`
   Verification: `go test -race ./internal/ui/ ./internal/tmuxctl/`
   Independent Test: `go test -race -run 'Preview|Capture' ./internal/ui/ ./internal/tmuxctl/`
   > note: done — commit 695bb91. Preview = right half when shown; 150ms debounce + seq token, one capture in flight; ANSI stripped; strays by PaneID, others by WindowID.

### Phase 7 — Option C prototype · sequential
14. [x] Prototype an attention-first "Agents" tab in the main TUI
   Outcome: The user can Tab to an "Agents" view in cogitator showing the popup's grouped list, jump with Enter, and judge whether it should replace the Repos/Workspaces split.
   Context: Tests the "split by job (triage vs manage)" idea. Embed step 9's `agentlist` fed from the model's in-process agents (no socket). Enter uses step 7's focus path with client "" (the TUI's own client); `x` seen; `/` filter. Add `viewAgents` to `viewMode` without changing the zero value (tests rely on `viewSessions` = 0) and to `viewTabs`; Tab cycles all three; default stays Workspaces. Update help + hint bar. Management keys stay in Repos/Workspaces. Record what felt missing as `> note:` lines here to seed a follow-up redesign plan.
   Read first: `internal/ui/model.go:44`, `internal/ui/model.go:1860`, `internal/ui/chrome.go:23`, `internal/ui/render.go`, `internal/ui/agentlist.go`, `docs/guide/key-bindings.md`
   Done when:
     - GIVEN the TUI open, Tab cycles Workspaces → Repos → Agents → Workspaces and the header shows all three tabs.
     - GIVEN a Done agent in Agents, Enter jumps to it and it moves to Idle.
     - Repos/Workspaces key handling and existing tests are unchanged.
   Touchpoints: `internal/ui/model.go`, `internal/ui/chrome.go`, `internal/ui/render.go`, `internal/ui/agents_view.go`, `internal/ui/agents_view_test.go`, `docs/guide/key-bindings.md`
   Verification: `make ci`
   Independent Test: `go test -race -run 'AgentsView|Tab' ./internal/ui/`; manual: `make run`, Tab to Agents.
   > note: done — commit bda854e. Also edited `internal/ui/workspace_view_test.go` (3 Tab-cycle tests hard-coded the 2-view toggle). `make ci` stops at lint (golangci-lint not installed); vet + tests + linux build pass apart from pre-existing `internal/git` failure. Agents tab is empty when feed isn't bound (`refreshAgents` returns early on `m.feed == nil`, e.g. `--demo`).
   > note: missing — no per-group counts / "N need you" in header; no cross-tab attention indicator or default landing on Agents when something needs you.
   > note: missing — no preview of what the agent is asking; reasons are one word; the popup's pane preview isn't reused here.
   > note: missing — Enter gives no feedback on which agent was focused; no n/N "next needing attention" key; no bulk mark-seen per group.
   > note: missing — no launch/create/manage from this tab; stopped rows don't signal that Enter resumes.
   > note: missing — strays distinguished only by a " stray" suffix.
   > note: follow-up redesign (user picked "list + live preview") — commit 73b9694: Repos-style rows (glyph, repo·branch, muted title, harness, right-aligned age), coloured group headers, selection band; list beside a live pane preview (shared `agentPreview`, 150ms debounce + 1s refresh) in both the Agents tab and the popup, list-only below 100 cols. ctrl+P scoped to the active tab in 3652647 (+ aacdf2f preview follow); docs in 4ebb226, 34e398a.

### Phase 8 — tmux installer · sequential
15. [x] Add `cogitator tmux install` / `uninstall` to wire the bindings into the user's tmux config
   Outcome: One command makes prefix+Space (next) and prefix+a (pick) permanent and live immediately; one command removes them.
   Context:
     - The user reversed publish-only for bindings only: cogitator may edit tmux config, but only through this explicit command, never at runtime, and never formats/themes (PowerKit etc. own `status-right`/`window-status-format`; badges stay a documented snippet).
     - New seam package `internal/tmuxinstall` (no ui/bubbletea imports); tmux calls go through the `tmuxctl.Runner` seam so tests use fakes.
     - Owned file `cogitator/tmux.conf` under cogitator's config dir (reuse however `internal/settings`/`internal/workspace` resolve it; typically `~/.config/cogitator/`): a "managed by cogitator — regenerate with `cogitator tmux install`" header, then the two bindings from `docs/guide/tmux.md` (same flags, same popup size), with the binary as an absolute path shell-quoted so a path containing spaces works. Binary = `--binary`, else `exec.LookPath("cogitator")`, else `os.Executable()`; reject a `go run` temp-dir binary with a clear error.
     - User tmux.conf = `--config`, else tmux's lookup order: `~/.tmux.conf`, `$XDG_CONFIG_HOME/tmux/tmux.conf`, `~/.config/tmux/tmux.conf`; first that exists; none → create `~/.tmux.conf`. Resolve symlinks before writing — the user's tmux.conf is a symlink into a dotfiles repo, and temp+rename on the link path would replace the symlink with a regular file. Preserve file mode.
     - Install appends `source-file -q <owned path>` at the end only when no line already references the owned path (idempotent; re-running rewrites only the owned file). Uninstall removes that line and the owned file; all other tmux.conf bytes stay identical.
     - Live apply: when tmux is available, install runs `source-file <owned>` so the bindings work immediately; uninstall unbinds the keys in the running server only if their current binding runs cogitator (`list-keys -T prefix <key>`). `--no-reload` skips touching the running server.
     - Conflicts: warn when a key is already bound in the running server to something other than cogitator, then install anyway (explicit user choice).
     - Flags: `--next-key` (default `Space`), `--pick-key` (default `a`), `--config`, `--binary`, `--dry-run` (print the owned file and the tmux.conf change, write nothing), `--no-reload`. Route `cogitator tmux install|uninstall` in `cmd/cogitator/main.go` like `pick`/`next` (own FlagSet; usage errors exit 2).
     - Output: unlike `next`, this runs from a terminal — print what changed (paths, keys) to stdout, errors to stderr with a non-zero exit.
   Read first: `cmd/cogitator/main.go`, `internal/tmuxctl/tmux.go:68`, `docs/guide/tmux.md`, `internal/settings/`, `internal/workspace/store.go`
   Done when:
     - GIVEN a temp HOME whose `~/.tmux.conf` is a symlink, install writes the owned file, appends exactly one `source-file -q` line to the symlink target, and `~/.tmux.conf` is still a symlink.
     - GIVEN install run twice, tmux.conf contains the line once.
     - GIVEN no tmux.conf anywhere, install creates `~/.tmux.conf` containing the line.
     - GIVEN `--next-key n --pick-key A`, the owned file binds those keys.
     - GIVEN a binary path containing a space, the owned file sources into a scratch tmux server without error and the bound command names the full path.
     - GIVEN uninstall, the line and the owned file are gone and the rest of tmux.conf is byte-identical.
     - GIVEN `--dry-run`, no file changes.
   Touchpoints: `internal/tmuxinstall/install.go`, `internal/tmuxinstall/install_test.go`, `cmd/cogitator/main.go`
   Anti-touch: the user's real `~/.tmux.conf`, `~/.dotfiles/**`, the real `~/.config/cogitator/tmux.conf`, and the user's default tmux server — tests and manual checks use temp HOME/XDG dirs, `--config`, `--no-reload`, and `tmux -L cogtest`.
   Verification: `go vet ./... && go test -race ./internal/tmuxinstall/ && make build`
   Independent Test: `go test -race ./internal/tmuxinstall/`; then in a temp dir: `HOME=$tmp XDG_CONFIG_HOME=$tmp/.config ./cogitator tmux install --config $tmp/tmux.conf --no-reload --dry-run`, the same without `--dry-run`, then `tmux -L cogtest -f $tmp/tmux.conf new-session -d \; list-keys -T prefix Space` shows the cogitator binding, then `tmux -L cogtest kill-server`.
   > note: done — commit bd79720. Owned file = `dir(settings.ConfigPath())/tmux.conf`. "Runs cogitator" detected via `next --client`/`pick --client` in list-keys. Uninstall needs the same `--config` as install. A tmux.conf without trailing newline gains one (user's file already ends in `\n`).
16. [x] Document `cogitator tmux install` and make the manual snippets reload-safe
   Outcome: The tmux guide leads with one install command, and the badge/status snippets no longer duplicate on every config reload.
   Context:
     - Setup: `cogitator tmux install` first (what it writes: an owned file + one `source-file -q` line; idempotent; live immediately; flags; `uninstall`; `--dry-run`); keep the manual bindings snippet as an alternative.
     - Bug: the current snippet uses `set -ga window-status-format …`, which appends again on every `source-file ~/.tmux.conf`. Replace it with full `set -g` formats built from tmux's default (`#I:#W#{?window_flags,#{window_flags}, }`) plus the badge, and a full `status-right` example; tell users to merge into their own format.
     - Themes own window formats: add a short "Themes" subsection with a PowerKit example (`@powerkit_active_window_title` / `@powerkit_inactive_window_title` containing `#{?@cog_status, [#{@cog_status}],}`, set before `run tpm`) and the general rule: put `#{@cog_status}` in whatever window-title option the theme exposes.
     - Back key: say many users already bind `switch-client -l`; the installer doesn't add one.
     - Replace "cogitator never edits your tmux config" with "only when you run `cogitator tmux install`". CLI reference: `tmux install|uninstall` flags. AGENTS.md Layout: `internal/tmuxinstall`.
   Read first: `docs/guide/tmux.md`, `docs/reference/cli.md`, `AGENTS.md`, `internal/tmuxinstall/install.go`, `cmd/cogitator/main.go`
   Done when:
     - `npm run build` in `docs/` passes.
     - Sourcing the guide's manual snippet twice into a scratch tmux server leaves exactly one badge in `window-status-format`.
     - The flags in the docs match `cogitator tmux install -h`.
   Touchpoints: `docs/guide/tmux.md`, `docs/reference/cli.md`, `AGENTS.md`
   Verification: `cd docs && npm run build`
   Independent Test: `tmux -L cogtest -f /dev/null new-session -d \; source-file $snip \; source-file $snip \; show -gv window-status-format` → exactly one `@cog_status`; then `tmux -L cogtest kill-server`.
   > note: done — commit 55e0d54. Double-source leaves formats identical; docs build passes; flag tables match `-h`.

## Acceptance criteria
- [ ] `make ci` (vet, lint, `go test -race`, cross-build) passes after every phase.
- [ ] `internal/ui` remains the only bubbletea importer; `internal/agents`, `internal/agentfeed`, `internal/hookipc`, `internal/tmuxctl` import neither bubbletea nor `internal/ui`.
- [ ] No tmux/git shell-out runs on the Bubble Tea Update goroutine.
- [ ] All path comparisons go through `pathnorm.Canonical`.
- [ ] No new persisted files; `roster.json`, `workspaces.json`, `config.json` schemas unchanged.
- [ ] At runtime cogitator touches tmux state only via `@cog_*` user options it owns and unsets them on clean exit. tmux config and bindings change only via explicit `cogitator tmux install`/`uninstall` (one `source-file` line + a cogitator-owned file, reversible); themes and status/window formats are never edited.
- [ ] `pick`, `next`, and `--status` never take the single-instance lock and degrade gracefully when the TUI isn't running.
- [ ] Feed socket is owner-only (0600) in the runtime dir.
- [ ] Repos/Workspaces/ctrl+P behaviour unchanged, apart from step 14's added tab.

## File touchpoints
- `internal/agents/agents.go` — new — unified projection, ordering, Next, Counts.
- `internal/agents/badges.go` — new — desired-vs-actual option diff, ClearAll, focus-seen.
- `internal/hookipc/hookipc.go:82` — update — ListenRPC/Call over shared bind helper.
- `internal/tmuxctl/tmux.go:517` — update — client-targeted select, ListPanes, ListClientFocus, ReadGlobalOptions, ApplyOptions, DisplayMessage, CapturePane.
- `internal/agentfeed/feed.go` — new — protocol, Holder, Serve, client.
- `internal/agentfeed/next.go` — new — RunNext.
- `internal/ui/tui.go:21` — update — start feed, SIGHUP → quit, shutdown ClearAll.
- `internal/ui/model.go:276` — update — tmuxOps client selects, panes/agents state, feed actions, focus poll.
- `internal/ui/agents_feed.go` — new — feed action handling.
- `internal/ui/agents_badges.go` — new — badge publish + focus-ack cmds.
- `internal/ui/agentlist.go` — new — shared grouped agents list component.
- `internal/ui/pick.go` — new — popup program (+ preview).
- `internal/ui/status.go:17` — update — feed-first `--status`.
- `internal/ui/agents_view.go` — new — Agents tab prototype.
- `internal/ui/chrome.go:23` — update — third tab.
- `internal/config/config.go:144` — update — FocusPollEvery.
- `cmd/cogitator/main.go:26` — update — pick/next routing.
- `docs/guide/tmux.md` — new — tmux integration guide.
- `docs/reference/cli.md` — update — pick, next, --status.
- `docs/.vitepress/config.mts` — update — sidebar.
- `README.md`, `AGENTS.md` — update — brief mention, Layout entries.
- `internal/tmuxinstall/install.go` — new — render owned bindings file, edit tmux.conf source line, install/uninstall.
- `cmd/cogitator/main.go:26` — update — `tmux install|uninstall` routing.
- `docs/guide/tmux.md` — update — install-first setup, reload-safe snippets, theme (PowerKit) example.
