package domain

import "strings"

// PlanSchemaVersion is the schema version planning agents must emit.
const PlanSchemaVersion = "1"

// PlanEffort is a coarse size for the whole change, the one number a
// maintainer reads before deciding who picks the ticket up.
type PlanEffort string

const (
	EffortSmall  PlanEffort = "small"
	EffortMedium PlanEffort = "medium"
	EffortLarge  PlanEffort = "large"
)

// PlanEfforts lists every known effort, from smallest to largest.
var PlanEfforts = []PlanEffort{EffortSmall, EffortMedium, EffortLarge}

// Valid reports whether the effort is known.
func (e PlanEffort) Valid() bool {
	for _, k := range PlanEfforts {
		if k == e {
			return true
		}
	}
	return false
}

// NormalizeEffort maps free text to a known effort, "" when unknown.
func NormalizeEffort(raw string) PlanEffort {
	v := PlanEffort(strings.ToLower(strings.TrimSpace(raw)))
	if v.Valid() {
		return v
	}
	return ""
}

// PlanStep is one coherent change, in the order it must be made.
type PlanStep struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Details says what to change and why, in terms of the code that exists.
	Details string `json:"details"`
	// Files are the repository paths the step touches. A path that does not
	// exist yet is legitimate: a step may create a file.
	Files []string `json:"files,omitempty"`
	// Validation is how one knows the step is done: a test, a command, an
	// observable behaviour.
	Validation string `json:"validation,omitempty"`
	// DependsOn lists the ids of the steps that must land first.
	DependsOn []string `json:"depends_on,omitempty"`
}

// PlanRisk is what could go wrong, and what keeps it from happening.
type PlanRisk struct {
	Description string `json:"description"`
	Mitigation  string `json:"mitigation,omitempty"`
}

// PlanAlternative is an approach that was considered and set aside. It is
// what makes the chosen approach reviewable.
type PlanAlternative struct {
	Approach string `json:"approach"`
	WhyNot   string `json:"why_not"`
}

// PlanReport is the JSON document a planning agent produces for one issue.
type PlanReport struct {
	SchemaVersion string   `json:"schema_version"`
	Reviewer      Reviewer `json:"reviewer"`
	Number        int64    `json:"number"`
	// Understanding restates what the ticket asks in terms of the code today.
	Understanding string            `json:"understanding"`
	Approach      string            `json:"approach"`
	Alternatives  []PlanAlternative `json:"alternatives,omitempty"`
	Steps         []PlanStep        `json:"steps"`
	Tests         []string          `json:"tests,omitempty"`
	Risks         []PlanRisk        `json:"risks,omitempty"`
	OpenQuestions []string          `json:"open_questions,omitempty"`
	Effort        PlanEffort        `json:"effort"`
	Confidence    float64           `json:"confidence"`
}

// ConsolidatedPlan is the final output of a plan run.
type ConsolidatedPlan struct {
	SchemaVersion string            `json:"schema_version"`
	Number        int64             `json:"number"`
	Title         string            `json:"title,omitempty"`
	WebURL        string            `json:"web_url,omitempty"`
	Summary       string            `json:"summary"`
	Understanding string            `json:"understanding,omitempty"`
	Approach      string            `json:"approach"`
	Alternatives  []PlanAlternative `json:"alternatives,omitempty"`
	Steps         []PlanStep        `json:"steps"`
	Tests         []string          `json:"tests,omitempty"`
	Risks         []PlanRisk        `json:"risks,omitempty"`
	OpenQuestions []string          `json:"open_questions,omitempty"`
	Effort        PlanEffort        `json:"effort,omitempty"`
	Confidence    float64           `json:"confidence"`
	ReportedBy    []string          `json:"reported_by"`
	Failed        []FailedReviewer  `json:"failed,omitempty"`
	Warnings      []string          `json:"warnings,omitempty"`
	// Meta is filled by the orchestrator, never by the lead.
	Meta PlanMeta `json:"meta"`
}

// PlanMeta carries run information for rendering.
type PlanMeta struct {
	RunID             string            `json:"run_id"`
	Repository        string            `json:"repository"`
	IssueNumber       int64             `json:"issue_number"`
	HeadSHA           string            `json:"head_sha"`
	Branch            string            `json:"branch"`
	PlannersTotal     int               `json:"planners_total"`
	PlannersSucceeded int               `json:"planners_succeeded"`
	LeadID            string            `json:"lead_id"`
	LeadUsed          bool              `json:"lead_used"`
	Models            map[string]string `json:"models,omitempty"`
}
