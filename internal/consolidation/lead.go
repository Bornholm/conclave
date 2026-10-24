package consolidation

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/bornholm/conclave/internal/agent"
	"github.com/bornholm/conclave/internal/domain"
)

// ParseLeadReview decodes and validates the lead output. Findings attributed
// to an unknown reviewer or with invalid fields are dropped with a warning.
// failed_reviewers is always overwritten from the actual outcomes, and
// out_of_scope is recomputed from the changed file set when one is given so
// the lead cannot promote an out-of-scope finding into the verdict.
//
// previous is the number of findings of the previous review in an incremental
// review, zero otherwise: links and statuses outside P1 to P<previous> are
// dropped with a warning, and a status attributed to no successful reviewer
// keeps the lead's word alone.
func ParseLeadReview(raw []byte, succeeded []string, outcomes []ReviewerOutcome, changed map[string]bool, exists agent.FileChecker, maxFindings, previous int) (*domain.ConsolidatedReview, []string, error) {
	data, err := agent.ExtractJSON(raw, "schema_version")
	if err != nil {
		return nil, nil, err
	}
	var review domain.ConsolidatedReview
	if err := json.Unmarshal(data, &review); err != nil {
		return nil, nil, fmt.Errorf("decode lead review: %w", err)
	}
	if review.SchemaVersion != domain.ReportSchemaVersion {
		return nil, nil, fmt.Errorf("unsupported schema_version %q", review.SchemaVersion)
	}
	if !review.Verdict.Valid() {
		return nil, nil, fmt.Errorf("invalid verdict %q", review.Verdict)
	}
	known := map[string]bool{}
	for _, id := range succeeded {
		known[id] = true
	}
	var warnings []string
	kept := review.Findings[:0]
	for i, f := range review.Findings {
		if maxFindings > 0 && len(kept) >= maxFindings {
			warnings = append(warnings, fmt.Sprintf("lead: dropped findings beyond the limit of %d", maxFindings))
			break
		}
		f, err := normalizeConsolidated(f, known, exists)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("lead: finding %d (%q) rejected: %v", i, f.Title, err))
			continue
		}
		if len(changed) > 0 {
			f.OutOfScope = !changed[f.File]
		}
		if f.PreviousID, err = agent.NormalizePreviousID(f.PreviousID, previous); err != nil {
			warnings = append(warnings, fmt.Sprintf("lead: finding %d (%q): %v, link dropped", i, f.Title, err))
		}
		kept = append(kept, f)
	}
	var pw []string
	review.PreviousFindings, pw = agent.NormalizePrevious(review.PreviousFindings, previous, 0)
	for _, w := range pw {
		warnings = append(warnings, "lead: "+w)
	}
	given := map[string]bool{}
	for _, p := range review.PreviousFindings {
		given[p.ID] = true
	}
	// A previous finding the lead gave no status but one of its findings
	// continues is still present: it gets the status here, credited to the
	// reporters of those findings and with the reviewers' claims, rather
	// than a bare one from the reconciliation.
	added := map[string]int{}
	for _, f := range kept {
		if f.PreviousID == "" || given[f.PreviousID] {
			continue
		}
		i, ok := added[f.PreviousID]
		if !ok {
			i = len(review.PreviousFindings)
			added[f.PreviousID] = i
			review.PreviousFindings = append(review.PreviousFindings, domain.PreviousStatus{ID: f.PreviousID, Status: domain.PreviousStillPresent})
		}
		for _, id := range f.ReportedBy {
			if !slices.Contains(review.PreviousFindings[i].ReportedBy, id) {
				review.PreviousFindings[i].ReportedBy = append(review.PreviousFindings[i].ReportedBy, id)
			}
		}
	}
	for i := range review.PreviousFindings {
		p := &review.PreviousFindings[i]
		p.ReportedBy = knownOnly(p.ReportedBy, known)
		// A status the lead gave without evidence takes the first a reviewer
		// gave for it, as without a lead, and credits that reviewer.
		if p.Evidence == "" {
			if evidence, reviewer := reviewerEvidence(*p, outcomes); evidence != "" {
				p.Evidence = evidence
				if !slices.Contains(p.ReportedBy, reviewer) {
					p.ReportedBy = append(p.ReportedBy, reviewer)
				}
			}
		}
		p.Claims = claims(*p, outcomes)
	}
	sort.SliceStable(review.PreviousFindings, func(i, j int) bool {
		return previousOrder(review.PreviousFindings[i].ID) < previousOrder(review.PreviousFindings[j].ID)
	})
	if previous > 0 {
		warnings = append(warnings, omittedPrevious(review.PreviousFindings, outcomes)...)
	}
	review.Findings = kept
	if review.Findings == nil {
		review.Findings = []domain.ConsolidatedFinding{}
	}
	SortConsolidated(review.Findings)
	review.FailedReviewers = FailedReviewers(outcomes)
	return &review, warnings, nil
}

func normalizeConsolidated(f domain.ConsolidatedFinding, known map[string]bool, exists agent.FileChecker) (domain.ConsolidatedFinding, error) {
	nf, err := agent.NormalizeFindingFields(domain.Finding{
		Category: f.Category, Severity: f.Severity, Confidence: f.Confidence, File: f.File,
		StartLine: f.StartLine, EndLine: f.EndLine, Title: f.Title, Description: f.Description,
		Evidence: f.Evidence, Suggestion: f.Suggestion,
	}, exists)
	if err != nil {
		return f, err
	}
	f.Category, f.Severity, f.File, f.EndLine = nf.Category, nf.Severity, nf.File, nf.EndLine
	f.Title, f.Description, f.Evidence, f.Suggestion = nf.Title, nf.Description, nf.Evidence, nf.Suggestion
	by := knownOnly(f.ReportedBy, known)
	if len(by) == 0 {
		return f, fmt.Errorf("reported_by %v names no successful reviewer", f.ReportedBy)
	}
	f.ReportedBy = by
	return f, nil
}

// knownOnly keeps the ids of successful reviewers, once each, in order.
func knownOnly(ids []string, known map[string]bool) []string {
	var by []string
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if known[id] && !seen[id] {
			by = append(by, id)
			seen[id] = true
		}
	}
	return by
}

// omittedPrevious warns about the previous findings the reviewers gave a
// status and the lead did not. They stay not checked: a "fixed" the lead did
// not verify must not take a problem off the review. The warning keeps what
// the reviewers said.
func omittedPrevious(settled []domain.PreviousStatus, outcomes []ReviewerOutcome) []string {
	given := map[string]bool{}
	for _, p := range settled {
		given[p.ID] = true
	}
	var warnings []string
	for _, p := range MergePrevious(outcomes) {
		if given[p.ID] {
			continue
		}
		said := []string{claimText(domain.PreviousClaim{Status: p.Status, Evidence: p.Evidence, ReportedBy: p.ReportedBy})}
		for _, c := range p.Claims {
			said = append(said, claimText(c))
		}
		warnings = append(warnings, fmt.Sprintf("lead: previous finding %s left without a status, it is not checked; the reviewers said %s", p.ID, strings.Join(said, "; ")))
	}
	return warnings
}

func claimText(c domain.PreviousClaim) string {
	s := string(c.Status)
	if len(c.ReportedBy) > 0 {
		s += " (" + strings.Join(c.ReportedBy, ", ")
		if c.Evidence != "" {
			s += ": " + c.Evidence
		}
		return s + ")"
	}
	if c.Evidence != "" {
		s += " (" + c.Evidence + ")"
	}
	return s
}
