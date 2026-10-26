package config

import (
	"os"
	"path/filepath"
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
	// An ask uses every reviewer, like a plan.
	if len(cfg.Respondents()) != 1 || !cfg.AskUsesLead() || cfg.Ask.Limits.MaxContextBytes != DefaultMaxContextBytes {
		t.Errorf("ask defaults: %+v", cfg.Ask)
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
		"redmine no url":      {strings.Replace(validYAML, "provider: github", "provider: redmine", 1), "base_url: required"},
		"bad token env":       {strings.Replace(validYAML, "provider: github", "provider: github\n  token_env: ghp-secret", 1), "token_env"},
		"protected env":       {strings.Replace(validYAML, "command: [echo]", "command: [echo]\n    environment: {PATH: /x}", 1), "PATH is protected"},
		"dup id":              {strings.Replace(validYAML, "id: lead", "id: rev", 1), "duplicate"},
		"bad version":         {strings.Replace(validYAML, "version: 1", "version: 2", 1), "version"},
		"file no placeholder": {strings.Replace(validYAML, "command: [echo]", "command: [echo]\n    input: file", 1), "placeholder"},
		"unknown planner":     {strings.Replace(validYAML, "review:", "plan:\n  planners: [nope]\nreview:", 1), "plan.planners"},
		"unknown respondent":  {strings.Replace(validYAML, "review:", "ask:\n  respondents: [nope]\nreview:", 1), "ask.respondents"},
		"respondent twice":    {strings.Replace(validYAML, "review:", "ask:\n  respondents: [rev, rev]\nreview:", 1), `ask.respondents: "rev" is listed twice`},
		"planner twice":       {strings.Replace(validYAML, "review:", "plan:\n  planners: [rev, rev]\nreview:", 1), `plan.planners: "rev" is listed twice`},
		"unknown writer":      {strings.Replace(validYAML, "review:", "loop:\n  writer: nope\nreview:", 1), `loop.writer: "nope" is not a configured agent`},
		"tiny comment cap":    {strings.Replace(validYAML, "review:", "loop:\n  max_comment_bytes: 100\nreview:", 1), "loop.max_comment_bytes: must be at least 1024"},
		"negative rounds":     {strings.Replace(validYAML, "review:", "loop:\n  max_rounds: -1\nreview:", 1), "loop.max_rounds"},
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

// writeConfig writes a configuration file, creating its directory.
func writeConfig(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadInheritsTheParentDirectories(t *testing.T) {
	top := t.TempDir()
	// The top file stops the search, so that nothing above the test's
	// directory is read.
	writeConfig(t, filepath.Join(top, DefaultFileName), `
version: 1
inherits: false
review:
  timeout: 20m
  diff_exclude: ["*.min.css"]
agents:
  - id: rev
    role: reviewer
    command: [pi, --model, a]
    environment: {A: "1"}
  - id: lead
    role: lead
    command: [pi]
`)
	// A directory between the two without a configuration is skipped.
	project := filepath.Join(top, "group", "middle", "project")
	writeConfig(t, filepath.Join(top, "group", DefaultFileName), `
review:
  agent_timeout: 5m
`)
	writeConfig(t, filepath.Join(project, DefaultFileName), `
forge:
  provider: github
review:
  diff_exclude: ["*_templ.go"]
agents:
  - id: rev
    model: b
    environment: {B: "2"}
  - id: rev2
    role: reviewer
    command: [claude]
`)
	cfg, err := Load(filepath.Join(project, DefaultFileName))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(top, DefaultFileName), filepath.Join(top, "group", DefaultFileName), filepath.Join(project, DefaultFileName)}
	if strings.Join(cfg.Sources, "\n") != strings.Join(want, "\n") {
		t.Errorf("sources: %v", cfg.Sources)
	}
	if cfg.Forge.Provider != ProviderGitHub || cfg.Review.Timeout != 20*time.Minute || cfg.Review.AgentTimeout != 5*time.Minute {
		t.Errorf("fields: %+v %+v", cfg.Forge, cfg.Review)
	}
	// A list other than the agents replaces the inherited one.
	if strings.Join(cfg.Review.DiffExclude, ",") != "*_templ.go" {
		t.Errorf("diff_exclude: %v", cfg.Review.DiffExclude)
	}
	// The agents merge by id: rev keeps its command and gains a model,
	// rev2 is added after the inherited ones.
	var ids []string
	for _, a := range cfg.Agents {
		ids = append(ids, a.ID)
	}
	if strings.Join(ids, ",") != "rev,lead,rev2" {
		t.Fatalf("agents: %v", ids)
	}
	rev := cfg.Agents[0]
	if strings.Join(rev.Command, " ") != "pi --model a" || rev.Model != "b" || rev.Environment["A"] != "1" || rev.Environment["B"] != "2" {
		t.Errorf("rev: %+v", rev)
	}
}

func TestLoadStopsWhereInheritsIsFalse(t *testing.T) {
	top := t.TempDir()
	writeConfig(t, filepath.Join(top, DefaultFileName), "version: 1\ninherits: false\nreview:\n  timeout: 20m\n")
	project := filepath.Join(top, "project")
	writeConfig(t, filepath.Join(project, DefaultFileName), "inherits: false\n"+validYAML)
	cfg, err := Load(filepath.Join(project, DefaultFileName))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Sources) != 1 || cfg.Review.Timeout != 20*time.Minute {
		t.Errorf("a configuration with inherits: false must not inherit: %v %v", cfg.Sources, cfg.Review.Timeout)
	}
	// With "inherits: false" on the parent, its own parents are not read.
	writeConfig(t, filepath.Join(top, DefaultFileName), "version: 1\ninherits: false\nreview:\n  timeout: nope\n")
	writeConfig(t, filepath.Join(project, "group", DefaultFileName), "inherits: false\nreview:\n  timeout: 10m\n")
	writeConfig(t, filepath.Join(project, "group", "leaf", DefaultFileName), "forge:\n  provider: github\nagents: [{id: rev, role: reviewer, command: [echo]}, {id: lead, role: lead, command: [echo]}]\nversion: 1\n")
	cfg, err = Load(filepath.Join(project, "group", "leaf", DefaultFileName))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Sources) != 2 || cfg.Review.Timeout != 10*time.Minute {
		t.Errorf("sources %v timeout %v", cfg.Sources, cfg.Review.Timeout)
	}
}

