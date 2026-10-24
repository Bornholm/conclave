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

func TestMarkdownSince(t *testing.T) {
	review := sample()
	review.Findings[0].PreviousID = "P1"
	review.Findings[2].PreviousID = "P4"
	review.Meta.Since = &domain.PreviousReview{HeadSHA: "abcdef0123", Findings: []domain.ConsolidatedFinding{
		{Severity: "critical", File: "internal/session/store.go", StartLine: 40, Title: "Concurrent access to session map"},
		{Severity: "medium", File: "y.go", StartLine: 2, Title: "Leak"},
		{Severity: "low", File: "z.go", StartLine: 3, Title: "Typo"},
		{Severity: "high", File: "legacy.go", StartLine: 9, Title: "Old bug"},
		{Severity: "high", File: "w.go", StartLine: 4, Title: "Race"},
	}}
	review.PreviousFindings = []domain.PreviousStatus{
		{ID: "P1", Status: domain.PreviousStillPresent},
		{ID: "P2", Status: domain.PreviousFixed, Evidence: "closed in abc", Claims: []domain.PreviousClaim{
			{Status: domain.PreviousStillPresent, Evidence: "y.go:2 still leaks", ReportedBy: []string{"r2"}},
		}},
		{ID: "P3", Status: domain.PreviousWithdrawn, Evidence: "the author's reason holds"},
		{ID: "P4", Status: domain.PreviousStillPresent},
	}
	// P5 is left without a status.
	var buf bytes.Buffer
	if err := Markdown(&buf, review, Options{}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		// No run id: the revision alone, never an invented one.
		"Measured against revision `abcdef0`: 1 fixed, 1 withdrawn, 2 still present, 1 not checked.",
		"**Not checked**", "w.go", ", high",
		"**Fixed**", "closed in abc", "**Withdrawn**", "the author's reason holds",
		// A status overruled is shown under the one kept.
		"closed in abc\n  - also said still-present: y.go:2 still leaks\n",
		// An out-of-scope finding keeps its link to the previous review.
		"**Old bug** (`legacy.go:9`, high, security), still present from the previous review (P4)",
		"**Still present** from the previous review (P1)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("markdown lacks %q\n%s", want, out)
		}
	}
}
