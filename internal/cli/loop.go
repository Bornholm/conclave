package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/bornholm/conclave/internal/app"
	"github.com/bornholm/conclave/internal/config"
)

func runLoop(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("loop", stderr)
	configPath := fs.String("config", config.DefaultFileName, "configuration file")
	once := fs.Bool("once", false, "make one pass over the open pull requests, then exit")
	dryRun := fs.Bool("dry-run", false, "make one pass and print the comments instead of submitting them")
	interval := fs.Duration("interval", 0, "time between two passes (default: loop.interval)")
	maxRounds := fs.Int("max-rounds", 0, "reviews a pull request gets at most (default: loop.max_rounds)")
	verbose := fs.Bool("verbose", false, "verbose logging on stderr")
	positional, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 0 {
		return errors.New("usage: conclave loop [--once] [--dry-run] [--interval D] [--max-rounds N]")
	}
	if *interval < 0 || *maxRounds < 0 {
		return errors.New("--interval and --max-rounds must be positive")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if *interval > 0 {
		cfg.Loop.Interval = *interval
	}
	if *maxRounds > 0 {
		cfg.Loop.MaxRounds = *maxRounds
	}
	logger := newLogger(stderr, *verbose)
	logInherited(logger, cfg)
	for _, w := range config.Warnings(cfg) {
		logger.Warn(w)
	}
	a := &app.App{Config: cfg, Logger: logger}
	if err := a.Loop(ctx, app.LoopRequest{Once: *once, DryRun: *dryRun, Out: stdout}); err != nil {
		return fmt.Errorf("loop: %w", err)
	}
	return nil
}