func TestLoadNamesTheInheritedFileAtFault(t *testing.T) {
	top := t.TempDir()
	parent := filepath.Join(top, DefaultFileName)
	writeConfig(t, parent, "version: 1\ninherits: false\nreveiw: {}\n")
	project := filepath.Join(top, "project", DefaultFileName)
	writeConfig(t, project, validYAML)
	if _, err := Load(project); err == nil || !strings.Contains(err.Error(), parent) || !strings.Contains(err.Error(), "reveiw") {
		t.Errorf("an unknown field in a parent must name the parent: %v", err)
	}
	writeConfig(t, parent, "version: 2\ninherits: false\n")
	if _, err := Load(project); err == nil || !strings.Contains(err.Error(), parent) || !strings.Contains(err.Error(), "version: must be 1") {
		t.Errorf("another version in a parent: %v", err)
	}
	// The merged result is validated: a parent cannot be left incomplete
	// unless the project completes it, and the error names both.
	writeConfig(t, parent, "version: 1\ninherits: false\n")
	writeConfig(t, project, "agents: []\n")
	if _, err := Load(project); err == nil || !strings.Contains(err.Error(), "merged with "+parent) || !strings.Contains(err.Error(), "forge.provider") {
		t.Errorf("merged validation: %v", err)
	}
}

func TestLoadExpandsAnAliasWhoseAnchorIsReplaced(t *testing.T) {
	top := t.TempDir()
	// The parent's agents alias the command its review section anchors;
	// the project replaces that section, anchor and all.
	writeConfig(t, filepath.Join(top, DefaultFileName), `
version: 1
inherits: false
review:
  diff_exclude: &cmd [pi, -p]
agents:
  - {id: rev, role: reviewer, command: *cmd}
  - {id: lead, role: lead, command: *cmd}
`)
	project := filepath.Join(top, "project", DefaultFileName)
	writeConfig(t, project, "forge: {provider: github}\nreview:\n  diff_exclude: []\n")
	cfg, err := Load(project)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(cfg.Agents[0].Command, " ") != "pi -p" || len(cfg.Review.DiffExclude) != 0 {
		t.Errorf("agents %+v, diff_exclude %v", cfg.Agents, cfg.Review.DiffExclude)
	}
	if cfg.Inherits != nil {
		t.Errorf("inherits is not part of the merged configuration: %v", *cfg.Inherits)
	}
}

