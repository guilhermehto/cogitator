package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/goliveira/opencode-monitor/internal/discovery"
	"github.com/goliveira/opencode-monitor/internal/oc"
	"github.com/goliveira/opencode-monitor/internal/state"
)

const recentWindow = 30 * time.Minute

type instanceLifecycle struct {
	cancel context.CancelFunc
}

type supervisor struct {
	mu        sync.Mutex
	store     *state.Store
	instances map[string]*instanceLifecycle
	pollEvery time.Duration
}

func newSupervisor(store *state.Store) *supervisor {
	return &supervisor{
		store:     store,
		instances: map[string]*instanceLifecycle{},
		pollEvery: 5 * time.Second,
	}
}

func (s *supervisor) onAdd(parent context.Context, inst discovery.Instance) {
	s.mu.Lock()
	if _, exists := s.instances[inst.ID]; exists {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	s.instances[inst.ID] = &instanceLifecycle{cancel: cancel}
	s.mu.Unlock()
	s.store.AddInstance(inst)
	go s.run(ctx, inst)
}

func (s *supervisor) onRemove(id string) {
	s.mu.Lock()
	lc := s.instances[id]
	delete(s.instances, id)
	s.mu.Unlock()
	if lc != nil {
		lc.cancel()
	}
	s.store.RemoveInstance(id)
}

func (s *supervisor) run(ctx context.Context, inst discovery.Instance) {
	client := oc.NewClient(inst.BaseURL())

	syncPerms := func() {
		pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if perms, err := client.PendingPermissions(pctx); err == nil {
			s.store.SyncPermissions(inst.ID, perms)
		}
	}
	syncRecent := func() {
		rctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		if sessions, err := client.ListRecentSessions(rctx, recentWindow); err == nil {
			s.store.SyncRecent(inst.ID, sessions)
		}
	}

	syncPerms()
	syncRecent()

	// Permissions poll: fast, real-time signal.
	pTicker := time.NewTicker(s.pollEvery)
	defer pTicker.Stop()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-pTicker.C:
				syncPerms()
			}
		}
	}()
	// Recency poll: slower; just for discovery, not real-time.
	rTicker := time.NewTicker(30 * time.Second)
	defer rTicker.Stop()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-rTicker.C:
				syncRecent()
			}
		}
	}()

	attempt := 0
	for ctx.Err() == nil {
		events := make(chan oc.Event, 32)
		done := make(chan error, 1)
		go func() { done <- client.SubscribeEvents(ctx, events) }()
	stream:
		for {
			select {
			case <-ctx.Done():
				return
			case evt := <-events:
				attempt = 0
				s.store.ApplyEvent(inst.ID, evt)
			case <-done:
				break stream
			}
		}
		attempt++
		oc.SleepBackoff(ctx, attempt)
	}
}

// ---- Bubble Tea ----

type snapshotMsg state.Snapshot

type model struct {
	snap   state.Snapshot
	width  int
	height int
	snaps  <-chan state.Snapshot
}

func (m model) Init() tea.Cmd {
	return waitSnapshot(m.snaps)
}

func waitSnapshot(ch <-chan state.Snapshot) tea.Cmd {
	return func() tea.Msg {
		s, ok := <-ch
		if !ok {
			return nil
		}
		return snapshotMsg(s)
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case snapshotMsg:
		m.snap = state.Snapshot(msg)
		return m, waitSnapshot(m.snaps)
	}
	return m, nil
}

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63")).Padding(0, 1)
	headerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("244"))
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	agentStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("141")).Italic(true)
	recentStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Italic(true)
	paneStyle   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)

	attnPermStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
	attnErrStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
	attnInactiveStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	attnActiveStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("82"))

	statusBusyStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
)

