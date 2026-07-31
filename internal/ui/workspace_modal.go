package ui

// workspace_modal.go — the repo-membership modal ('e' on a workspace in the
// Workspaces view): a fresh $HOME scan offers not-yet-member repos to add,
// combined with the workspace's current members offered for removal, in one
// fuzzy-filterable list (mirroring renderRepoFinder's/scanReposCmd's shape,
// repofinder.go, but keyed on workspace membership rather than
// settings-configured repos). Committing either action persists it
// immediately (Store.AttachRepo/DetachRepo) and closes the modal — this file
// changes membership only. Backfilling that change into the workspace's
// existing sessions is step 15's job (workspace_backfill.go), reached only
// through the membershipChangedMsg emitted below and handled in model.go;
// nothing in this file consumes it. Kept separate from workspace_view.go
// (pure navigation) and workspace_cmd.go (session creation), per the phase
// convention that every workspace mode routes its key handling through its
// own method rather than adding arms to Update.

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/guilhermehto/cogitator/internal/git"
	"github.com/guilhermehto/cogitator/internal/settings"
	"github.com/guilhermehto/cogitator/internal/workspace"
)

// wsModalEntry is one row in the repo-membership modal's combined list: an
// existing member of the target workspace (member == true, offered for
// removal) or a freshly scanned candidate that is not yet a member (offered
// for addition).
type wsModalEntry struct {
	path   string
	member bool
}

// membershipChangedMsg reports a committed workspace-membership change: repo
// was attached to (attached == true) or detached from (attached == false)
// workspace. This is the seam step 15 (workspace_backfill.go) uses to offer
// backfilling the change into the workspace's existing sessions — that
// handler lives in model.go, not here; nothing consumes this message yet.
type membershipChangedMsg struct {
	workspace string
	repo      string
	attached  bool
}

// wsModalActionErrMsg reports a failed attach/detach commit: an invalid
// candidate (git.RepoRoot failure, a hidden basename, or a basename
// collision with an existing member) or a store failure. Kept distinct from
// membershipChangedMsg so that message's shape stays exactly
// {workspace, repo, attached bool} for step 15's handler.
type wsModalActionErrMsg struct {
	err error
}

// updateWorkspaceModal handles 'e' in the Workspaces view: it opens the
// repo-membership modal for the workspace under the cursor and kicks off a
// fresh $HOME scan. Tried from the idle-prompt Workspaces-view routing chain
// in model.go, mirroring updateWorkspaceLifecycle/Delete/Launch. Returns
// handled=false for any other key, or when there is no workspace under the
// cursor (an empty list), so the caller falls through to
// updateWorkspaceView. Once the modal is open, its keys
// (esc/enter/arrows/filter typing) are handled by updateWorkspaceModalActive
// instead, reached via the promptWorkspaceModal case in Update's prompt
// pre-empt switch (model.go), mirroring updateSettings.
func (m model) updateWorkspaceModal(msg tea.KeyMsg) (model, tea.Cmd, bool) {
	if msg.String() != "e" {
		return m, nil, false
	}
	ws, ok := m.wsUnderCursor()
	if !ok {
		return m, nil, false
	}
	return m.openWorkspaceModal(ws)
}

// openWorkspaceModal resets the modal state for ws and dispatches the
// background scan.
func (m model) openWorkspaceModal(ws workspace.Workspace) (model, tea.Cmd, bool) {
	m.wsModalWorkspace = ws.Name
	m.wsModalScanning = true
	m.wsModalEntries = nil
	m.wsModalMatches = nil
	m.wsModalCursor = 0
	m.wsModalErr = ""
	m.prompt = promptWorkspaceModal
	m.input.Placeholder = "filter repos"
	m.input.SetValue("")
	cmd := scanWorkspaceModalCmd(repoFinderRoot(), ws.Name, memberPaths(ws.Members), m.workspaceRoot)
	return m, tea.Batch(m.input.Focus(), cmd), true
}

// memberPaths extracts the canonical paths from members, in order.
func memberPaths(members []workspace.MemberRepo) []string {
	paths := make([]string, len(members))
	for i, mem := range members {
		paths[i] = mem.Path
	}
	return paths
}

