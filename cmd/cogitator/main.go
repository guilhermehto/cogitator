package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"

	"github.com/guilhermehto/cogitator/internal/agentfeed"
	"github.com/guilhermehto/cogitator/internal/claudecode"
	"github.com/guilhermehto/cogitator/internal/codex"
	"github.com/guilhermehto/cogitator/internal/config"
	"github.com/guilhermehto/cogitator/internal/logging"
	"github.com/guilhermehto/cogitator/internal/omp"
	"github.com/guilhermehto/cogitator/internal/settings"
	"github.com/guilhermehto/cogitator/internal/tmuxctl"
	"github.com/guilhermehto/cogitator/internal/tmuxinstall"
	"github.com/guilhermehto/cogitator/internal/ui"
)

var (
	version = "dev"
	commit  = ""
	date    = ""
)

func main() {
	// Route subcommands before flag.Parse() so bare subcommand names are not
	// misinterpreted as unknown flags.
	if len(os.Args) > 1 && os.Args[1] == "codex-hook" {
		if err := codex.SendHook(context.Background(), os.Stdin); err != nil {
			// A closed cogitator TUI is the expected case, not a failure:
			// exit 0 silently so Codex never shows a "hook failed" banner.
			if errors.Is(err, codex.ErrListenerUnavailable) {
				return
			}
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	if len(os.Args) > 1 && os.Args[1] == "claude-hook" {
		if err := claudecode.SendHook(context.Background(), os.Stdin); err != nil {
			// A closed cogitator TUI is the expected case, not a failure:
			// exit 0 silently so Claude Code never shows a "hook failed" banner.
			if errors.Is(err, claudecode.ErrListenerUnavailable) {
				return
			}
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	if len(os.Args) > 1 && os.Args[1] == "omp-hook" {
		// `omp-hook install` writes the live-attention extension into the omp
		// extensions directory, baking in this binary's absolute path; the bare
		// form forwards a hook event from stdin.
		if len(os.Args) > 2 && os.Args[2] == "install" {
			exe, err := os.Executable()
			if err != nil {
				exe = "" // fall back to "cogitator" on PATH
			}
			path, err := omp.InstallExtension("", exe)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			fmt.Printf("Installed omp live-attention extension: %s\n", path)
			fmt.Println("Restart any running omp sessions for it to take effect.")
			return
		}
		if err := omp.SendHook(context.Background(), os.Stdin); err != nil {
			// A closed cogitator TUI is the expected case, not a failure:
			// exit 0 silently so omp never shows a "hook failed" banner.
			if errors.Is(err, omp.ErrListenerUnavailable) {
				return
			}
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
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
// tmux, so the popup can omit it.
func runPickCommand(args []string) {
	flags := flag.NewFlagSet("pick", flag.ExitOnError)
	client := flags.String("client", "", "tmux client to jump (default: the invoking client)")
	flags.Parse(args)

	logger, closer := setupSubcommandLogger("pick")
	defer closer()

	if *client == "" {
		*client = tmuxDisplayValue("#{client_name}")
	}

	if err := ui.RunPick(config.Default(), logger, *client); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// runNextCommand jumps to the next agent needing attention. It always exits 0
// and prints nothing after flag parsing: run-shell would overlay any output.
func runNextCommand(args []string) {
	flags := flag.NewFlagSet("next", flag.ExitOnError)
	client := flags.String("client", "", "tmux client to jump (default: the invoking client)")
	windowID := flags.String("window", "", "tmux window id the client is looking at (default: current)")
	sessionID := flags.String("session", "", "tmux session id the client is looking at (default: current)")
	flags.Parse(args)

	logger, closer := setupSubcommandLogger("next")
	defer closer()

	if *client == "" {
		*client = tmuxDisplayValue("#{client_name}")
	}
	if *windowID == "" {
		*windowID = tmuxDisplayValue("#{window_id}")
	}
	if *sessionID == "" {
		*sessionID = tmuxDisplayValue("#{session_id}")
	}

	if err := agentfeed.RunNext(context.Background(), *client, *windowID, *sessionID, logger); err != nil {
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

func tmuxDisplayValue(format string) string {
	out, err := tmuxctl.DefaultRunner.Run("display-message", "-p", format)
	if err != nil {
		return ""
	}

	return strings.TrimSpace(out)
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
