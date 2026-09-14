package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/bornholm/conclave/internal/app"
	"github.com/bornholm/conclave/internal/config"
	"github.com/bornholm/conclave/internal/output"
)

func runReview(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("review", stderr)
	configPath := fs.String("config", config.DefaultFileName, "configuration file")
	format := fs.String("format", "", "output format: markdown or json (default: configuration)")
	keep := fs.Bool("keep-worktrees", false, "keep the temporary worktrees after the run")
	verbose := fs.Bool("verbose", false, "verbose logging on stderr")
	positional, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return errors.New("usage: conclave review <pull-request-number>")
	}
	number, err := strconv.ParseInt(positional[0], 10, 64)
	if err != nil || number <= 0 {
		return fmt.Errorf("invalid pull request number %q", positional[0])
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if *format != "" {
		if *format != config.FormatMarkdown && *format != config.FormatJSON {
			return fmt.Errorf("invalid --format %q", *format)
		}
		cfg.Output.Format = *format
	}
	logger := newLogger(stderr, *verbose)
	for _, w := range config.Warnings(cfg) {
		logger.Warn(w)
	}
	a := &app.App{Config: cfg, Logger: logger}
	res, err := a.Review(ctx, app.ReviewRequest{Number: number, KeepWorktrees: *keep})
	if err != nil {
		if res != nil && res.RunDir != "" {
			fmt.Fprintln(stderr, "artifacts:", res.RunDir)
		}
		return err
	}
	if cfg.Output.Format == config.FormatJSON {
		return output.JSON(stdout, res.Review)
	}
	return output.Markdown(stdout, res.Review, output.Options{
		ShowAttribution: cfg.ShowAttribution(), ShowFailedAgents: cfg.ShowFailedAgents(),
	})
}
