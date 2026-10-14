package setup

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/guilhermehto/cogitator/internal/hookinstall"
	"github.com/guilhermehto/cogitator/internal/installkit"
	"github.com/guilhermehto/cogitator/internal/tmuxinstall"
	"github.com/guilhermehto/cogitator/internal/zellijinstall"
)

// pinnedZellij is the zellij minor the plugin's zellij-tile dependency pins.
const pinnedZellij = "0.45."

// Env is how setup detects tools on this machine: a PATH lookup and a
// command's output.
type Env struct {
	LookPath func(file string) (string, error)
	Output   func(name string, args ...string) (string, error)
}

func (e Env) has(tool string) bool {
	_, err := e.LookPath(tool)

	return err == nil
}

func unavailable(components []string, detail string) []Row {
	rows := make([]Row, 0, len(components))
	for _, component := range components {
		rows = append(rows, Row{Component: component, Status: installkit.Status{State: installkit.Unavailable, Detail: detail}})
	}

	return rows
}

// TmuxGroup wires the tmux next/pick keys. reload applies them to the
// running tmux server; without it the follow-up says how.
func TmuxGroup(env Env, installer tmuxinstall.Installer, reload bool) Group {
	const component = "tmux keybinds"

	group := Group{Name: "tmux", Shared: true}
	if !env.has("tmux") {
		group.Check = func(string) []Row { return unavailable([]string{component}, "tmux not on PATH") }
		return group
	}

	managed := installer.WithManagedKeys(tmuxinstall.Options{})
	group.Keys = []Key{
		{Name: "next key (prefix +)", Default: orDefault(managed.NextKey, tmuxinstall.DefaultNextKey)},
		{Name: "pick key (prefix +)", Default: orDefault(managed.PickKey, tmuxinstall.DefaultPickKey)},
	}

	options := func(binary string, keys []string) tmuxinstall.Options {
		return tmuxinstall.Options{Binary: binary, NextKey: keys[0], PickKey: keys[1], NoReload: !reload}
	}

	group.Check = func(binary string) []Row {
		status, err := installer.Check(options(binary, defaults(group.Keys)))
		return []Row{{Component: component, Status: status, Err: err}}
	}

	group.Install = func(binary string, keys []string, _ []Row) ([]string, error) {
		if err := installer.Install(options(binary, keys)); err != nil {
			return nil, err
		}
		if reload {
			return nil, nil
		}
		return []string{fmt.Sprintf("Reload running tmux servers for the keys: tmux source-file %s", installer.OwnedPath)}, nil
	}

	return group
}

// ZellijGroup wires the zellij next/pick keys and, when this build bundles
// the plugin, the sidebar. Both live in one config.kdl, so wiring the user
// wrote by hand in either skips the whole install.
func ZellijGroup(env Env, installer zellijinstall.Installer, bundled *zellijinstall.BundledPlugin) Group {
	const keybinds, sidebar = "zellij keybinds", "zellij sidebar"

	group := Group{Name: "zellij", Shared: true}
	if !env.has("zellij") {
		group.Check = func(string) []Row { return unavailable([]string{keybinds, sidebar}, "zellij not on PATH") }
		return group
	}

	group.Notes = zellijNotes(env)
	managed, err := installer.WithManagedKeys(zellijinstall.Options{Bundled: bundled})
	if err != nil {
		managed = zellijinstall.Options{}
	}
	group.Keys = []Key{
		{Name: "next key", Default: orDefault(managed.NextKey, zellijinstall.DefaultNextKey)},
		{Name: "pick key", Default: orDefault(managed.PickKey, zellijinstall.DefaultPickKey)},
	}
	if bundled != nil {
		group.Keys = append(group.Keys, Key{Name: "sidebar key", Default: orDefault(managed.SidebarKey, zellijinstall.DefaultSidebarKey)})
	}

	options := func(binary string, keys []string) zellijinstall.Options {
		opts := zellijinstall.Options{
			Binary:  binary,
			NextKey: unlessDefault(keys[0], zellijinstall.DefaultNextKey),
			PickKey: unlessDefault(keys[1], zellijinstall.DefaultPickKey),
			Bundled: bundled,
		}
		if bundled != nil {
			opts.SidebarKey = unlessDefault(keys[2], zellijinstall.DefaultSidebarKey)
		}
		return opts
	}

	group.Check = func(binary string) []Row {
		report, err := installer.Check(options(binary, defaults(group.Keys)))
		if err != nil {
			return []Row{{Component: keybinds, Err: err}}
		}
		if bundled == nil {
			report.Sidebar = installkit.Status{State: installkit.Unavailable, Detail: "plugin not bundled in this build"}
		}
		return []Row{{Component: keybinds, Status: report.Keybinds}, {Component: sidebar, Status: report.Sidebar}}
	}

	group.Install = func(binary string, keys []string, pending []Row) ([]string, error) {
		if err := installer.Install(options(binary, keys)); err != nil {
			return nil, err
		}
		if !hasComponent(pending, sidebar) {
			return nil, nil
		}
		return []string{"Start a new zellij session for the cogitator sidebar; running sessions keep their layout."}, nil
	}

	return group
}