// updateWorkspaceModalActive handles every key while the repo-membership
// modal is open: enter commits the highlighted row (attaching a candidate or
// detaching a member), the arrow keys (and ctrl+n/p) move the selection, esc
// cancels with no change, and everything else edits the filter query and
// re-ranks matches — mirroring promptAddRepo's embedded-finder key handling
// (model.go), applied to the combined member+candidate list.
func (m model) updateWorkspaceModalActive(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.closeWorkspaceModal()
		return m, nil
	case "enter":
		if len(m.wsModalMatches) == 0 {
			return m, nil
		}
		sel := m.wsModalEntries[m.wsModalMatches[clampIndex(m.wsModalCursor, len(m.wsModalMatches))]]
		workspaceName := m.wsModalWorkspace
		members := memberEntryPaths(m.wsModalEntries)
		m.closeWorkspaceModal()
		if sel.member {
			return m, detachWorkspaceRepoCmd(m.store, workspaceName, sel.path)
		}
		return m, attachWorkspaceRepoCmd(m.store, workspaceName, sel.path, members)
	case "up", "ctrl+p":
		m.wsModalCursor = clampIndex(m.wsModalCursor-1, len(m.wsModalMatches))
		return m, nil
	case "down", "ctrl+n":
		m.wsModalCursor = clampIndex(m.wsModalCursor+1, len(m.wsModalMatches))
		return m, nil
	default:
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.wsModalMatches = fuzzyMatchIndices(m.input.Value(), wsModalEntryPaths(m.wsModalEntries))
		m.wsModalCursor = clampIndex(m.wsModalCursor, len(m.wsModalMatches))
		return m, cmd
	}
}

// closeWorkspaceModal resets the modal back to the idle state, mirroring
// closeRepoFinder (model.go).
func (m *model) closeWorkspaceModal() {
	m.prompt = promptIdle
	m.wsModalWorkspace = ""
	m.wsModalScanning = false
	m.wsModalEntries = nil
	m.wsModalMatches = nil
	m.wsModalCursor = 0
	m.wsModalErr = ""
	m.input.Blur()
	m.input.SetValue("")
}

// wsModalEntryPaths extracts entries' paths, in order, for fuzzy ranking.
func wsModalEntryPaths(entries []wsModalEntry) []string {
	paths := make([]string, len(entries))
	for i, e := range entries {
		paths[i] = e.path
	}
	return paths
}

// memberEntryPaths returns the paths of entries currently flagged as
// members, used as the existing-membership set for a candidate's
// basename-collision pre-flight (attachWorkspaceRepoCmd).
func memberEntryPaths(entries []wsModalEntry) []string {
	var out []string
	for _, e := range entries {
		if e.member {
			out = append(out, e.path)
		}
	}
	return out
}

// wsModalScanMsg carries the result of the background repo scan started when
// the repo-membership modal opens. workspace guards against a stale result
// landing after the modal was closed, or reopened for a different workspace,
// in the meantime. entries is the combined, alphabetically sorted
// member+candidate set; err is set when the scan itself failed.
type wsModalScanMsg struct {
	workspace string
	entries   []wsModalEntry
	err       error
}

