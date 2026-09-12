package agent

import (
	"strings"
	"testing"

	"github.com/bornholm/conclave/internal/domain"
)

const triageJSON = `{
 "schema_version": "1", "reviewer": {"id": "r1"}, "number": 42,
 "labels": ["Type/Bug", "invented", "type/bug"],
 "status": "Still_Present", "confidence": 0.7,
 "summary": "still there", "evidence": "a.go:12"
}`

var known = map[string]string{"type/bug": "type/bug", "area/proxy": "area/proxy"}

func TestParseTriageReport(t *testing.T) {
	rep, warnings, err := ParseTriageReport([]byte(triageJSON), "r1", 42, known, TriageLimits{MaxLabels: 4})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != domain.TriageStillPresent {
		t.Errorf("status: %q", rep.Status)
	}
	if len(rep.Labels) != 1 || rep.Labels[0] != "type/bug" {
		t.Errorf("labels: %v", rep.Labels)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "invented") {
		t.Errorf("warnings: %v", warnings)
	}
}

func TestParseTriageReportRejects(t *testing.T) {
	cases := map[string]struct{ in, want string }{
		"wrong issue":   {strings.Replace(triageJSON, `"number": 42`, `"number": 7`, 1), "expected #42"},
		"wrong agent":   {strings.Replace(triageJSON, `"id": "r1"`, `"id": "r2"`, 1), "does not match"},
		"bad status":    {strings.Replace(triageJSON, `"Still_Present"`, `"maybe"`, 1), "unknown status"},
		"bad schema":    {strings.Replace(triageJSON, `"schema_version": "1"`, `"schema_version": "2"`, 1), "schema_version"},
		"no evidence":   {strings.Replace(triageJSON, `"evidence": "a.go:12"`, `"evidence": ""`, 1), "requires evidence"},
		"evidence none": {strings.Replace(triageJSON, `"evidence": "a.go:12"`, `"evidence": "none"`, 1), "requires evidence"},
		"no question":   {strings.Replace(triageJSON, `"Still_Present"`, `"needs-info"`, 1), "requires a question"},
		"no duplicate":  {strings.Replace(triageJSON, `"Still_Present"`, `"duplicate-of"`, 1), "requires duplicate_of"},
		"confidence":    {strings.Replace(triageJSON, `0.7`, `2`, 1), "confidence"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := ParseTriageReport([]byte(tc.in), "r1", 42, known, TriageLimits{MaxLabels: 4})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

func TestParseTriageSelfDuplicate(t *testing.T) {
	in := strings.Replace(triageJSON, `"status": "Still_Present"`, `"status": "duplicate-of", "duplicate_of": 42`, 1)
	if _, _, err := ParseTriageReport([]byte(in), "r1", 42, known, TriageLimits{MaxLabels: 4}); err == nil ||
		!strings.Contains(err.Error(), "duplicate of itself") {
		t.Fatalf("got %v", err)
	}
}

func TestTriageMaxLabels(t *testing.T) {
	in := strings.Replace(triageJSON, `["Type/Bug", "invented", "type/bug"]`, `["type/bug", "area/proxy"]`, 1)
	rep, warnings, err := ParseTriageReport([]byte(in), "r1", 42, known, TriageLimits{MaxLabels: 1})
	if err != nil || len(rep.Labels) != 1 || len(warnings) == 0 {
		t.Fatalf("%v %v %v", err, rep.Labels, warnings)
	}
}