// zellijNotes warns when zellij is not the minor the plugin is built for.
func zellijNotes(env Env) []string {
	out, err := env.Output("zellij", "--version")
	if err != nil {
		return []string{fmt.Sprintf("warning: could not read the zellij version (%v); the sidebar plugin needs zellij %sx", err, pinnedZellij)}
	}

	fields := strings.Fields(out)
	if len(fields) > 0 && strings.HasPrefix(fields[len(fields)-1], pinnedZellij) {
		return nil
	}

	return []string{fmt.Sprintf("warning: found %s; the sidebar plugin is built for zellij %sx and may not load", strings.TrimSpace(out), pinnedZellij)}
}

// HooksGroup wires every detected harness's hooks; hooks the user wrote by
// hand are reported and only the other harnesses are installed. opencode is
// listed without a hook since cogitator discovers it over mDNS.
func HooksGroup(env Env, harnesses []hookinstall.Harness, out io.Writer) Group {
	byName := make(map[string]hookinstall.Harness, len(harnesses))
	for _, harness := range harnesses {
		byName[harness.Name] = harness
	}

	group := Group{Name: "hooks"}
	group.Check = func(binary string) []Row {
		rows := make([]Row, 0, len(harnesses)+1)
		for _, harness := range harnesses {
			if !harness.Detected() {
				rows = append(rows, unavailable([]string{harness.Name}, "not found")...)
				continue
			}
			status, err := harness.Check(binary)
			rows = append(rows, Row{Component: harness.Name, Status: status, Err: err})
		}
		return append(rows, opencodeRow(env))
	}

	group.Install = func(binary string, _ []string, pending []Row) ([]string, error) {
		var followUps []string
		var errs []error
		for _, row := range pending {
			harness := byName[row.Component]
			status, err := harness.Install(binary, false)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", harness.Name, err))
				continue
			}
			fmt.Fprintf(out, "%s: %s %s\n", harness.Name, installedVerb(status), status.Path)
			followUps = append(followUps, harness.FollowUp(binary))
		}
		return followUps, errors.Join(errs...)
	}

	return group
}

func installedVerb(status installkit.Status) string {
	if status.State == installkit.Stale {
		return "updated"
	}

	return "installed"
}

func opencodeRow(env Env) Row {
	found := "opencode not on PATH"
	if env.has("opencode") {
		found = "opencode on PATH"
	}

	return Row{Component: "opencode", Status: installkit.Status{State: installkit.Unavailable, Detail: found}, Note: "no hook needed (discovered over mDNS)"}
}

func hasComponent(rows []Row, component string) bool {
	for _, row := range rows {
		if row.Component == component {
			return true
		}
	}

	return false
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}

	return value
}

// unlessDefault passes the installer default as "" so the installer still
// refuses a default key the user's config already binds.
func unlessDefault(key, installerDefault string) string {
	if key == installerDefault {
		return ""
	}

	return key
}
