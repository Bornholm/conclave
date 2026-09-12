package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/bornholm/conclave/internal/app"
	"github.com/bornholm/conclave/internal/config"
	"github.com/bornholm/conclave/internal/forge"
	"github.com/bornholm/conclave/internal/output"
)

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			*s = append(*s, part)
		}
	}
	return nil
}

func runTriage(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("triage", stderr)
	configPath := fs.String("config", config.DefaultFileName, "configuration file")
	format := fs.String("format", "", "output format: markdown or json (default: configuration)")
	rev := fs.String("rev", "HEAD", "revision the agents read the code at")
	all := fs.Bool("all", false, "triage the issues matching the filters instead of the given numbers")
	state := fs.String("state", forge.StateOpen, "with --all: open, closed or all")
	since := fs.String("since", "", "with --all: only issues updated since, e.g. 90d, 12w, 2026-01-01")
	limit := fs.Int("limit", 0, "with --all: maximum number of issues (default: triage.limits.max_issues)")
	var labels stringList
	fs.Var(&labels, "label", "with --all: only issues carrying this label, repeatable")
	apply := fs.Bool("apply", false, "add the proposed labels to the issues on the forge (adds only, never removes or closes)")
	keep := fs.Bool("keep-worktrees", false, "keep the temporary worktrees after the run")
	verbose := fs.Bool("verbose", false, "verbose logging on stderr")
	positional, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}

	req := app.TriageRequest{Revision: *rev, KeepWorktrees: *keep, Apply: *apply}
	if *all {
		if len(positional) > 0 {
			return errors.New("--all and explicit issue numbers are mutually exclusive")
		}
		switch *state {
		case forge.StateOpen, forge.StateClosed, forge.StateAll:
		default:
			return fmt.Errorf("invalid --state %q", *state)
		}
		req.Query = forge.IssueQuery{State: *state, Labels: labels, Limit: *limit}
		if *since != "" {
			t, err := parseSince(*since, time.Now())
			if err != nil {
				return err
			}
			req.Query.Since = t
		}
	} else {
		if len(positional) == 0 {
			return errors.New("usage: conclave triage <issue-number>... (or --all)")
		}
		for _, arg := range positional {
			n, err := strconv.ParseInt(strings.TrimPrefix(arg, "#"), 10, 64)
			if err != nil || n <= 0 {
				return fmt.Errorf("invalid issue number %q", arg)
			}
			req.Numbers = append(req.Numbers, n)
		}
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
	a := &app.App{Config: cfg, Logger: newLogger(stderr, *verbose)}
	res, err := a.Triage(ctx, req)
	if err != nil {
		if res != nil && res.RunDir != "" {
			fmt.Fprintln(stderr, "artifacts:", res.RunDir)
		}
		return err
	}
	if cfg.Output.Format == config.FormatJSON {
		return output.TriageJSON(stdout, res.Triage)
	}
	return output.TriageMarkdown(stdout, res.Triage, output.Options{ShowAttribution: cfg.ShowAttribution()})
}

// parseSince accepts a duration with a day or week suffix, a Go duration, or
// a date. Days and weeks are what people actually type for stale issues.
func parseSince(v string, now time.Time) (time.Time, error) {
	v = strings.TrimSpace(v)
	if n, ok := strings.CutSuffix(v, "d"); ok {
		days, err := strconv.Atoi(n)
		if err == nil {
			return now.AddDate(0, 0, -days), nil
		}
	}
	if n, ok := strings.CutSuffix(v, "w"); ok {
		weeks, err := strconv.Atoi(n)
		if err == nil {
			return now.AddDate(0, 0, -7*weeks), nil
		}
	}
	if n, ok := strings.CutSuffix(v, "m"); ok {
		months, err := strconv.Atoi(n)
		if err == nil && !strings.HasSuffix(v, "ms") {
			return now.AddDate(0, -months, 0), nil
		}
	}
	if d, err := time.ParseDuration(v); err == nil {
		return now.Add(-d), nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid --since %q (use 90d, 12w, 720h or 2026-01-01)", v)
}
