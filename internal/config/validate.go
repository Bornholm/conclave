package config

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/bornholm/conclave/internal/domain"
)

var (
	envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	agentIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
)

// protectedEnv lists variables an agent may not override without allow_protected_env.
var protectedEnv = map[string]bool{
	"PATH": true, "HOME": true,
	"GIT_DIR": true, "GIT_WORK_TREE": true, "GIT_INDEX_FILE": true,
	"GIT_COMMON_DIR": true, "GIT_OBJECT_DIRECTORY": true, "GIT_ALTERNATE_OBJECT_DIRECTORIES": true,
}

// IsProtectedEnv reports whether name is a protected environment variable.
func IsProtectedEnv(name string) bool { return protectedEnv[name] }

// Validate checks structural and semantic rules. All problems are reported together.
func Validate(cfg *Config) error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	if cfg.Version != CurrentVersion {
		add("version: must be %d, got %d", CurrentVersion, cfg.Version)
	}

	switch cfg.Forge.Provider {
	case ProviderGitHub, ProviderGitea, ProviderRedmine:
	case "":
		add("forge.provider: required (github, gitea or redmine)")
	default:
		add("forge.provider: unsupported %q (github, gitea or redmine)", cfg.Forge.Provider)
	}
	if (cfg.Forge.Provider == ProviderGitea || cfg.Forge.Provider == ProviderRedmine) && cfg.Forge.BaseURL == "" {
		add("forge.base_url: required for %s", cfg.Forge.Provider)
	}
	if cfg.Forge.BaseURL != "" {
		u, err := url.Parse(cfg.Forge.BaseURL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			add("forge.base_url: invalid URL %q", cfg.Forge.BaseURL)
		} else if u.Scheme != "https" && u.Scheme != "http" {
			add("forge.base_url: scheme must be http or https")
		}
	}
	if cfg.Forge.TokenEnv != "" && !envNameRe.MatchString(cfg.Forge.TokenEnv) {
		add("forge.token_env: must be an environment variable name, got %q", cfg.Forge.TokenEnv)
	}
	if strings.TrimSpace(cfg.Forge.Remote) == "" {
		add("forge.remote: required")
	}

	if cfg.Review.MaxParallel < 1 {
		add("review.max_parallel: must be >= 1")
	}
	for name, d := range map[string]int64{
		"review.timeout": int64(cfg.Review.Timeout), "review.agent_timeout": int64(cfg.Review.AgentTimeout), "review.lead_timeout": int64(cfg.Review.LeadTimeout),
	} {
		if d <= 0 {
			add("%s: must be positive", name)
		}
	}
	if cfg.Review.Limits.MaxOutputBytes <= 0 || cfg.Review.Limits.MaxDiffBytes <= 0 || cfg.Review.Limits.MaxFiles <= 0 || cfg.Review.Limits.MaxFindings <= 0 ||
		cfg.Review.Limits.MaxIssues <= 0 || cfg.Review.Limits.MaxComments <= 0 || cfg.Review.Limits.MaxCommentBytes <= 0 {
		add("review.limits: all limits must be positive")
	}

	switch cfg.Triage.Labels.Source {
	case LabelSourceForge:
	case LabelSourceList:
		if len(cfg.Triage.Labels.List) == 0 {
			add("triage.labels.list: required when source is list")
		}
	default:
		add("triage.labels.source: must be forge or list, got %q", cfg.Triage.Labels.Source)
	}
	if cfg.Triage.MaxParallel < 1 {
		add("triage.max_parallel: must be >= 1")
	}
	if cfg.Triage.Labels.MaxLabels < 1 {
		add("triage.labels.max_labels: must be >= 1")
	}
	if cfg.Triage.Limits.MaxIssues <= 0 || cfg.Triage.Limits.MaxComments <= 0 || cfg.Triage.Limits.MaxReferences <= 0 {
		add("triage.limits: all limits must be positive")
	}
	for status := range cfg.Triage.StatusLabels {
		if domain.NormalizeStatus(status) == "" {
			add("triage.status_labels: unknown status %q", status)
		}
	}

	if cfg.Plan.MaxParallel < 1 {
		add("plan.max_parallel: must be >= 1")
	}
	if cfg.Plan.Limits.MaxSteps <= 0 || cfg.Plan.Limits.MaxComments <= 0 || cfg.Plan.Limits.MaxReferences <= 0 {
		add("plan.limits: all limits must be positive")
	}

	if cfg.Ask.MaxParallel < 1 {
		add("ask.max_parallel: must be >= 1")
	}
	if cfg.Ask.Limits.MaxQuestionBytes <= 0 || cfg.Ask.Limits.MaxContextBytes <= 0 || cfg.Ask.Limits.MaxAnswerBytes <= 0 ||
		cfg.Ask.Limits.MaxKeyPoints <= 0 || cfg.Ask.Limits.MaxReferences <= 0 {
		add("ask.limits: all limits must be positive")
	}

	switch cfg.Output.Format {
	case FormatMarkdown, FormatJSON:
	default:
		add("output.format: must be markdown or json, got %q", cfg.Output.Format)
	}

	seen := map[string]bool{}
	reviewers, leads := 0, 0
	reviewerIDs := map[string]bool{}
	for i, a := range cfg.Agents {
		prefix := fmt.Sprintf("agents[%d]", i)
		if a.ID != "" {
			prefix = fmt.Sprintf("agents[%s]", a.ID)
		}
		if a.ID == "" {
			add("%s.id: required", prefix)
		} else if !agentIDRe.MatchString(a.ID) {
			add("%s.id: must match %s", prefix, agentIDRe)
		} else if seen[a.ID] {
			add("%s.id: duplicate", prefix)
		}
		seen[a.ID] = true
		switch a.Role {
		case RoleReviewer:
			reviewers++
			reviewerIDs[a.ID] = true
		case RoleLead:
			leads++
		default:
			add("%s.role: must be reviewer or lead, got %q", prefix, a.Role)
		}
		if len(a.Command) == 0 || strings.TrimSpace(a.Command[0]) == "" {
			add("%s.command: must contain at least the executable", prefix)
		}
		switch a.Input {
		case InputStdin, InputArgument:
		case InputFile:
			found := false
			for _, arg := range a.Command {
				if strings.Contains(arg, PromptFilePlaceholder) {
					found = true
				}
			}
			if !found {
				add("%s.command: input=file requires a %s placeholder", prefix, PromptFilePlaceholder)
			}
		default:
			add("%s.input: must be stdin, argument or file, got %q", prefix, a.Input)
		}
		switch a.Output {
		case OutputAuto, OutputPiJSON:
		default:
			add("%s.output: must be auto or pi-json, got %q", prefix, a.Output)
		}
		if a.MaxOutputBytes < 0 {
			add("%s.max_output_bytes: must be positive", prefix)
		}
		if a.Timeout < 0 {
			add("%s.timeout: must be positive", prefix)
		}
		for name := range a.Environment {
			if !envNameRe.MatchString(name) {
				add("%s.environment: invalid variable name %q", prefix, name)
				continue
			}
			if IsProtectedEnv(name) && !a.AllowProtectedEnv {
				add("%s.environment: %s is protected; set allow_protected_env: true to override", prefix, name)
			}
		}
	}
	for _, id := range cfg.Triage.Reviewers {
		if !reviewerIDs[id] {
			add("triage.reviewers: %q is not a configured reviewer", id)
		}
	}
	for _, id := range cfg.Plan.Planners {
		if !reviewerIDs[id] {
			add("plan.planners: %q is not a configured reviewer", id)
		}
	}
	for _, id := range cfg.Ask.Respondents {
		if !reviewerIDs[id] {
			add("ask.respondents: %q is not a configured reviewer", id)
		}
	}
	if reviewers == 0 {
		add("agents: at least one reviewer is required")
	}
	if leads != 1 {
		add("agents: exactly one lead is required, got %d", leads)
	}
	return errors.Join(errs...)
}
