package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bornholm/conclave/internal/config"
	"github.com/bornholm/conclave/internal/domain"
	"github.com/bornholm/conclave/internal/forge"
)

func newTriageApp(t *testing.T, agents ...config.AgentConfig) *App {
	t.Helper()
	a, _ := newApp(t, agents...)
	f := a.Forge.(*fakeForge)
	f.labels = []domain.Label{
		{Name: "type/bug", Description: "something is broken"},
		{Name: "type/enhancement"},
		{Name: "area/proxy"},
		{Name: "wontfix"},
	}
	f.issues = []domain.Issue{
		{Number: 11, Title: "Crash on login", Description: "panic in store", State: "open", Author: "alice", Labels: []string{"type/bug"}},
		{Number: 12, Title: "Add retries", Description: "would be nice", State: "open", Author: "bob"},
	}
	f.refs = map[int64][]domain.Reference{
		11: {{Kind: domain.ReferencePullRequest, Ref: "20", Title: "fix the store", State: "merged"}},
	}
	a.Config.Triage.Labels.Exclude = []string{"wontfix"}
	return a
}

func TestTriageEndToEnd(t *testing.T) {
	a := newTriageApp(t,
		agentCfg(t, "r1", config.RoleReviewer, "triage"),
		agentCfg(t, "lead", config.RoleLead, "triage-lead"))
	res, err := a.Triage(context.Background(), TriageRequest{Numbers: []int64{11, 12}})
	if err != nil {
		t.Fatal(err)
	}
	tr := res.Triage
	if len(tr.Issues) != 2 || !tr.Meta.LeadUsed || tr.Meta.Total != 2 || tr.Meta.Succeeded != 2 {
		t.Fatalf("meta: %+v issues: %d", tr.Meta, len(tr.Issues))
	}
	byNumber := map[int64]domain.TriagedIssue{}
	for _, i := range tr.Issues {
		byNumber[i.Number] = i
	}
	if got := byNumber[11]; got.Status != domain.TriageStillPresent || got.Title != "Crash on login" || len(got.ReportedBy) != 1 {
		t.Errorf("issue 11: %+v", got)
	}
	if got := byNumber[12]; got.Status != domain.TriageNeedsInfo || got.Question == "" {
		t.Errorf("issue 12: %+v", got)
	}
	// "nope" is not a repository label and must be dropped with a warning.
	if got := byNumber[11]; len(got.Labels) != 1 || got.Labels[0] != "type/bug" {
		t.Errorf("labels: %+v", got.Labels)
	}
	if !strings.Contains(strings.Join(tr.Warnings, "\n"), `"nope" is not defined`) {
		t.Errorf("warnings: %v", tr.Warnings)
	}
	// Issue 12 carries no label yet, so type/bug is an addition.
	if add := byNumber[12].AddedLabels(); len(add) != 1 || add[0] != "type/bug" {
		t.Errorf("added labels: %v", add)
	}
	for _, p := range []string{
		"manifest.json", "context/issues.json", "context/labels.json",
		"prompts/triage-11-r1.txt", "prompts/lead-triage.txt",
		"reports/triage-11-r1.json", "raw/lead-triage.stdout",
		"final/triage.json", "final/triage.md",
	} {
		if _, err := os.Stat(filepath.Join(res.RunDir, p)); err != nil {
			t.Errorf("artifact %s: %v", p, err)
		}
	}
	prompt, _ := os.ReadFile(filepath.Join(res.RunDir, "prompts", "triage-11-r1.txt"))
	for _, want := range []string{
		"type/bug: something is broken", "UNTRUSTED ISSUE BODY", "panic in store",
		"pull_request 20", "still happening", "- #12: Add retries",
	} {
		if !strings.Contains(string(prompt), want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	if strings.Contains(string(prompt), "wontfix") {
		t.Error("excluded label reached the prompt")
	}
	if res.Manifest.Kind != "triage" || len(res.Manifest.Issues) != 2 {
		t.Errorf("manifest: %+v", res.Manifest)
	}
	if entries, _ := os.ReadDir(a.WorktreeBase); len(entries) != 0 {
		t.Errorf("worktrees left behind: %v", entries)
	}
}

func TestTriageListsIssues(t *testing.T) {
	a := newTriageApp(t,
		agentCfg(t, "r1", config.RoleReviewer, "triage"),
		agentCfg(t, "lead", config.RoleLead, "triage-lead"))
	res, err := a.Triage(context.Background(), TriageRequest{Query: forge.IssueQuery{State: forge.StateOpen, Limit: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Triage.Issues) != 1 || res.Triage.Issues[0].Number != 11 {
		t.Errorf("issues: %+v", res.Triage.Issues)
	}
}

func TestTriageLeadFallbackAndGhostReviewer(t *testing.T) {
	a := newTriageApp(t,
		agentCfg(t, "r1", config.RoleReviewer, "triage"),
		agentCfg(t, "lead", config.RoleLead, "invalid-json"))
	res, err := a.Triage(context.Background(), TriageRequest{Numbers: []int64{11}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Triage.Meta.LeadUsed || !strings.Contains(res.Triage.Summary, "Deterministic") {
		t.Errorf("fallback: %+v", res.Triage)
	}
	if len(res.Triage.Issues) != 1 || res.Triage.Issues[0].Status != domain.TriageStillPresent {
		t.Errorf("fallback issues: %+v", res.Triage.Issues)
	}

	a2 := newTriageApp(t,
		agentCfg(t, "r1", config.RoleReviewer, "triage"),
		agentCfg(t, "lead", config.RoleLead, "triage-lead-ghost"))
	res2, err := a2.Triage(context.Background(), TriageRequest{Numbers: []int64{11}})
	if err != nil {
		t.Fatal(err)
	}
	if got := res2.Triage.Issues[0].ReportedBy; len(got) != 0 {
		t.Errorf("a ghost reviewer must be dropped, got %v", got)
	}
}

func TestTriageRejectsUnsubstantiatedStatus(t *testing.T) {
	a := newTriageApp(t,
		agentCfg(t, "r1", config.RoleReviewer, "triage-no-evidence"),
		agentCfg(t, "lead", config.RoleLead, "triage-lead"))
	_, err := a.Triage(context.Background(), TriageRequest{Numbers: []int64{11}})
	if err == nil || !strings.Contains(err.Error(), "requires evidence") {
		t.Fatalf("a fixed without evidence must fail the report, got %v", err)
	}
}

func TestTriageWithoutLead(t *testing.T) {
	a := newTriageApp(t,
		agentCfg(t, "r1", config.RoleReviewer, "triage"),
		agentCfg(t, "lead", config.RoleLead, "valid"))
	no := false
	a.Config.Triage.UseLead = &no
	res, err := a.Triage(context.Background(), TriageRequest{Numbers: []int64{11}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Triage.Meta.LeadUsed || len(res.Triage.Issues) != 1 {
		t.Errorf("no-lead run: %+v", res.Triage.Meta)
	}
	if !strings.Contains(res.Triage.Summary, "r1") {
		t.Errorf("summary should name the agent: %q", res.Triage.Summary)
	}
}
