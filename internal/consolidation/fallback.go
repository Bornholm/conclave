package consolidation

import (
	"fmt"
	"sort"
	"strings"

	"github.com/bornholm/conclave/internal/domain"
)

// Fallback builds a consolidated review without a lead: one finding per
// group, taking the most severe and most confident representative.
func Fallback(groups []domain.FindingGroup, outcomes []ReviewerOutcome) *domain.ConsolidatedReview {
	review := &domain.ConsolidatedReview{
		SchemaVersion:   domain.ReportSchemaVersion,
		Verdict:         domain.VerdictApprove,
		Findings:        []domain.ConsolidatedFinding{},
		FailedReviewers: FailedReviewers(outcomes),
	}
	for _, g := range groups {
		rep := representative(g)
		review.Findings = append(review.Findings, domain.ConsolidatedFinding{
			Category: rep.Category, Severity: rep.Severity, Confidence: rep.Confidence,
			File: rep.File, StartLine: rep.StartLine, EndLine: rep.EndLine,
			Title: rep.Title, Description: rep.Description, Evidence: rep.Evidence, Suggestion: rep.Suggestion,
			ReportedBy: append([]string(nil), g.ReportedBy...),
			OutOfScope: rep.OutOfScope,
		})
	}
	SortConsolidated(review.Findings)
	var summaries []string
	// Out-of-scope findings never drive the verdict; in-scope severity does.
	for _, f := range review.Findings {
		if !f.OutOfScope && (f.Severity == domain.SeverityCritical || f.Severity == domain.SeverityHigh) {
			review.Verdict = domain.VerdictRequestChanges
		}
	}
	for _, o := range outcomes {
		if o.Report != nil {
			if o.Report.Verdict == domain.VerdictRequestChanges {
				review.Verdict = domain.VerdictRequestChanges
			} else if o.Report.Verdict == domain.VerdictComment && review.Verdict == domain.VerdictApprove {
				review.Verdict = domain.VerdictComment
			}
			if s := strings.TrimSpace(o.Report.Summary); s != "" {
				summaries = append(summaries, fmt.Sprintf("%s: %s", o.ID, s))
			}
			review.Questions = append(review.Questions, o.Report.Questions...)
		}
	}
	review.Summary = "Deterministic consolidation (lead unavailable). " + strings.Join(summaries, " ")
	return review
}

func representative(g domain.FindingGroup) domain.NormalizedFinding {
	best := g.Findings[0]
	for _, f := range g.Findings[1:] {
		if f.Severity.Rank() < best.Severity.Rank() || (f.Severity.Rank() == best.Severity.Rank() && f.Confidence > best.Confidence) {
			best = f
		}
	}
	return best
}

// FailedReviewers lists reviewers without a usable report.
func FailedReviewers(outcomes []ReviewerOutcome) []domain.FailedReviewer {
	out := []domain.FailedReviewer{}
	for _, o := range outcomes {
		if o.Report == nil {
			reason := "no report"
			if o.Err != nil {
				reason = o.Err.Error()
			}
			out = append(out, domain.FailedReviewer{ID: o.ID, Reason: reason})
		}
	}
	return out
}

// SortConsolidated orders in-scope findings first, then by severity,
// confidence desc, file and line.
func SortConsolidated(fs []domain.ConsolidatedFinding) {
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if a.OutOfScope != b.OutOfScope {
			return !a.OutOfScope
		}
		if a.Severity.Rank() != b.Severity.Rank() {
			return a.Severity.Rank() < b.Severity.Rank()
		}
		if a.Confidence != b.Confidence {
			return a.Confidence > b.Confidence
		}
		if a.File != b.File {
			return a.File < b.File
		}
		return a.StartLine < b.StartLine
	})
}
