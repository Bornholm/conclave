// Package factory instantiates forge backends from configuration.
package factory

import (
	"context"
	"fmt"
	"net/http"

	"github.com/bornholm/conclave/internal/config"
	"github.com/bornholm/conclave/internal/domain"
	"github.com/bornholm/conclave/internal/forge"
	"github.com/bornholm/conclave/internal/forge/gitea"
	"github.com/bornholm/conclave/internal/forge/github"
)

// New builds the forge backend described by the configuration. The token
// comes from ResolveToken; it may be empty for public repositories.
func New(cfg config.ForgeConfig, hc *http.Client) (forge.Forge, error) {
	token, _ := ResolveToken(context.Background(), cfg)
	switch cfg.Provider {
	case config.ProviderGitHub:
		return github.New(cfg.BaseURL, token, hc)
	case config.ProviderGitea:
		return gitea.New(cfg.BaseURL, token, hc)
	default:
		return nil, fmt.Errorf("unsupported forge provider %q", cfg.Provider)
	}
}

// ExpectedHost returns the host a Git remote must point to for this forge.
// For github.com the API lives on api.github.com while remotes use github.com.
func ExpectedHost(cfg config.ForgeConfig) string {
	u, err := parseHost(cfg.BaseURL)
	if err != nil {
		return ""
	}
	if cfg.Provider == config.ProviderGitHub && u == "api.github.com" {
		return "github.com"
	}
	return u
}

// MatchesRepository reports whether the remote repository host belongs to the forge.
func MatchesRepository(cfg config.ForgeConfig, repo domain.Repository) bool {
	want := ExpectedHost(cfg)
	if want == "" {
		return true
	}
	return equalHost(want, repo.Host)
}
