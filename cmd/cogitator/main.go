package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime/debug"

	"github.com/guilhermehto/cogitator/internal/claudecode"
	"github.com/guilhermehto/cogitator/internal/codex"
	"github.com/guilhermehto/cogitator/internal/config"
	"github.com/guilhermehto/cogitator/internal/logging"
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
