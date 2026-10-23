package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/bornholm/conclave/internal/agent"
	"github.com/bornholm/conclave/internal/config"
	"github.com/bornholm/conclave/internal/gitrepo"
	"github.com/bornholm/conclave/internal/integration"
)

func runAgents(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: conclave agents check|install")
	}
	switch args[0] {
	case "check":
		return runAgentsCheck(ctx, args[1:], stdout, stderr)
	case "install":
		return runAgentsInstall(ctx, args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown agents subcommand %q", args[0])
	}
}

func runAgentsInstall(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("agents install", stderr)
	project := fs.String("project", "", "install into this repository (\".\" for the current one) instead of the user configuration")
	force := fs.Bool("force", false, "replace files that differ from the bundled version")
	positional, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return fmt.Errorf("usage: conclave agents install <%s> [--project PATH] [--force]", strings.Join(integration.Agents(), "|"))
	}
	a, err := integration.Lookup(positional[0])
	if err != nil {
		return err
	}
	opts := integration.Options{Force: *force}
	if *project != "" {
		// The repository root, not the directory given: that is where the
		// agent looks for project files when Conclave runs it in a worktree.
		git, err := gitrepo.Open(ctx, *project)
		if err != nil {
			return err
		}
		opts.Project = git.Root()
	}
	rep, err := integration.Install(a, opts)
	if rep != nil {
		for _, r := range rep.Results {
			fmt.Fprintf(stdout, "%s %s\n", installMarks[r.Status], r.Path)
		}
		for _, w := range rep.Warnings {
			fmt.Fprintf(stdout, "! %s\n", w)
		}
	}
	return err
}

var installMarks = map[integration.Status]string{
	integration.Created:   "✓ created  ",
	integration.Unchanged: "= unchanged",
	integration.Replaced:  "✓ replaced ",
	integration.Conflict:  "✗ differs  ",
}

func runAgentsCheck(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("agents check", stderr)
	configPath := fs.String("config", config.DefaultFileName, "configuration file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	failed := 0
	for _, a := range cfg.Agents {
		path, err := agent.Check(a)
		if err != nil {
			failed++
			fmt.Fprintf(stdout, "✗ %-24s %-9s %v\n", a.ID, a.Role, err)
			continue
		}
		model := a.Model
		if model == "" {
			model = "unset (detected from output when possible)"
		}
		fmt.Fprintf(stdout, "✓ %-24s %-9s %s (input=%s, output=%s, timeout=%s, model=%s)\n", a.ID, a.Role, path, a.Input, a.Output, cfg.EffectiveTimeout(a), model)
	}
	if failed > 0 {
		return fmt.Errorf("%d agent(s) unavailable", failed)
	}
	return nil
}
