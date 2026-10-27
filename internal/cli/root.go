// Package cli implements the conclave command line.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/bornholm/conclave/internal/config"
	"github.com/bornholm/conclave/internal/integration"
)

// Version is set at build time with -ldflags "-X .../cli.Version=x".
var Version = "dev"

// usage lists the installable agents from the integration package, so that
// adding one never leaves this text behind.
var usage = strings.Replace(usageText, "AGENTS", strings.Join(integration.Agents(), "|"), 1)

const usageText = `conclave — multi-agent pull request review

Usage:
  conclave review <number> [--config PATH] [--format markdown|json] [--keep-worktrees] [--verbose]
                  [--since last|RUN [--responses FILE|-]]
  conclave loop [--once] [--dry-run] [--interval 10m] [--max-rounds N] [--include N]... [--config PATH] [--verbose]
  conclave triage <number>... [--apply] [--rev REV] [--config PATH] [--format markdown|json]
  conclave triage --all [--state open|closed|all] [--label L] [--since 90d] [--limit N]
  conclave plan <number> [--rev REV] [--config PATH] [--format markdown|json] [--keep-worktrees]
  conclave ask [question] [--question TEXT] [--context FILE|-] [--project PATH] [--rev REV]
               [--config PATH] [--format markdown|json] [--keep-worktrees]
               (with no question anywhere, the question is read from stdin)
  conclave config validate [--config PATH]
  conclave config example
  conclave agents check [--config PATH]
  conclave agents install AGENTS [--project PATH] [--force]
  conclave version
`

// Main runs the CLI and returns the process exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch args[0] {
	case "review":
		err = runReview(ctx, args[1:], stdout, stderr)
	case "loop":
		err = runLoop(ctx, args[1:], stdout, stderr)
	case "triage":
		err = runTriage(ctx, args[1:], stdout, stderr)
	case "plan":
		err = runPlan(ctx, args[1:], stdout, stderr)
	case "ask":
		err = runAsk(ctx, args[1:], stdout, stderr)
	case "config":
		err = runConfig(ctx, args[1:], stdout, stderr)
	case "agents":
		err = runAgents(ctx, args[1:], stdout, stderr)
	case "version":
		fmt.Fprintln(stdout, "conclave", Version)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", args[0], usage)
		return 2
	}
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 2
		}
		fmt.Fprintln(stderr, "error:", err)
		if errors.Is(err, context.Canceled) {
			return 130
		}
		return 1
	}
	return 0
}

func newLogger(w io.Writer, verbose bool) *slog.Logger {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}))
}

// loadConfig loads the configuration, then its env file into the
// environment, before anything reads a token or starts an agent.
func loadConfig(path string) (*config.Config, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	if err := cfg.LoadEnv(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// logSources names the configuration files inherited and the env file
// read, on every run: the first can declare agents, that is commands the
// run executes, the second sets what they and the forge client see. Only
// the variable names are logged, never their values.
func logSources(logger *slog.Logger, cfg *config.Config) {
	for _, src := range cfg.Inherited() {
		logger.Info("configuration inherited", "file", src)
	}
	switch env := cfg.Env; {
	case env.Ignored != nil:
		logger.Warn("env file ignored: it cannot be read or does not parse", "file", env.File, "error", env.Ignored)
	case env.File != "":
		logger.Info("environment loaded", "file", env.File, "set", env.Set, "kept", env.Kept)
		if len(env.Refused) > 0 {
			logger.Warn("env file variables refused: protected", "file", env.File, "variables", env.Refused)
		}
	}
}

// printEnv says, for the commands that check rather than run, what the env
// file set: the names only, never the values.
func printEnv(w io.Writer, env config.EnvReport) {
	switch {
	case env.Ignored != nil:
		fmt.Fprintf(w, "! %s ignored, it cannot be read or does not parse: %v\n", env.File, env.Ignored)
	case env.File != "":
		fmt.Fprintf(w, "✓ environment from %s: set %s; already set %s\n", env.File, names(env.Set), names(env.Kept))
		if len(env.Refused) > 0 {
			fmt.Fprintf(w, "! %s: protected, not set: %s\n", env.File, strings.Join(env.Refused, ", "))
		}
	}
}

func names(list []string) string {
	if len(list) == 0 {
		return "none"
	}
	return strings.Join(list, ", ")
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

// parseInterspersed parses flags that may appear before or after positional
// arguments (the standard flag package stops at the first positional).
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}
