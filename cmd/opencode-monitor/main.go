package main

import (
	"context"
	"fmt"
	"hash/fnv"
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
	snap              state.Snapshot
	width             int
	height            int
	snaps             <-chan state.Snapshot
	inactiveCollapsed bool
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
		case "a":
			m.inactiveCollapsed = !m.inactiveCollapsed
			return m, nil
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
	attnQuestionStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("226")).Bold(true)
	attnErrStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
	attnInactiveStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	attnActiveStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("82"))

	statusBusyStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
)

// agentPalette is a curated set of 256-color codes that read well against
// a dark terminal background. The four attention colours (214/226/196/82)
// are intentionally excluded so a stable agent colour can never visually
// collide with PERMISSION/QUESTION/ERROR/active.
var agentPalette = []string{
	"33", "39", "45", "51", "75", "81",
	"99", "105", "111", "117", "135", "141",
	"147", "153", "165", "171", "177", "183",
	"203", "207", "213", "219",
}

// agentColor returns a stable italic style for a given agent name. The
// hash-mod indexing means the same agent always lands on the same
// colour across snapshots and across instances.
func agentColor(name string) lipgloss.Style {
	if name == "" {
		return agentStyle
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	idx := h.Sum32() % uint32(len(agentPalette))
	return lipgloss.NewStyle().Foreground(lipgloss.Color(agentPalette[idx])).Italic(true)
}

// Single-cell mdi glyphs that replace the padded state label. Each glyph
// is paired with the existing attention colour so an attention row visibly
// dominates an inactive one without reading the label.
const (
	glyphActive     = "\U000f09de" // 󰧞
	glyphInactive   = "\U000f0764" // 󰝤
	glyphRecent     = "\U000f02da" // 󰋚
	glyphQuestion   = "\U000f0625" // 󰘥
	glyphPermission = "\U000f033e" // 󰌾
	glyphError      = "\U000f0026" // 󰀦
)

// attnLabel returns a single coloured glyph plus a trailing space so the
// STATE column occupies a stable 2-cell footprint regardless of state.
func attnLabel(a state.Attention, source state.Source) string {
	switch a {
	case state.AttnPermissionPending:
		return attnPermStyle.Render(glyphPermission) + " "
	case state.AttnQuestionPending:
		return attnQuestionStyle.Render(glyphQuestion) + " "
	case state.AttnErrored:
		return attnErrStyle.Render(glyphError) + " "
	}
	if source == state.SourceRecent {
		return recentStyle.Render(glyphRecent) + " "
	}
	if a == state.AttnInactive {
		return attnInactiveStyle.Render(glyphInactive) + " "
	}
	return attnActiveStyle.Render(glyphActive) + " "
}

// formatRelative renders a compact "Xm"/"Xh" duration suitable for the
// ACTIVITY column. Diffs under a minute collapse to "now"; a zero
// timestamp returns "" so the column stays empty for sessions we have
// no activity record for.
func formatRelative(now, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return "now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	return fmt.Sprintf("%dh", int(d/time.Hour))
}

func styledStatus(s string) string {
	switch s {
	case "busy", "generating":
		return statusBusyStyle.Render(s)
	case "", "idle":
		// idle is the default state; collapsing it removes visual noise
		// so the eye lands on rows that actually need attention.
		return ""
	default:
		return dimStyle.Render(s)
	}
}

type rowKey struct {
	instanceID string
	sessionID  string
}

func needsAttention(a state.Attention) bool {
	return a == state.AttnPermissionPending || a == state.AttnQuestionPending || a == state.AttnErrored
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

// visibleSessions filters the snapshot rows for the sessions pane. It
// always drops finished subagents (idle leaf nodes) and reparents
// surviving children across hidden ancestors. When collapseInactive is
// true it additionally drops top-level rows whose attention is
// AttnInactive so the pane fills with sessions that actually need eyes.
//
// The second return value tracks how many inactive rows belong to each
// instance (regardless of whether they were dropped) so the caller can
// surface a `▸ N inactive` summary line per group.
func visibleSessions(all []state.SessionView, collapseInactive bool) ([]state.SessionView, map[string]int) {
	byKey := make(map[rowKey]state.SessionView, len(all))
	hidden := make(map[rowKey]bool, len(all))
	for _, sv := range all {
		k := rowKey{instanceID: sv.InstanceID, sessionID: sv.SessionID}
		byKey[k] = sv
		if shouldHideSubagent(sv) {
			hidden[k] = true
		}
	}

	inactiveByInstance := make(map[string]int)
	out := make([]state.SessionView, 0, len(all))
	for _, sv := range all {
		k := rowKey{instanceID: sv.InstanceID, sessionID: sv.SessionID}
		if hidden[k] {
			continue
		}
		if sv.Attention == state.AttnInactive {
			inactiveByInstance[sv.InstanceName]++
			if collapseInactive {
				continue
			}
		}
		sv.ParentID = nearestVisibleParentID(sv, byKey, hidden)
		out = append(out, sv)
	}
	return out, inactiveByInstance
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
	rows, inactiveByInstance := visibleSessions(m.snap.Sessions, m.inactiveCollapsed)
	paneW := m.width - 2
	if paneW < 30 {
		paneW = 30
	}
	body := paneStyle.Width(paneW).Render(m.renderAllSessions(paneW, rows, inactiveByInstance))
	live, recent := 0, 0
	for _, sv := range rows {
		if sv.Source == state.SourceRecent {
			recent++
		} else {
			live++
		}
	}
	header := titleStyle.Render("opencode-monitor") + dimStyle.Render(
		fmt.Sprintf("  %d live · %d recent (≤%dm)  ·  updated %s  ·  a to %s inactive  ·  q to quit",
			live, recent, int(recentWindow.Minutes()), m.snap.UpdatedAt.Format("15:04:05"),
			toggleVerb(m.inactiveCollapsed)))
	return header + "\n" + body
}

func toggleVerb(collapsed bool) string {
	if collapsed {
		return "show"
	}
	return "hide"
}

func (m model) renderAllSessions(width int, rows []state.SessionView, inactiveByInstance map[string]int) string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("Sessions") + "\n")
	if len(rows) == 0 && len(inactiveByInstance) == 0 {
		b.WriteString(dimStyle.Render("(no live or recent sessions on discovered instances)"))
		return b.String()
	}
	b.WriteString(columnHeader(width-2) + "\n")
	now := m.snap.UpdatedAt
	if now.IsZero() {
		now = time.Now()
	}
	groups := map[string][]state.SessionView{}
	for _, sv := range rows {
		groups[sv.InstanceName] = append(groups[sv.InstanceName], sv)
	}
	keySet := map[string]struct{}{}
	for k := range groups {
		keySet[k] = struct{}{}
	}
	for k := range inactiveByInstance {
		keySet[k] = struct{}{}
	}
	keys := make([]string, 0, len(keySet))
	for k := range keySet {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for i, k := range keys {
		if i > 0 {
			b.WriteString("\n")
		}
		if rows := groups[k]; len(rows) > 0 {
			b.WriteString(renderTree(now, rows, width-2))
		}
		if n := inactiveByInstance[k]; n > 0 {
			marker := "▸"
			if !m.inactiveCollapsed {
				marker = "▾"
			}
			b.WriteString(dimStyle.Render(fmt.Sprintf("%s %d inactive", marker, n)) + "\n")
		}
	}
	return b.String()
}

// columnHeader renders the dim labels above the trees so users can map
// each glyph/text fragment back to a logical column without a legend.
func columnHeader(width int) string {
	left := dimStyle.Render("STATE  AGENT  TITLE")
	right := dimStyle.Render("STATUS  ACTIVITY")
	pad := width - lipgloss.Width(left) - lipgloss.Width(right)
	if pad < 1 {
		pad = 1
	}
	return left + strings.Repeat(" ", pad) + right
}

func renderTree(now time.Time, rows []state.SessionView, width int) string {
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
		b.WriteString(formatRow(now, r, width, false) + "\n")
		kids := byParent[r.SessionID]
		sort.Slice(kids, func(i, j int) bool {
			if kids[i].LastActivity.Equal(kids[j].LastActivity) {
				return kids[i].SessionID < kids[j].SessionID
			}
			return kids[i].LastActivity.After(kids[j].LastActivity)
		})
		for _, c := range kids {
			b.WriteString(formatRow(now, c, width, true) + "\n")
		}
	}
	return b.String()
}

