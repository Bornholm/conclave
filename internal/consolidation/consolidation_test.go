package consolidation

import (
	"errors"
	"strings"
	"testing"

	"github.com/bornholm/conclave/internal/domain"
)

func nf(rev, file string, start, end int, cat, sev, title string, conf float64) domain.NormalizedFinding {
	f := domain.Finding{File: file, StartLine: start, EndLine: end, Category: domain.Category(cat), Severity: domain.Severity(sev), Title: title, Confidence: conf}
	return domain.NormalizedFinding{Finding: f, ReviewerID: rev, Fingerprint: Fingerprint(f)}
}

func TestGroup(t *testing.T) {
	fs := []domain.NormalizedFinding{
		nf("r1", "a.go", 10, 12, "concurrency", "high", "Concurrent map write in Store", 0.9),
		nf("r2", "a.go", 11, 11, "concurrency", "medium", "Store map written concurrently", 0.7),
		nf("r2", "a.go", 10, 12, "concurrency", "low", "Unrelated naming of the map", 0.3),
		nf("r3", "a.go", 200, 200, "concurrency", "high", "Concurrent map write in Store", 0.9),
		nf("r1", "b.go", 1, 1, "security", "high", "SQL injection", 0.9),
		nf("r2", "b.go", 1, 1, "security", "high", "SQL injection", 0.9),
		nf("r1", "b.go", 1, 1, "security", "high", "SQL injection duplicate from same reviewer", 0.9),
	}
	SortFindings(fs)
	groups := Group(fs)
	if len(groups) != 5 {
		for _, g := range groups {
			t.Logf("%v: %s", g.ReportedBy, g.Findings[0].Title)
		}
		t.Fatalf("got %d groups", len(groups))
	}
	found := 0
	for _, g := range groups {
		if len(g.ReportedBy) == 2 {
			found++
		}
		if len(g.ReportedBy) > 2 {
			t.Errorf("over-merged: %v", g.ReportedBy)
		}
	}
	if found != 2 {
		t.Errorf("expected 2 merged groups, got %d", found)
	}
}

func TestFallbackAndFailed(t *testing.T) {
	outcomes := []ReviewerOutcome{
		{ID: "r1", Report: &domain.AgentReport{Verdict: domain.VerdictComment, Summary: "fine", Findings: []domain.Finding{
			{File: "a.go", StartLine: 1, EndLine: 1, Category: "security", Severity: "low", Title: "x", Confidence: 0.4}}}},
		{ID: "r2", Err: errors.New("timed out")},
		{ID: "r3", Report: &domain.AgentReport{Verdict: domain.VerdictRequestChanges, Findings: []domain.Finding{
			{File: "a.go", StartLine: 1, EndLine: 1, Category: "security", Severity: "critical", Title: "x", Confidence: 0.9}}}},
	}
	groups := Group(Normalize(outcomes))
	review := Fallback(groups, outcomes)
	if review.Verdict != domain.VerdictRequestChanges || len(review.Findings) != 1 || review.Findings[0].Severity != "critical" {
		t.Errorf("fallback: %+v", review)
	}
	// An out-of-scope critical finding alone must not force request_changes.
	oos := []ReviewerOutcome{{ID: "r1", Report: &domain.AgentReport{Verdict: domain.VerdictComment, Findings: []domain.Finding{
		{File: "old.go", StartLine: 1, EndLine: 1, Category: "security", Severity: "critical", Title: "legacy", Confidence: 0.9, OutOfScope: true}}}}}
	if r := Fallback(Group(Normalize(oos)), oos); r.Verdict != domain.VerdictComment || !r.Findings[0].OutOfScope {
		t.Errorf("out-of-scope fallback: %+v", r)
	}
	if len(review.Findings[0].ReportedBy) != 2 {
		t.Errorf("provenance: %v", review.Findings[0].ReportedBy)
	}
	if len(review.FailedReviewers) != 1 || review.FailedReviewers[0].ID != "r2" || review.FailedReviewers[0].Reason != "timed out" {
		t.Errorf("failed: %+v", review.FailedReviewers)
	}
	if !strings.Contains(review.Summary, "r1: fine") {
		t.Errorf("summary: %s", review.Summary)
	}
}

