package domain

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// ReportSchemaVersion is the schema version agents must emit.
const ReportSchemaVersion = "1"

// Severity of a finding.
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityHigh     Severity = "high"
	SeverityMedium   Severity = "medium"
	SeverityLow      Severity = "low"
	SeverityInfo     Severity = "info"
)

// Severities lists all known severities from most to least severe.
var Severities = []Severity{SeverityCritical, SeverityHigh, SeverityMedium, SeverityLow, SeverityInfo}

// Rank returns a sortable rank, 0 being the most severe. Unknown severities rank last.
func (s Severity) Rank() int {
	for i, k := range Severities {
		if k == s {
			return i
		}
	}
	return len(Severities)
}

// Valid reports whether the severity is known.
func (s Severity) Valid() bool { return s.Rank() < len(Severities) }

// Category of a finding.
type Category string

const CategoryOther Category = "other"

// Categories lists the normalized categories.
var Categories = []Category{
	"correctness", "security", "concurrency", "performance", "testing",
	"maintainability", "documentation", "dependencies", "api", "error-handling", CategoryOther,
}

// NormalizeCategory maps free text to a known category, defaulting to other.
func NormalizeCategory(raw string) Category {
	v := Category(strings.ToLower(strings.TrimSpace(raw)))
	for _, c := range Categories {
		if c == v {
			return c
		}
	}
	return CategoryOther
}

// Verdict of a review.
type Verdict string

const (
	VerdictApprove        Verdict = "approve"
	VerdictComment        Verdict = "comment"
	VerdictRequestChanges Verdict = "request_changes"
)

// Valid reports whether the verdict is known.
func (v Verdict) Valid() bool {
	return v == VerdictApprove || v == VerdictComment || v == VerdictRequestChanges
}

// AgentReport is the JSON document a reviewer agent must produce.
type AgentReport struct {
	SchemaVersion string     `json:"schema_version"`
	Reviewer      Reviewer   `json:"reviewer"`
	Summary       string     `json:"summary"`
	Findings      []Finding  `json:"findings"`
	Questions     []Question `json:"questions,omitempty"`
	Verdict       Verdict    `json:"verdict"`
	// PreviousFindings gives the status, at this head, of each finding of the
	// previous review, when the review is incremental.
	PreviousFindings []PreviousStatus `json:"previous_findings,omitempty"`
}

// PreviousStatus says what became of one finding of the previous review.
type PreviousStatus struct {
	// ID is the previous finding's id in the prompt, "P1" to "Pn".
	ID       string             `json:"id"`
	Status   PreviousStatusKind `json:"status"`
	Evidence string             `json:"evidence"`
	// ReportedBy lists the reviewers that gave this status; filled by the
	// lead and by the deterministic consolidation, not by a reviewer.
	ReportedBy []string `json:"reported_by,omitempty"`
	// Claims are the statuses given to this finding that Status and Evidence
	// do not show: a status overruled, by the consolidation or because a
	// finding still continues it, and a reviewer's evidence for the same
	// status beyond the one kept. Filled by Conclave, never by an agent.
	Claims []PreviousClaim `json:"claims,omitempty"`
}

// PreviousClaim is a status that some reviewers gave a previous finding.
type PreviousClaim struct {
	Status     PreviousStatusKind `json:"status"`
	Evidence   string             `json:"evidence,omitempty"`
	ReportedBy []string           `json:"reported_by,omitempty"`
}

// PreviousStatusKind is what became of a previous finding.
type PreviousStatusKind string

const (
	// PreviousFixed: a change since the previous review addressed it.
	PreviousFixed PreviousStatusKind = "fixed"
	// PreviousStillPresent: the problem is still in the code at this head.
	PreviousStillPresent PreviousStatusKind = "still-present"
	// PreviousWithdrawn: the finding was wrong in the first place, for
	// instance because the author's response refutes it with evidence.
	PreviousWithdrawn PreviousStatusKind = "withdrawn"
)

// Valid reports whether the status is known.
func (k PreviousStatusKind) Valid() bool {
	return k == PreviousFixed || k == PreviousStillPresent || k == PreviousWithdrawn
}

// PreviousReview is the review an incremental review is measured against.
type PreviousReview struct {
	RunID   string `json:"run_id"`
	HeadSHA string `json:"head_sha"`
	// Findings are the previous review's findings, in its order: the first
	// is "P1".
	Findings []ConsolidatedFinding `json:"findings"`
	// Responses is the author's free-text answer to the previous review.
	Responses string `json:"responses,omitempty"`
}

// PreviousID is the id the prompt gives to the i-th previous finding.
func PreviousID(i int) string { return "P" + strconv.Itoa(i+1) }

