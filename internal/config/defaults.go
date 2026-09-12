package config

import "time"

// Default values applied when the YAML leaves a field empty.
const (
	DefaultRemote          = "origin"
	DefaultGitHubBaseURL   = "https://api.github.com"
	DefaultMaxParallel     = 3
	DefaultTimeout         = 30 * time.Minute
	DefaultAgentTimeout    = 15 * time.Minute
	DefaultLeadTimeout     = 15 * time.Minute
	DefaultMaxOutputBytes  = 1 << 20
	DefaultMaxDiffBytes    = 5 << 20
	DefaultMaxFiles        = 3000
	DefaultMaxFindings     = 200
	DefaultMaxIssues       = 10
	DefaultMaxComments     = 200
	DefaultMaxCommentBytes = 8 << 10
)

func applyDefaults(cfg *Config) {
	if cfg.Forge.Remote == "" {
		cfg.Forge.Remote = DefaultRemote
	}
	if cfg.Forge.BaseURL == "" && cfg.Forge.Provider == ProviderGitHub {
		cfg.Forge.BaseURL = DefaultGitHubBaseURL
	}
	if cfg.Forge.TokenEnv == "" {
		switch cfg.Forge.Provider {
		case ProviderGitHub:
			cfg.Forge.TokenEnv = "GITHUB_TOKEN"
		case ProviderGitea:
			cfg.Forge.TokenEnv = "GITEA_TOKEN"
		}
	}
	if cfg.Review.MaxParallel == 0 {
		cfg.Review.MaxParallel = DefaultMaxParallel
	}
	if cfg.Review.Timeout == 0 {
		cfg.Review.Timeout = DefaultTimeout
	}
	if cfg.Review.AgentTimeout == 0 {
		cfg.Review.AgentTimeout = DefaultAgentTimeout
	}
	if cfg.Review.LeadTimeout == 0 {
		cfg.Review.LeadTimeout = DefaultLeadTimeout
	}
	if cfg.Review.Limits.MaxOutputBytes == 0 {
		cfg.Review.Limits.MaxOutputBytes = DefaultMaxOutputBytes
	}
	if cfg.Review.Limits.MaxDiffBytes == 0 {
		cfg.Review.Limits.MaxDiffBytes = DefaultMaxDiffBytes
	}
	if cfg.Review.Limits.MaxFiles == 0 {
		cfg.Review.Limits.MaxFiles = DefaultMaxFiles
	}
	if cfg.Review.Limits.MaxFindings == 0 {
		cfg.Review.Limits.MaxFindings = DefaultMaxFindings
	}
	if cfg.Review.Limits.MaxIssues == 0 {
		cfg.Review.Limits.MaxIssues = DefaultMaxIssues
	}
	if cfg.Review.Limits.MaxComments == 0 {
		cfg.Review.Limits.MaxComments = DefaultMaxComments
	}
	if cfg.Review.Limits.MaxCommentBytes == 0 {
		cfg.Review.Limits.MaxCommentBytes = DefaultMaxCommentBytes
	}
	if cfg.Output.Format == "" {
		cfg.Output.Format = FormatMarkdown
	}
	for i := range cfg.Agents {
		if cfg.Agents[i].Input == "" {
			cfg.Agents[i].Input = InputStdin
		}
		if cfg.Agents[i].Output == "" {
			cfg.Agents[i].Output = OutputAuto
		}
	}
}
