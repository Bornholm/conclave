// Package gitlab implements the forge interface for GitLab instances.
package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bornholm/conclave/internal/domain"
	"github.com/bornholm/conclave/internal/forge"
	"github.com/bornholm/conclave/internal/forge/httpx"
)

// pageSize is asked of every paginated endpoint.
const pageSize = 50

// maxPages bounds the pages read from one endpoint: a server that keeps
// announcing a next page must not keep the review paging.
const maxPages = 50

// Client is the GitLab backend.
type Client struct {
	http *httpx.Client
}

// New creates a GitLab client. baseURL is the public instance URL, possibly
// under a sub-path (https://git.example.com/gitlab). The /api/v4 prefix is
// appended unless already present.
func New(baseURL, token string, hc *http.Client) (*Client, error) {
	base := strings.TrimRight(baseURL, "/")
	if !strings.HasSuffix(base, "/api/v4") {
		base += "/api/v4"
	}
	var opts []httpx.Option
	if token != "" {
		// GitLab accepts a personal, project or group access token as a
		// bearer token. httpx only sends it to the instance host.
		opts = append(opts, httpx.WithToken("Bearer", token))
	}
	if hc != nil {
		opts = append(opts, httpx.WithHTTPClient(hc))
	}
	c, err := httpx.New(base, opts...)
	if err != nil {
		return nil, err
	}
	return &Client{http: c}, nil
}

// Name implements forge.Forge.
func (c *Client) Name() string { return "gitlab" }

// projectPath is the API path of the project, identified by its namespaced
// path encoded as a single segment ("group%2Fproject"), as GitLab expects.
//
// ponytail: gitrepo.ParseRemoteURL keeps the last two segments of the remote
// path, so a project in a nested subgroup resolves to the wrong namespace.
// Flat group/project works; deeper paths need the parser to know where the
// instance root ends.
func projectPath(repo domain.Repository) string {
	return "/projects/" + url.PathEscape(repo.Owner+"/"+repo.Name)
}

// resolve builds the absolute URL of an already-escaped path. It does not go
// through httpx.Resolve: that one assigns to url.URL.Path, which re-escapes
// the "%2F" of the project path into "%252F".
func (c *Client) resolve(path string, q url.Values) string {
	u := c.http.BaseURL().String() + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

func (c *Client) do(ctx context.Context, method, path string, q url.Values, body any, out any) (*httpx.Response, error) {
	resp, err := c.http.Do(ctx, method, c.resolve(path, q), body, "")
	if err != nil {
		if httpx.IsStatus(err, http.StatusNotFound) {
			return nil, fmt.Errorf("%w: %v", forge.ErrNotFound, err)
		}
		return nil, err
	}
	if out != nil {
		if err := json.Unmarshal(resp.Body, out); err != nil {
			return nil, fmt.Errorf("%s %s: unexpected GitLab response: %w", method, path, err)
		}
	}
	return resp, nil
}

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) (*httpx.Response, error) {
	return c.do(ctx, http.MethodGet, path, q, nil, out)
}

