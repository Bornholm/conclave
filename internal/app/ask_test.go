package app

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bornholm/conclave/internal/config"
	"github.com/bornholm/conclave/internal/domain"
)

// newAskApp builds an app with no forge and no repository: a question needs
// neither, and the test must fail if the code starts reaching for one.
func newAskApp(t *testing.T, agents ...config.AgentConfig) *App {
	t.Helper()
	cfg := &config.Config{Version: 1,
		Forge: config.ForgeConfig{Provider: config.ProviderGitHub, Remote: "origin", BaseURL: config.DefaultGitHubBaseURL},
		Review: config.ReviewConfig{MaxParallel: 2, Timeout: time.Minute, AgentTimeout: 10 * time.Second, LeadTimeout: 10 * time.Second,
			Limits: config.LimitsConfig{MaxOutputBytes: 1 << 20, MaxDiffBytes: 1 << 20, MaxFiles: 100, MaxFindings: 50, MaxIssues: 10, MaxComments: 100, MaxCommentBytes: 4096}},
		Output: config.OutputConfig{Format: "markdown"}, Agents: agents}
	config.ApplyDefaults(cfg)
	return &App{Config: cfg, WorktreeBase: filepath.Join(t.TempDir(), "wt"),
		RunsBase: filepath.Join(t.TempDir(), "runs"), Logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))}
}

