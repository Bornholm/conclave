// Package gitea implements the forge interface for Gitea and Forgejo instances.
package gitea

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

// Client is the Gitea backend.
type Client struct {
	http *httpx.Client
}

// New creates a Gitea client. baseURL is the public instance URL, possibly
// under a sub-path (https://git.example.com/gitea). The /api/v1 prefix is
// appended unless already present.
func New(baseURL, token string, hc *http.Client) (*Client, error) {
	base := strings.TrimRight(baseURL, "/")
	if !strings.HasSuffix(base, "/api/v1") {
		base += "/api/v1"
	}
	var opts []httpx.Option
	if token != "" {
		opts = append(opts, httpx.WithToken("token", token))
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
func (c *Client) Name() string { return "gitea" }

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) (*httpx.Response, error) {
	resp, err := c.http.Do(ctx, http.MethodGet, c.http.Resolve(path, q), nil, "")
	if err != nil {
		if httpx.IsStatus(err, http.StatusNotFound) {
			return nil, fmt.Errorf("%w: %v", forge.ErrNotFound, err)
		}
		return nil, err
	}
	if out != nil {
		if err := json.Unmarshal(resp.Body, out); err != nil {
			return nil, fmt.Errorf("GET %s: unexpected Gitea response: %w", path, err)
		}
	}
	return resp, nil
}

func repoPath(repo domain.Repository) string {
	return "/repos/" + url.PathEscape(repo.Owner) + "/" + url.PathEscape(repo.Name)
}

// GetPullRequest implements forge.Forge.
func (c *Client) GetPullRequest(ctx context.Context, repo domain.Repository, number int64) (*domain.PullRequest, error) {
	var raw pullRequest
	if _, err := c.get(ctx, fmt.Sprintf("%s/pulls/%d", repoPath(repo), number), nil, &raw); err != nil {
		return nil, err
	}
	return mapPullRequest(raw)
}

// ListChangedFiles implements forge.Forge. Gitea paginates with page/limit
// and signals continuation with the X-HasMore header.
func (c *Client) ListChangedFiles(ctx context.Context, repo domain.Repository, number int64, maxFiles int) ([]domain.ChangedFile, error) {
	path := fmt.Sprintf("%s/pulls/%d/files", repoPath(repo), number)
	var out []domain.ChangedFile
	for page := 1; ; page++ {
		var raw []changedFile
		q := url.Values{"page": {strconv.Itoa(page)}, "limit": {"50"}}
		resp, err := c.get(ctx, path, q, &raw)
		if err != nil {
			return nil, err
		}
		for _, f := range raw {
			out = append(out, mapFile(f))
		}
		if len(out) > maxFiles {
			return nil, fmt.Errorf("%w (limit %d)", forge.ErrTooManyFiles, maxFiles)
		}
		if len(raw) == 0 || !strings.EqualFold(resp.Header.Get("X-HasMore"), "true") {
			break
		}
	}
	return out, nil
}

// ListDiscussion implements forge.Forge.
func (c *Client) ListDiscussion(ctx context.Context, repo domain.Repository, number int64, maxComments int) ([]domain.Comment, error) {
	var out []domain.Comment
	var comments []comment
	if err := c.getAll(ctx, fmt.Sprintf("%s/issues/%d/comments", repoPath(repo), number), &comments); err != nil {
		return nil, err
	}
	for _, cm := range comments {
		out = append(out, domain.Comment{Kind: domain.CommentKindComment, Author: cm.User.Login, CreatedAt: cm.CreatedAt, Body: cm.Body})
	}
	var reviews []review
	if err := c.getAll(ctx, fmt.Sprintf("%s/pulls/%d/reviews", repoPath(repo), number), &reviews); err != nil {
		return nil, err
	}
	for _, rv := range reviews {
		state := strings.ToLower(strings.TrimPrefix(rv.State, "REQUEST_"))
		if strings.TrimSpace(rv.Body) != "" || (state != "comment" && state != "pending") {
			out = append(out, domain.Comment{Kind: domain.CommentKindReview, Author: rv.User.Login, CreatedAt: rv.SubmittedAt, Body: rv.Body, State: state})
		}
		if rv.CommentsCount == 0 {
			continue
		}
		var inline []reviewComment
		if err := c.getAll(ctx, fmt.Sprintf("%s/pulls/%d/reviews/%d/comments", repoPath(repo), number, rv.ID), &inline); err != nil {
			return nil, err
		}
		for _, ic := range inline {
			out = append(out, domain.Comment{Kind: domain.CommentKindInline, Author: ic.User.Login, CreatedAt: ic.CreatedAt, Body: ic.Body, Path: ic.Path, Line: ic.Line})
		}
	}
	return forge.SortAndCap(out, maxComments), nil
}

// getAll follows page/limit pagination (X-HasMore) and appends every page into out.
func (c *Client) getAll(ctx context.Context, path string, out any) error {
	var pages []json.RawMessage
	for page := 1; ; page++ {
		var raw json.RawMessage
		resp, err := c.get(ctx, path, url.Values{"page": {strconv.Itoa(page)}, "limit": {"50"}}, &raw)
		if err != nil {
			return err
		}
		pages = append(pages, raw)
		if !strings.EqualFold(resp.Header.Get("X-HasMore"), "true") || len(bytes.TrimSpace(raw)) <= 2 {
			break
		}
	}
	return forge.MergeJSONArrays(pages, out)
}

// ListLabels implements forge.Forge. Repository labels come first, then the
// organization labels the repository can also use.
func (c *Client) ListLabels(ctx context.Context, repo domain.Repository) ([]domain.Label, error) {
	var raw []label
	if err := c.getAll(ctx, repoPath(repo)+"/labels", &raw); err != nil {
		return nil, err
	}
	var org []label
	if err := c.getAll(ctx, "/orgs/"+url.PathEscape(repo.Owner)+"/labels", &org); err == nil {
		raw = append(raw, org...)
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

// AddIssueLabels implements forge.Forge. Gitea identifies labels by id, so a
// label conclave never saw in ListLabels cannot be attached.
func (c *Client) AddIssueLabels(ctx context.Context, repo domain.Repository, number int64, labels []domain.Label) error {
	if len(labels) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(labels))
	for _, l := range labels {
		if l.ID == 0 {
			return fmt.Errorf("label %q has no id on this instance", l.Name)
		}
		ids = append(ids, l.ID)
	}
	path := fmt.Sprintf("%s/issues/%d/labels", repoPath(repo), number)
	_, err := c.http.Do(ctx, http.MethodPost, c.http.Resolve(path, nil), map[string]any{"labels": ids}, "")
	return err
}

// ListIssues implements forge.Forge.
func (c *Client) ListIssues(ctx context.Context, repo domain.Repository, q forge.IssueQuery) ([]domain.Issue, error) {
	state := q.State
	switch state {
	case forge.StateClosed, forge.StateAll:
	default:
		state = forge.StateOpen
	}
	var out []domain.Issue
	for page := 1; ; page++ {
		values := url.Values{
			"page": {strconv.Itoa(page)}, "limit": {"50"},
			"type": {"issues"}, "state": {state},
		}
		if len(q.Labels) > 0 {
			values.Set("labels", strings.Join(q.Labels, ","))
		}
		if !q.Since.IsZero() {
			values.Set("since", q.Since.UTC().Format(time.RFC3339))
		}
		var raw []issueListItem
		resp, err := c.get(ctx, repoPath(repo)+"/issues", values, &raw)
		if err != nil {
			return nil, err
		}
		for _, it := range raw {
			if it.PullRequest != nil {
				continue
			}
			out = append(out, mapIssueItem(it))
			if q.Limit > 0 && len(out) >= q.Limit {
				return out, nil
			}
		}
		if len(raw) == 0 || !strings.EqualFold(resp.Header.Get("X-HasMore"), "true") {
			break
		}
	}
	return out, nil
}

// ListIssueComments implements forge.Forge.
func (c *Client) ListIssueComments(ctx context.Context, repo domain.Repository, number int64, maxComments int) ([]domain.Comment, error) {
	var raw []comment
	if err := c.getAll(ctx, fmt.Sprintf("%s/issues/%d/comments", repoPath(repo), number), &raw); err != nil {
		return nil, err
	}
	out := make([]domain.Comment, 0, len(raw))
	for _, cm := range raw {
		out = append(out, domain.Comment{Kind: domain.CommentKindComment, Author: cm.User.Login, CreatedAt: cm.CreatedAt, Body: cm.Body})
	}
	return forge.SortAndCap(out, maxComments), nil
}

// ListReferences implements forge.Forge, reading the issue timeline.
func (c *Client) ListReferences(ctx context.Context, repo domain.Repository, number int64, max int) ([]domain.Reference, error) {
	var raw []timelineEntry
	if err := c.getAll(ctx, fmt.Sprintf("%s/issues/%d/timeline", repoPath(repo), number), &raw); err != nil {
		if errors.Is(err, forge.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	var out []domain.Reference
	for _, ev := range raw {
		switch ev.Type {
		case "commit_ref", "close_commit":
			sha := ev.RefCommitSHA
			if sha == "" {
				sha = strings.TrimSpace(ev.Body)
			}
			if sha == "" {
				continue
			}
			out = append(out, domain.Reference{
				Kind: domain.ReferenceCommit, Ref: sha, State: ev.Type,
				Actor: ev.User.Login, CreatedAt: ev.CreatedAt,
			})
		case "issue_ref", "pull_ref", "ref_issue":
			if ev.RefIssue == nil {
				continue
			}
			ref := domain.Reference{
				Kind: domain.ReferenceIssue, Ref: strconv.FormatInt(ev.RefIssue.Number, 10),
				Title: ev.RefIssue.Title, State: ev.RefIssue.State, WebURL: ev.RefIssue.HTMLURL,
				Actor: ev.User.Login, CreatedAt: ev.CreatedAt,
			}
			if ev.RefIssue.PullRequest != nil {
				ref.Kind = domain.ReferencePullRequest
				if ev.RefIssue.PullRequest.Merged {
					ref.State = "merged"
				}
			}
			out = append(out, ref)
		}
	}
	if max > 0 && len(out) > max {
		out = out[len(out)-max:]
	}
	return out, nil
}

// GetIssue implements forge.Forge.
func (c *Client) GetIssue(ctx context.Context, repo domain.Repository, number int64) (*domain.Issue, error) {
	var raw issueListItem
	if _, err := c.get(ctx, fmt.Sprintf("%s/issues/%d", repoPath(repo), number), nil, &raw); err != nil {
		return nil, err
	}
	if raw.PullRequest != nil {
		return nil, fmt.Errorf("%w: #%d is a pull request", forge.ErrNotFound, number)
	}
	issue := mapIssueItem(raw)
	return &issue, nil
}
