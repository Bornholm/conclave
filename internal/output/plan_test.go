package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bornholm/conclave/internal/domain"
)

func samplePlan() *domain.ConsolidatedPlan {
	return &domain.ConsolidatedPlan{
		SchemaVersion: "1", Number: 42, Title: "Retry the\nfailing call", WebURL: "https://forge/issues/42",
		Summary: "Wrap the call in a bounded retry.", Understanding: "The call gives up after one attempt.",
		Approach:     "Bounded loop in internal/http/client.go.",
		Alternatives: []domain.PlanAlternative{{Approach: "Retry in the caller", WhyNot: "every caller would repeat it"}},
		Steps: []domain.PlanStep{
			{ID: "s1", Title: "Add the loop", Details: "Wrap the call.", Files: []string{"internal/http/client.go"}, Validation: "go test ./..."},
			{ID: "s2", Title: "Document the flag", Details: "In the README.", DependsOn: []string{"s1"}},
		},
		Tests:         []string{"a unit test covering the second attempt"},
		Risks:         []domain.PlanRisk{{Description: "slow calls are retried", Mitigation: "cap the duration"}},
		OpenQuestions: []string{"How many attempts?"},
		Effort:        domain.EffortSmall, Confidence: 0.8, ReportedBy: []string{"a", "b"},
		Failed:   []domain.FailedReviewer{{ID: "pi", Reason: "timed out after 15m"}},
		Warnings: []string{"a: 1 step rejected"},
		Meta: domain.PlanMeta{RunID: "run1", Repository: "acme/proj", IssueNumber: 42, HeadSHA: "7a88e4f0000",
			Branch: "main", PlannersTotal: 3, PlannersSucceeded: 2, LeadUsed: true, Models: map[string]string{"a": "m1"}},
	}
}

func TestPlanMarkdown(t *testing.T) {
	var buf bytes.Buffer
	if err := PlanMarkdown(&buf, samplePlan(), Options{ShowAttribution: true, ShowFailedAgents: true}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"## Conclave plan — #42 Retry the failing call", "**Link:** https://forge/issues/42",
		"`7a88e4f` (main)", "**Effort:** small", "**Confidence:** 80%", "**Planners:** 2 of 3",
		"**Planned by:** a, b", "**Models:** a (m1)", "### What the ticket asks", "### Approach",
		"### Alternatives considered", "**Retry in the caller** — every caller would repeat it",
		"### Steps (2)", "#### 1. Add the loop", "**Files:** `internal/http/client.go`",
		"**Done when:** go test ./...", "**Depends on:** s1", "### Tests", "### Risks",
		"_mitigation:_ cap the duration", "### Open questions", "### Planners that failed",
		"### Warnings", "_Run `run1`_",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("markdown lacks %q\n%s", want, out)
		}
	}
	buf.Reset()
	PlanMarkdown(&buf, samplePlan(), Options{})
	if strings.Contains(buf.String(), "Planned by") || strings.Contains(buf.String(), "Planners that failed") {
		t.Error("options not honored")
	}
}

func TestPlanJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := PlanJSON(&buf, samplePlan()); err != nil {
		t.Fatal(err)
	}
	var back domain.ConsolidatedPlan
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil || len(back.Steps) != 2 || back.Number != 42 {
		t.Fatalf("round trip: %v %+v", err, back)
	}
}
