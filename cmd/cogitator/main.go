package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"

	"github.com/guilhermehto/cogitator/internal/agentfeed"
	"github.com/guilhermehto/cogitator/internal/claudecode"
	"github.com/guilhermehto/cogitator/internal/codex"
	"github.com/guilhermehto/cogitator/internal/config"
	"github.com/guilhermehto/cogitator/internal/hookipc"
	"github.com/guilhermehto/cogitator/internal/installkit"
	"github.com/guilhermehto/cogitator/internal/logging"
	"github.com/guilhermehto/cogitator/internal/omp"
	"github.com/guilhermehto/cogitator/internal/settings"
	"github.com/guilhermehto/cogitator/internal/tmuxctl"
	"github.com/guilhermehto/cogitator/internal/tmuxinstall"
	"github.com/guilhermehto/cogitator/internal/ui"
	"github.com/guilhermehto/cogitator/internal/zellijinstall"
)

var (
	version = "dev"
	commit  = ""
	date    = ""
)

// hookStdin is the hook payload on stdin, stamped with the multiplexer pane
// the harness runs in so sibling sessions in one directory stay apart.
func hookStdin() io.Reader {
	payload, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, fmt.Errorf("read hook payload: %w", err))
		os.Exit(1)
	}

	return bytes.NewReader(hookipc.StampPaneID(payload, ui.NewBackend().SelfPaneID()))
}

