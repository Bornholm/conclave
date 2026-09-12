// Package config loads and validates the .conclave.yaml configuration file.
package config

import "time"

const (
	// CurrentVersion is the only supported configuration schema version.
	CurrentVersion = 1

	ProviderGitHub = "github"
	ProviderGitea  = "gitea"

	RoleReviewer = "reviewer"
	RoleLead     = "lead"

	InputStdin    = "stdin"
	InputArgument = "argument"
	InputFile     = "file"

	FormatMarkdown = "markdown"
	FormatJSON     = "json"

	OutputAuto   = "auto"
	OutputPiJSON = "pi-json"

	// PromptFilePlaceholder is substituted in agent commands when input is "file".
	PromptFilePlaceholder = "{prompt_file}"
	// WorktreePlaceholder is substituted in agent commands with the absolute
	// worktree path, for tools that take an explicit directory flag.
	WorktreePlaceholder = "{worktree}"

	DefaultFileName = ".conclave.yaml"
)

// Config is the root configuration document.
type Config struct {
	Version int           `yaml:"version"`
	Forge   ForgeConfig   `yaml:"forge"`
	Review  ReviewConfig  `yaml:"review"`
	Output  OutputConfig  `yaml:"output"`
	Agents  []AgentConfig `yaml:"agents"`
}

// ForgeConfig describes the forge hosting the pull request.
type ForgeConfig struct {
	Provider string `yaml:"provider"`
	Remote   string `yaml:"remote"`
	BaseURL  string `yaml:"base_url"`
	TokenEnv string `yaml:"token_env"`
}

// ReviewConfig tunes the review run.
type ReviewConfig struct {
	MaxParallel   int           `yaml:"max_parallel"`
	Timeout       time.Duration `yaml:"timeout"`
	AgentTimeout  time.Duration `yaml:"agent_timeout"`
	LeadTimeout   time.Duration `yaml:"lead_timeout"`
	KeepWorktrees bool          `yaml:"keep_worktrees"`
	FailFast      bool          `yaml:"fail_fast"`
	Include       IncludeConfig `yaml:"include"`
	Limits        LimitsConfig  `yaml:"limits"`
}

// IncludeConfig selects which context is given to agents.
type IncludeConfig struct {
	AssociatedIssues *bool `yaml:"associated_issues"`
	Discussion       *bool `yaml:"discussion"`
	ChangedFiles     *bool `yaml:"changed_files"`
	Diff             *bool `yaml:"diff"`
}

// LimitsConfig bounds sizes to keep the run safe.
type LimitsConfig struct {
	MaxOutputBytes  int64 `yaml:"max_output_bytes"`
	MaxDiffBytes    int64 `yaml:"max_diff_bytes"`
	MaxFiles        int   `yaml:"max_files"`
	MaxFindings     int   `yaml:"max_findings"`
	MaxIssues       int   `yaml:"max_issues"`
	MaxComments     int   `yaml:"max_comments"`
	MaxCommentBytes int   `yaml:"max_comment_bytes"`
}

// OutputConfig controls rendering.
type OutputConfig struct {
	Format           string `yaml:"format"`
	ShowAttribution  *bool  `yaml:"show_attribution"`
	ShowFailedAgents *bool  `yaml:"show_failed_agents"`
}

// AgentConfig describes one local agent command.
type AgentConfig struct {
	ID                string            `yaml:"id"`
	Role              string            `yaml:"role"`
	Command           []string          `yaml:"command"`
	Input             string            `yaml:"input"`
	Output            string            `yaml:"output"`
	Model             string            `yaml:"model"`
	MaxOutputBytes    int64             `yaml:"max_output_bytes"`
	Specialties       []string          `yaml:"specialties"`
	Environment       map[string]string `yaml:"environment"`
	InheritEnv        *bool             `yaml:"inherit_env"`
	AllowProtectedEnv bool              `yaml:"allow_protected_env"`
	Timeout           time.Duration     `yaml:"timeout"`
}

// Reviewers returns the agents with the reviewer role, in configuration order.
func (c *Config) Reviewers() []AgentConfig {
	var out []AgentConfig
	for _, a := range c.Agents {
		if a.Role == RoleReviewer {
			out = append(out, a)
		}
	}
	return out
}

// Lead returns the lead agent. Validate guarantees exactly one exists.
func (c *Config) Lead() AgentConfig {
	for _, a := range c.Agents {
		if a.Role == RoleLead {
			return a
		}
	}
	return AgentConfig{}
}

// EffectiveTimeout returns the agent timeout, falling back to the review defaults.
func (c *Config) EffectiveTimeout(a AgentConfig) time.Duration {
	if a.Timeout > 0 {
		return a.Timeout
	}
	if a.Role == RoleLead {
		return c.Review.LeadTimeout
	}
	return c.Review.AgentTimeout
}

// EffectiveMaxOutputBytes returns the stdout/stderr cap for an agent.
func (c *Config) EffectiveMaxOutputBytes(a AgentConfig) int64 {
	if a.MaxOutputBytes > 0 {
		return a.MaxOutputBytes
	}
	return c.Review.Limits.MaxOutputBytes
}

// InheritsEnv reports whether the agent inherits the parent environment.
func (a AgentConfig) InheritsEnv() bool {
	return a.InheritEnv == nil || *a.InheritEnv
}

func boolValue(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

// IncludeIssues reports whether associated issues are collected.
func (c *Config) IncludeIssues() bool { return boolValue(c.Review.Include.AssociatedIssues, true) }

// IncludeDiscussion reports whether pull request comments and reviews are collected.
func (c *Config) IncludeDiscussion() bool { return boolValue(c.Review.Include.Discussion, true) }

// IncludeChangedFiles reports whether the changed file list is given to agents.
func (c *Config) IncludeChangedFiles() bool { return boolValue(c.Review.Include.ChangedFiles, true) }

// IncludeDiff reports whether the diff is given to agents.
func (c *Config) IncludeDiff() bool { return boolValue(c.Review.Include.Diff, true) }

// ShowAttribution reports whether reviewer attribution is rendered.
func (c *Config) ShowAttribution() bool { return boolValue(c.Output.ShowAttribution, true) }

// ShowFailedAgents reports whether failed agents are rendered.
func (c *Config) ShowFailedAgents() bool { return boolValue(c.Output.ShowFailedAgents, true) }
