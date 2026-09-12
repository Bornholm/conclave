package domain

import "strings"

// TriageSchemaVersion is the schema version triage agents must emit.
const TriageSchemaVersion = "1"

// Label is a label defined on the forge repository. The description is what
// makes a label usable by an agent, so it travels with the name.
type Label struct {
	// ID is the forge identifier. Gitea needs it to attach a label to an issue.
	ID          int64  `json:"id,omitempty"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Color       string `json:"color,omitempty"`
}

// Reference kinds.
const (
	ReferenceCommit      = "commit"
	ReferencePullRequest = "pull_request"
	ReferenceIssue       = "issue"
)

// Reference is a commit, pull request or issue that mentions an issue. It is
// the evidence that lets a triage say "fixed" instead of "probably fixed".
type Reference struct {
	Kind      string `json:"kind"`
	Ref       string `json:"ref"`
	Title     string `json:"title,omitempty"`
	State     string `json:"state,omitempty"`
	WebURL    string `json:"web_url,omitempty"`
	Actor     string `json:"actor,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

// IssueState values.
const (
	IssueOpen   = "open"
	IssueClosed = "closed"
)

// TriageStatus says whether an issue still describes something true today.
type TriageStatus string

const (
	// TriageStillPresent: the problem is still in the code, with a location.
	TriageStillPresent TriageStatus = "still-present"
	// TriageFixed: a commit or pull request addressed it, named as evidence.
	TriageFixed TriageStatus = "fixed"
	// TriageObsolete: the code it describes is gone or was replaced.
	TriageObsolete TriageStatus = "obsolete"
	// TriageDuplicate: another issue covers the same thing.
	TriageDuplicate TriageStatus = "duplicate-of"
	// TriageNeedsInfo: the issue cannot be decided without an answer.
	TriageNeedsInfo TriageStatus = "needs-info"
)

// TriageStatuses lists every known status.
var TriageStatuses = []TriageStatus{
	TriageStillPresent, TriageFixed, TriageObsolete, TriageDuplicate, TriageNeedsInfo,
}

// Valid reports whether the status is known.
func (s TriageStatus) Valid() bool {
	for _, k := range TriageStatuses {
		if k == s {
			return true
		}
	}
	return false
}

// Rank orders statuses for display: what needs action first.
func (s TriageStatus) Rank() int {
	for i, k := range TriageStatuses {
		if k == s {
			return i
		}
	}
	return len(TriageStatuses)
}

// NormalizeStatus maps free text to a known status, "" when unknown.
func NormalizeStatus(raw string) TriageStatus {
	v := TriageStatus(strings.ReplaceAll(strings.ToLower(strings.TrimSpace(raw)), "_", "-"))
	if v.Valid() {
		return v
	}
	return ""
}

// TriageReport is the JSON document a triage agent produces for one issue.
type TriageReport struct {
	SchemaVersion string       `json:"schema_version"`
	Reviewer      Reviewer     `json:"reviewer"`
	Number        int64        `json:"number"`
	Labels        []string     `json:"labels"`
	Status        TriageStatus `json:"status"`
	Confidence    float64      `json:"confidence"`
	Summary       string       `json:"summary"`
	Evidence      string       `json:"evidence"`
	DuplicateOf   int64        `json:"duplicate_of,omitempty"`
	Question      string       `json:"question,omitempty"`
}

// ConsolidatedTriage is the final output of a triage run.
type ConsolidatedTriage struct {
	SchemaVersion string         `json:"schema_version"`
	Summary       string         `json:"summary"`
	Issues        []TriagedIssue `json:"issues"`
	Failed        []FailedIssue  `json:"failed,omitempty"`
	Warnings      []string       `json:"warnings,omitempty"`
	Meta          TriageMeta     `json:"meta"`
}

// TriagedIssue is one issue after consolidation.
type TriagedIssue struct {
	Number        int64        `json:"number"`
	Title         string       `json:"title"`
	WebURL        string       `json:"web_url,omitempty"`
	State         string       `json:"state,omitempty"`
	CurrentLabels []string     `json:"current_labels,omitempty"`
	Labels        []string     `json:"labels"`
	Status        TriageStatus `json:"status"`
	Confidence    float64      `json:"confidence"`
	Summary       string       `json:"summary"`
	Evidence      string       `json:"evidence,omitempty"`
	DuplicateOf   int64        `json:"duplicate_of,omitempty"`
	Question      string       `json:"question,omitempty"`
	ReportedBy    []string     `json:"reported_by"`

	// Applied lists the labels conclave actually added on the forge, and
	// ApplyError says why it could not.
	Applied    []string `json:"applied,omitempty"`
	ApplyError string   `json:"apply_error,omitempty"`
}

// AddedLabels returns the proposed labels the issue does not carry yet.
func (t TriagedIssue) AddedLabels() []string {
	current := make(map[string]bool, len(t.CurrentLabels))
	for _, l := range t.CurrentLabels {
		current[strings.ToLower(l)] = true
	}
	var out []string
	for _, l := range t.Labels {
		if !current[strings.ToLower(l)] {
			out = append(out, l)
		}
	}
	return out
}

// FailedIssue records an issue no agent could triage.
type FailedIssue struct {
	Number int64  `json:"number"`
	Reason string `json:"reason"`
}

// TriageMeta carries run information for rendering.
type TriageMeta struct {
	RunID      string            `json:"run_id"`
	Repository string            `json:"repository"`
	HeadSHA    string            `json:"head_sha"`
	Branch     string            `json:"branch"`
	Total      int               `json:"total"`
	Succeeded  int               `json:"succeeded"`
	LeadID     string            `json:"lead_id"`
	LeadUsed   bool              `json:"lead_used"`
	Models     map[string]string `json:"models,omitempty"`
}
