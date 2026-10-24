package agent

import (
	"strings"
	"testing"
)

const report = `{
 "schema_version": "1", "reviewer": {"id": "r1"}, "summary": "s", "verdict": "comment",
 "findings": [
  {"id":"a","category":"Correctness","severity":"HIGH","confidence":0.5,"file":"./src/a.go","start_line":3,"end_line":1,"title":"ok"},
  {"id":"b","category":"x","severity":"high","confidence":0.5,"file":"../etc/passwd","start_line":1,"end_line":1,"title":"escape"},
  {"id":"c","category":"x","severity":"nope","confidence":0.5,"file":"src/a.go","start_line":1,"end_line":1,"title":"sev"},
  {"id":"d","category":"x","severity":"low","confidence":1.5,"file":"src/a.go","start_line":1,"end_line":1,"title":"conf"},
  {"id":"e","category":"x","severity":"low","confidence":0.1,"file":"src/a.go","start_line":0,"end_line":1,"title":"line"},
  {"id":"f","category":"x","severity":"low","confidence":0.1,"file":"src/other.go","start_line":1,"end_line":1,"title":"unchanged"},
  {"id":"g","category":"x","severity":"low","confidence":0.1,"file":"src/a.go","start_line":1,"end_line":1,"title":""},
  {"id":"h","category":"x","severity":"low","confidence":0.1,"file":"/abs/src/a.go","start_line":1,"end_line":1,"title":"abs"}
 ],
 "questions": [{"question": ""}, {"question": "why?", "file": "../x"}]
}`

func TestParseReport(t *testing.T) {
	changed := map[string]bool{"src/a.go": true}
	rep, warnings, err := ParseReport([]byte(report), "r1", changed, func(p string) bool { return p == "src/a.go" || p == "src/other.go" }, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Findings) != 3 {
		t.Fatalf("kept %d findings: %+v\n%v", len(rep.Findings), rep.Findings, warnings)
	}
	f := rep.Findings[0]
	if f.Category != "correctness" || f.Severity != "high" || f.File != "src/a.go" || f.EndLine != 3 || f.OutOfScope {
		t.Errorf("normalized: %+v", f)
	}
	if whole := rep.Findings[1]; whole.ID != "e" || whole.StartLine != 1 || whole.EndLine != 1 {
		t.Errorf("a finding without a line should be anchored on the first: %+v", whole)
	}
	if oos := rep.Findings[2]; !oos.OutOfScope || oos.File != "src/other.go" {
		t.Errorf("out of scope finding should be kept and flagged: %+v", oos)
	}
	if len(warnings) != 5 {
		t.Errorf("warnings: %d %v", len(warnings), warnings)
	}
	if len(rep.Questions) != 1 || rep.Questions[0].File != "" {
		t.Errorf("questions: %+v", rep.Questions)
	}
}

func TestParseReportStructuralErrors(t *testing.T) {
	cases := map[string]string{
		"wrong reviewer": strings.Replace(report, `"id": "r1"`, `"id": "r2"`, 1),
		"bad schema":     strings.Replace(report, `"schema_version": "1"`, `"schema_version": "9"`, 1),
		"bad verdict":    strings.Replace(report, `"verdict": "comment"`, `"verdict": "lgtm"`, 1),
		"no json":        "nothing",
	}
	for name, in := range cases {
		if _, _, err := ParseReport([]byte(in), "r1", nil, nil, Limits{}); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestNumericFindingID(t *testing.T) {
	in := `{"schema_version":"1","reviewer":{"id":"r1"},"summary":"s","verdict":"comment","findings":[
	 {"id":7,"category":"correctness","severity":"low","confidence":0.5,"file":"a.go","start_line":1,"end_line":1,"title":"num"},
	 {"id":null,"category":"correctness","severity":"low","confidence":0.5,"file":"a.go","start_line":2,"end_line":2,"title":"null"},
	 {"id":"s","category":"correctness","severity":"low","confidence":0.5,"file":"a.go","start_line":3,"end_line":3,"title":"str"}]}`
	rep, _, err := ParseReport([]byte(in), "r1", nil, nil, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Findings) != 3 || rep.Findings[0].ID != "7" || rep.Findings[1].ID != "r1-2" || rep.Findings[2].ID != "s" {
		t.Errorf("ids: %+v", rep.Findings)
	}
}

func TestMaxFindings(t *testing.T) {
	rep, warnings, err := ParseReport([]byte(report), "r1", nil, nil, Limits{MaxFindings: 1, MaxFieldBytes: 100})
	if err != nil || len(rep.Findings) != 1 || len(warnings) == 0 {
		t.Fatalf("%v %d %v", err, len(rep.Findings), warnings)
	}
}

const incrementalReport = `{
 "schema_version": "1", "reviewer": {"id": "r1"}, "summary": "s", "verdict": "comment",
 "findings": [
  {"id":"a","category":"x","severity":"high","confidence":0.5,"file":"src/a.go","start_line":1,"end_line":1,"title":"still","previous_id":"p2"},
  {"id":"b","category":"x","severity":"low","confidence":0.5,"file":"src/a.go","start_line":1,"end_line":1,"title":"bad link","previous_id":"P9"}
 ],
 "previous_findings": [
  {"id":"P1","status":"fixed","evidence":"the nil check is in 3b2","claims":[{"status":"withdrawn","evidence":"made up"}]},
  {"id":"p2","status":"Still-Present","evidence":""},
  {"id":"P3","status":"withdrawn","evidence":""},
  {"id":"P1","status":"still-present"},
  {"id":"P7","status":"fixed","evidence":"x"},
  {"id":"P3","status":"gone","evidence":"x"}
 ]
}`

func TestParseReportPreviousFindings(t *testing.T) {
	exists := func(string) bool { return true }
	rep, warnings, err := ParseReport([]byte(incrementalReport), "r1", nil, exists, Limits{Previous: 3})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Findings[0].PreviousID != "P2" || rep.Findings[1].PreviousID != "" {
		t.Errorf("links: %q %q", rep.Findings[0].PreviousID, rep.Findings[1].PreviousID)
	}
	got := map[string]string{}
	for _, p := range rep.PreviousFindings {
		got[p.ID] = string(p.Status)
	}
	// P3 "withdrawn" without evidence is not a status: taking a finding off the
	// review needs a reason. P1 keeps its first status, P7 does not exist.
	if len(got) != 2 || got["P1"] != "fixed" || got["P2"] != "still-present" {
		t.Errorf("statuses: %v (%v)", got, warnings)
	}
	if len(rep.PreviousFindings[0].Claims) != 0 {
		t.Errorf("a reviewer's own claims must be dropped: %+v", rep.PreviousFindings[0])
	}
	if len(warnings) != 6 {
		t.Errorf("warnings: %d %v", len(warnings), warnings)
	}

	// Outside an incremental review, the fields are dropped.
	rep, warnings, err = ParseReport([]byte(incrementalReport), "r1", nil, exists, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.PreviousFindings) != 0 || rep.Findings[0].PreviousID != "" {
		t.Errorf("a full review kept incremental fields: %+v", rep)
	}
	if len(warnings) == 0 {
		t.Error("dropping them should say so")
	}
}
