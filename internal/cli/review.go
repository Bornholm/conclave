package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
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
	since := fs.String("since", "", "review incrementally against a previous review: \"last\", a run id, a run directory or a review.json")
	responses := fs.String("responses", "", "with --since: file holding the author's answers to the previous review, \"-\" for standard input")
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
	if *responses != "" && *since == "" {
		return errors.New("--responses answers a previous review: it needs --since")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	var answers string
	if *responses != "" {
		data, err := readResponses(*responses, cfg.Review.Limits.MaxCommentBytes)
		if err != nil {
			return err
		}
		answers = string(data)
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
	res, err := a.Review(ctx, app.ReviewRequest{Number: number, KeepWorktrees: *keep, Since: *since, Responses: answers})
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

// readResponses reads the author's answers from a file or, with "-", from
// standard input, at most max bytes plus one: the app cuts them to max and
// warns, and the rest is never held in memory.
func readResponses(path string, max int) ([]byte, error) {
	if path == "-" {
		data, err := readAllStdin(max)
		if err != nil {
			return nil, fmt.Errorf("--responses: %w", err)
		}
		return data, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("--responses: %w", err)
	}
	defer f.Close()
	data, err := readLimited(f, max)
	if err != nil {
		return nil, fmt.Errorf("--responses: %w", err)
	}
	return data, nil
}
