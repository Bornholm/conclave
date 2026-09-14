package cli

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"

	"github.com/bornholm/conclave/internal/agent"
	"github.com/bornholm/conclave/internal/config"
	"github.com/bornholm/conclave/internal/forge/factory"
	"github.com/bornholm/conclave/internal/gitrepo"
)

//go:embed example.yaml
var exampleConfig string

func runConfig(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: conclave config validate|example")
	}
	switch args[0] {
	case "validate":
		return runConfigValidate(ctx, args[1:], stdout, stderr)
	case "example":
		_, err := io.WriteString(stdout, exampleConfig)
		return err
	default:
		return fmt.Errorf("unknown config subcommand %q", args[0])
	}
}

func runConfigValidate(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("config validate", stderr)
	configPath := fs.String("config", config.DefaultFileName, "configuration file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(stdout, "✗ %v\n", err)
		return errors.New("configuration invalid")
	}
	fmt.Fprintf(stdout, "✓ configuration %s valid\n", *configPath)
	for _, w := range config.Warnings(cfg) {
		fmt.Fprintf(stdout, "! %s\n", w)
	}
	failed := false
	fail := func(format string, a ...any) {
		failed = true
		fmt.Fprintf(stdout, "✗ "+format+"\n", a...)
	}

	git, err := gitrepo.Open(ctx, ".")
	if err != nil {
		fail("git repository: %v", err)
	} else {
		fmt.Fprintf(stdout, "✓ git repository: %s\n", git.Root())
		repo, err := git.Repository(ctx, cfg.Forge.Remote)
		if err != nil {
			fail("remote %s: %v", cfg.Forge.Remote, err)
		} else if !factory.MatchesRepository(cfg.Forge, repo) {
			fail("remote %s: %s/%s is hosted on %s, forge expects %s", cfg.Forge.Remote, repo.Owner, repo.Name, repo.Host, factory.ExpectedHost(cfg.Forge))
		} else {
			fmt.Fprintf(stdout, "✓ remote %s: %s/%s/%s\n", cfg.Forge.Remote, repo.Host, repo.Owner, repo.Name)
		}
	}
	switch _, src := factory.ResolveToken(ctx, cfg.Forge); src {
	case factory.TokenSourceEnv:
		fmt.Fprintf(stdout, "✓ token from %s\n", cfg.Forge.TokenEnv)
	case factory.TokenSourceGH:
		fmt.Fprintf(stdout, "✓ token from gh auth token (%s is empty)\n", cfg.Forge.TokenEnv)
	default:
		fmt.Fprintf(stdout, "! no token: %s is empty and gh auth token is unavailable (only public repositories will work)\n", cfg.Forge.TokenEnv)
	}
	fmt.Fprintf(stdout, "✓ %d reviewer(s) configured\n", len(cfg.Reviewers()))
	fmt.Fprintf(stdout, "✓ 1 lead configured (%s)\n", cfg.Lead().ID)
	for _, a := range cfg.Agents {
		if _, err := agent.Check(a); err != nil {
			fail("agent %s: %v", a.ID, err)
		}
	}
	if failed {
		return errors.New("validation reported problems")
	}
	return nil
}