func main() {
	// Route subcommands before flag.Parse() so bare subcommand names are not
	// misinterpreted as unknown flags.
	if len(os.Args) > 1 && runHookCommand(os.Args[1], os.Args[2:]) {
		return
	}

	if len(os.Args) > 1 && os.Args[1] == "pick" {
		runPickCommand(os.Args[2:])
		return
	}

	if len(os.Args) > 1 && os.Args[1] == "next" {
		runNextCommand(os.Args[2:])
		return
	}

	if len(os.Args) > 1 && os.Args[1] == "tmux" {
		runTmuxCommand(os.Args[2:])
		return
	}

	if len(os.Args) > 1 && os.Args[1] == "zellij" {
		runZellijCommand(os.Args[2:])
		return
	}

	if len(os.Args) > 1 && os.Args[1] == "agents" {
		runAgentsCommand(os.Args[2:])
		return
	}

	bell := flag.Bool("bell", false, "ring terminal bell on transitions into attention states")
	status := flag.Bool("status", false, "print a one-shot icons-only attention summary and exit")
	demo := flag.Bool("demo", false, "run the TUI with a curated synthetic snapshot (for screenshots); no mDNS, no shell-outs")
	debug := flag.Bool("debug", false, "show diagnostic UI elements (e.g. unreachable-instance footer)")
	logLevel := flag.String("log-level", "info", "log level: debug|info|warn|error")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(versionLine())
		return
	}

	cfg := config.Default()
	logger, closer, logPath, err := logging.Setup(*logLevel)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	defer closer.Close()
	logger.Info("logging initialized", "path", logPath, "level", *logLevel)

	if *status {
		logger.Info("running status mode")
		if err := ui.RunStatus(cfg, logger); err != nil {
			fmt.Fprintln(os.Stderr, "mdns:", err)
			os.Exit(1)
		}
		return
	}

	if *demo {
		if err := ui.RunDemo(cfg, logger); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	logger.Info("running tui mode", "bell", *bell, "debug", *debug)
	if err := ui.RunTUI(cfg, logger, *bell, *debug); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// runPickCommand shows the agent picker. A missing --client is resolved from
// the multiplexer, so the popup can omit it.
func runPickCommand(args []string) {
	flags := flag.NewFlagSet("pick", flag.ExitOnError)
	client := flags.String("client", "", "tmux client to jump (default: the invoking client; ignored under zellij)")
	flags.Parse(args)

	logger, closer := setupSubcommandLogger("pick")
	defer closer()

	if *client == "" {
		loc, _ := ui.NewBackend().CurrentLocation()
		*client = loc.Client
	}

	if err := ui.RunPick(config.Default(), logger, *client); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// runNextCommand jumps to the next agent needing attention. It always exits 0.
// Under tmux it prints nothing after flag parsing (run-shell would overlay any
// output); under zellij it prints its message in its own floating pane.
func runNextCommand(args []string) {
	flags := flag.NewFlagSet("next", flag.ExitOnError)
	client := flags.String("client", "", "tmux client to jump (default: the invoking client; ignored under zellij)")
	windowID := flags.String("window", "", "window (tmux) or tab (zellij) id the client is looking at (default: current)")
	sessionID := flags.String("session", "", "session id the client is looking at (default: current)")
	flags.Parse(args)

	logger, closer := setupSubcommandLogger("next")
	defer closer()

	backend := ui.NewBackend()
	if *client == "" || *windowID == "" || *sessionID == "" {
		loc, _ := backend.CurrentLocation()
		fillEmpty(client, loc.Client)
		fillEmpty(windowID, loc.WindowID)
		fillEmpty(sessionID, loc.SessionID)
	}

	if err := agentfeed.RunNext(context.Background(), backend, *client, *windowID, *sessionID, logger); err != nil {
		logger.Error("next failed", "err", err)
	}
}

// runTmuxCommand wires or removes the tmux key bindings. Unlike pick and next
// it runs from a terminal, so it reports to stdout/stderr and exits non-zero
// on failure.
func runTmuxCommand(args []string) {
	if len(args) == 0 || (args[0] != "install" && args[0] != "uninstall") {
		fmt.Fprintln(os.Stderr, "usage: cogitator tmux install|uninstall [flags]")
		os.Exit(2)
	}

	action := args[0]
	flags := flag.NewFlagSet("tmux "+action, flag.ExitOnError)
	opts := tmuxinstall.Options{}
	flags.StringVar(&opts.ConfigPath, "config", "", "tmux config file to edit (default: tmux's lookup order)")
	flags.BoolVar(&opts.DryRun, "dry-run", false, "print the changes without writing anything")
	flags.BoolVar(&opts.NoReload, "no-reload", false, "do not touch the running tmux server")
	if action == "install" {
		flags.StringVar(&opts.NextKey, "next-key", tmuxinstall.DefaultNextKey, "prefix key that jumps to the next agent needing attention")
		flags.StringVar(&opts.PickKey, "pick-key", tmuxinstall.DefaultPickKey, "prefix key that opens the agent picker")
		flags.StringVar(&opts.Binary, "binary", "", "cogitator binary to bind (default: PATH lookup, then this executable)")
		flags.BoolVar(&opts.Force, "force", false, "install even when cogitator is already bound outside the owned file")
	}
	flags.Parse(args[1:])

	installer, err := newTmuxInstaller()
	if err == nil && action == "install" {
		err = installer.Install(opts)
	} else if err == nil {
		err = installer.Uninstall(opts)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newTmuxInstaller() (tmuxinstall.Installer, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return tmuxinstall.Installer{}, fmt.Errorf("resolve home directory: %w", err)
	}

	configPath, err := settings.ConfigPath()
	if err != nil {
		return tmuxinstall.Installer{}, err
	}

	return tmuxinstall.Installer{
		Home:          home,
		XDGConfigHome: os.Getenv("XDG_CONFIG_HOME"),
		OwnedPath:     filepath.Join(filepath.Dir(configPath), "tmux.conf"),
		LookPath:      exec.LookPath,
		Executable:    os.Executable,
		Tmux:          tmuxctl.DefaultRunner,
		Out:           os.Stdout,
		Warn:          os.Stderr,
	}, nil
}

// runZellijCommand adds or removes the next/pick keybinds in the user's
// zellij config. zellij hot-reloads config.kdl, so there is no reload step.
func runZellijCommand(args []string) {
	if len(args) == 0 || (args[0] != "install" && args[0] != "uninstall") {
		fmt.Fprintln(os.Stderr, "usage: cogitator zellij install|uninstall [flags]")
		os.Exit(2)
	}

	action := args[0]
	flags := flag.NewFlagSet("zellij "+action, flag.ExitOnError)
	opts := zellijinstall.Options{}
	flags.StringVar(&opts.ConfigPath, "config", "", "zellij config file to edit (default: zellij's lookup order)")
	flags.BoolVar(&opts.DryRun, "dry-run", false, "print the changes without writing anything")
	if action == "install" {
		flags.StringVar(&opts.NextKey, "next-key", "", fmt.Sprintf("normal-mode key that jumps to the next agent needing attention (default %q)", zellijinstall.DefaultNextKey))
		flags.StringVar(&opts.PickKey, "pick-key", "", fmt.Sprintf("normal-mode key that opens the agent picker (default %q)", zellijinstall.DefaultPickKey))
		flags.StringVar(&opts.Binary, "binary", "", "cogitator binary to bind (default: PATH lookup, then this executable)")
		flags.StringVar(&opts.Plugin, "plugin", "", "cogitator zellij plugin wasm to dock as a sidebar in new sessions (sets default_layout; default: no sidebar)")
		flags.StringVar(&opts.SidebarKey, "sidebar-key", "", fmt.Sprintf("normal-mode key that focuses the cogitator sidebar (needs --plugin; default %q)", zellijinstall.DefaultSidebarKey))
		flags.BoolVar(&opts.Force, "force", false, "install even when the config already wires cogitator outside the owned block")
	}
	_ = flags.Parse(args[1:])

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve home directory: %v\n", err)
		os.Exit(1)
	}

	installer := zellijinstall.Installer{
		Home:       home,
		ConfigFile: os.Getenv("ZELLIJ_CONFIG_FILE"),
		ConfigDir:  os.Getenv("ZELLIJ_CONFIG_DIR"),
		LookPath:   exec.LookPath,
		Executable: os.Executable,
		Out:        os.Stdout,
	}

	if action == "install" {
		err = installer.Install(opts)
	} else {
		err = installer.Uninstall(opts)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// runHookCommand routes the hook subcommands and reports whether name was
// one. `<harness>-hook install` wires the harness; the bare form forwards a
// hook event from stdin.
func runHookCommand(name string, args []string) bool {
	switch name {
	case "claude-hook":
		installOrForwardHook(args, runClaudeHookInstall, claudecode.SendHook)
	case "codex-hook":
		installOrForwardHook(args, runCodexHookInstall, codex.SendHook)
	case "omp-hook":
		installOrForwardHook(args, runOmpHookInstall, omp.SendHook)
	default:
		return false
	}

	return true
}

func installOrForwardHook(args []string, install func([]string), send func(context.Context, io.Reader) error) {
	if len(args) > 0 && args[0] == "install" {
		install(args[1:])
		return
	}

	// A closed cogitator TUI is the expected case, not a failure: exit 0
	// silently so the harness never shows a "hook failed" banner.
	if err := send(context.Background(), hookStdin()); err != nil && !errors.Is(err, hookipc.ErrListenerUnavailable) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// runClaudeHookInstall merges cogitator's hooks into Claude Code's
// settings.json with the binary's absolute path in every command.
func runClaudeHookInstall(args []string) {
	flags := flag.NewFlagSet("claude-hook install", flag.ExitOnError)
	binary := flags.String("binary", "", "cogitator binary the hooks run (default: PATH lookup, then this executable)")
	force := flags.Bool("force", false, "install even when cogitator is already wired into Claude Code by hand")
	_ = flags.Parse(args)

	found, err := claudecode.InstallHooks(config.Default().ClaudeCodeHome, resolveHookBinary(*binary), *force)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if found.State == installkit.Current {
		fmt.Printf("Claude Code hooks already set up: %s\n", found.Path)
		return
	}
	fmt.Printf("Installed Claude Code hooks: %s\n", found.Path)
	fmt.Println("Restart Claude Code for the hooks to take effect.")
}

// runCodexHookInstall merges cogitator's hooks into Codex's hooks.json.
// Codex skips untrusted hooks silently, so it ends with the trust step.
func runCodexHookInstall(args []string) {
	flags := flag.NewFlagSet("codex-hook install", flag.ExitOnError)
	binary := flags.String("binary", "", "cogitator binary the hooks run (default: PATH lookup, then this executable)")
	force := flags.Bool("force", false, "install even when cogitator is already wired into Codex by hand")
	_ = flags.Parse(args)

	bin := resolveHookBinary(*binary)
	found, err := codex.InstallHooks(config.Default().CodexHome, bin, *force)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if found.State == installkit.Current {
		fmt.Printf("Codex hooks already set up: %s\n", found.Path)
		return
	}
	fmt.Printf("Installed Codex hooks: %s\n", found.Path)
	fmt.Printf("Start codex, run /hooks, and trust `%s`.\n", installkit.HookCommand(bin, codex.HookSubcommand))
}

// resolveHookBinary is the absolute cogitator binary hook commands run.
func resolveHookBinary(flagValue string) string {
	bin, err := installkit.ResolveBinary(flagValue, exec.LookPath, os.Executable)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	return bin
}

// runOmpHookInstall writes the omp extension with this binary's absolute path
// baked in, so omp does not need cogitator on its PATH.
func runOmpHookInstall(args []string) {
	flags := flag.NewFlagSet("omp-hook install", flag.ExitOnError)
	force := flags.Bool("force", false, "install even when cogitator is already wired into omp by hand")
	_ = flags.Parse(args)

	exe, err := os.Executable()
	if err != nil {
		exe = ""
	}

	found, err := omp.InstallExtension("", exe, *force)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if found.State == installkit.Current {
		fmt.Printf("omp live-attention extension already set up: %s\n", found.Path)
		return
	}
	fmt.Printf("Installed omp live-attention extension: %s\n", found.Path)
	fmt.Println("Restart any running omp sessions for it to take effect.")
}

// runAgentsCommand exposes the running TUI's agent feed to callers that cannot
// dial its socket, such as the zellij plugin.
func runAgentsCommand(args []string) {
	if len(args) == 0 || (args[0] != "list" && args[0] != "focus") {
		fmt.Fprintln(os.Stderr, "usage: cogitator agents list | focus --client <client> <agent-id>")
		os.Exit(2)
	}

	ctx := context.Background()
	feed := agentfeed.NewClient()

	if args[0] == "list" {
		exitOnAgentsError(printAgents(ctx, feed))
		return
	}

	flags := flag.NewFlagSet("agents focus", flag.ExitOnError)
	client := flags.String("client", "", "client to jump (under zellij: the viewed session name)")
	_ = flags.Parse(args[1:])
	if flags.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: cogitator agents focus --client <client> <agent-id>")
		os.Exit(2)
	}

	exitOnAgentsError(feed.Focus(ctx, flags.Arg(0), *client))
}

func printAgents(ctx context.Context, feed agentfeed.Client) error {
	list, err := feed.List(ctx)
	if err != nil {
		return err
	}

	home, _ := os.UserHomeDir()
	out, err := agentfeed.ExportJSON(list, home)
	if err != nil {
		return err
	}

	_, err = os.Stdout.Write(append(out, '\n'))

	return err
}

func exitOnAgentsError(err error) {
	if err == nil {
		return
	}

	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

// setupSubcommandLogger logs to the cogitator log file; a failure to open it
// must not surface in a popup or run-shell overlay, so it falls back to the
// default logger.
func setupSubcommandLogger(name string) (*slog.Logger, func()) {
	logger, closer, _, err := logging.Setup("info")
	if err != nil {
		return slog.Default(), func() {}
	}

	logger.Info("running subcommand", "name", name)

	return logger, func() { closer.Close() }
}

func fillEmpty(dst *string, value string) {
	if *dst == "" {
		*dst = value
	}
}

func versionLine() string {
	v := version
	c := commit
	d := date
	modulePath := "cogitator"

	if bi, ok := debug.ReadBuildInfo(); ok {
		if bi.Main.Path != "" {
			modulePath = bi.Main.Path
		}
		if (v == "" || v == "dev") && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			v = bi.Main.Version
		}
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if c == "" {
					c = s.Value
				}
			case "vcs.time":
				if d == "" {
					d = s.Value
				}
			case "vcs.modified":
				if s.Value == "true" && c != "" {
					c += "-dirty"
				}
			}
		}
	}

	if v == "" {
		v = "dev"
	}
	if c == "" {
		c = "unknown"
	}
	if d == "" {
		d = "unknown"
	}
	return fmt.Sprintf("%s version=%s commit=%s date=%s", modulePath, v, c, d)
}
