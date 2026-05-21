package ui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/guilhermehto/cogitator/internal/taskwarrior"
)

// renderTasksPane renders the full content string for the tasks pane.
// outerH and outerW are the total visible dimensions of the rendered pane
// (border included). The caller subtracts the border (2 rows / 2 cols) when
// passing values to lipgloss .Height/.Width. The inner usable area for body
// rows is outerH - 2 (border) - 1 (title) - 1 (column header) - promptRows;
// the inner usable width is 4 columns narrower (border + padding each side).
func (m model) renderTasksPane(outerH, outerW int) string {
	var b strings.Builder

	b.WriteString(headerStyle.Render("Tasks") + "\n")

	// Placeholder states — return early with a single dim message.
	if !m.twAvail {
		b.WriteString(dimStyle.Render("taskwarrior not installed"))
		return b.String()
	}
	if !m.tasksLoaded {
		b.WriteString(dimStyle.Render("loading tasks…"))
		return b.String()
	}
	if len(m.tasks) == 0 {
		b.WriteString(dimStyle.Render("no pending tasks"))
		return b.String()
	}

	// m.tasks is already sorted at load time (see tasksLoadedMsg handler in
	// model.go). Render in order so the displayed index matches m.taskCursor
	// — critical for action routing (done/start/stop/edit/delete all use
	// m.tasks[m.taskCursor] and must target the highlighted row).

	// Inner width: border (1 each side) + padding (1 each side) = 4.
	innerW := outerW - 4
	if innerW < 1 {
		innerW = 1
	}

	// Column header.
	b.WriteString(taskColumnHeader(innerW) + "\n")

	// Budget: border top+bottom (2) + title row (1) + column header (1).
	promptRows := 0
	if m.prompt != promptIdle {
		promptRows = 1
	}
	maxBodyRows := outerH - 2 - 1 - 1 - promptRows
	if maxBodyRows < 1 {
		maxBodyRows = 1
	}

	// Render task rows.
	rows := make([]string, 0, len(m.tasks))
	for i, tv := range m.tasks {
		selected := m.focus == focusTasks && m.taskCursor >= 0 && i == m.taskCursor
		active := !tv.Start.IsZero()
		rows = append(rows, formatTaskRow(tv, innerW, selected, active))
	}

	if len(rows) > maxBodyRows {
		overflow := len(rows) - (maxBodyRows - 1)
		rows = rows[:maxBodyRows-1]
		rows = append(rows, dimStyle.Render(fmt.Sprintf("… +%d more", overflow)))
	}

	b.WriteString(strings.Join(rows, "\n"))

	// Prompt line at the bottom of the pane body.
	if m.prompt != promptIdle {
		b.WriteString("\n")
		b.WriteString(m.taskPromptLine())
	}

	return b.String()
}

// sortedTasks returns a copy of tasks ordered for display: running tasks
// (non-zero Start) first, then by numeric ID ascending (lexicographic when an
// ID is not a valid int — Taskwarrior IDs are normally small integers, but
// non-numeric synthetic IDs sort stably).
//
// Stable ordering matters because m.taskCursor is an index into the rendered
// list; sortedTasks is called once at load time and the result is stored as
// m.tasks, so the cursor stays in sync with both the display and action
// dispatch (Done/Start/Stop/Edit/Delete all read m.tasks[m.taskCursor]).
func sortedTasks(tasks []taskwarrior.TaskView) []taskwarrior.TaskView {
	cp := append([]taskwarrior.TaskView(nil), tasks...)
	sort.SliceStable(cp, func(i, j int) bool {
		ri := !cp[i].Start.IsZero()
		rj := !cp[j].Start.IsZero()
		if ri != rj {
			return ri
		}
		ni, erri := strconv.Atoi(cp[i].ID)
		nj, errj := strconv.Atoi(cp[j].ID)
		if erri == nil && errj == nil {
			return ni < nj
		}
		return cp[i].ID < cp[j].ID
	})
	return cp
}

