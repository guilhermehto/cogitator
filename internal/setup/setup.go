// Package setup is `cogitator setup`: it checks every component cogitator
// can wire on this machine, prints one plan, asks once per group, and runs
// the existing installers with one cogitator binary. Wiring the user wrote by
// hand is reported and never touched.
//
// No import of bubbletea or internal/ui is permitted in this package.
package setup

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"text/tabwriter"

	"github.com/guilhermehto/cogitator/internal/installkit"
)

// Mode is how far Run goes after printing the plan.
type Mode int

const (
	// Interactive asks once per group and lets the user change keys.
	Interactive Mode = iota
	// Yes accepts the plan with the default keys without prompting.
	Yes
	// DryRun prints the plan and writes nothing.
	DryRun
	// CheckOnly prints the plan, writes nothing, and fails with
	// ErrIncomplete when something would be installed or updated.
	CheckOnly
)

// ModeFor maps the setup flags to a Mode; the flags that write nothing win.
func ModeFor(yes, dryRun, check bool) Mode {
	switch {
	case check:
		return CheckOnly
	case dryRun:
		return DryRun
	case yes:
		return Yes
	default:
		return Interactive
	}
}

// ErrIncomplete is CheckOnly's result when something is missing or stale.
var ErrIncomplete = errors.New("cogitator is not fully set up; run `cogitator setup`")

// Row is one component in the plan. Note is the fixed action of a row setup
// never installs; Err is a failed check, which skips the row.
type Row struct {
	Component string
	Status    installkit.Status
	Note      string
	Err       error
}

func (r Row) pending() bool {
	return r.Note == "" && r.Err == nil && (r.Status.State == installkit.Missing || r.Status.State == installkit.Stale)
}

// Key is a key the group's installer binds, with the value offered as the
// prompt default.
type Key struct {
	Name    string
	Default string
}

// Group is one prompt's worth of components sharing an installer: a
// multiplexer, or the harness hooks. A Shared group writes one config, so a
// Foreign row skips the whole group; otherwise only the pending rows are
// installed. Install gets the chosen keys in Keys order and returns the
// follow-ups for what it changed.
type Group struct {
	Name    string
	Notes   []string
	Keys    []Key
	Shared  bool
	Check   func(binary string) []Row
	Install func(binary string, keys []string, pending []Row) ([]string, error)
}

// Wizard runs the setup. Home, and the directory it resolves to, is
// shortened to ~ in the plan.
type Wizard struct {
	Binary string
	Home   string
	Groups []Group
	In     io.Reader
	Out    io.Writer

	homePrefix *regexp.Regexp
}

// planned is a group with its checked rows.
type planned struct {
	group   Group
	rows    []Row
	blocked bool
}

func (p planned) pending() []Row {
	if p.blocked {
		return nil
	}

	var rows []Row
	for _, row := range p.rows {
		if row.pending() {
			rows = append(rows, row)
		}
	}

	return rows
}

// Run checks every group, prints the plan, and in Interactive and Yes mode
// runs the installers the user accepts. It returns an error naming every
// check or install that failed, or ErrIncomplete from CheckOnly.
func (w Wizard) Run(mode Mode) error {
	w.homePrefix = homePrefix(w.Home)
	fmt.Fprintf(w.Out, "cogitator binary: %s\n\n", w.tilde(w.Binary))

	plans := w.check()
	w.printPlan(plans)

	failed := checkFailures(plans)
	if !anyPending(plans) {
		fmt.Fprintln(w.Out, "\neverything is already set up")
		return joinFailures(failed)
	}

	switch mode {
	case CheckOnly:
		return errors.Join(ErrIncomplete, joinFailures(failed))
	case DryRun:
		fmt.Fprintln(w.Out, "\ndry run: nothing written")
		return joinFailures(failed)
	}

	followUps, installed, installFailed := w.install(plans, mode)
	if installed {
		w.printFollowUps(followUps)
	}

	return joinFailures(append(failed, installFailed...))
}

func (w Wizard) check() []planned {
	plans := make([]planned, 0, len(w.Groups))
	for _, group := range w.Groups {
		p := planned{group: group, rows: group.Check(w.Binary)}
		for _, row := range p.rows {
			if group.Shared && (row.Err != nil || row.Status.State == installkit.Foreign) {
				p.blocked = true
			}
		}
		plans = append(plans, p)
	}

	return plans
}

func (w Wizard) printPlan(plans []planned) {
	for _, p := range plans {
		for _, note := range p.group.Notes {
			fmt.Fprintln(w.Out, note)
		}
	}

	table := tabwriter.NewWriter(w.Out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "COMPONENT\tFOUND\tACTION")
	for _, p := range plans {
		for _, row := range p.rows {
			fmt.Fprintf(table, "%s\t%s\t%s\n", row.Component, w.found(row), w.action(row, p))
		}
	}
	_ = table.Flush()
}

