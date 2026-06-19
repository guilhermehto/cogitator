package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"

	"github.com/guilhermehto/cogitator/internal/claudecode"
	"github.com/guilhermehto/cogitator/internal/codex"
	"github.com/guilhermehto/cogitator/internal/config"
	"github.com/guilhermehto/cogitator/internal/logging"
	"github.com/guilhermehto/cogitator/internal/omp"
	"github.com/guilhermehto/cogitator/internal/ui"
)

var (
	version = "dev"
	commit  = ""
	date    = ""
)

func main() {
	// Route hook subcommands before flag.Parse() so bare subcommand names are
	// not misinterpreted as unknown flags.
	if routeSubcommand(os.Args) {
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

// routeSubcommand handles the hook subcommands (codex-hook, claude-hook,
// omp-hook[ install]) that must run before flag parsing. It returns true when a
// subcommand was handled and main should exit.
func routeSubcommand(args []string) bool {
	if len(args) < 2 {
		return false
	}
	switch args[1] {
	case "codex-hook":
		forwardHook(codex.SendHook, codex.ErrListenerUnavailable)
	case "claude-hook":
		forwardHook(claudecode.SendHook, claudecode.ErrListenerUnavailable)
	case "omp-hook":
		// `omp-hook install` writes the JS bridge extension; the bare form
		// forwards a hook event from stdin.
		if len(args) > 2 && args[2] == "install" {
			installOMPHook()
		} else {
			forwardHook(omp.SendHook, omp.ErrListenerUnavailable)
		}
	default:
		return false
	}
	return true
}

// forwardHook reads a hook event from stdin and relays it via send. A closed
// cogitator TUI is the expected case, not a failure: exit 0 silently (when send
// reports unavailable) so the invoking agent never shows a "hook failed"
// banner. Any other error exits non-zero.
func forwardHook(send func(context.Context, io.Reader) error, unavailable error) {
	if err := send(context.Background(), os.Stdin); err != nil {
		if errors.Is(err, unavailable) {
			return
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// installOMPHook writes the omp live-attention bridge extension into the omp
// extensions directory, baking in this binary's absolute path.
func installOMPHook() {
	exe, err := os.Executable()
	if err != nil {
		exe = "" // fall back to "cogitator" on PATH
	}
	path, err := omp.InstallExtension("", exe)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("Installed omp live-attention bridge: %s\n", path)
	fmt.Println("Restart any running omp sessions for it to take effect.")
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
