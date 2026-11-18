package factory

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/bornholm/conclave/internal/config"
)

// Token sources reported by ResolveToken.
const (
	TokenSourceEnv  = "env"
	TokenSourceGH   = "gh"
	TokenSourceGlab = "glab"
	TokenSourceNone = ""
)

// ResolveToken returns the forge token and where it came from. The
// environment variable named by token_env wins. When it is unset or empty,
// the forge's own CLI is asked for the host's token: `gh auth token` for
// GitHub, `glab config get token` for GitLab. The token is never logged.
func ResolveToken(ctx context.Context, cfg config.ForgeConfig) (string, string) {
	if cfg.TokenEnv != "" {
		if v := strings.TrimSpace(os.Getenv(cfg.TokenEnv)); v != "" {
			return v, TokenSourceEnv
		}
	}
	host := ExpectedHost(cfg)
	switch cfg.Provider {
	case config.ProviderGitHub:
		args := []string{"auth", "token"}
		if host != "" {
			args = append(args, "--hostname", host)
		}
		if tok := cliToken(ctx, "gh", args, "GH_PROMPT_DISABLED=1"); tok != "" {
			return tok, TokenSourceGH
		}
	case config.ProviderGitLab:
		// glab reads the token per host and has no default: without --host it
		// prints nothing, so an unknown host is not worth asking about.
		if host == "" {
			break
		}
		if tok := cliToken(ctx, "glab", []string{"config", "get", "token", "--host", host}); tok != "" {
			return tok, TokenSourceGlab
		}
	}
	return "", TokenSourceNone
}

// cliToken runs a forge CLI and returns the token it printed. Anything that is
// not a single bare word is discarded: glab answers an unknown subcommand with
// its help text on stdout and exit status 0, which must never pass for a token.
func cliToken(ctx context.Context, bin string, args []string, env ...string) string {
	path, err := exec.LookPath(bin)
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdin = nil
	cmd.Env = append(append(os.Environ(), "NO_COLOR=1"), env...)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	tok := strings.TrimSpace(string(out))
	if tok == "" || strings.ContainsAny(tok, " \t\r\n") {
		return ""
	}
	return tok
}
