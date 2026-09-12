package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bornholm/conclave/internal/domain"
)

// TriageLimits bound one triage report.
type TriageLimits struct {
	MaxLabels     int
	MaxFieldBytes int
}

// ParseTriageReport decodes and validates the triage of one issue. A label
// the forge does not define is dropped with a warning rather than failing the
// report: an agent inventing "needs-triage" should not cost the whole answer.
func ParseTriageReport(raw []byte, agentID string, number int64, known map[string]string, lim TriageLimits) (*domain.TriageReport, []string, error) {
	data, err := ExtractJSON(raw, "schema_version")
	if err != nil {
		return nil, nil, err
	}
	var rep domain.TriageReport
	if err := json.Unmarshal(data, &rep); err != nil {
		return nil, nil, fmt.Errorf("decode triage report: %w", err)
	}
	if rep.SchemaVersion != domain.TriageSchemaVersion {
		return nil, nil, fmt.Errorf("unsupported schema_version %q", rep.SchemaVersion)
	}
	if rep.Reviewer.ID != agentID {
		return nil, nil, fmt.Errorf("reviewer id %q does not match agent %q", rep.Reviewer.ID, agentID)
	}
	if rep.Number != number {
		return nil, nil, fmt.Errorf("report is about issue #%d, expected #%d", rep.Number, number)
	}
	status := domain.NormalizeStatus(string(rep.Status))
	if status == "" {
		return nil, nil, fmt.Errorf("unknown status %q", rep.Status)
	}
	rep.Status = status
	if rep.Confidence < 0 || rep.Confidence > 1 {
		return nil, nil, fmt.Errorf("confidence %v out of [0,1]", rep.Confidence)
	}
	if lim.MaxFieldBytes <= 0 {
		lim.MaxFieldBytes = DefaultLimits.MaxFieldBytes
	}

	var warnings []string
	rep.Labels, warnings = normalizeLabels(rep.Labels, known, lim.MaxLabels, "")
	rep.Summary = truncate(strings.TrimSpace(rep.Summary), lim.MaxFieldBytes)
	rep.Evidence = truncate(strings.TrimSpace(rep.Evidence), lim.MaxFieldBytes)
	rep.Question = truncate(strings.TrimSpace(rep.Question), lim.MaxFieldBytes)

	if err := checkStatusFields(rep.Status, rep.Evidence, rep.Question, rep.DuplicateOf, number); err != nil {
		return nil, warnings, err
	}
	return &rep, warnings, nil
}

// checkStatusFields enforces what each status must carry. A status without
// its evidence is the failure mode that closes real bugs, so it is an error
// and not a warning.
func checkStatusFields(status domain.TriageStatus, evidence, question string, duplicateOf, number int64) error {
	switch status {
	case domain.TriageNeedsInfo:
		if question == "" {
			return fmt.Errorf("status needs-info requires a question")
		}
	case domain.TriageDuplicate:
		if duplicateOf <= 0 {
			return fmt.Errorf("status duplicate-of requires duplicate_of")
		}
		if duplicateOf == number {
			return fmt.Errorf("issue #%d cannot be a duplicate of itself", number)
		}
	default:
		if evidence == "" || strings.EqualFold(evidence, "none") {
			return fmt.Errorf("status %s requires evidence", status)
		}
	}
	return nil
}

// normalizeLabels keeps the labels the forge defines, in canonical case,
// deduplicated and capped.
func normalizeLabels(labels []string, known map[string]string, max int, prefix string) ([]string, []string) {
	var out, warnings []string
	seen := map[string]bool{}
	for _, l := range labels {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		canonical, ok := known[strings.ToLower(l)]
		if !ok {
			warnings = append(warnings, fmt.Sprintf("%slabel %q is not defined on the repository", prefix, truncate(l, 60)))
			continue
		}
		if seen[canonical] {
			continue
		}
		if max > 0 && len(out) >= max {
			warnings = append(warnings, fmt.Sprintf("%sdropped labels beyond the limit of %d", prefix, max))
			break
		}
		seen[canonical] = true
		out = append(out, canonical)
	}
	return out, warnings
}

// NormalizeLabels is the exported form used to validate the lead output.
func NormalizeLabels(labels []string, known map[string]string, max int, prefix string) ([]string, []string) {
	return normalizeLabels(labels, known, max, prefix)
}

// CheckTriageStatusFields is the exported form used to validate the lead output.
func CheckTriageStatusFields(status domain.TriageStatus, evidence, question string, duplicateOf, number int64) error {
	return checkStatusFields(status, evidence, question, duplicateOf, number)
}

// TruncateField is the exported truncation used by the consolidation package.
func TruncateField(s string, max int) string { return truncate(strings.TrimSpace(s), max) }
