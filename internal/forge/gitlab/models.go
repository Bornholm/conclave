package gitlab

type user struct {
	Username string `json:"username"`
}

type project struct {
	ID                int64  `json:"id"`
	Name              string `json:"name"`
	PathWithNamespace string `json:"path_with_namespace"`
	HTTPURLToRepo     string `json:"http_url_to_repo"`
	ForkedFromProject *struct {
		PathWithNamespace string `json:"path_with_namespace"`
	} `json:"forked_from_project"`
}

type diffRefs struct {
	BaseSHA  string `json:"base_sha"`
	StartSHA string `json:"start_sha"`
	HeadSHA  string `json:"head_sha"`
}

type mergeRequest struct {
	IID             int64    `json:"iid"`
	Title           string   `json:"title"`
	Description     string   `json:"description"`
	State           string   `json:"state"`
	WebURL          string   `json:"web_url"`
	Author          user     `json:"author"`
	SourceBranch    string   `json:"source_branch"`
	TargetBranch    string   `json:"target_branch"`
	SHA             string   `json:"sha"`
	DiffRefs        diffRefs `json:"diff_refs"`
	SourceProjectID int64    `json:"source_project_id"`
	TargetProjectID int64    `json:"target_project_id"`
	Labels          []string `json:"labels"`
	Draft           bool     `json:"draft"`
}

// mergeRequestDiff is one entry of the MR diff list. GitLab reports no
// per-file line counts: they are counted off the unified diff.
type mergeRequestDiff struct {
	OldPath     string `json:"old_path"`
	NewPath     string `json:"new_path"`
	NewFile     bool   `json:"new_file"`
	RenamedFile bool   `json:"renamed_file"`
	DeletedFile bool   `json:"deleted_file"`
	Diff        string `json:"diff"`
}

type notePosition struct {
	NewPath string `json:"new_path"`
	OldPath string `json:"old_path"`
	NewLine int    `json:"new_line"`
	OldLine int    `json:"old_line"`
}

type note struct {
	ID        int64         `json:"id"`
	Type      string        `json:"type"`
	Body      string        `json:"body"`
	Author    user          `json:"author"`
	CreatedAt string        `json:"created_at"`
	System    bool          `json:"system"`
	Position  *notePosition `json:"position"`
}

type discussion struct {
	ID    string `json:"id"`
	Notes []note `json:"notes"`
}

type approvals struct {
	ApprovedBy []struct {
		User       user   `json:"user"`
		ApprovedAt string `json:"approved_at"`
	} `json:"approved_by"`
}

type label struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Color       string `json:"color"`
}

type issue struct {
	IID         int64    `json:"iid"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	State       string   `json:"state"`
	WebURL      string   `json:"web_url"`
	Author      user     `json:"author"`
	Labels      []string `json:"labels"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
	ClosedAt    string   `json:"closed_at"`
}

// relatedMergeRequest is one entry of an issue's related merge requests: the
// only timeline GitLab exposes that names what acted on an issue.
type relatedMergeRequest struct {
	IID       int64  `json:"iid"`
	Title     string `json:"title"`
	State     string `json:"state"`
	WebURL    string `json:"web_url"`
	Author    user   `json:"author"`
	CreatedAt string `json:"created_at"`
}

type commitStatus struct {
	Name         string `json:"name"`
	Status       string `json:"status"`
	Description  string `json:"description"`
	TargetURL    string `json:"target_url"`
	AllowFailure bool   `json:"allow_failure"`
}
