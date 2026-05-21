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
// outerH and outerW are the dimensions passed to lipgloss (.Height/.Width)
// including the border; the inner usable area is 2 rows shorter (top+bottom
// border) and 4 columns narrower (border + padding each side).
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

	// Sort: urgency descending, tie-break by numeric ID ascending.
	sorted := sortedTasks(m.tasks)

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
	rows := make([]string, 0, len(sorted))
	for i, tv := range sorted {
		selected := m.focus == focusTasks && m.taskCursor >= 0 && i == m.taskCursor
		rows = append(rows, formatTaskRow(tv, innerW, selected))
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

// sortedTasks returns a copy of tasks sorted by urgency descending, then ID
// ascending (numeric when parseable, lexicographic otherwise).
func sortedTasks(tasks []taskwarrior.TaskView) []taskwarrior.TaskView {
	cp := append([]taskwarrior.TaskView(nil), tasks...)
	sort.SliceStable(cp, func(i, j int) bool {
		if cp[i].Urgency != cp[j].Urgency {
			return cp[i].Urgency > cp[j].Urgency
		}
		// Tie-break: numeric ID ascending.
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
// and the tasks pane is focused.
func formatTaskRow(tv taskwarrior.TaskView, innerW int, selected bool) string {
	stateCell := taskPriorityGlyph[tv.Priority] // blank when not in map

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
