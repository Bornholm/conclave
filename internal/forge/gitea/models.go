package gitea

type user struct {
	Login string `json:"login"`
}

type repository struct {
	Name     string      `json:"name"`
	FullName string      `json:"full_name"`
	Owner    user        `json:"owner"`
	CloneURL string      `json:"clone_url"`
	Fork     bool        `json:"fork"`
	Parent   *repository `json:"parent"`
}

type ref struct {
	Ref  string      `json:"ref"`
	SHA  string      `json:"sha"`
	Repo *repository `json:"repo"`
}

type pullRequest struct {
	Number  int64   `json:"number"`
	Title   string  `json:"title"`
	Body    string  `json:"body"`
	State   string  `json:"state"`
	HTMLURL string  `json:"html_url"`
	User    user    `json:"user"`
	Base    ref     `json:"base"`
	Head    ref     `json:"head"`
	Labels  []label `json:"labels"`
	Draft   bool    `json:"draft"`
}

type changedFile struct {
	Filename         string `json:"filename"`
	PreviousFilename string `json:"previous_filename"`
	Status           string `json:"status"`
	Additions        int    `json:"additions"`
	Deletions        int    `json:"deletions"`
}

type comment struct {
	ID        int64  `json:"id"`
	Body      string `json:"body"`
	User      user   `json:"user"`
	CreatedAt string `json:"created_at"`
}

type review struct {
	ID            int64  `json:"id"`
	Body          string `json:"body"`
	User          user   `json:"user"`
	State         string `json:"state"`
	SubmittedAt   string `json:"submitted_at"`
	CommentsCount int    `json:"comments_count"`
}

type reviewComment struct {
	ID        int64  `json:"id"`
	Body      string `json:"body"`
	User      user   `json:"user"`
	CreatedAt string `json:"created_at"`
	Path      string `json:"path"`
	Line      int    `json:"line"`
}

type label struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Color       string `json:"color"`
}

type issueListItem struct {
	Number      int64   `json:"number"`
	Title       string  `json:"title"`
	Body        string  `json:"body"`
	State       string  `json:"state"`
	HTMLURL     string  `json:"html_url"`
	User        user    `json:"user"`
	Labels      []label `json:"labels"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
	ClosedAt    string  `json:"closed_at"`
	PullRequest *struct {
		Merged bool `json:"merged"`
	} `json:"pull_request"`
}

// timelineEntry is one entry of a Gitea issue timeline. Gitea reuses the
// comment shape and tells the kind apart with "type".
type timelineEntry struct {
	Type      string `json:"type"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
	User      user   `json:"user"`
	RefIssue  *struct {
		Number      int64  `json:"number"`
		Title       string `json:"title"`
		State       string `json:"state"`
		HTMLURL     string `json:"html_url"`
		PullRequest *struct {
			Merged bool `json:"merged"`
		} `json:"pull_request"`
	} `json:"ref_issue"`
	RefCommitSHA string `json:"ref_commit_sha"`
}