// getAll follows GitLab's offset pagination (X-Next-Page) and decodes every
// page into out, a pointer to a slice. extra carries the endpoint's own
// query parameters.
func (c *Client) getAll(ctx context.Context, path string, extra url.Values, out any) error {
	var pages []json.RawMessage
	for page := 1; page <= maxPages; page++ {
		q := url.Values{"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(pageSize)}}
		for k, vs := range extra {
			q[k] = vs
		}
		var raw json.RawMessage
		resp, err := c.get(ctx, path, q, &raw)
		if err != nil {
			return err
		}
		pages = append(pages, raw)
		if strings.TrimSpace(resp.Header.Get("X-Next-Page")) == "" {
			break
		}
	}
	return forge.MergeJSONArrays(pages, out)
}

// GetPullRequest implements forge.Forge. The number is the merge request's
// iid, the one the web UI shows, never GitLab's instance-wide id.
func (c *Client) GetPullRequest(ctx context.Context, repo domain.Repository, number int64) (*domain.PullRequest, error) {
	var raw mergeRequest
	if _, err := c.get(ctx, fmt.Sprintf("%s/merge_requests/%d", projectPath(repo), number), nil, &raw); err != nil {
		return nil, err
	}
	return mapMergeRequest(raw, repo)
}

// ListChangedFiles implements forge.Forge.
func (c *Client) ListChangedFiles(ctx context.Context, repo domain.Repository, number int64, maxFiles int) ([]domain.ChangedFile, error) {
	path := fmt.Sprintf("%s/merge_requests/%d/diffs", projectPath(repo), number)
	var out []domain.ChangedFile
	for page := 1; page <= maxPages; page++ {
		var raw []mergeRequestDiff
		q := url.Values{"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(pageSize)}}
		resp, err := c.get(ctx, path, q, &raw)
		if err != nil {
			return nil, err
		}
		for _, d := range raw {
			out = append(out, mapDiff(d))
		}
		if len(out) > maxFiles {
			return nil, fmt.Errorf("%w (limit %d)", forge.ErrTooManyFiles, maxFiles)
		}
		if len(raw) == 0 || strings.TrimSpace(resp.Header.Get("X-Next-Page")) == "" {
			break
		}
	}
	return out, nil
}

// ListDiscussion implements forge.Forge. GitLab nests the notes inside
// discussions; the system notes are the activity log, not the conversation.
// GitLab has no review object, so the only review-like entries are the
// approvals.
func (c *Client) ListDiscussion(ctx context.Context, repo domain.Repository, number int64, maxComments int) ([]domain.Comment, error) {
	var raw []discussion
	if err := c.getAll(ctx, fmt.Sprintf("%s/merge_requests/%d/discussions", projectPath(repo), number), nil, &raw); err != nil {
		return nil, err
	}
	var out []domain.Comment
	for _, d := range raw {
		for _, n := range d.Notes {
			if n.System {
				continue
			}
			out = append(out, mapNote(n))
		}
	}
	var app approvals
	if _, err := c.get(ctx, fmt.Sprintf("%s/merge_requests/%d/approvals", projectPath(repo), number), nil, &app); err == nil {
		for _, a := range app.ApprovedBy {
			out = append(out, domain.Comment{
				Kind: domain.CommentKindReview, Author: a.User.Username,
				CreatedAt: a.ApprovedAt, State: "approved",
			})
		}
	}
	return forge.SortAndCap(out, maxComments), nil
}

// ParentRepository implements forge.ForkResolver.
func (c *Client) ParentRepository(ctx context.Context, repo domain.Repository) (domain.Repository, error) {
	var raw project
	if _, err := c.get(ctx, projectPath(repo), nil, &raw); err != nil {
		return domain.Repository{}, err
	}
	if raw.ForkedFromProject == nil {
		return domain.Repository{}, forge.ErrNotFork
	}
	owner, name, ok := splitPath(raw.ForkedFromProject.PathWithNamespace)
	if !ok {
		return domain.Repository{}, forge.ErrNotFork
	}
	return domain.Repository{Host: repo.Host, Owner: owner, Name: name}, nil
}

// splitPath cuts a namespaced project path into its namespace and its name.
func splitPath(p string) (owner, name string, ok bool) {
	i := strings.LastIndex(p, "/")
	if i <= 0 || i == len(p)-1 {
		return "", "", false
	}
	return p[:i], p[i+1:], true
}

// ListLabels implements forge.Forge. The project labels endpoint already
// includes the labels its ancestor groups define.
func (c *Client) ListLabels(ctx context.Context, repo domain.Repository) ([]domain.Label, error) {
	var raw []label
	if err := c.getAll(ctx, projectPath(repo)+"/labels", nil, &raw); err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(raw))
	out := make([]domain.Label, 0, len(raw))
	for _, l := range raw {
		if seen[l.Name] {
			continue
		}
		seen[l.Name] = true
		out = append(out, domain.Label{ID: l.ID, Name: l.Name, Description: l.Description, Color: l.Color})
	}
	return out, nil
}

// AddIssueLabels implements forge.Forge. GitLab attaches labels by name and
// add_labels only ever adds, so a label conclave never listed still works.
func (c *Client) AddIssueLabels(ctx context.Context, repo domain.Repository, number int64, labels []domain.Label) error {
	names := labelNames(labels)
	if names == "" {
		return nil
	}
	path := fmt.Sprintf("%s/issues/%d", projectPath(repo), number)
	_, err := c.do(ctx, http.MethodPut, path, nil, map[string]any{"add_labels": names}, nil)
	return err
}

func labelNames(labels []domain.Label) string {
	names := make([]string, 0, len(labels))
	for _, l := range labels {
		// A comma would split the name in two: GitLab forbids one in a
		// label name, so such a name is not a label of this instance.
		if n := strings.TrimSpace(l.Name); n != "" && !strings.Contains(n, ",") {
			names = append(names, n)
		}
	}
	return strings.Join(names, ",")
}

// AddPullRequestLabel implements forge.PullRequestLabeler.
func (c *Client) AddPullRequestLabel(ctx context.Context, repo domain.Repository, number int64, name string) error {
	return c.updateMergeRequestLabels(ctx, repo, number, "add_labels", name)
}

// RemovePullRequestLabel implements forge.PullRequestLabeler. A label the
// merge request does not carry is not an error for GitLab either.
func (c *Client) RemovePullRequestLabel(ctx context.Context, repo domain.Repository, number int64, name string) error {
	return c.updateMergeRequestLabels(ctx, repo, number, "remove_labels", name)
}

func (c *Client) updateMergeRequestLabels(ctx context.Context, repo domain.Repository, number int64, field, name string) error {
	name = strings.TrimSpace(name)
	if name == "" || strings.Contains(name, ",") {
		return fmt.Errorf("invalid label name %q", name)
	}
	path := fmt.Sprintf("%s/merge_requests/%d", projectPath(repo), number)
	_, err := c.do(ctx, http.MethodPut, path, nil, map[string]any{field: name}, nil)
	return err
}

// ListIssues implements forge.Forge.
func (c *Client) ListIssues(ctx context.Context, repo domain.Repository, q forge.IssueQuery) ([]domain.Issue, error) {
	state := "opened"
	switch q.State {
	case forge.StateClosed:
		state = "closed"
	case forge.StateAll:
		state = "all"
	}
	extra := url.Values{"state": {state}}
	if len(q.Labels) > 0 {
		extra.Set("labels", strings.Join(q.Labels, ","))
	}
	if !q.Since.IsZero() {
		extra.Set("updated_after", q.Since.UTC().Format(time.RFC3339))
	}
	var raw []issue
	if err := c.getAll(ctx, projectPath(repo)+"/issues", extra, &raw); err != nil {
		return nil, err
	}
	out := make([]domain.Issue, 0, len(raw))
	for _, it := range raw {
		out = append(out, mapIssue(it))
		if q.Limit > 0 && len(out) >= q.Limit {
			break
		}
	}
	return out, nil
}

// GetIssue implements forge.Forge.
func (c *Client) GetIssue(ctx context.Context, repo domain.Repository, number int64) (*domain.Issue, error) {
	var raw issue
	if _, err := c.get(ctx, fmt.Sprintf("%s/issues/%d", projectPath(repo), number), nil, &raw); err != nil {
		return nil, err
	}
	out := mapIssue(raw)
	return &out, nil
}

// ListIssueComments implements forge.Forge.
func (c *Client) ListIssueComments(ctx context.Context, repo domain.Repository, number int64, maxComments int) ([]domain.Comment, error) {
	var raw []note
	if err := c.getAll(ctx, fmt.Sprintf("%s/issues/%d/notes", projectPath(repo), number), nil, &raw); err != nil {
		return nil, err
	}
	out := make([]domain.Comment, 0, len(raw))
	for _, n := range raw {
		if n.System {
			continue
		}
		cm := mapNote(n)
		cm.Kind = domain.CommentKindComment
		out = append(out, cm)
	}
	return forge.SortAndCap(out, maxComments), nil
}

// ListReferences implements forge.Forge. GitLab exposes no issue timeline,
// so the merge requests that mention the issue are all there is; the
// contract allows a forge to return nothing.
func (c *Client) ListReferences(ctx context.Context, repo domain.Repository, number int64, max int) ([]domain.Reference, error) {
	var raw []relatedMergeRequest
	path := fmt.Sprintf("%s/issues/%d/related_merge_requests", projectPath(repo), number)
	if err := c.getAll(ctx, path, nil, &raw); err != nil {
		// Best-effort: an instance that hides the endpoint leaves the
		// triage without references rather than without a result.
		return nil, nil
	}
	out := make([]domain.Reference, 0, len(raw))
	for _, mr := range raw {
		out = append(out, domain.Reference{
			Kind: domain.ReferencePullRequest, Ref: strconv.FormatInt(mr.IID, 10),
			Title: mr.Title, State: mapState(mr.State), WebURL: mr.WebURL,
			Actor: mr.Author.Username, CreatedAt: mr.CreatedAt,
		})
	}
	if max > 0 && len(out) > max {
		out = out[len(out)-max:]
	}
	return out, nil
}