// PreviousIndex is the inverse of PreviousID: the 0-based index of a previous
// finding id, false when id is not one PreviousID gives.
func PreviousIndex(id string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimPrefix(id, "P"))
	if err != nil || n < 1 || PreviousID(n-1) != id {
		return 0, false
	}
	return n - 1, true
}

// Reviewer identifies the agent that produced a report.
type Reviewer struct {
	ID    string `json:"id"`
	Model string `json:"model,omitempty"`
}

// Finding is a single review observation.
type Finding struct {
	ID          string   `json:"id"`
	Category    Category `json:"category"`
	Severity    Severity `json:"severity"`
	Confidence  float64  `json:"confidence"`
	File        string   `json:"file"`
	StartLine   int      `json:"start_line"`
	EndLine     int      `json:"end_line"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Evidence    string   `json:"evidence,omitempty"`
	Suggestion  string   `json:"suggestion,omitempty"`
	// OutOfScope marks a finding located in a file the pull request does not
	// touch. It is kept for information and must not drive the verdict.
	OutOfScope bool `json:"out_of_scope,omitempty"`
	// PreviousID links the finding to the previous review's finding it
	// continues, in an incremental review.
	PreviousID string `json:"previous_id,omitempty"`
}

// Question raised by a reviewer for the author.
type Question struct {
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Question string `json:"question"`
}

// NormalizedFinding is a validated finding attributed to its reviewer.
type NormalizedFinding struct {
	Finding
	ReviewerID  string `json:"reviewer_id"`
	Fingerprint string `json:"fingerprint"`
}

// FindingGroup is a set of similar findings from different reviewers.
type FindingGroup struct {
	ID         string              `json:"id"`
	Findings   []NormalizedFinding `json:"findings"`
	ReportedBy []string            `json:"reported_by"`
}

// ConsolidatedReview is the final output of a run.
type ConsolidatedReview struct {
	SchemaVersion   string                `json:"schema_version"`
	Summary         string                `json:"summary"`
	Verdict         Verdict               `json:"verdict"`
	Findings        []ConsolidatedFinding `json:"findings"`
	Questions       []Question            `json:"questions,omitempty"`
	FailedReviewers []FailedReviewer      `json:"failed_reviewers"`
	Warnings        []string              `json:"warnings,omitempty"`
	// PreviousFindings is what became of the previous review's findings, in
	// an incremental review.
	PreviousFindings []PreviousStatus `json:"previous_findings,omitempty"`
	// Meta is filled by the orchestrator, never by the lead.
	Meta ReviewMeta `json:"meta"`
}

// ConsolidatedFinding is a finding retained by the lead, with provenance.
type ConsolidatedFinding struct {
	Category    Category `json:"category"`
	Severity    Severity `json:"severity"`
	Confidence  float64  `json:"confidence"`
	File        string   `json:"file"`
	StartLine   int      `json:"start_line"`
	EndLine     int      `json:"end_line"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Evidence    string   `json:"evidence,omitempty"`
	Suggestion  string   `json:"suggestion,omitempty"`
	ReportedBy  []string `json:"reported_by"`
	OutOfScope  bool     `json:"out_of_scope,omitempty"`
	PreviousID  string   `json:"previous_id,omitempty"`
}

// FailedReviewer describes a reviewer whose report could not be used.
type FailedReviewer struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// ReviewMeta carries run information for rendering.
type ReviewMeta struct {
	RunID    string `json:"run_id"`
	PRNumber int64  `json:"pr_number"`
	PRTitle  string `json:"pr_title"`
	PRURL    string `json:"pr_url"`
	// Branch is the revision a local review reviewed, a branch or a range
	// such as main..feature, empty for a pull request.
	Branch             string   `json:"branch,omitempty"`
	HeadSHA            string   `json:"head_sha"`
	MergeBaseSHA       string   `json:"merge_base_sha"`
	ReviewersTotal     int      `json:"reviewers_total"`
	ReviewersSucceeded int      `json:"reviewers_succeeded"`
	LeadID             string   `json:"lead_id"`
	LeadUsed           bool     `json:"lead_used"`
	SucceededReviewers []string `json:"succeeded_reviewers"`
	// Models maps agent ids to the model that actually answered, when known
	// (from configuration or detected in the agent output).
	Models map[string]string `json:"models,omitempty"`
	// Since is the previous review an incremental review is measured
	// against: its findings, so the rendering can name what was fixed.
	Since *PreviousReview `json:"since,omitempty"`
}

// UnmarshalJSON accepts a numeric or string "id": agents disagree on the type
// and a report must not be lost over it.
func (f *Finding) UnmarshalJSON(data []byte) error {
	type plain Finding
	aux := struct {
		ID json.RawMessage `json:"id"`
		*plain
	}{plain: (*plain)(f)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	f.ID = flexibleString(aux.ID)
	return nil
}

// flexibleString renders a JSON scalar as a string: "x" -> x, 12 -> 12, null -> "".
func flexibleString(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}