func attnLabel(a state.Attention, source state.Source) string {
	switch a {
	case state.AttnPermissionPending:
		return attnPermStyle.Render("PERMISSION")
	case state.AttnErrored:
		return attnErrStyle.Render("ERROR     ")
	}
	if source == state.SourceRecent {
		return recentStyle.Render("recent    ")
	}
	if a == state.AttnInactive {
		return attnInactiveStyle.Render("inactive  ")
	}
	return attnActiveStyle.Render("active    ")
}

func styledStatus(s string) string {
	switch s {
	case "busy", "generating":
		return statusBusyStyle.Render(s)
	case "":
		return dimStyle.Render("·")
	default:
		return dimStyle.Render(s)
	}
}

type rowKey struct {
	instanceID string
	sessionID  string
}

func needsAttention(a state.Attention) bool {
	return a == state.AttnPermissionPending || a == state.AttnErrored
}

func shouldHideSubagent(sv state.SessionView) bool {
	if sv.ParentID == "" {
		return false
	}
	if needsAttention(sv.Attention) {
		return false
	}
	return sv.StatusType == "idle" || sv.StatusType == ""
}

func visibleSessions(all []state.SessionView) []state.SessionView {
	byKey := make(map[rowKey]state.SessionView, len(all))
	hidden := make(map[rowKey]bool, len(all))
	for _, sv := range all {
		k := rowKey{instanceID: sv.InstanceID, sessionID: sv.SessionID}
		byKey[k] = sv
		if shouldHideSubagent(sv) {
			hidden[k] = true
		}
	}

	out := make([]state.SessionView, 0, len(all))
	for _, sv := range all {
		k := rowKey{instanceID: sv.InstanceID, sessionID: sv.SessionID}
		if hidden[k] {
			continue
		}
		sv.ParentID = nearestVisibleParentID(sv, byKey, hidden)
		out = append(out, sv)
	}
	return out
}

func nearestVisibleParentID(sv state.SessionView, byKey map[rowKey]state.SessionView, hidden map[rowKey]bool) string {
	parentID := sv.ParentID
	for hops := 0; parentID != "" && hops < 32; hops++ {
		k := rowKey{instanceID: sv.InstanceID, sessionID: parentID}
		parent, ok := byKey[k]
		if !ok {
			return ""
		}
		if !hidden[k] {
			return parentID
		}
		parentID = parent.ParentID
	}
	return ""
}

func (m model) View() string {
	if m.width == 0 {
		return "loading..."
	}
	rows := visibleSessions(m.snap.Sessions)
	colW := (m.width - 4) / 2
	if colW < 30 {
		colW = 30
	}
	left := m.renderAllSessions(colW, rows)
	right := m.renderAttention(colW, rows)
	body := lipgloss.JoinHorizontal(lipgloss.Top,
		paneStyle.Width(colW).Render(left),
		paneStyle.Width(colW).Render(right),
	)
	live, recent := 0, 0
	for _, sv := range rows {
		if sv.Source == state.SourceRecent {
			recent++
		} else {
			live++
		}
	}
	header := titleStyle.Render("opencode-monitor") + dimStyle.Render(
		fmt.Sprintf("  %d live · %d recent (≤%dm)  ·  updated %s  ·  q to quit",
			live, recent, int(recentWindow.Minutes()), m.snap.UpdatedAt.Format("15:04:05")))
	return header + "\n" + body
}

func (m model) renderAllSessions(width int, rows []state.SessionView) string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("Sessions") + "\n")
	if len(rows) == 0 {
		b.WriteString(dimStyle.Render("(no live or recent sessions on discovered instances)"))
		return b.String()
	}
	groups := map[string][]state.SessionView{}
	for _, sv := range rows {
		groups[sv.InstanceName] = append(groups[sv.InstanceName], sv)
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString("\n" + headerStyle.Render(k) + "\n")
		b.WriteString(renderTree(groups[k], width-2))
	}
	return b.String()
}