func (w Wizard) found(row Row) string {
	switch {
	case row.Note != "" || row.Status.State == installkit.Unavailable:
		return orDash(row.Status.Detail)
	case row.Err != nil:
		return "-"
	case row.Status.State == installkit.Missing:
		return "not set up"
	case row.Status.State == installkit.Foreign:
		return w.tilde(row.Status.Location())
	default:
		return w.tilde(row.Status.Path)
	}
}

func (w Wizard) action(row Row, p planned) string {
	switch {
	case row.Note != "":
		return row.Note
	case row.Err != nil:
		return "error: " + row.Err.Error()
	case row.Status.State == installkit.Foreign:
		return "yours, left alone"
	case row.pending() && p.blocked:
		return "skip, " + p.group.Name + " is wired by hand"
	}

	switch row.Status.State {
	case installkit.Current:
		return "already set up, skip"
	case installkit.Stale:
		return w.tilde(strings.TrimSuffix("update: "+row.Status.Detail, ": "))
	case installkit.Missing:
		return "install"
	default:
		return "not installed, skip"
	}
}

// install runs the accepted groups' installers, returning their follow-ups,
// whether any installer ran, and the groups that failed.
func (w Wizard) install(plans []planned, mode Mode) ([]string, bool, []string) {
	in := bufio.NewReader(w.In)
	var followUps, failed []string
	installed := false

	for _, p := range plans {
		pending := p.pending()
		if len(pending) == 0 {
			continue
		}

		keys := defaults(p.group.Keys)
		if mode == Interactive {
			if !w.confirm(in, p.group.Name, pending) {
				continue
			}
			keys = w.askKeys(in, p.group.Keys)
		}

		fmt.Fprintf(w.Out, "\n%s:\n", p.group.Name)
		changed, err := p.group.Install(w.Binary, keys, pending)
		followUps = append(followUps, changed...)
		installed = true
		if err != nil {
			fmt.Fprintf(w.Out, "%s: %v\n", p.group.Name, err)
			failed = append(failed, p.group.Name)
		}
	}

	return followUps, installed, failed
}

func (w Wizard) confirm(in *bufio.Reader, name string, pending []Row) bool {
	components := make([]string, 0, len(pending))
	for _, row := range pending {
		components = append(components, row.Component)
	}

	fmt.Fprintf(w.Out, "\nSet up %s (%s)? [y/N] ", name, strings.Join(components, ", "))
	answer := strings.ToLower(readLine(in))

	return answer == "y" || answer == "yes"
}

func (w Wizard) askKeys(in *bufio.Reader, keys []Key) []string {
	chosen := make([]string, 0, len(keys))
	for _, key := range keys {
		fmt.Fprintf(w.Out, "  %s [%s]: ", key.Name, key.Default)
		value := readLine(in)
		if value == "" {
			value = key.Default
		}
		chosen = append(chosen, value)
	}

	return chosen
}

func (w Wizard) printFollowUps(followUps []string) {
	fmt.Fprintln(w.Out, "\nNext:")
	for _, followUp := range append(followUps, "Keep the cogitator TUI running: pick, next, and the sidebar read agent state from it.") {
		fmt.Fprintf(w.Out, "  %s\n", w.tilde(followUp))
	}
}

// homePrefix matches home, or the directory it resolves to, at the start
// of a path; nil when there is no home to shorten.
func homePrefix(home string) *regexp.Regexp {
	if home == "" || home == "/" {
		return nil
	}

	alternatives := []string{regexp.QuoteMeta(home)}
	if resolved, err := filepath.EvalSymlinks(home); err == nil && resolved != home {
		alternatives = append(alternatives, regexp.QuoteMeta(resolved))
	}

	return regexp.MustCompile(`(^|[\s(])(?:` + strings.Join(alternatives, "|") + `)/`)
}

func (w Wizard) tilde(text string) string {
	if w.homePrefix == nil {
		return text
	}

	return w.homePrefix.ReplaceAllString(text, "${1}~/")
}

// readLine reads one answer; a closed input answers with the default.
func readLine(in *bufio.Reader) string {
	line, _ := in.ReadString('\n')

	return strings.TrimSpace(line)
}

func defaults(keys []Key) []string {
	values := make([]string, 0, len(keys))
	for _, key := range keys {
		values = append(values, key.Default)
	}

	return values
}

func anyPending(plans []planned) bool {
	for _, p := range plans {
		if len(p.pending()) > 0 {
			return true
		}
	}

	return false
}

func checkFailures(plans []planned) []string {
	var failed []string
	for _, p := range plans {
		for _, row := range p.rows {
			if row.Err != nil {
				failed = append(failed, row.Component)
			}
		}
	}

	return failed
}

func joinFailures(failed []string) error {
	if len(failed) == 0 {
		return nil
	}

	return fmt.Errorf("setup failed for %s", strings.Join(failed, ", "))
}

func orDash(text string) string {
	if text == "" {
		return "-"
	}

	return text
}
