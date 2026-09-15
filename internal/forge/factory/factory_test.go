package factory

import (
	"testing"

	"github.com/bornholm/conclave/internal/config"
	"github.com/bornholm/conclave/internal/domain"
)

func TestMatchesRepository(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.ForgeConfig
		repo domain.Repository
		want bool
	}{
		{
			name: "github matches github.com",
			cfg:  config.ForgeConfig{Provider: config.ProviderGitHub, BaseURL: "https://api.github.com"},
			repo: domain.Repository{Host: "github.com", Owner: "acme", Name: "proj"},
			want: true,
		},
		{
			name: "github does not match gitea host",
			cfg:  config.ForgeConfig{Provider: config.ProviderGitHub, BaseURL: "https://api.github.com"},
			repo: domain.Repository{Host: "forge.cadoles.com", Owner: "acme", Name: "proj"},
			want: false,
		},
		{
			name: "gitea matches its host",
			cfg:  config.ForgeConfig{Provider: config.ProviderGitea, BaseURL: "https://forge.cadoles.com"},
			repo: domain.Repository{Host: "forge.cadoles.com", Owner: "acme", Name: "proj"},
			want: true,
		},
		{
			name: "redmine always matches (not a git host)",
			cfg:  config.ForgeConfig{Provider: config.ProviderRedmine, BaseURL: "https://dproj.cnous.fr"},
			repo: domain.Repository{Host: "forge.cadoles.com", Owner: "acme", Name: "proj"},
			want: true,
		},
		{
			name: "gitea does not match github",
			cfg:  config.ForgeConfig{Provider: config.ProviderGitea, BaseURL: "https://git.example.com"},
			repo: domain.Repository{Host: "github.com", Owner: "acme", Name: "proj"},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MatchesRepository(tc.cfg, tc.repo); got != tc.want {
				t.Errorf("MatchesRepository() = %v, want %v", got, tc.want)
			}
		})
	}
}
