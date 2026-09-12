// Package domain holds the forge-agnostic models shared by the whole program.
package domain

// Repository identifies a repository on a forge.
type Repository struct {
	Host  string `json:"host"`
	Owner string `json:"owner"`
	Name  string `json:"name"`
}

// FullName returns owner/name.
func (r Repository) FullName() string { return r.Owner + "/" + r.Name }

// PullRequest is the normalized pull request model.
type PullRequest struct {
	Number      int64  `json:"number"`
	Title       string `json:"title"`
	Description string `json:"description"`
	State       string `json:"state"`
	WebURL      string `json:"web_url"`
	Author      string `json:"author"`

	Base RepositoryRef `json:"base"`
	Head RepositoryRef `json:"head"`

	MergeBaseSHA string `json:"merge_base_sha"`

	Labels       []string      `json:"labels"`
	ChangedFiles []ChangedFile `json:"changed_files"`
	Issues       []Issue       `json:"issues"`
	Discussion   []Comment     `json:"discussion"`
}

// Comment kinds.
const (
	CommentKindComment = "comment" // conversation comment on the pull request
	CommentKindReview  = "review"  // review summary (approve, request changes, comment)
	CommentKindInline  = "inline"  // review comment attached to a diff line
)

// Comment is one entry of the pull request discussion, in chronological order.
type Comment struct {
	Kind      string `json:"kind"`
	Author    string `json:"author"`
	CreatedAt string `json:"created_at"`
	Body      string `json:"body"`
	// State is the review decision for review comments (approved, changes_requested, commented).
	State string `json:"state,omitempty"`
	// Path and Line locate an inline comment.
	Path string `json:"path,omitempty"`
	Line int    `json:"line,omitempty"`
}

// RepositoryRef points at a commit in a repository.
type RepositoryRef struct {
	Owner    string `json:"owner"`
	Name     string `json:"name"`
	CloneURL string `json:"clone_url"`
	Branch   string `json:"branch"`
	SHA      string `json:"sha"`
	IsFork   bool   `json:"is_fork"`
}

// File change statuses.
const (
	FileAdded    = "added"
	FileModified = "modified"
	FileDeleted  = "deleted"
	FileRenamed  = "renamed"
	FileCopied   = "copied"
	FileChanged  = "changed"
)

// ChangedFile is one entry of the pull request file list.
type ChangedFile struct {
	Path         string `json:"path"`
	PreviousPath string `json:"previous_path,omitempty"`
	Status       string `json:"status"`
	Additions    int    `json:"additions"`
	Deletions    int    `json:"deletions"`
}

// Issue is a ticket associated with the pull request.
type Issue struct {
	Number      int64  `json:"number"`
	Title       string `json:"title"`
	Description string `json:"description"`
	WebURL      string `json:"web_url"`
}