func TestAskWithoutProject(t *testing.T) {
	a := newAskApp(t,
		agentCfg(t, "r1", config.RoleReviewer, "ask"),
		agentCfg(t, "r2", config.RoleReviewer, "exit-1"),
		agentCfg(t, "lead", config.RoleLead, "ask-lead"))
	res, err := a.Ask(context.Background(), AskRequest{
		Question: "Why does the call give up so early?",
		Context:  "here is the log: boom",
	})
	if err != nil {
		t.Fatal(err)
	}
	ans := res.Answer
	if !ans.Meta.LeadUsed || ans.Meta.RespondentsTotal != 2 || ans.Meta.RespondentsSucceeded != 1 {
		t.Errorf("meta: %+v", ans.Meta)
	}
	if ans.Question != "Why does the call give up so early?" {
		t.Errorf("the question must be echoed back: %q", ans.Question)
	}
	if !strings.Contains(ans.Answer, "not retried") || len(ans.KeyPoints) != 1 {
		t.Errorf("answer: %q key points: %v", ans.Answer, ans.KeyPoints)
	}
	// The lead named an agent that never answered, in reported_by and in a
	// position: both must be dropped.
	if len(ans.ReportedBy) != 1 || ans.ReportedBy[0] != "r1" {
		t.Errorf("reported by: %v", ans.ReportedBy)
	}
	if len(ans.Disagreements) != 1 || len(ans.Disagreements[0].Positions) != 1 {
		t.Errorf("disagreements: %+v", ans.Disagreements)
	}
	if len(ans.Failed) != 1 || ans.Failed[0].ID != "r2" || !strings.Contains(ans.Failed[0].Reason, "code 1") {
		t.Errorf("failed: %+v", ans.Failed)
	}
	for _, path := range []string{
		"manifest.json", "context/question.txt", "context/context.txt",
		"prompts/ask-r1.txt", "prompts/lead-ask.txt",
		"reports/ask-r1.json", "raw/lead-ask.stdout",
		"final/answer.json", "final/answer.md",
	} {
		if _, err := os.Stat(filepath.Join(res.RunDir, path)); err != nil {
			t.Errorf("artifact %s: %v", path, err)
		}
	}
	prompt, _ := os.ReadFile(filepath.Join(res.RunDir, "prompts", "ask-r1.txt"))
	for _, want := range []string{
		"UNTRUSTED QUESTION", "Why does the call give up so early?",
		"UNTRUSTED CONTEXT", "here is the log: boom",
		"The question is not about a project",
	} {
		if !strings.Contains(string(prompt), want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	if strings.Contains(string(prompt), "disposable Git worktree") {
		t.Error("a question without a project must not claim a checkout")
	}
	if res.Manifest.Kind != "ask" || res.Manifest.Status != domain.RunSucceeded || res.Manifest.HeadSHA != "" {
		t.Errorf("manifest: %+v", res.Manifest)
	}
	if entries, _ := os.ReadDir(a.WorktreeBase); len(entries) != 0 {
		t.Errorf("working directories left behind: %v", entries)
	}
}

func TestAskWithProject(t *testing.T) {
	dir, _, _ := fixtureRepo(t)
	a := newAskApp(t,
		agentCfg(t, "r1", config.RoleReviewer, "ask"),
		agentCfg(t, "lead", config.RoleLead, "ask-lead"))
	// The runs go next to the repository, not in the base the test set.
	a.RunsBase = ""
	res, err := a.Ask(context.Background(), AskRequest{
		Question: "Where is the retry?", Project: dir, Revision: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	m := res.Answer.Meta
	if m.Branch != "main" || m.HeadSHA == "" {
		t.Errorf("revision: %+v", m)
	}
	if m.Repository != "acme/proj" || m.Project != dir {
		t.Errorf("project: repository=%q project=%q", m.Repository, m.Project)
	}
	if !strings.HasPrefix(res.RunDir, filepath.Join(dir, ".git", "conclave", "runs")) {
		t.Errorf("artifacts should live in the repository: %s", res.RunDir)
	}
	prompt, _ := os.ReadFile(filepath.Join(res.RunDir, "prompts", "ask-r1.txt"))
	if !strings.Contains(string(prompt), "disposable Git worktree") {
		t.Error("the prompt must describe the checkout")
	}
	if entries, _ := os.ReadDir(a.WorktreeBase); len(entries) != 0 {
		t.Errorf("worktrees left behind: %v", entries)
	}
}

func TestAskLeadFallback(t *testing.T) {
	a := newAskApp(t,
		agentCfg(t, "r1", config.RoleReviewer, "ask"),
		agentCfg(t, "r2", config.RoleReviewer, "ask"),
		agentCfg(t, "lead", config.RoleLead, "invalid-json"))
	res, err := a.Ask(context.Background(), AskRequest{Question: "Why?"})
	if err != nil {
		t.Fatal(err)
	}
	ans := res.Answer
	if ans.Meta.LeadUsed || len(ans.ReportedBy) != 2 {
		t.Errorf("fallback: %+v reported_by=%v", ans.Meta, ans.ReportedBy)
	}
	if len(ans.OtherAnswers) != 1 || ans.OtherAnswers[0].AgentID != "r2" {
		t.Errorf("the answer not retained must be published: %+v", ans.OtherAnswers)
	}
	if len(ans.Caveats) == 0 || !strings.Contains(ans.Caveats[0], "Deterministic") {
		t.Errorf("the fallback must say it is one: %v", ans.Caveats)
	}
	if len(ans.OpenQuestions) != 1 {
		t.Errorf("open questions should be deduplicated: %v", ans.OpenQuestions)
	}
}

func TestAskRejectsEmptyQuestionAndEmptyAnswer(t *testing.T) {
	a := newAskApp(t,
		agentCfg(t, "r1", config.RoleReviewer, "ask-empty"),
		agentCfg(t, "lead", config.RoleLead, "ask-lead"))
	if _, err := a.Ask(context.Background(), AskRequest{Question: "   "}); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("an empty question must fail, got %v", err)
	}
	_, err := a.Ask(context.Background(), AskRequest{Question: "Why?"})
	if err == nil || !strings.Contains(err.Error(), "no answer") {
		t.Fatalf("an answer without text must fail the report, got %v", err)
	}
}

func TestAskLeadDisabledIsNotLeadUnavailable(t *testing.T) {
	a := newAskApp(t,
		agentCfg(t, "r1", config.RoleReviewer, "ask"),
		agentCfg(t, "lead", config.RoleLead, "ask-lead"))
	no := false
	a.Config.Ask.UseLead = &no
	res, err := a.Ask(context.Background(), AskRequest{Question: "Why?"})
	if err != nil {
		t.Fatal(err)
	}
	// LeadID is what the renderer reads as "a lead was expected here". A run
	// that asked for none must leave it empty, or the report claims the lead
	// was unavailable when it was simply never called.
	if res.Answer.Meta.LeadID != "" {
		t.Errorf("a lead that was never asked for must not be named: %+v", res.Answer.Meta)
	}
	for _, c := range res.Answer.Caveats {
		if strings.Contains(c, "unavailable") {
			t.Errorf("caveat claims an unavailable lead: %q", c)
		}
	}
}

func TestAskLeadFailureKeepsItsModelAndWarnsOnBadConfidence(t *testing.T) {
	a := newAskApp(t,
		agentCfg(t, "r1", config.RoleReviewer, "ask"),
		agentCfg(t, "lead", config.RoleLead, "invalid-json"))
	a.Config.Agents[1].Model = "fake/lead"
	res, err := a.Ask(context.Background(), AskRequest{Question: "Why?"})
	if err != nil {
		t.Fatal(err)
	}
	m := res.Answer.Meta
	if m.LeadID != "lead" || m.LeadUsed {
		t.Errorf("a lead that ran and failed must still be named: %+v", m)
	}
	if m.Models["lead"] != "fake/lead" {
		t.Errorf("the model that failed the consolidation must be recorded: %v", m.Models)
	}

	// A lead confidence outside [0,1] is read as 0, and the reader is told.
	b := newAskApp(t,
		agentCfg(t, "r1", config.RoleReviewer, "ask"),
		agentCfg(t, "lead", config.RoleLead, "ask-lead-badconf"))
	res2, err := b.Ask(context.Background(), AskRequest{Question: "Why?"})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Answer.Confidence != 0 {
		t.Errorf("confidence: %v", res2.Answer.Confidence)
	}
	if !strings.Contains(strings.Join(res2.Answer.Warnings, "\n"), "out of [0,1]") {
		t.Errorf("the clamp must be reported: %v", res2.Answer.Warnings)
	}
}

func TestAskSelectsRespondentsAndSkipsLead(t *testing.T) {
	a := newAskApp(t,
		agentCfg(t, "r1", config.RoleReviewer, "ask"),
		agentCfg(t, "r2", config.RoleReviewer, "ask"),
		agentCfg(t, "lead", config.RoleLead, "ask-lead"))
	a.Config.Ask.Respondents = []string{"r2"}
	no := false
	a.Config.Ask.UseLead = &no
	res, err := a.Ask(context.Background(), AskRequest{Question: "Why?"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Answer.Meta.RespondentsTotal != 1 || res.Answer.Meta.LeadUsed {
		t.Errorf("respondents: %+v", res.Answer.Meta)
	}
	if len(res.Manifest.Agents) != 1 {
		t.Errorf("the lead must not run: %+v", res.Manifest.Agents)
	}
	if _, err := os.Stat(filepath.Join(res.RunDir, "prompts", "ask-r1.txt")); err == nil {
		t.Error("r1 must not have been asked")
	}
}