// taskColumnHeader renders the column header row for the tasks pane.
func taskColumnHeader(innerW int) string {
	descW := taskDescWidth(innerW)
	cells := []string{
		padCell(dimStyle.Render("ST"), colTaskStateW, lipgloss.Left),
		padCell(dimStyle.Render("ID"), colTaskIDW, lipgloss.Left),
		padCell(dimStyle.Render("DESC"), descW, lipgloss.Left),
		padCell(dimStyle.Render("PROJECT"), colTaskProjectW, lipgloss.Left),
		padCell(dimStyle.Render("TAGS"), colTaskTagsW, lipgloss.Left),
		padCell(dimStyle.Render("DUE"), colTaskDueW, lipgloss.Left),
	}
	return strings.Join(cells, strings.Repeat(" ", colGap))
}

// taskDescWidth computes the remaining width for the DESC column after all
// fixed columns and gaps are subtracted.
func taskDescWidth(innerW int) int {
	// 5 fixed columns + 5 gaps between 6 columns.
	fixed := colTaskStateW + colTaskIDW + colTaskProjectW + colTaskTagsW + colTaskDueW
	gaps := 5 * colGap
	w := innerW - fixed - gaps
	if w < 1 {
		w = 1
	}
	return w
}

// formatTaskRow renders a single task as a padded column row.
// selected applies a reverse-video highlight when the row is the cursor row
// and the tasks pane is focused. active marks the task as currently running:
// the ST cell switches from the priority glyph to glyphTaskActive and the
// entire row is rendered bold + green via taskActiveStyle. selected wins over
// active when both apply (reverse-video is applied last so the cursor row is
// always visually unambiguous).
func formatTaskRow(tv taskwarrior.TaskView, innerW int, selected, active bool) string {
	stateCell := taskPriorityGlyph[tv.Priority] // blank when not in map
	if active {
		stateCell = glyphTaskActive
	}

	idCell := tv.ID

	descW := taskDescWidth(innerW)
	descCell := tv.Description

	projectCell := tv.Project

	tagsCell := strings.Join(tv.Tags, " ")

	dueCell := ""
	if !tv.Due.IsZero() {
		dueCell = tv.Due.UTC().Format(time.DateOnly)
	}

	cells := []string{
		padCell(stateCell, colTaskStateW, lipgloss.Left),
		padCell(idCell, colTaskIDW, lipgloss.Left),
		padCell(descCell, descW, lipgloss.Left),
		padCell(projectCell, colTaskProjectW, lipgloss.Left),
		padCell(tagsCell, colTaskTagsW, lipgloss.Left),
		padCell(dueCell, colTaskDueW, lipgloss.Left),
	}
	row := strings.Join(cells, strings.Repeat(" ", colGap))

	if active {
		row = taskActiveStyle.Render(row)
	}
	if selected {
		row = lipgloss.NewStyle().Reverse(true).Render(row)
	}
	return row
}

// taskPromptLine returns the prompt line rendered at the bottom of the tasks
// pane body. It is only called when m.prompt != promptIdle.
func (m model) taskPromptLine() string {
	switch m.prompt {
	case promptAdd:
		return "add: " + m.input.View()
	case promptEdit:
		id := ""
		if m.taskCursor >= 0 && m.taskCursor < len(m.tasks) {
			id = m.tasks[m.taskCursor].ID
		}
		return fmt.Sprintf("edit #%s: ", id) + m.input.View()
	case promptConfirmDelete:
		id := ""
		desc := ""
		if m.taskCursor >= 0 && m.taskCursor < len(m.tasks) {
			id = m.tasks[m.taskCursor].ID
			desc = m.tasks[m.taskCursor].Description
		}
		return fmt.Sprintf("delete #%s ('%s')? [y/N]", id, desc)
	default:
		return ""
	}
}

// flattenTaskDSL converts a TaskView back into a Taskwarrior DSL string
// suitable for pre-filling the edit prompt. Empty fields are omitted.
// Due is formatted as YYYY-MM-DD (extended ISO 8601).
func flattenTaskDSL(tv taskwarrior.TaskView) string {
	parts := []string{}

	if tv.Description != "" {
		parts = append(parts, tv.Description)
	}
	if tv.Project != "" {
		parts = append(parts, "project:"+tv.Project)
	}
	for _, tag := range tv.Tags {
		if tag != "" {
			parts = append(parts, "+"+tag)
		}
	}
	if tv.Priority != "" {
		parts = append(parts, "priority:"+tv.Priority)
	}
	if !tv.Due.IsZero() {
		parts = append(parts, "due:"+tv.Due.UTC().Format("2006-01-02"))
	}

	return strings.Join(parts, " ")
}