// scanWorkspaceModalCmd discovers git repositories under root off the UI
// goroutine and combines them with workspaceName's current members into one
// list for the repo-membership modal. Two exclusions keep the discovered set
// to genuine candidates: members drops repos already attached to the
// workspace (filterConfigured, repofinder.go — shared with the 'A' finder's
// settings-configured exclusion), and workspaceRoot drops anything under the
// resolved workspace root, since a member's own worktree there contains a
// `.git` *file* and would otherwise be rediscovered as a spurious candidate.
func scanWorkspaceModalCmd(root, workspaceName string, members []string, workspaceRoot string) tea.Cmd {
	return func() tea.Msg {
		discovered, err := settings.DiscoverRepos(root)
		if err != nil {
			return wsModalScanMsg{workspace: workspaceName, err: err}
		}
		discovered = excludeWorkspaceRootSubtree(discovered, workspaceRoot)
		candidates := filterConfigured(discovered, members)

		entries := make([]wsModalEntry, 0, len(candidates)+len(members))
		for _, p := range members {
			entries = append(entries, wsModalEntry{path: p, member: true})
		}
		for _, p := range candidates {
			entries = append(entries, wsModalEntry{path: p})
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
		return wsModalScanMsg{workspace: workspaceName, entries: entries}
	}
}

// excludeWorkspaceRootSubtree drops any discovered path that falls under
// workspaceRoot (settings.PathUnderRoot, the same segment-boundary
// path-prefix test settings.ResolveWorkspaceRoot's callers already use). An
// empty workspaceRoot (not yet resolved) is a no-op.
func excludeWorkspaceRootSubtree(paths []string, workspaceRoot string) []string {
	if workspaceRoot == "" {
		return paths
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if !settings.PathUnderRoot(workspaceRoot, p) {
			out = append(out, p)
		}
	}
	return out
}

// attachWorkspaceRepoCmd validates path as a candidate member repo and, when
// valid, persists it as a new member of workspaceName via the store. It runs
// off the UI goroutine because it shells out to git (git.RepoRoot) and writes
// workspaces.json. members is the workspace's current member paths, checked
// against path's resolved root with the same basename-collision and
// hidden-basename pre-flight AssembleMember (internal/workspace/assemble.go)
// runs before adding a worktree — catching it here means a doomed-to-fail
// member is refused at attach time instead of only surfacing when a session
// is next assembled.
func attachWorkspaceRepoCmd(store storeOps, workspaceName, path string, members []string) tea.Cmd {
	return func() tea.Msg {
		repoRoot, err := git.RepoRoot(path)
		if err != nil {
			return wsModalActionErrMsg{err: err}
		}
		roots := append(append([]string{}, members...), repoRoot)
		if err := workspace.CheckBasenameCollisions(roots); err != nil {
			return wsModalActionErrMsg{err: err}
		}
		if _, err := workspace.MemberDirName(repoRoot); err != nil {
			return wsModalActionErrMsg{err: err}
		}
		if store == nil {
			return wsModalActionErrMsg{err: fmt.Errorf("workspace store is not available")}
		}
		if err := store.AttachRepo(workspaceName, repoRoot); err != nil {
			return wsModalActionErrMsg{err: err}
		}
		return membershipChangedMsg{workspace: workspaceName, repo: repoRoot, attached: true}
	}
}

// detachWorkspaceRepoCmd removes path from workspaceName's membership via the
// store. It only forgets the repo; nothing on disk is touched, mirroring
// removeRepoCmd (repofinder.go).
func detachWorkspaceRepoCmd(store storeOps, workspaceName, path string) tea.Cmd {
	return func() tea.Msg {
		if store == nil {
			return wsModalActionErrMsg{err: fmt.Errorf("workspace store is not available")}
		}
		if err := store.DetachRepo(workspaceName, path); err != nil {
			return wsModalActionErrMsg{err: err}
		}
		return membershipChangedMsg{workspace: workspaceName, repo: path, attached: false}
	}
}

// renderWorkspaceModal renders the floating repo-membership box shown while
// prompt == promptWorkspaceModal, composited (centred) over the Workspaces
// view by View via overlayBox (render.go) — mirroring renderWsNamePrompt/
// renderWsDeleteConfirm (workspace_cmd.go/workspace_delete.go) rather than
// renderRepoFinder's full-pane takeover, since the Workspaces view keeps
// rendering behind it. The query line, scanning indicator, empty states, and
// windowed/cursor-highlighted list mirror renderRepoFinder's structure
// (render.go) — the same embedded fuzzy-finder shape, applied to a combined
// member+candidate list instead of a single candidate list. fieldW/fieldH
// are the Workspaces pane's inner dimensions, used to cap the box width and
// window the list so the whole modal fits the pane.
func (m model) renderWorkspaceModal(fieldW, fieldH int) string {
	contentW := fieldW - 10
	if contentW > 72 {
		contentW = 72
	}
	if contentW < 20 {
		contentW = max(1, fieldW-4)
	}

	var b strings.Builder
	b.WriteString(headerStyle.Render("Repo membership: " + m.wsModalWorkspace))
	b.WriteString("\n" + dimStyle.Render("filter > ") + m.input.View())

	switch {
	case m.wsModalErr != "":
		b.WriteString("\n" + wtHintStyle.Render(m.wsModalErr))
		return paletteBoxStyle.Render(b.String())
	case m.wsModalScanning:
		b.WriteString("\n" + dimStyle.Render("scanning "+shortenDirectory(repoFinderRoot())+" …"))
		return paletteBoxStyle.Render(b.String())
	case len(m.wsModalMatches) == 0:
		if len(m.wsModalEntries) == 0 {
			b.WriteString("\n" + dimStyle.Render("no git repositories found under "+shortenDirectory(repoFinderRoot())))
		} else {
			b.WriteString("\n" + dimStyle.Render("no match"))
		}
		return paletteBoxStyle.Render(b.String())
	}

	listH := max(1, fieldH-8)
	cursor := clampIndex(m.wsModalCursor, len(m.wsModalMatches))
	start := 0
	if cursor >= listH {
		start = cursor - listH + 1
	}
	end := min(start+listH, len(m.wsModalMatches))

	for i := start; i < end; i++ {
		entry := m.wsModalEntries[m.wsModalMatches[i]]
		tag := "add"
		if entry.member {
			tag = "member"
		}
		line := ansi.Truncate(fmt.Sprintf("  [%s] %s", tag, shortenDirectory(entry.path)), contentW, "…")
		if i == cursor {
			line = wtCursorStyle.Render(line)
		}
		b.WriteString("\n" + line)
	}

	b.WriteString("\n" + dimStyle.Render(fmt.Sprintf("%d repos · ↑↓ move · enter attach/detach · esc cancel", len(m.wsModalMatches))))
	return paletteBoxStyle.Render(b.String())
}