func TestLoadDisablesAnInheritedAgent(t *testing.T) {
	top := t.TempDir()
	writeConfig(t, filepath.Join(top, DefaultFileName), `
version: 1
inherits: false
agents:
  - {id: rev, role: reviewer, command: [pi]}
  - {id: claude, role: reviewer, command: [claude]}
  - {id: lead, role: lead, command: [pi]}
`)
	project := filepath.Join(top, "project", DefaultFileName)
	writeConfig(t, project, "forge: {provider: github}\nagents:\n  - {id: claude, disabled: true}\n")
	cfg, err := Load(project)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, a := range cfg.Agents {
		ids = append(ids, a.ID)
	}
	if strings.Join(ids, ",") != "rev,lead" {
		t.Errorf("agents: %v", ids)
	}
	// A mark that matches no inherited agent is a wrong id, not a no-op.
	writeConfig(t, project, "forge: {provider: github}\nagents:\n  - {id: cluade, disabled: true}\n")
	if _, err := Load(project); err == nil || !strings.Contains(err.Error(), "no agent cluade is declared") {
		t.Errorf("a disabled id that matches nothing: %v", err)
	}
}

func TestLoadRefusesTwoAgentsWithOneIDInAFile(t *testing.T) {
	top := t.TempDir()
	writeConfig(t, filepath.Join(top, DefaultFileName), "version: 1\ninherits: false\nagents: [{id: lead, role: lead, command: [pi]}]\n")
	project := filepath.Join(top, "project", DefaultFileName)
	// Merged over the parent's agents, the two would be folded into one.
	writeConfig(t, project, "forge: {provider: github}\nagents:\n  - {id: rev, role: reviewer, command: [pi]}\n  - {id: rev, model: x}\n")
	if _, err := Load(project); err == nil || !strings.Contains(err.Error(), project) || !strings.Contains(err.Error(), "agents[rev].id: duplicate") {
		t.Errorf("a duplicate id must be refused against its file: %v", err)
	}
}

func TestLoadKeepsMergeKeysAndRefusesARecursiveAnchor(t *testing.T) {
	p := filepath.Join(t.TempDir(), DefaultFileName)
	writeConfig(t, p, `
version: 1
inherits: false
forge: {provider: github}
agents:
  - &base {id: rev, role: reviewer, command: [pi]}
  - {<<: *base, id: rev2}
  - {id: lead, role: lead, command: [pi]}
`)
	cfg, err := Load(p)
	if err != nil || len(cfg.Agents) != 3 || cfg.Agents[1].Role != RoleReviewer || cfg.Agents[1].Command[0] != "pi" {
		t.Fatalf("a merge key must survive the alias expansion: %v %+v", err, cfg)
	}
	// No field of the configuration can hold itself: the typed decode
	// refuses a recursive anchor before the aliases are expanded.
	writeConfig(t, p, "version: 1\ninherits: false\nagents: &a [*a]\n")
	if _, err := Load(p); err == nil {
		t.Error("a recursive anchor must be refused")
	}
}

func TestLoadEdgeCasesOfInheritance(t *testing.T) {
	top := t.TempDir()
	parent := filepath.Join(top, DefaultFileName)
	project := filepath.Join(top, "project", DefaultFileName)
	// An inherited file with nothing but comments declares nothing.
	writeConfig(t, parent, "# placeholder\n")
	writeConfig(t, project, "inherits: true\n"+validYAML)
	if _, err := Load(project); err != nil {
		t.Errorf("a comment-only parent: %v", err)
	}
	// A disabled agent a list still names is said as such.
	writeConfig(t, parent, "inherits: false\ntriage: {reviewers: [rev]}\n"+validYAML+"  - {id: rev2, role: reviewer, command: [echo]}\n")
	writeConfig(t, project, "agents: [{id: rev, disabled: true}]\n")
	if _, err := Load(project); err == nil || !strings.Contains(err.Error(), "agents[rev]: disabled, but triage.reviewers still names it") {
		t.Errorf("a disabled agent still listed: %v", err)
	}
	writeConfig(t, parent, "inherits: false\nloop: {writer: rev}\n"+validYAML+"  - {id: rev2, role: reviewer, command: [echo]}\n")
	if _, err := Load(project); err == nil || !strings.Contains(err.Error(), "agents[rev]: disabled, but loop.writer still names it") {
		t.Errorf("a disabled writer: %v", err)
	}
}

func TestLoopWriter(t *testing.T) {
	cfg, err := Decode(strings.NewReader(validYAML))
	if err != nil {
		t.Fatal(err)
	}
	if w := cfg.LoopWriter(); w.ID != "lead" {
		t.Errorf("default writer: %s", w.ID)
	}
	if cfg.Loop.Interval != DefaultLoopInterval || cfg.Loop.MaxRounds != DefaultLoopMaxRounds || cfg.Loop.MaxCommentBytes != DefaultLoopMaxCommentBytes {
		t.Errorf("defaults: %+v", cfg.Loop)
	}
	cfg.Loop.Writer = "rev"
	if w := cfg.LoopWriter(); w.ID != "rev" {
		t.Errorf("writer: %s", w.ID)
	}
}
