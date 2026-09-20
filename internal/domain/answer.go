package domain

// AnswerSchemaVersion is the schema version answering agents must emit.
const AnswerSchemaVersion = "1"

// AnswerReference points at what an answer is grounded in: a file of the
// project when the question has one, a command, a document, a URL. It is the
// difference between an answer one can check and an assertion.
type AnswerReference struct {
	// Source is a repository path when a project is attached, free text
	// otherwise.
	Source string `json:"source"`
	Line   int    `json:"line,omitempty"`
	Note   string `json:"note,omitempty"`
}

// AnswerReport is the JSON document an answering agent produces.
type AnswerReport struct {
	SchemaVersion string   `json:"schema_version"`
	Reviewer      Reviewer `json:"reviewer"`
	// Answer is the answer itself, in Markdown, self-contained.
	Answer        string            `json:"answer"`
	KeyPoints     []string          `json:"key_points,omitempty"`
	References    []AnswerReference `json:"references,omitempty"`
	Caveats       []string          `json:"caveats,omitempty"`
	OpenQuestions []string          `json:"open_questions,omitempty"`
	Confidence    float64           `json:"confidence"`
}

// AnswerPosition is what one or more agents held on a contested point.
type AnswerPosition struct {
	By       []string `json:"by"`
	Position string   `json:"position"`
}

// AnswerDisagreement records a point the agents did not answer the same way.
// Asking several agents is only worth the cost if the places they diverge
// survive the consolidation instead of being averaged away.
type AnswerDisagreement struct {
	Topic     string           `json:"topic"`
	Positions []AnswerPosition `json:"positions"`
}

// AgentAnswer is one agent's answer kept whole. The deterministic fallback
// uses it to publish what the agents it did not pick had written.
type AgentAnswer struct {
	AgentID string `json:"agent_id"`
	Answer  string `json:"answer"`
}

// ConsolidatedAnswer is the final output of an ask run.
type ConsolidatedAnswer struct {
	SchemaVersion string `json:"schema_version"`
	// Question is the question as it was asked, echoed back so an artifact
	// read months later still says what was answered.
	Question      string               `json:"question"`
	Answer        string               `json:"answer"`
	KeyPoints     []string             `json:"key_points,omitempty"`
	Disagreements []AnswerDisagreement `json:"disagreements,omitempty"`
	References    []AnswerReference    `json:"references,omitempty"`
	Caveats       []string             `json:"caveats,omitempty"`
	OpenQuestions []string             `json:"open_questions,omitempty"`
	Confidence    float64              `json:"confidence"`
	ReportedBy    []string             `json:"reported_by"`
	OtherAnswers  []AgentAnswer        `json:"other_answers,omitempty"`
	Failed        []FailedReviewer     `json:"failed,omitempty"`
	Warnings      []string             `json:"warnings,omitempty"`
	// Meta is filled by the orchestrator, never by the lead.
	Meta AnswerMeta `json:"meta"`
}

// AnswerMeta carries run information for rendering.
type AnswerMeta struct {
	RunID string `json:"run_id"`
	// Project is the repository the question was attached to, empty when the
	// question stands on its own.
	Project              string            `json:"project,omitempty"`
	Repository           string            `json:"repository,omitempty"`
	HeadSHA              string            `json:"head_sha,omitempty"`
	Branch               string            `json:"branch,omitempty"`
	RespondentsTotal     int               `json:"respondents_total"`
	RespondentsSucceeded int               `json:"respondents_succeeded"`
	LeadID               string            `json:"lead_id"`
	LeadUsed             bool              `json:"lead_used"`
	Models               map[string]string `json:"models,omitempty"`
}
