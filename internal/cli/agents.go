package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/bornholm/conclave/internal/agent"
	"github.com/bornholm/conclave/internal/config"
)

func runAgents(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "check" {
		return errors.New("usage: conclave agents check [--config PATH]")
	}
	fs := newFlagSet("agents check", stderr)
	configPath := fs.String("config", config.DefaultFileName, "configuration file")
	if err := fs.Parse(args[1:]); err != nil {
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
