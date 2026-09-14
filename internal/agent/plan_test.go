package agent

import (
	"strings"
	"testing"

	"github.com/bornholm/conclave/internal/domain"
)

const planJSON = `{
 "schema_version": "1", "reviewer": {"id": "r1"}, "number": 42,
 "understanding": "the retry stops too early",
 "approach": "wrap the call in a bounded loop",
 "alternatives": [{"approach": "retry in the caller", "why_not": "duplicated"}, {"approach": "  "}],
 "steps": [
   {"id": "s1", "title": "Add the loop", "details": "in a.go", "files": ["./a.go", "/etc/passwd"]},
   {"id": "s1", "title": "Document it", "details": "in the README", "depends_on": ["s1", "s9"]},
   {"id": "s3", "title": "", "details": "nothing"}
 ],
 "tests": ["a unit test", "   "],
 "risks": [{"description": "slow calls are retried"}, {"mitigation": "orphan"}],
 "open_questions": ["how many attempts?"],
 "effort": "Small", "confidence": 0.7
}`

func TestParsePlanReport(t *testing.T) {
	rep, warnings, err := ParsePlanReport([]byte(planJSON), "r1", 42, PlanLimits{MaxSteps: 10})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Effort != domain.EffortSmall || rep.Confidence != 0.7 {
		t.Errorf("effort %q confidence %v", rep.Effort, rep.Confidence)
	}
	if len(rep.Steps) != 2 {
		t.Fatalf("the step without a title must be dropped: %+v", rep.Steps)
	}
	if got := rep.Steps[0].Files; len(got) != 1 || got[0] != "a.go" {
		t.Errorf("paths: %v", got)
	}
	// A duplicate id is replaced, and depends_on may only name an earlier step.
	if rep.Steps[1].ID == "s1" {
		t.Errorf("duplicate id kept: %+v", rep.Steps[1])
	}
	if got := rep.Steps[1].DependsOn; len(got) != 1 || got[0] != "s1" {
		t.Errorf("depends_on: %v", got)
	}
	if len(rep.Alternatives) != 1 || len(rep.Tests) != 1 || len(rep.Risks) != 1 {
		t.Errorf("empty entries kept: %+v %v %+v", rep.Alternatives, rep.Tests, rep.Risks)
	}
	joined := strings.Join(warnings, "\n")
	for _, want := range []string{"absolute path", "rejected", "depends_on"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings lack %q: %v", want, warnings)
		}
	}
}

func TestParsePlanReportRejects(t *testing.T) {
	noStep := strings.Replace(planJSON, `{"id": "s1", "title": "Add the loop"`, `{"id": "s1", "title": ""`, 1)
	noStep = strings.Replace(noStep, `{"id": "s1", "title": "Document it"`, `{"id": "s1", "title": ""`, 1)
	cases := map[string]struct{ in, want string }{
		"wrong issue": {strings.Replace(planJSON, `"number": 42`, `"number": 7`, 1), "expected #42"},
		"wrong agent": {strings.Replace(planJSON, `"id": "r1"`, `"id": "r2"`, 1), "does not match"},
		"bad schema":  {strings.Replace(planJSON, `"schema_version": "1"`, `"schema_version": "2"`, 1), "schema_version"},
		"confidence":  {strings.Replace(planJSON, `0.7`, `2`, 1), "confidence"},
		"no approach": {strings.Replace(planJSON, `"wrap the call in a bounded loop"`, `"  "`, 1), "no approach"},
		"no step":     {noStep, "no usable step"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := ParsePlanReport([]byte(tc.in), "r1", 42, PlanLimits{MaxSteps: 10}); err == nil ||
				!strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

func TestPlanMaxSteps(t *testing.T) {
	rep, warnings, err := ParsePlanReport([]byte(planJSON), "r1", 42, PlanLimits{MaxSteps: 1})
	if err != nil || len(rep.Steps) != 1 {
		t.Fatalf("%v %+v", err, rep)
	}
	if !strings.Contains(strings.Join(warnings, "\n"), "beyond the limit of 1") {
		t.Errorf("warnings: %v", warnings)
	}
}

func TestPlanUnknownEffort(t *testing.T) {
	rep, warnings, err := ParsePlanReport([]byte(strings.Replace(planJSON, `"Small"`, `"epic"`, 1)), "r1", 42, PlanLimits{MaxSteps: 10})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Effort != "" || !strings.Contains(strings.Join(warnings, "\n"), "unknown effort") {
		t.Errorf("effort %q warnings %v", rep.Effort, warnings)
	}
}
