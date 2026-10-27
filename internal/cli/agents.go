package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	project := fs.String("project", "", "install into the repository holding PATH, at its root (\".\" for the current one), instead of the user configuration")
	force := fs.Bool("force", false, "replace files that differ from the bundled version")
	configPath := fs.String("config", config.DefaultFileName, "configuration whose agent commands are checked for what the install replaces, when it exists (with --project, the one at its root)")
	positional, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	projectSet := set["project"]
	if projectSet && *project == "" {
		// Most likely an unset variable: installing into the user
		// configuration instead would be a different command.
		return errors.New("--project needs a path, \".\" for the current repository")
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
			return fmt.Errorf("--project %s: %w", *project, err)
		}
		opts.Project = git.Root()
		// The configuration of the repository installed into, not of
		// wherever the command happens to run.
		if !set["config"] {
			*configPath = filepath.Join(opts.Project, config.DefaultFileName)
		}
	}
	rep, err := integration.Install(a, opts)
	if rep != nil {
		for _, r := range rep.Results {
			if r.Err != nil {
				fmt.Fprintf(stdout, "%s %s: %v\n", installMarks[r.Status], r.Path, r.Err)
				continue
			}
			fmt.Fprintf(stdout, "%s %s\n", installMarks[r.Status], r.Path)
		}
		for _, w := range rep.Warnings {
			fmt.Fprintf(stdout, "! %s\n", w)
		}
	}
	for _, w := range staleCommands(a, *configPath) {
		fmt.Fprintf(stdout, "! %s\n", w)
	}
	return err
}

// staleCommands warns about agents of the configuration at path whose command
// still loads or configures what the install replaces. A missing
// configuration is not this command's concern: it says nothing then. One
// that does not load, an inherited file refused for instance, is said, since
// its commands then go unchecked.
func staleCommands(a integration.Agent, path string) []string {
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	// The commands are read, not run: the env file is not needed.
	cfg, err := config.Load(path)
	if err != nil {
		return []string{fmt.Sprintf("agent commands not checked: %v", err)}
	}
	// A stale command may come from an inherited file, which is where it
	// must be removed.
	where := path
	if inherited := cfg.Inherited(); len(inherited) > 0 {
		where = fmt.Sprintf("%s or a file it inherits (%s)", path, strings.Join(inherited, ", "))
	}
	var warnings []string
	for _, ag := range cfg.Agents {
		if stale := a.StaleArgs(ag.Command); len(stale) > 0 {
			warnings = append(warnings, fmt.Sprintf("%s: agent %s still passes %s, which the installed files replace: remove it from its command", where, ag.ID, strings.Join(stale, " ")))
		}
	}
	return warnings
}

var installMarks = map[integration.Status]string{
	integration.Created:   "✓ created  ",
	integration.Unchanged: "= unchanged",
	integration.Replaced:  "✓ replaced ",
	integration.Conflict:  "✗ differs  ",
	integration.Failed:    "✗ failed   ",
}

func runAgentsCheck(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("agents check", stderr)
	configPath := fs.String("config", config.DefaultFileName, "configuration file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	// An inherited file can declare the commands checked below.
	for _, src := range cfg.Inherited() {
		fmt.Fprintf(stdout, "✓ inherits %s\n", src)
	}
	printEnv(stdout, cfg.Env)
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
