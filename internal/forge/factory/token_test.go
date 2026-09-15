package factory

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bornholm/conclave/internal/config"
)

// fakeGH installs a gh stand-in at the front of PATH that prints token for
// the hostname it receives, and records the hostname asked.
func fakeGH(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "gh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func TestResolveTokenEnvWins(t *testing.T) {
	fakeGH(t, `echo ghtoken`)
	t.Setenv("MY_TOKEN", "envtoken")
	tok, src := ResolveToken(context.Background(), config.ForgeConfig{Provider: config.ProviderGitHub, TokenEnv: "MY_TOKEN", BaseURL: config.DefaultGitHubBaseURL})
	if tok != "envtoken" || src != TokenSourceEnv {
		t.Errorf("got %q %q", tok, src)
	}
}

func TestResolveTokenFallsBackToGH(t *testing.T) {
	dir := fakeGH(t, `echo "$@" > "$(dirname "$0")/args"; echo ghtoken`)
	t.Setenv("MY_TOKEN", "")
	tok, src := ResolveToken(context.Background(), config.ForgeConfig{Provider: config.ProviderGitHub, TokenEnv: "MY_TOKEN", BaseURL: "https://ghe.example.com/api/v3"})
	if tok != "ghtoken" || src != TokenSourceGH {
		t.Fatalf("got %q %q", tok, src)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	if string(args) != "auth token --hostname ghe.example.com\n" {
		t.Errorf("gh args: %q", args)
	}
}

func TestResolveTokenGHFailure(t *testing.T) {
	fakeGH(t, `echo "not logged in" >&2; exit 1`)
	t.Setenv("MY_TOKEN", "")
	if tok, src := ResolveToken(context.Background(), config.ForgeConfig{Provider: config.ProviderGitHub, TokenEnv: "MY_TOKEN", BaseURL: config.DefaultGitHubBaseURL}); tok != "" || src != TokenSourceNone {
		t.Errorf("got %q %q", tok, src)
	}
}

func TestResolveTokenGiteaNoGH(t *testing.T) {
	fakeGH(t, `echo ghtoken`)
	t.Setenv("MY_TOKEN", "")
	if tok, _ := ResolveToken(context.Background(), config.ForgeConfig{Provider: config.ProviderGitea, TokenEnv: "MY_TOKEN", BaseURL: "https://git.example.com"}); tok != "" {
		t.Errorf("gh must not be used for gitea, got %q", tok)
	}
}

func TestResolveTokenRedmineNoGH(t *testing.T) {
	fakeGH(t, `echo ghtoken`)
	t.Setenv("REDMINE_TOKEN", "")
	if tok, _ := ResolveToken(context.Background(), config.ForgeConfig{Provider: config.ProviderRedmine, TokenEnv: "REDMINE_TOKEN", BaseURL: "https://redmine.example.com"}); tok != "" {
		t.Errorf("gh must not be used for redmine, got %q", tok)
	}
}
