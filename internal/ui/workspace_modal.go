package ui

// workspace_modal.go — the repo-membership modal ('e' on a workspace in the
// Workspaces view): a fresh $HOME scan offers not-yet-member repos to add,
// listed after the workspace's current members (offered for removal), in one
// fuzzy-filterable checklist (mirroring renderRepoFinder's/scanReposCmd's
// shape, repofinder.go, but keyed on workspace membership rather than
// settings-configured repos). Committing either action persists it
// immediately (Store.AttachRepo/DetachRepo). For a workspace without
// sessions the modal stays open so several repos can be toggled in one go;
// the outcome lands back here via applyWsModalCommit/failWsModalCommit. For a
// workspace with sessions it closes, because backfilling the change into
// those sessions is workspace_backfill.go's job, reached through the
// membershipChangedMsg emitted below and handled in model.go. Kept separate
// from workspace_view.go (pure navigation) and workspace_cmd.go (session
// creation), per the phase convention that every workspace mode routes its
// key handling through its own method rather than adding arms to Update.

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
	m.closeWorkspaceModal()
	m.wsModalWorkspace = ws.Name
	m.wsModalScanning = true
	m.prompt = promptWorkspaceModal
	m.input.Placeholder = "filter repos"
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
// modal is open: enter toggles the highlighted row (attaching a candidate or
// detaching a member), the arrow keys (and ctrl+n/p) move the selection, esc
// closes, and everything else edits the filter query and re-ranks matches —
// mirroring promptAddRepo's embedded-finder key handling (model.go), applied
// to the combined member+candidate list.
func (m model) updateWorkspaceModalActive(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.closeWorkspaceModal()
		return m, nil
	case "enter":
		return m.toggleWsModalSelection()
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

// toggleWsModalSelection commits the highlighted row: detach for a member,
// attach for a candidate. A workspace with sessions closes the modal so the
// backfill prompt can ask which sessions follow the change; otherwise the
// modal stays open with the row marked busy until the commit lands. Ignored
// while a previous commit is still in flight.
func (m model) toggleWsModalSelection() (tea.Model, tea.Cmd) {
	if len(m.wsModalMatches) == 0 || m.wsModalBusy != "" {
		return m, nil
	}
	sel := m.wsModalEntries[m.wsModalMatches[clampIndex(m.wsModalCursor, len(m.wsModalMatches))]]
	workspaceName := m.wsModalWorkspace

	var cmd tea.Cmd
	if sel.member {
		cmd = detachWorkspaceRepoCmd(m.store, workspaceName, sel.path)
	} else {
		cmd = attachWorkspaceRepoCmd(m.store, workspaceName, sel.path, memberEntryPaths(m.wsModalEntries))
	}

	if m.wsModalHandsOffToBackfill() {
		m.closeWorkspaceModal()
		return m, cmd
	}
	m.wsModalBusy = sel.path
	m.wsModalNotice = ""
	m.wsModalNoticeErr = false
	return m, cmd
}

// wsModalHandsOffToBackfill reports whether committing a change must close
// the modal: the target workspace has sessions that may need backfilling.
func (m model) wsModalHandsOffToBackfill() bool {
	return len(workspaceSessionNames(m.wsStatuses, m.wsModalWorkspace)) > 0
}

// wsModalAwaitingCommit reports whether the open modal dispatched the
// attach/detach whose outcome is arriving, so it should be shown in place.
func (m model) wsModalAwaitingCommit() bool {
	return m.prompt == promptWorkspaceModal && m.wsModalBusy != ""
}

// applyWsModalCommit flips the committed row in place — keeping its position
// so the list does not jump under the cursor — and reloads the Workspaces
// view behind the modal. The attach result carries the resolved repo root,
// which becomes the row's path so a later detach names the stored member.
func (m model) applyWsModalCommit(msg membershipChangedMsg) (model, tea.Cmd) {
	for i := range m.wsModalEntries {
		if m.wsModalEntries[i].path == m.wsModalBusy {
			m.wsModalEntries[i] = wsModalEntry{path: msg.repo, member: msg.attached}
		}
	}
	selected := -1
	if len(m.wsModalMatches) > 0 {
		selected = m.wsModalMatches[clampIndex(m.wsModalCursor, len(m.wsModalMatches))]
	}
	m.wsModalMatches = fuzzyMatchIndices(m.input.Value(), wsModalEntryPaths(m.wsModalEntries))
	for i, idx := range m.wsModalMatches {
		if idx == selected {
			m.wsModalCursor = i
		}
	}
	m.wsModalCursor = clampIndex(m.wsModalCursor, len(m.wsModalMatches))

	verb := "removed"
	if msg.attached {
		verb = "added"
	}
	m.wsModalBusy = ""
	m.wsModalNotice = fmt.Sprintf("%s %s", verb, filepath.Base(msg.repo))
	m.wsModalNoticeErr = false
	return m.reloadWsStatuses()
}

// failWsModalCommit reports a failed attach/detach inside the open modal and
// frees the row for another attempt.
func (m model) failWsModalCommit(err error) model {
	m.wsModalBusy = ""
	m.wsModalNotice = err.Error()
	m.wsModalNoticeErr = true
	return m
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
	m.wsModalBusy = ""
	m.wsModalNotice = ""
	m.wsModalNoticeErr = false
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
// in the meantime. entries is the member+candidate set ordered by
// sortWsModalEntries; err is set when the scan itself failed.
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
		sortWsModalEntries(entries)
		return wsModalScanMsg{workspace: workspaceName, entries: entries}
	}
}

