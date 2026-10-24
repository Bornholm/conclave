package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bornholm/conclave/internal/domain"
)

// writeRun writes a run directory under base with its manifest and, unless
// status says it failed, its final review.
func writeRun(t *testing.T, base, id string, m domain.RunManifest, review domain.ConsolidatedReview) string {
	t.Helper()
	dir := filepath.Join(base, id)
	if err := os.MkdirAll(filepath.Join(dir, "final"), 0o755); err != nil {
		t.Fatal(err)
	}
	m.ID = id
	data, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if m.Status == domain.RunSucceeded {
		data, _ = json.Marshal(review)
		if err := os.WriteFile(filepath.Join(dir, "final", "review.json"), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestLoadPrevious(t *testing.T) {
	repo := domain.Repository{Host: "github.com", Owner: "acme", Name: "proj"}
	pr := &domain.PullRequest{Number: 7, WebURL: "https://github.com/acme/proj/pull/7"}
	base := t.TempDir()
	ok := domain.RunManifest{PRNumber: 7, Status: domain.RunSucceeded, Owner: "acme", Repo: "proj"}
	review := func(head string) domain.ConsolidatedReview {
		return domain.ConsolidatedReview{Meta: domain.ReviewMeta{PRNumber: 7, PRURL: pr.WebURL, HeadSHA: head}}
	}
	older := writeRun(t, base, "20260101T000000Z-pr-7-a", ok, review("aaa"))
	writeRun(t, base, "20260102T000000Z-pr-7-b", ok, review("bbb"))
	failed := ok
	failed.Status = domain.RunFailed
	writeRun(t, base, "20260103T000000Z-pr-7-c", failed, domain.ConsolidatedReview{})
	elsewhere := ok
	elsewhere.Owner = "other"
	writeRun(t, base, "20260104T000000Z-pr-7-d", elsewhere, review("ddd"))

	bare := filepath.Join(t.TempDir(), "round1.json")
	data, _ := json.Marshal(review("eee"))
	if err := os.WriteFile(bare, data, 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ since, run, head string }{
		// The newest run of another repository and the failed one are skipped.
		{SinceLast, "20260102T000000Z-pr-7-b", "bbb"},
		{"20260101T000000Z-pr-7-a", "20260101T000000Z-pr-7-a", "aaa"},
		{older, "20260101T000000Z-pr-7-a", "aaa"},
		{filepath.Join(older, "final", "review.json"), "20260101T000000Z-pr-7-a", "aaa"},
		// A review.json outside a run directory, without a run id of its own,
		// is given none rather than its directory's name.
		{bare, "", "eee"},
	} {
		prev, err := loadPrevious(base, repo, pr, tc.since)
		if err != nil {
			t.Errorf("%s: %v", tc.since, err)
			continue
		}
		if prev.RunID != tc.run || prev.HeadSHA != tc.head {
			t.Errorf("%s: run %q head %q, want %q %q", tc.since, prev.RunID, prev.HeadSHA, tc.run, tc.head)
		}
	}

	for _, tc := range []struct{ since, want string }{
		{"20260109T000000Z-pr-7-z", "no such run directory"},
		{"20260104T000000Z-pr-7-d", "is a review of other/proj, not acme/proj"},
	} {
		if _, err := loadPrevious(base, repo, pr, tc.since); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.since, err, tc.want)
		}
	}
	anonymous := filepath.Join(t.TempDir(), "review.json")
	data, _ = json.Marshal(domain.ConsolidatedReview{Meta: domain.ReviewMeta{PRNumber: 7, HeadSHA: "fff"}})
	if err := os.WriteFile(anonymous, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPrevious(base, repo, pr, anonymous); err == nil || !strings.Contains(err.Error(), "no pull request URL to compare") {
		t.Errorf("a review.json that names no repository must be refused: %v", err)
	}
	noURL := &domain.PullRequest{Number: 7}
	if _, err := loadPrevious(base, repo, noURL, bare); err == nil || !strings.Contains(err.Error(), "no pull request URL to compare") {
		t.Errorf("a review.json must be refused when the forge gives no URL to compare: %v", err)
	}
	if _, err := loadPrevious(base, repo, noURL, older); err != nil {
		t.Errorf("a run directory needs no URL: %v", err)
	}
	other := &domain.PullRequest{Number: 7, WebURL: "https://github.com/acme/fork/pull/7"}
	if _, err := loadPrevious(base, repo, other, bare); err == nil || !strings.Contains(err.Error(), "not https://github.com/acme/fork/pull/7") {
		t.Errorf("a review of another repository's pull request must be refused: %v", err)
	}
}

func TestReconcilePrevious(t *testing.T) {
	review := &domain.ConsolidatedReview{
		Findings: []domain.ConsolidatedFinding{
			{Title: "still there", PreviousID: "P1", ReportedBy: []string{"r2"}},
			{Title: "no status", PreviousID: "P3", ReportedBy: []string{"r1"}},
			{Title: "new"},
		},
		PreviousFindings: []domain.PreviousStatus{
			{ID: "P1", Status: domain.PreviousFixed, Evidence: "done", ReportedBy: []string{"r1"}},
			{ID: "P2", Status: domain.PreviousWithdrawn, Evidence: "wrong"},
		},
	}
	warnings := reconcilePrevious(review)
	got := map[string]domain.PreviousStatus{}
	for _, p := range review.PreviousFindings {
		got[p.ID] = p
	}
	if p := got["P1"]; p.Status != domain.PreviousStillPresent || p.Evidence != "" || strings.Join(p.ReportedBy, ",") != "r2" {
		t.Errorf("a fixed finding that a finding still continues: %+v", p)
	}
	if c := got["P1"].Claims; len(c) != 1 || c[0].Status != domain.PreviousFixed || c[0].Evidence != "done" || strings.Join(c[0].ReportedBy, ",") != "r1" {
		t.Errorf("the status replaced must stay among the claims: %+v", c)
	}
	if got["P2"].Status != domain.PreviousWithdrawn {
		t.Errorf("an unlinked status must be left alone: %+v", got["P2"])
	}
	if p := got["P3"]; p.Status != domain.PreviousStillPresent || strings.Join(p.ReportedBy, ",") != "r1" {
		t.Errorf("a link with no status: %+v", p)
	}
	if ids := []string{review.PreviousFindings[0].ID, review.PreviousFindings[1].ID, review.PreviousFindings[2].ID}; strings.Join(ids, ",") != "P1,P2,P3" {
		t.Errorf("order: %v", ids)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "P1 is fixed, but a finding still continues it") || !strings.Contains(warnings[0], "(r1 said fixed: done)") {
		t.Errorf("warnings: %v", warnings)
	}
}

func TestKeepStillPresentWarnsAboutAnUnknownID(t *testing.T) {
	review := &domain.ConsolidatedReview{PreviousFindings: []domain.PreviousStatus{{ID: "P4", Status: domain.PreviousStillPresent}}}
	prev := &domain.PreviousReview{Findings: []domain.ConsolidatedFinding{{Title: "one"}}}
	warnings := keepStillPresent(review, prev, nil)
	if len(review.Findings) != 0 || len(warnings) != 1 || !strings.Contains(warnings[0], "P4 is still present but is not one of") {
		t.Errorf("findings=%+v warnings=%v", review.Findings, warnings)
	}
}

func TestKeepStillPresentLinksAFindingOnTheSameLines(t *testing.T) {
	review := &domain.ConsolidatedReview{
		Verdict:          domain.VerdictComment,
		Findings:         []domain.ConsolidatedFinding{{Title: "reworded", File: "a.go", StartLine: 12, EndLine: 14}},
		PreviousFindings: []domain.PreviousStatus{{ID: "P1", Status: domain.PreviousStillPresent}},
	}
	prev := &domain.PreviousReview{Findings: []domain.ConsolidatedFinding{{Title: "race", Severity: domain.SeverityHigh, File: "a.go", StartLine: 10, EndLine: 12}}}
	warnings := keepStillPresent(review, prev, nil)
	if len(review.Findings) != 1 || review.Findings[0].PreviousID != "P1" || review.Verdict != domain.VerdictComment {
		t.Errorf("verdict=%s findings=%+v", review.Verdict, review.Findings)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], `finding "reworded", on the same lines, carried no link`) {
		t.Errorf("warnings: %v", warnings)
	}
}
