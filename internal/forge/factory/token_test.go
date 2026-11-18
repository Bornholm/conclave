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

// fakeCLI installs a stand-in for name at the front of PATH and returns its
// directory, so a test can read back the arguments the script recorded.
func fakeCLI(t *testing.T, name, script string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func TestResolveTokenFallsBackToGlab(t *testing.T) {
	dir := fakeCLI(t, "glab", `echo "$@" > "$(dirname "$0")/args"; echo glabtoken`)
	t.Setenv("MY_TOKEN", "")
	tok, src := ResolveToken(context.Background(), config.ForgeConfig{Provider: config.ProviderGitLab, TokenEnv: "MY_TOKEN", BaseURL: "https://gitlab.com"})
	if tok != "glabtoken" || src != TokenSourceGlab {
		t.Fatalf("got %q %q", tok, src)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	if string(args) != "config get token --host gitlab.com\n" {
		t.Errorf("glab args: %q", args)
	}
}

func TestResolveTokenGlabEnvWins(t *testing.T) {
	fakeCLI(t, "glab", `echo glabtoken`)
	t.Setenv("MY_TOKEN", "envtoken")
	if tok, src := ResolveToken(context.Background(), config.ForgeConfig{Provider: config.ProviderGitLab, TokenEnv: "MY_TOKEN", BaseURL: "https://gitlab.com"}); tok != "envtoken" || src != TokenSourceEnv {
		t.Errorf("got %q %q", tok, src)
	}
}

func TestResolveTokenGlabFailure(t *testing.T) {
	fakeCLI(t, "glab", `echo "not logged in" >&2; exit 1`)
	t.Setenv("MY_TOKEN", "")
	if tok, src := ResolveToken(context.Background(), config.ForgeConfig{Provider: config.ProviderGitLab, TokenEnv: "MY_TOKEN", BaseURL: "https://gitlab.com"}); tok != "" || src != TokenSourceNone {
		t.Errorf("got %q %q", tok, src)
	}
}

// glab 1.121 has no `auth token` subcommand: an unknown subcommand prints the
// help on stdout and exits 0. Whatever the command, output that is not a single
// bare word must never be taken for a token.
func TestResolveTokenGlabHelpIsNotAToken(t *testing.T) {
	fakeCLI(t, "glab", `printf 'USAGE\n  glab auth <command>\n'`)
	t.Setenv("MY_TOKEN", "")
	if tok, src := ResolveToken(context.Background(), config.ForgeConfig{Provider: config.ProviderGitLab, TokenEnv: "MY_TOKEN", BaseURL: "https://gitlab.com"}); tok != "" || src != TokenSourceNone {
		t.Errorf("help output must not pass as a token, got %q %q", tok, src)
	}
}

func TestResolveTokenGiteaNoGlab(t *testing.T) {
	fakeCLI(t, "glab", `echo glabtoken`)
	t.Setenv("MY_TOKEN", "")
	if tok, _ := ResolveToken(context.Background(), config.ForgeConfig{Provider: config.ProviderGitea, TokenEnv: "MY_TOKEN", BaseURL: "https://git.example.com"}); tok != "" {
		t.Errorf("glab must not be used for gitea, got %q", tok)
	}
}