const leadJSON = `{"schema_version":"1","summary":"ok","verdict":"comment","failed_reviewers":[{"id":"bogus","reason":"x"}],
"findings":[
 {"category":"security","severity":"high","confidence":0.8,"file":"a.go","start_line":1,"end_line":1,"title":"real","description":"d","reported_by":["r1","ghost","r1"]},
 {"category":"security","severity":"high","confidence":0.8,"file":"a.go","start_line":1,"end_line":1,"title":"invented","description":"d","reported_by":["ghost"]},
 {"category":"security","severity":"high","confidence":0.8,"file":"../x","start_line":1,"end_line":1,"title":"escape","description":"d","reported_by":["r1"]}
]}`

func TestParseLeadReview(t *testing.T) {
	outcomes := []ReviewerOutcome{{ID: "r1", Report: &domain.AgentReport{}}, {ID: "r2", Err: errors.New("boom")}}
	review, warnings, err := ParseLeadReview([]byte("```json\n"+leadJSON+"\n```"), []string{"r1"}, outcomes, map[string]bool{"b.go": true}, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(review.Findings) != 1 || len(review.Findings[0].ReportedBy) != 1 || review.Findings[0].ReportedBy[0] != "r1" {
		t.Errorf("findings: %+v", review.Findings)
	}
	if !review.Findings[0].OutOfScope {
		t.Error("a.go is not in the changed set: finding must be flagged out of scope")
	}
	if len(warnings) != 2 {
		t.Errorf("warnings: %v", warnings)
	}
	if len(review.FailedReviewers) != 1 || review.FailedReviewers[0].ID != "r2" {
		t.Errorf("failed reviewers must come from outcomes: %+v", review.FailedReviewers)
	}
	if _, _, err := ParseLeadReview([]byte(`{"schema_version":"1","verdict":"maybe"}`), nil, nil, nil, nil, 10); err == nil {
		t.Error("expected verdict error")
	}
}

func TestTitleSimilarity(t *testing.T) {
	if s := TitleSimilarity("Concurrent map write in Store", "Store map written concurrently"); s < 0.3 {
		t.Errorf("similarity %v", s)
	}
	if s := TitleSimilarity("SQL injection", "Missing test"); s != 0 {
		t.Errorf("similarity %v", s)
	}
}

const leadTriageJSON = `{"schema_version":"1","summary":"done","issues":[
 {"number":11,"labels":["type/bug","ghostlabel"],"status":"still-present","confidence":0.9,
  "summary":"yes","evidence":"a.go:1","reported_by":["r1","ghost"]},
 {"number":99,"labels":[],"status":"fixed","confidence":0.9,"summary":"x","evidence":"y","reported_by":["r1"]},
 {"number":12,"labels":[],"status":"fixed","confidence":0.5,"summary":"maybe","evidence":"","reported_by":["r1"]}
]}`

func TestParseLeadTriage(t *testing.T) {
	issues := []domain.Issue{
		{Number: 11, Title: "Crash", State: "open", Labels: []string{"type/bug"}},
		{Number: 12, Title: "Retries", State: "open"},
	}
	known := map[string]string{"type/bug": "type/bug"}
	tr, warnings, err := ParseLeadTriage([]byte(leadTriageJSON), issues, []string{"r1"}, known, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Issues) != 2 {
		t.Fatalf("issues: %+v", tr.Issues)
	}
	byNumber := map[int64]domain.TriagedIssue{}
	for _, i := range tr.Issues {
		byNumber[i.Number] = i
	}
	got := byNumber[11]
	if len(got.Labels) != 1 || got.Title != "Crash" || len(got.ReportedBy) != 1 || got.ReportedBy[0] != "r1" {
		t.Errorf("issue 11: %+v", got)
	}
	// A "fixed" with no evidence must be downgraded, never trusted.
	if got := byNumber[12]; got.Status != domain.TriageNeedsInfo || got.Question == "" {
		t.Errorf("issue 12 should be downgraded: %+v", got)
	}
	joined := strings.Join(warnings, "\n")
	for _, want := range []string{"#99 was not in the batch", "ghostlabel", "requires evidence"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings lack %q: %v", want, warnings)
		}
	}
}

