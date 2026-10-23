package consolidation

import (
	"fmt"
	"sort"
	"strconv"
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
			PreviousID: previousID(g),
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
	review.PreviousFindings = MergePrevious(outcomes)
	return review
}

// previousID is the previous finding a group continues: the first link any of
// its findings gives.
func previousID(g domain.FindingGroup) string {
	for _, f := range g.Findings {
		if f.PreviousID != "" {
			return f.PreviousID
		}
	}
	return ""
}

// MergePrevious merges the reviewers' statuses for the previous findings
// without a lead, conservatively: one reviewer that still sees a problem keeps
// it, since a wrong "fixed" buries it and a wrong "still-present" costs a
// second look. Between "fixed" and "withdrawn", "fixed" wins: it claims less
// about the previous review. An id no reviewer gave a status is left out, and
// the rendering lists it as not checked.
func MergePrevious(outcomes []ReviewerOutcome) []domain.PreviousStatus {
	byID := map[string][]domain.PreviousStatus{}
	var order []string
	for _, o := range outcomes {
		if o.Report == nil {
			continue
		}
		for _, p := range o.Report.PreviousFindings {
			if _, ok := byID[p.ID]; !ok {
				order = append(order, p.ID)
			}
			p.ReportedBy = []string{o.ID}
			byID[p.ID] = append(byID[p.ID], p)
		}
	}
	var out []domain.PreviousStatus
	for _, id := range order {
		var chosen *domain.PreviousStatus
		for _, want := range []domain.PreviousStatusKind{domain.PreviousStillPresent, domain.PreviousFixed, domain.PreviousWithdrawn} {
			for _, p := range byID[id] {
				if p.Status != want {
					continue
				}
				if chosen == nil {
					c := p
					chosen = &c
					continue
				}
				chosen.ReportedBy = append(chosen.ReportedBy, p.ReportedBy...)
			}
			if chosen != nil {
				break
			}
		}
		out = append(out, *chosen)
	}
	sort.SliceStable(out, func(i, j int) bool { return previousIndex(out[i].ID) < previousIndex(out[j].ID) })
	return out
}

// previousIndex orders "P2" before "P10".
func previousIndex(id string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(id, "P"))
	if err != nil {
		return 1 << 30
	}
	return n
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
