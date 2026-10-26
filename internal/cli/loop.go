package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/bornholm/conclave/internal/app"
	"github.com/bornholm/conclave/internal/config"
)

// includeList is the pull request numbers of --include: 7, #7 or 7,8. A
// value naming none is refused rather than ignored, an unset variable most
// likely.
type includeList []int64

func (l *includeList) String() string {
	parts := make([]string, len(*l))
	for i, n := range *l {
		parts[i] = strconv.FormatInt(n, 10)
	}
	return strings.Join(parts, ",")
}

func (l *includeList) Set(v string) error {
	if strings.TrimSpace(strings.ReplaceAll(v, ",", "")) == "" {
		return errors.New("no pull request number")
	}
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimPrefix(part, "#"), 10, 64)
		if err != nil || n <= 0 {
			return fmt.Errorf("%q is not a pull request number", part)
		}
		*l = append(*l, n)
	}
	return nil
}

func runLoop(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("loop", stderr)
	configPath := fs.String("config", config.DefaultFileName, "configuration file")
	once := fs.Bool("once", false, "make one pass over the open pull requests, then exit")
	dryRun := fs.Bool("dry-run", false, "make one pass and print the comments instead of submitting them")
	interval := fs.Duration("interval", 0, "time between two passes (default: loop.interval)")
	maxRounds := fs.Int("max-rounds", 0, "reviews a pull request gets at most (default: loop.max_rounds)")
	var include includeList
	fs.Var(&include, "include", "take up this pull request even if it was reviewed by hand, repeatable")
	verbose := fs.Bool("verbose", false, "verbose logging on stderr")
	positional, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 0 {
		return errors.New("usage: conclave loop [--once] [--dry-run] [--interval D] [--max-rounds N] [--include N]...")
	}
	if *interval < 0 || *maxRounds < 0 {
		return errors.New("--interval and --max-rounds must be positive")
	}
	cfg, err := loadConfig(*configPath)
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
	logSources(logger, cfg)
	for _, w := range config.Warnings(cfg) {
		logger.Warn(w)
	}
	a := &app.App{Config: cfg, Logger: logger}
	if err := a.Loop(ctx, app.LoopRequest{Once: *once, DryRun: *dryRun, Include: include, Out: stdout}); err != nil {
		return fmt.Errorf("loop: %w", err)
	}
	return nil
}
