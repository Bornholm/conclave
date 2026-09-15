// Package redmine implements the forge interface for Redmine instances.
package redmine

// issueResponse is the top-level JSON wrapper for a single issue.
type issueResponse struct {
	Issue issue `json:"issue"`
}

// issuesResponse is the top-level JSON wrapper for a list of issues.
type issuesResponse struct {
	Issues     []issue `json:"issues"`
	TotalCount int     `json:"total_count"`
	Offset     int     `json:"offset"`
	Limit      int     `json:"limit"`
}

// issue maps a Redmine issue.
type issue struct {
	ID             int           `json:"id"`
	Project        idName        `json:"project"`
	Tracker        idName        `json:"tracker"`
	Status         idName        `json:"status"`
	Priority       idName        `json:"priority"`
	Author         idName        `json:"author"`
	AssignedTo     *idName       `json:"assigned_to,omitempty"`
	Category       *idName       `json:"category,omitempty"`
	Subject        string        `json:"subject"`
	Description    string        `json:"description"`
	StartDate      string        `json:"start_date,omitempty"`
	DueDate        string        `json:"due_date,omitempty"`
	DoneRatio      int           `json:"done_ratio"`
	EstimatedHours *float64      `json:"estimated_hours,omitempty"`
	CreatedOn      string        `json:"created_on"`
	UpdatedOn      string        `json:"updated_on"`
	ClosedOn       string        `json:"closed_on,omitempty"`
	IsPrivate      bool          `json:"is_private"`
	CustomFields   []customField `json:"custom_fields,omitempty"`
	Journals       []journal     `json:"journals,omitempty"`
	Changesets     []changeset   `json:"changesets,omitempty"`
	Attachments    []attachment  `json:"attachments,omitempty"`
	Relations      []relation    `json:"relations,omitempty"`
	Watchers       []idName      `json:"watchers,omitempty"`
}

type idName struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type customField struct {
	ID       int         `json:"id"`
	Name     string      `json:"name"`
	Value    interface{} `json:"value"`
	Multiple bool        `json:"multiple,omitempty"`
}

type journal struct {
	ID        int      `json:"id"`
	User      idName   `json:"user"`
	Notes     string   `json:"notes"`
	CreatedOn string   `json:"created_on"`
	Details   []detail `json:"details,omitempty"`
}

type detail struct {
	Property string `json:"property"`
	Name     string `json:"name"`
	OldValue string `json:"old_value,omitempty"`
	NewValue string `json:"new_value,omitempty"`
}

type changeset struct {
	Revision    int    `json:"revision"`
	CommittedOn string `json:"committed_on"`
	Comments    string `json:"comments"`
}

type attachment struct {
	ID          int    `json:"id"`
	Filename    string `json:"filename"`
	ContentURL  string `json:"content_url"`
	ContentType string `json:"content_type"`
	CreatedOn   string `json:"created_on"`
	Author      idName `json:"author"`
	FileSize    int    `json:"filesize"`
}

type relation struct {
	ID           int    `json:"id"`
	IssueID      int    `json:"issue_id"`
	IssueToID    int    `json:"issue_to_id"`
	RelationType string `json:"relation_type"`
	Delay        *int   `json:"delay,omitempty"`
}

// projectResponse is the top-level JSON wrapper for a single project.
type projectResponse struct {
	Project project `json:"project"`
}

// project maps a Redmine project.
type project struct {
	ID          int     `json:"id"`
	Name        string  `json:"name"`
	Identifier  string  `json:"identifier"`
	Description string  `json:"description"`
	HomePage    string  `json:"homepage,omitempty"`
	CreatedOn   string  `json:"created_on"`
	UpdatedOn   string  `json:"updated_on"`
	Parent      *idName `json:"parent,omitempty"`
}

// projectsResponse is the top-level JSON wrapper for a list of projects.
type projectsResponse struct {
	Projects   []project `json:"projects"`
	TotalCount int       `json:"total_count"`
	Offset     int       `json:"offset"`
	Limit      int       `json:"limit"`
}
