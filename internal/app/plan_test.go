package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bornholm/conclave/internal/config"
	"github.com/bornholm/conclave/internal/domain"
)

func newPlanApp(t *testing.T, agents ...config.AgentConfig) *App {
	t.Helper()
	a, _ := newApp(t, agents...)
	f := a.Forge.(*fakeForge)
	f.issues = []domain.Issue{
		{Number: 11, Title: "Retry the failing call", Description: "it gives up too early", State: "open", Author: "alice", Labels: []string{"type/bug"}},
	}
	f.refs = map[int64][]domain.Reference{
		11: {{Kind: domain.ReferencePullRequest, Ref: "20", Title: "first attempt", State: "closed"}},
	}
	return a
}

func TestPlanEndToEnd(t *testing.T) {
	a := newPlanApp(t,
		agentCfg(t, "r1", config.RoleReviewer, "plan"),
		agentCfg(t, "r2", config.RoleReviewer, "exit-1"),
		agentCfg(t, "lead", config.RoleLead, "plan-lead"))
	res, err := a.Plan(context.Background(), PlanRequest{Number: 11})
	if err != nil {
		t.Fatal(err)
	}
	p := res.Plan
	if !p.Meta.LeadUsed || p.Meta.PlannersTotal != 2 || p.Meta.PlannersSucceeded != 1 {
		t.Errorf("meta: %+v", p.Meta)
	}
	if p.Number != 11 || p.Title != "Retry the failing call" {
		t.Errorf("plan identity: #%d %q", p.Number, p.Title)
	}
	if len(p.Steps) != 1 || p.Steps[0].Files[0] != "changed.txt" || p.Effort != domain.EffortSmall {
		t.Errorf("steps: %+v effort=%q", p.Steps, p.Effort)
	}
	if len(p.ReportedBy) != 1 || p.ReportedBy[0] != "r1" {
		t.Errorf("reported by: %v", p.ReportedBy)
	}
	if len(p.Failed) != 1 || p.Failed[0].ID != "r2" || !strings.Contains(p.Failed[0].Reason, "code 1") {
		t.Errorf("failed: %+v", p.Failed)
	}
	// The planner's invalid path and dangling dependency are warnings, not failures.
	joined := strings.Join(p.Warnings, "\n")
	if !strings.Contains(joined, "escapes the repository") || !strings.Contains(joined, "depends_on") {
		t.Errorf("warnings: %v", p.Warnings)
	}
	for _, path := range []string{
		"manifest.json", "context/issue.json",
		"prompts/plan-r1.txt", "prompts/lead-plan.txt",
		"reports/plan-r1.json", "raw/lead-plan.stdout",
		"final/plan.json", "final/plan.md",
	} {
		if _, err := os.Stat(filepath.Join(res.RunDir, path)); err != nil {
			t.Errorf("artifact %s: %v", path, err)
		}
	}
	prompt, _ := os.ReadFile(filepath.Join(res.RunDir, "prompts", "plan-r1.txt"))
	for _, want := range []string{
		"UNTRUSTED ISSUE BODY", "it gives up too early", "still happening",
		"pull_request 20", `"number" must be 11`, "at most 30",
	} {
		if !strings.Contains(string(prompt), want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	if res.Manifest.Kind != "plan" || len(res.Manifest.Issues) != 1 || res.Manifest.Status != domain.RunSucceeded {
		t.Errorf("manifest: %+v", res.Manifest)
	}
	if entries, _ := os.ReadDir(a.WorktreeBase); len(entries) != 0 {
		t.Errorf("worktrees left behind: %v", entries)
	}
}

func TestPlanLeadFallbackAndGhostPlanner(t *testing.T) {
	a := newPlanApp(t,
		agentCfg(t, "r1", config.RoleReviewer, "plan"),
		agentCfg(t, "r2", config.RoleReviewer, "plan"),
		agentCfg(t, "lead", config.RoleLead, "invalid-json"))
	res, err := a.Plan(context.Background(), PlanRequest{Number: 11})
	if err != nil {
		t.Fatal(err)
	}
	p := res.Plan
	if p.Meta.LeadUsed || !strings.Contains(p.Summary, "Deterministic") {
		t.Errorf("fallback: %+v", p.Meta)
	}
	// Both planners answered the same confidence, so the first wins whole and
	// the other's approach is kept as an alternative.
	if len(p.Steps) != 2 || len(p.ReportedBy) != 2 {
		t.Errorf("fallback plan: steps=%d reported_by=%v", len(p.Steps), p.ReportedBy)
	}
	if len(p.Alternatives) != 2 || !strings.Contains(p.Alternatives[1].WhyNot, "not retained") {
		t.Errorf("alternatives: %+v", p.Alternatives)
	}
	if len(p.OpenQuestions) != 1 {
		t.Errorf("open questions should be deduplicated: %v", p.OpenQuestions)
	}

	a2 := newPlanApp(t,
		agentCfg(t, "r1", config.RoleReviewer, "plan"),
		agentCfg(t, "lead", config.RoleLead, "plan-lead-ghost"))
	res2, err := a2.Plan(context.Background(), PlanRequest{Number: 11})
	if err != nil {
		t.Fatal(err)
	}
	if got := res2.Plan.ReportedBy; len(got) != 0 {
		t.Errorf("a ghost planner must be dropped, got %v", got)
	}
}

func TestPlanRejectsPlanWithoutStep(t *testing.T) {
	a := newPlanApp(t,
		agentCfg(t, "r1", config.RoleReviewer, "plan-no-steps"),
		agentCfg(t, "lead", config.RoleLead, "plan-lead"))
	_, err := a.Plan(context.Background(), PlanRequest{Number: 11})
	if err == nil || !strings.Contains(err.Error(), "no usable step") {
		t.Fatalf("a plan without a step must fail the report, got %v", err)
	}
}

func TestPlanWithoutLead(t *testing.T) {
	a := newPlanApp(t,
		agentCfg(t, "r1", config.RoleReviewer, "plan"),
		agentCfg(t, "lead", config.RoleLead, "valid"))
	no := false
	a.Config.Plan.UseLead = &no
	res, err := a.Plan(context.Background(), PlanRequest{Number: 11})
	if err != nil {
		t.Fatal(err)
	}
	if res.Plan.Meta.LeadUsed || len(res.Plan.Steps) != 2 {
		t.Errorf("no-lead run: %+v", res.Plan.Meta)
	}
	if !strings.Contains(res.Plan.Summary, "r1") {
		t.Errorf("summary should name the agent: %q", res.Plan.Summary)
	}
	if len(res.Manifest.Agents) != 1 {
		t.Errorf("the lead must not run: %+v", res.Manifest.Agents)
	}
}

func TestPlanSelectsPlanners(t *testing.T) {
	a := newPlanApp(t,
		agentCfg(t, "r1", config.RoleReviewer, "plan"),
		agentCfg(t, "r2", config.RoleReviewer, "plan"),
		agentCfg(t, "lead", config.RoleLead, "plan-lead"))
	a.Config.Plan.Planners = []string{"r2"}
	res, err := a.Plan(context.Background(), PlanRequest{Number: 11})
	if err != nil {
		t.Fatal(err)
	}
	if res.Plan.Meta.PlannersTotal != 1 {
		t.Errorf("planners: %+v", res.Plan.Meta)
	}
	if _, err := os.Stat(filepath.Join(res.RunDir, "prompts", "plan-r1.txt")); err == nil {
		t.Error("r1 must not have been asked for a plan")
	}
}