// sortWsModalEntries orders the checklist members first, then by repo
// basename (what the modal shows most prominently), then by full path.
func sortWsModalEntries(entries []wsModalEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.member != b.member {
			return a.member
		}
		if ab, bb := filepath.Base(a.path), filepath.Base(b.path); ab != bb {
			return ab < bb
		}
		return a.path < b.path
	})
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
	return workspaceMutationCmd(func() tea.Msg {
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
	})
}

// detachWorkspaceRepoCmd removes path from workspaceName's membership via the
// store. It only forgets the repo; nothing on disk is touched, mirroring
// removeRepoCmd (repofinder.go).
func detachWorkspaceRepoCmd(store storeOps, workspaceName, path string) tea.Cmd {
	return workspaceMutationCmd(func() tea.Msg {
		if store == nil {
			return wsModalActionErrMsg{err: fmt.Errorf("workspace store is not available")}
		}
		if err := store.DetachRepo(workspaceName, path); err != nil {
			return wsModalActionErrMsg{err: err}
		}
		return membershipChangedMsg{workspace: workspaceName, repo: path, attached: false}
	})
}

// wsModalMemberStyle colours the checked marker of an attached repo.
var wsModalMemberStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("78")).Bold(true)

// renderWorkspaceModal renders the floating repo-membership checklist shown
// while prompt == promptWorkspaceModal, composited (centred) over the
// Workspaces view by View via overlayBox (render.go). Each row is a checkbox
// (● attached, ○ not), the repo name with the filter's matched characters
// highlighted, and its parent directory. The box keeps a fixed size while
// the filter narrows results so it does not jump around. fieldW/fieldH are
// the Workspaces pane's dimensions, used to cap the box width and window the
// list so the whole modal fits the pane.
func (m model) renderWorkspaceModal(fieldW, fieldH int) string {
	contentW := min(72, max(20, fieldW-8))

	attached := len(memberEntryPaths(m.wsModalEntries))
	title := headerStyle.Render("Repos in " + m.wsModalWorkspace)
	count := dimStyle.Render(pluralize(attached, "repo") + " attached")
	in := m.input
	in.Width = max(1, contentW-3)

	lines := []string{
		title + strings.Repeat(" ", max(1, contentW-lipgloss.Width(title)-lipgloss.Width(count))) + count,
		"",
		promptMarker() + in.View(),
		"",
	}

	const chromeLines = 9 // title, blank, filter, blank, blank, notice, footer + border
	listH := max(1, min(len(m.wsModalEntries), fieldH-chromeLines))
	body := m.wsModalBodyLines(contentW, listH)
	for len(body) < listH {
		body = append(body, "")
	}
	lines = append(lines, body...)

	lines = append(lines, "", m.wsModalNoticeLine(), dimStyle.Render(m.wsModalFooter()))
	for i, line := range lines {
		lines[i] = padToWidth(ansi.Truncate(line, contentW, "…"), contentW)
	}
	return modalBoxStyle.Render(strings.Join(lines, "\n"))
}

