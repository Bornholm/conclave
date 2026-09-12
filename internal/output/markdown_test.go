package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bornholm/conclave/internal/domain"
)

func sample() *domain.ConsolidatedReview {
	return &domain.ConsolidatedReview{
		SchemaVersion: "1", Summary: "Two issues.", Verdict: domain.VerdictRequestChanges,
		Findings: []domain.ConsolidatedFinding{
			{Category: "concurrency", Severity: "critical", Confidence: 0.95, File: "internal/session/store.go", StartLine: 48, EndLine: 57,
				Title: "Concurrent access to session map", Description: "Unlocked read.", Suggestion: "Lock.", ReportedBy: []string{"a", "b"}},
			{Category: "testing", Severity: "low", Confidence: 0.5, File: "x.go", StartLine: 1, EndLine: 1, Title: "No test", ReportedBy: []string{"a"}},
			{Category: "security", Severity: "high", Confidence: 0.8, File: "legacy.go", StartLine: 9, EndLine: 9, Title: "Old bug", Description: "pre-existing", ReportedBy: []string{"b"}, OutOfScope: true},
		},
		Questions:       []domain.Question{{File: "x.go", Line: 3, Question: "Why?"}},
		FailedReviewers: []domain.FailedReviewer{{ID: "pi-performance", Reason: "timed out after 15m"}},
		Warnings:        []string{"a: 1 finding rejected"},
		Meta:            domain.ReviewMeta{RunID: "run1", PRNumber: 123, PRTitle: "Prevent\nwrites", HeadSHA: "7a88e4f0000", ReviewersTotal: 3, ReviewersSucceeded: 2, LeadUsed: true, Models: map[string]string{"a": "m1"}},
	}
}

func TestMarkdown(t *testing.T) {
	var buf bytes.Buffer
	if err := Markdown(&buf, sample(), Options{ShowAttribution: true, ShowFailedAgents: true}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"**Verdict:** Changes requested", "**Models:** a (m1)", "#123 — Prevent writes", "`7a88e4f`", "2/3 successful",
		"### Critical findings", "`internal/session/store.go:48-57`", "**Confidence:** 95%", "**Reported by:** a, b",
		"### Low severity findings", "### Questions for the author", "`x.go:3`: Why?",
		"### Reviewer failures", "`pi-performance`: timed out", "### Warnings", "_Run `run1`_",
		"### Out of scope", "**Old bug** (`legacy.go:9`, high, security, reported by b) — pre-existing",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("markdown lacks %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "### High severity findings") {
		t.Error("out-of-scope finding rendered as in-scope")
	}
	buf.Reset()
	Markdown(&buf, sample(), Options{})
	if strings.Contains(buf.String(), "Reported by") || strings.Contains(buf.String(), "Reviewer failures") {
		t.Error("options not honored")
	}
	buf.Reset()
	Markdown(&buf, &domain.ConsolidatedReview{Verdict: domain.VerdictApprove}, Options{})
	if !strings.Contains(buf.String(), "_No findings in the pull request scope._") {
		t.Error("empty review")
	}
}

func TestJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := JSON(&buf, sample()); err != nil {
		t.Fatal(err)
	}
	var back domain.ConsolidatedReview
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil || len(back.Findings) != 3 {
		t.Fatalf("round trip: %v", err)
	}
}
