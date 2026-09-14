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
	"syscall"
)

// Version is set at build time with -ldflags "-X .../cli.Version=x".
var Version = "dev"

const usage = `conclave — multi-agent pull request review

Usage:
  conclave review <number> [--config PATH] [--format markdown|json] [--keep-worktrees] [--verbose]
  conclave triage <number>... [--apply] [--rev REV] [--config PATH] [--format markdown|json]
  conclave triage --all [--state open|closed|all] [--label L] [--since 90d] [--limit N]
  conclave plan <number> [--rev REV] [--config PATH] [--format markdown|json] [--keep-worktrees]
  conclave config validate [--config PATH]
  conclave config example
  conclave agents check [--config PATH]
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
	case "triage":
		err = runTriage(ctx, args[1:], stdout, stderr)
	case "plan":
		err = runPlan(ctx, args[1:], stdout, stderr)
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