// wsModalBodyLines renders the list area: a status line while scanning or
// when nothing matches, otherwise the cursor-windowed checklist rows.
func (m model) wsModalBodyLines(contentW, listH int) []string {
	switch {
	case m.wsModalErr != "":
		return []string{wtHintStyle.Render(m.wsModalErr)}
	case m.wsModalScanning:
		return []string{dimStyle.Render("scanning " + shortenDirectory(repoFinderRoot()) + " …")}
	case len(m.wsModalEntries) == 0:
		return []string{dimStyle.Render("no git repositories found under " + shortenDirectory(repoFinderRoot()))}
	case len(m.wsModalMatches) == 0:
		return []string{dimStyle.Render("no match")}
	}

	nameW := 0
	for _, e := range m.wsModalEntries {
		nameW = max(nameW, lipgloss.Width(filepath.Base(e.path)))
	}
	nameW = min(nameW, contentW/2)

	cursor := clampIndex(m.wsModalCursor, len(m.wsModalMatches))
	start := max(0, cursor-listH+1)
	end := min(start+listH, len(m.wsModalMatches))
	query := m.input.Value()

	rows := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		row := padToWidth(m.formatWsModalRow(m.wsModalEntries[m.wsModalMatches[i]], query, nameW), contentW)
		if i == cursor {
			row = highlightSelectedRow(row)
		}
		rows = append(rows, row)
	}
	return rows
}

// formatWsModalRow renders one checklist row: the membership marker (or an
// ellipsis while its commit is in flight), the repo name with fuzzy matches
// highlighted, and the dimmed parent directory.
func (m model) formatWsModalRow(entry wsModalEntry, query string, nameW int) string {
	marker := dimStyle.Render("○")
	nameStyle := lipgloss.NewStyle()
	switch {
	case entry.path == m.wsModalBusy:
		marker = dimStyle.Render("…")
	case entry.member:
		marker = wsModalMemberStyle.Render("●")
		nameStyle = nameStyle.Bold(true)
	}

	name := filepath.Base(entry.path)
	positions, _ := fuzzyMatchPositions(query, entry.path)
	matched := make(map[int]bool, len(positions))
	for _, p := range positions {
		matched[p] = true
	}
	nameOffset := len([]rune(entry.path)) - len([]rune(name))

	nameCell := padCell(highlightSegment(name, nameStyle, matched, nameOffset), nameW, lipgloss.Left)
	return " " + marker + " " + nameCell + "  " + wtPathStyle.Render(shortenDirectory(filepath.Dir(entry.path)))
}

// wsModalNoticeLine reports the last in-modal commit: what was added or
// removed, or why it failed.
func (m model) wsModalNoticeLine() string {
	switch {
	case m.wsModalNotice == "":
		return ""
	case m.wsModalNoticeErr:
		return attnErrStyle.Render("✗ " + m.wsModalNotice)
	default:
		return wsModalMemberStyle.Render("✓ ") + dimStyle.Render(m.wsModalNotice)
	}
}

// wsModalFooter is the key hint line. When the workspace has sessions, a
// toggle hands off to the backfill prompt, so the hint says what comes next.
func (m model) wsModalFooter() string {
	if m.wsModalHandsOffToBackfill() {
		return "enter toggle, then pick sessions · ↑↓ move · esc close"
	}
	return "enter toggle · ↑↓ move · type to filter · esc done"
}
