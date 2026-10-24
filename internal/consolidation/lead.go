package consolidation

import (
	"encoding/json"
	"fmt"
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
	for i, p := range review.PreviousFindings {
		review.PreviousFindings[i].ReportedBy = knownOnly(p.ReportedBy, known)
		// A status the lead gave without evidence takes the first a reviewer
		// gave for it, as without a lead.
		if p.Evidence == "" {
			review.PreviousFindings[i].Evidence = reviewerEvidence(p, outcomes)
		}
		review.PreviousFindings[i].Claims = claims(review.PreviousFindings[i], outcomes)
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
