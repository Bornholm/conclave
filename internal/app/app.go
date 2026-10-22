// Package app orchestrates a review run end to end.
package app

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/bornholm/conclave/internal/agent"
	"github.com/bornholm/conclave/internal/config"
	"github.com/bornholm/conclave/internal/forge"
	"github.com/bornholm/conclave/internal/forge/factory"
	"github.com/bornholm/conclave/internal/gitrepo"
	"github.com/bornholm/conclave/internal/process"
	"github.com/bornholm/conclave/internal/prompt"
)

// App wires the components. Zero values fall back to production defaults.
type App struct {
	Config *config.Config
	Logger *slog.Logger
	// Dir is the directory used to locate the repository (default: cwd).
	Dir string
	// Forge overrides the forge built from the configuration (tests).
	Forge forge.Forge
	// HTTPClient is used by the forge when Forge is nil.
	HTTPClient *http.Client
	// Process runs agent commands (default: ExecRunner).
	Process process.Runner
	// WorktreeBase hosts the temporary worktrees (default: os.TempDir()/conclave).
	WorktreeBase string
	// RunsBase hosts the run artifacts (default: <git common dir>/conclave/runs).
	RunsBase string
}

func (a *App) logger() *slog.Logger {
	if a.Logger == nil {
		return slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	return a.Logger
}

func (a *App) forge() (forge.Forge, error) {
	if a.Forge != nil {
		return a.Forge, nil
	}
	return factory.New(a.Config.Forge, a.HTTPClient)
}

func (a *App) process() process.Runner {
	if a.Process != nil {
		return a.Process
	}
	return &process.ExecRunner{}
}

// OpenRepository locates the Git repository and resolves the configured remote.
func (a *App) OpenRepository(ctx context.Context) (*gitrepo.Git, error) {
	dir := a.Dir
	if dir == "" {
		dir = "."
	}
	return gitrepo.Open(ctx, dir)
}

// timeBudget is the time an agent started now has before it is stopped: its
// own timeout, or less when the run's deadline comes first. Telling the agent
// lets it pace itself instead of being killed with nothing to show.
func timeBudget(ctx context.Context, cfg *config.Config, a config.AgentConfig) prompt.TimeBudget {
	return newTimeBudget(time.Now(), agent.EffectiveTimeout(ctx, cfg, a))
}

// newTimeBudget renders a duration as a prompt budget, leaving it out when
// there is none left to state.
func newTimeBudget(now time.Time, d time.Duration) prompt.TimeBudget {
	if d <= 0 {
		return prompt.TimeBudget{}
	}
	return prompt.NewTimeBudget(now, d)
}