func formatRow(now time.Time, sv state.SessionView, width int, child bool) string {
	title := sv.Title
	if title == "" {
		title = sv.Slug
	}
	if title == "" {
		title = sv.SessionID
	}
	title = trimAgentSuffix(title, sv.Agent)
	prefix := ""
	if child {
		prefix = dimStyle.Render("  ↳ ")
	}
	agentTag := ""
	if sv.Agent != "" {
		agentTag = agentColor(sv.Agent).Render("@" + sv.Agent)
	}
	titleRender := title
	if sv.Source == state.SourceRecent {
		titleRender = dimStyle.Render(title)
	}
	left := fmt.Sprintf("%s%s  %s", prefix, attnLabel(sv.Attention, sv.Source), titleRender)
	if agentTag != "" {
		left = fmt.Sprintf("%s%s  %s %s", prefix, attnLabel(sv.Attention, sv.Source), agentTag, titleRender)
	}
	status := styledStatus(sv.StatusType)
	activity := dimStyle.Render(formatRelative(now, sv.LastActivity))
	right := activity
	if status != "" {
		if activity != "" {
			right = status + "  " + activity
		} else {
			right = status
		}
	}
	pad := width - lipgloss.Width(left) - lipgloss.Width(right)
	if pad < 1 {
		pad = 1
	}
	return left + strings.Repeat(" ", pad) + right
}

func trimAgentSuffix(title, agent string) string {
	if agent == "" {
		return title
	}
	suffix := " (" + agent + ")"
	if strings.HasSuffix(title, suffix) {
		return strings.TrimSuffix(title, suffix)
	}
	return title
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

	m := model{snaps: store.Subscribe(), inactiveCollapsed: true}
	if _, err := tea.NewProgram(m, tea.WithAltScreen()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