func TestFallbackTriage(t *testing.T) {
	issues := []domain.Issue{{Number: 11, Title: "Crash", State: "open"}, {Number: 12, Title: "Lost"}}
	outcomes := []TriageOutcome{
		{Number: 11, Agent: "a", Report: &domain.TriageReport{Number: 11, Labels: []string{"type/bug"}, Status: domain.TriageNeedsInfo, Confidence: 0.4, Question: "?"}},
		{Number: 11, Agent: "b", Report: &domain.TriageReport{Number: 11, Labels: []string{"area/x"}, Status: domain.TriageStillPresent, Confidence: 0.9, Evidence: "a.go:1"}},
		{Number: 12, Agent: "a", Err: errors.New("timed out")},
	}
	tr := FallbackTriage(issues, outcomes)
	if len(tr.Issues) != 1 {
		t.Fatalf("issues: %+v", tr.Issues)
	}
	got := tr.Issues[0]
	if got.Status != domain.TriageStillPresent || len(got.Labels) != 2 || len(got.ReportedBy) != 2 {
		t.Errorf("consolidated: %+v", got)
	}
	failed := FailedIssues(issues, outcomes)
	if len(failed) != 1 || failed[0].Number != 12 || !strings.Contains(failed[0].Reason, "timed out") {
		t.Errorf("failed: %+v", failed)
	}
}

func TestApplyStatusLabels(t *testing.T) {
	known := map[string]string{"question": "question", "type/bug": "type/bug"}
	tr := &domain.ConsolidatedTriage{Issues: []domain.TriagedIssue{
		{Number: 1, Status: domain.TriageNeedsInfo, Labels: []string{"type/bug"}},
		{Number: 2, Status: domain.TriageFixed, Labels: []string{"type/bug"}},
		{Number: 3, Status: domain.TriageNeedsInfo, Labels: []string{"Question"}},
		{Number: 4, Status: domain.TriageNeedsInfo, Labels: []string{"type/bug"}},
	}}
	warnings := ApplyStatusLabels(tr, map[string]string{"needs-info": "question", "obsolete": "ghost"}, known, 2)
	if got := tr.Issues[0].Labels; len(got) != 2 || got[1] != "question" {
		t.Errorf("issue 1: %v", got)
	}
	if got := tr.Issues[1].Labels; len(got) != 1 {
		t.Errorf("a status with no mapping must not change: %v", got)
	}
	// "Question" was proposed in another case; the canonical name is what counts.
	if got := tr.Issues[2].Labels; len(got) != 2 {
		t.Errorf("issue 3: %v", got)
	}
	if warnings != nil {
		t.Errorf("an unused mapping to an undefined label must stay silent: %v", warnings)
	}
	// At the cap, the status label replaces the last proposal.
	tr2 := &domain.ConsolidatedTriage{Issues: []domain.TriagedIssue{
		{Number: 5, Status: domain.TriageNeedsInfo, Labels: []string{"type/bug", "type/bug"}},
	}}
	ApplyStatusLabels(tr2, map[string]string{"needs-info": "question"}, known, 2)
	if got := tr2.Issues[0].Labels; len(got) != 2 || got[1] != "question" {
		t.Errorf("capped: %v", got)
	}
	// A mapping to a label the repository does not define warns once.
	tr3 := &domain.ConsolidatedTriage{Issues: []domain.TriagedIssue{
		{Number: 6, Status: domain.TriageObsolete}, {Number: 7, Status: domain.TriageObsolete},
	}}
	if w := ApplyStatusLabels(tr3, map[string]string{"obsolete": "ghost"}, known, 2); len(w) != 1 {
		t.Errorf("warnings: %v", w)
	}
}
