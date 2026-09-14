package config

import (
	"strings"
	"testing"
	"time"
)

const validYAML = `
version: 1
forge:
  provider: github
review:
  timeout: 20m
agents:
  - id: rev
    role: reviewer
    command: [echo]
  - id: lead
    role: lead
    command: [echo]
`

func TestDecodeValidAppliesDefaults(t *testing.T) {
	cfg, err := Decode(strings.NewReader(validYAML))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Forge.BaseURL != DefaultGitHubBaseURL || cfg.Forge.TokenEnv != "GITHUB_TOKEN" || cfg.Forge.Remote != "origin" {
		t.Errorf("forge defaults not applied: %+v", cfg.Forge)
	}
	if cfg.Review.Timeout != 20*time.Minute || cfg.Review.AgentTimeout != DefaultAgentTimeout {
		t.Errorf("durations: %+v", cfg.Review)
	}
	if cfg.Agents[0].Input != InputStdin || !cfg.Agents[0].InheritsEnv() {
		t.Errorf("agent defaults: %+v", cfg.Agents[0])
	}
	if len(cfg.Reviewers()) != 1 || cfg.Lead().ID != "lead" {
		t.Errorf("role helpers")
	}
	if cfg.EffectiveTimeout(cfg.Lead()) != DefaultLeadTimeout {
		t.Errorf("lead timeout")
	}
	// A plan uses every reviewer by default, a triage only the first one.
	if len(cfg.Planners()) != 1 || !cfg.PlanUsesLead() || cfg.Plan.Limits.MaxSteps != DefaultMaxPlanSteps {
		t.Errorf("plan defaults: %+v", cfg.Plan)
	}
}

func TestDecodeRejects(t *testing.T) {
	cases := map[string]struct{ yaml, want string }{
		"unknown field":       {strings.Replace(validYAML, "timeout: 20m", "max_paralel: 3", 1), "field max_paralel not found"},
		"two leads":           {strings.Replace(validYAML, "role: reviewer", "role: lead", 1), "exactly one lead"},
		"no reviewer":         {strings.Replace(validYAML, "role: reviewer", "role: lead", 1), "at least one reviewer"},
		"empty command":       {strings.Replace(validYAML, "command: [echo]", "command: []", 1), "at least the executable"},
		"bad provider":        {strings.Replace(validYAML, "provider: github", "provider: gitlab", 1), "unsupported"},
		"gitea no url":        {strings.Replace(validYAML, "provider: github", "provider: gitea", 1), "base_url: required"},
		"bad token env":       {strings.Replace(validYAML, "provider: github", "provider: github\n  token_env: ghp-secret", 1), "token_env"},
		"protected env":       {strings.Replace(validYAML, "command: [echo]", "command: [echo]\n    environment: {PATH: /x}", 1), "PATH is protected"},
		"dup id":              {strings.Replace(validYAML, "id: lead", "id: rev", 1), "duplicate"},
		"bad version":         {strings.Replace(validYAML, "version: 1", "version: 2", 1), "version"},
		"file no placeholder": {strings.Replace(validYAML, "command: [echo]", "command: [echo]\n    input: file", 1), "placeholder"},
		"unknown planner":     {strings.Replace(validYAML, "review:", "plan:\n  planners: [nope]\nreview:", 1), "plan.planners"},
		"empty":               {"", "empty document"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Decode(strings.NewReader(tc.yaml))
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

func TestProtectedEnvAllowed(t *testing.T) {
	y := strings.Replace(validYAML, "command: [echo]", "command: [echo]\n    environment: {HOME: /x}\n    allow_protected_env: true", 1)
	if _, err := Decode(strings.NewReader(y)); err != nil {
		t.Fatal(err)
	}
}
