package domain

// CommentSchemaVersion is the schema version of the comment a writer agent
// returns.
const CommentSchemaVersion = "1"

// ForgeReview is one review submitted on a pull request, as the forge lists
// it.
type ForgeReview struct {
	Author string `json:"author"`
	// State is the review decision, lower case: approved,
	// changes_requested, commented, or what else the forge says.
	State     string `json:"state"`
	Body      string `json:"body"`
	CommitSHA string `json:"commit_sha"`
	// SubmittedAt is the forge's timestamp, RFC 3339.
	SubmittedAt string `json:"submitted_at"`
}

// CommentReport is the comment a writer agent turns a review into, in the
// pull request's language.
type CommentReport struct {
	SchemaVersion string `json:"schema_version"`
	// Language is the language the comment is written in, as the writer
	// names it.
	Language string `json:"language"`
	Body     string `json:"body"`
}