func renderTree(rows []state.SessionView, width int) string {
	byParent := map[string][]state.SessionView{}
	knownIDs := map[string]bool{}
	for _, r := range rows {
		knownIDs[r.SessionID] = true
	}
	roots := []state.SessionView{}
	for _, r := range rows {
		if r.ParentID != "" && knownIDs[r.ParentID] {
			byParent[r.ParentID] = append(byParent[r.ParentID], r)
		} else {
			roots = append(roots, r)
		}
	}
	// Sort: live first, then by recency.
	sort.Slice(roots, func(i, j int) bool {
		li, lj := roots[i].Source == state.SourceLive, roots[j].Source == state.SourceLive
		if li != lj {
			return li
		}
		if roots[i].LastActivity.Equal(roots[j].LastActivity) {
			return roots[i].SessionID < roots[j].SessionID
		}
		return roots[i].LastActivity.After(roots[j].LastActivity)
	})
	var b strings.Builder
	for _, r := range roots {
		b.WriteString(formatRow(r, width, false) + "\n")
		kids := byParent[r.SessionID]
		sort.Slice(kids, func(i, j int) bool {
			if kids[i].LastActivity.Equal(kids[j].LastActivity) {
				return kids[i].SessionID < kids[j].SessionID
			}
			return kids[i].LastActivity.After(kids[j].LastActivity)
		})
		for _, c := range kids {
			b.WriteString(formatRow(c, width, true) + "\n")
		}
	}
	return b.String()
}

func (m model) renderAttention(width int, sessions []state.SessionView) string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("Needs attention") + "\n")
	rows := make([]state.SessionView, 0, len(sessions))
	for _, sv := range sessions {
		if needsAttention(sv.Attention) {
			rows = append(rows, sv)
		}
	}
	if len(rows) == 0 {
		b.WriteString("\n" + dimStyle.Render("All clear."))
		return b.String()
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Attention.Rank() != rows[j].Attention.Rank() {
			return rows[i].Attention.Rank() < rows[j].Attention.Rank()
		}
		if rows[i].LastActivity.Equal(rows[j].LastActivity) {
			return rows[i].SessionID < rows[j].SessionID
		}
		return rows[i].LastActivity.After(rows[j].LastActivity)
	})
	for _, sv := range rows {
		b.WriteString("\n" + formatRow(sv, width-2, false))
	}
	return b.String()
}

func formatRow(sv state.SessionView, width int, child bool) string {
	title := sv.Title
	if title == "" {
		title = sv.Slug
	}
	if title == "" {
		title = sv.SessionID
	}
	prefix := ""
	if child {
		prefix = dimStyle.Render("  ↳ ")
	}
	agentTag := ""
	if sv.Agent != "" {
		agentTag = " " + agentStyle.Render("@"+sv.Agent)
	}
	titleRender := title
	if sv.Source == state.SourceRecent {
		titleRender = dimStyle.Render(title)
	}
	left := fmt.Sprintf("%s%s  %s%s", prefix, attnLabel(sv.Attention, sv.Source), titleRender, agentTag)
	right := styledStatus(sv.StatusType)
	pad := width - lipgloss.Width(left) - lipgloss.Width(right)
	if pad < 1 {
		pad = 1
	}
	return left + strings.Repeat(" ", pad) + right
}

func main() {
	logF, err := os.Create("/tmp/opencode-monitor.log")
	if err == nil {
		log.SetOutput(logF)
		defer logF.Close()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := state.New(ctx)
	sup := newSupervisor(store)

	events, err := discovery.Browse(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mdns:", err)
		os.Exit(1)
	}
	go func() {
		for ev := range events {
			switch {
			case ev.Added != nil:
				sup.onAdd(ctx, *ev.Added)
			case ev.Removed != nil:
				sup.onRemove(ev.Removed.ID)
			}
		}
	}()

	m := model{snaps: store.Subscribe()}
	if _, err := tea.NewProgram(m, tea.WithAltScreen()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
