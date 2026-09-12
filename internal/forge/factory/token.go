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
	TokenSourceNone = ""
)

// ResolveToken returns the forge token and where it came from. The
// environment variable named by token_env wins. For GitHub, when it is unset
// or empty and the gh CLI is installed and logged in, `gh auth token` is used
// for the forge host. The token is never logged.
func ResolveToken(ctx context.Context, cfg config.ForgeConfig) (string, string) {
	if cfg.TokenEnv != "" {
		if v := strings.TrimSpace(os.Getenv(cfg.TokenEnv)); v != "" {
			return v, TokenSourceEnv
		}
	}
	if cfg.Provider != config.ProviderGitHub {
		return "", TokenSourceNone
	}
	if tok := ghAuthToken(ctx, ExpectedHost(cfg)); tok != "" {
		return tok, TokenSourceGH
	}
	return "", TokenSourceNone
}

func ghAuthToken(ctx context.Context, host string) string {
	gh, err := exec.LookPath("gh")
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	args := []string{"auth", "token"}
	if host != "" {
		args = append(args, "--hostname", host)
	}
	cmd := exec.CommandContext(ctx, gh, args...)
	cmd.Stdin = nil
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "NO_COLOR=1")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
