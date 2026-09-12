// Package github implements the forge interface for GitHub.com and GitHub Enterprise.
package github

import (
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

// APIVersion is the REST API version pinned in every request.
const APIVersion = "2022-11-28"

// GitHubMaxFiles is the hard limit GitHub applies to the files endpoint.
const GitHubMaxFiles = 3000

// Client is the GitHub backend.
type Client struct {
	http *httpx.Client
}

// New creates a GitHub client. baseURL is the API root (https://api.github.com
// or https://ghe.example.com/api/v3).
func New(baseURL, token string, hc *http.Client) (*Client, error) {
	opts := []httpx.Option{
		httpx.WithHeader("X-GitHub-Api-Version", APIVersion),
	}
	if token != "" {
		opts = append(opts, httpx.WithToken("Bearer", token))
	}
	if hc != nil {
		opts = append(opts, httpx.WithHTTPClient(hc))
	}
	c, err := httpx.New(baseURL, opts...)
	if err != nil {
		return nil, err
	}
	return &Client{http: c}, nil
}

// Name implements forge.Forge.
func (c *Client) Name() string { return "github" }

const accept = "application/vnd.github+json"

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) (*httpx.Response, error) {
	resp, err := c.http.Do(ctx, http.MethodGet, c.http.Resolve(path, q), nil, accept)
	if err != nil {
		if httpx.IsStatus(err, http.StatusNotFound) {
			return nil, fmt.Errorf("%w: %v", forge.ErrNotFound, err)
		}
		return nil, err
	}
	if out != nil {
		if err := decode(resp.Body, out); err != nil {
			return nil, fmt.Errorf("GET %s: %w", path, err)
		}
	}
	return resp, nil
}

// GetPullRequest implements forge.Forge.
func (c *Client) GetPullRequest(ctx context.Context, repo domain.Repository, number int64) (*domain.PullRequest, error) {
	var raw pullRequest
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d", url.PathEscape(repo.Owner), url.PathEscape(repo.Name), number)
	if _, err := c.get(ctx, path, nil, &raw); err != nil {
		return nil, err
	}
	return mapPullRequest(raw)
}

// ListChangedFiles implements forge.Forge.
func (c *Client) ListChangedFiles(ctx context.Context, repo domain.Repository, number int64, maxFiles int) ([]domain.ChangedFile, error) {
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/files", url.PathEscape(repo.Owner), url.PathEscape(repo.Name), number)
	next := c.http.Resolve(path, url.Values{"per_page": {"100"}})
	var out []domain.ChangedFile
	for next != "" {
		var page []pullRequestFile
		resp, err := c.http.Do(ctx, http.MethodGet, next, nil, accept)
		if err != nil {
			return nil, err
		}
		if err := decode(resp.Body, &page); err != nil {
			return nil, fmt.Errorf("GET %s: %w", path, err)
		}
		for _, f := range page {
			out = append(out, mapFile(f))
		}
		if len(out) > maxFiles {
			return nil, fmt.Errorf("%w (limit %d)", forge.ErrTooManyFiles, maxFiles)
		}
		next = httpx.NextLink(resp.Header)
	}
	if len(out) >= GitHubMaxFiles {
		return nil, fmt.Errorf("%w: GitHub only exposes the first %d files", forge.ErrTooManyFiles, GitHubMaxFiles)
	}
	return out, nil
}

// ListDiscussion implements forge.Forge. It merges conversation comments,
// review summaries and inline review comments, oldest first.
func (c *Client) ListDiscussion(ctx context.Context, repo domain.Repository, number int64, maxComments int) ([]domain.Comment, error) {
	base := fmt.Sprintf("/repos/%s/%s", url.PathEscape(repo.Owner), url.PathEscape(repo.Name))
	var out []domain.Comment

	var comments []comment
	if err := c.getAll(ctx, fmt.Sprintf("%s/issues/%d/comments", base, number), &comments); err != nil {
		return nil, err
	}
	for _, cm := range comments {
		out = append(out, domain.Comment{Kind: domain.CommentKindComment, Author: cm.User.Login, CreatedAt: cm.CreatedAt, Body: cm.Body})
	}
	var reviews []review
	if err := c.getAll(ctx, fmt.Sprintf("%s/pulls/%d/reviews", base, number), &reviews); err != nil {
		return nil, err
	}
	for _, rv := range reviews {
		if strings.TrimSpace(rv.Body) == "" && rv.State == "COMMENTED" {
			continue // empty container for inline comments
		}
		out = append(out, domain.Comment{Kind: domain.CommentKindReview, Author: rv.User.Login, CreatedAt: rv.SubmittedAt, Body: rv.Body, State: strings.ToLower(rv.State)})
	}
	var inline []reviewComment
	if err := c.getAll(ctx, fmt.Sprintf("%s/pulls/%d/comments", base, number), &inline); err != nil {
		return nil, err
	}
	for _, ic := range inline {
		out = append(out, domain.Comment{Kind: domain.CommentKindInline, Author: ic.User.Login, CreatedAt: ic.CreatedAt, Body: ic.Body, Path: ic.Path, Line: ic.Line})
	}
	return forge.SortAndCap(out, maxComments), nil
}

// getAll follows Link pagination and appends every page into out (a pointer to a slice).
func (c *Client) getAll(ctx context.Context, path string, out any) error {
	next := c.http.Resolve(path, url.Values{"per_page": {"100"}})
	var pages []json.RawMessage
	for next != "" {
		resp, err := c.http.Do(ctx, http.MethodGet, next, nil, accept)
		if err != nil {
			if httpx.IsStatus(err, http.StatusNotFound) {
				return fmt.Errorf("%w: %v", forge.ErrNotFound, err)
			}
			return err
		}
		pages = append(pages, resp.Body)
		next = httpx.NextLink(resp.Header)
	}
	return forge.MergeJSONArrays(pages, out)
}

// ListLabels implements forge.Forge.
func (c *Client) ListLabels(ctx context.Context, repo domain.Repository) ([]domain.Label, error) {
	var raw []label
	path := fmt.Sprintf("/repos/%s/%s/labels", url.PathEscape(repo.Owner), url.PathEscape(repo.Name))
	if err := c.getAll(ctx, path, &raw); err != nil {
		return nil, err
	}
	out := make([]domain.Label, 0, len(raw))
	for _, l := range raw {
		out = append(out, mapLabel(l))
	}
	return out, nil
}

// AddIssueLabels implements forge.Forge. GitHub takes label names and adds
// them to what the issue already carries.
func (c *Client) AddIssueLabels(ctx context.Context, repo domain.Repository, number int64, labels []domain.Label) error {
	if len(labels) == 0 {
		return nil
	}
	names := make([]string, len(labels))
	for i, l := range labels {
		names[i] = l.Name
	}
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/labels", url.PathEscape(repo.Owner), url.PathEscape(repo.Name), number)
	_, err := c.http.Do(ctx, http.MethodPost, c.http.Resolve(path, nil), map[string]any{"labels": names}, accept)
	return err
}

// ListIssues implements forge.Forge. GitHub returns pull requests from the
// issues endpoint, so entries carrying a pull_request object are skipped.
func (c *Client) ListIssues(ctx context.Context, repo domain.Repository, q forge.IssueQuery) ([]domain.Issue, error) {
	values := url.Values{"per_page": {"100"}, "state": {issueState(q.State)}}
	if len(q.Labels) > 0 {
		values.Set("labels", strings.Join(q.Labels, ","))
	}
	if !q.Since.IsZero() {
		values.Set("since", q.Since.UTC().Format(time.RFC3339))
	}
	path := fmt.Sprintf("/repos/%s/%s/issues", url.PathEscape(repo.Owner), url.PathEscape(repo.Name))
	next := c.http.Resolve(path, values)
	var out []domain.Issue
	for next != "" {
		resp, err := c.http.Do(ctx, http.MethodGet, next, nil, accept)
		if err != nil {
			return nil, err
		}
		var page []issueListItem
		if err := decode(resp.Body, &page); err != nil {
			return nil, fmt.Errorf("GET %s: %w", path, err)
		}
		for _, it := range page {
			if it.PullRequest != nil {
				continue
			}
			out = append(out, mapIssueItem(it))
			if q.Limit > 0 && len(out) >= q.Limit {
				return out, nil
			}
		}
		next = httpx.NextLink(resp.Header)
	}
	return out, nil
}

// ListIssueComments implements forge.Forge.
func (c *Client) ListIssueComments(ctx context.Context, repo domain.Repository, number int64, maxComments int) ([]domain.Comment, error) {
	var raw []comment
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments", url.PathEscape(repo.Owner), url.PathEscape(repo.Name), number)
	if err := c.getAll(ctx, path, &raw); err != nil {
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
	var raw []timelineEvent
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/timeline", url.PathEscape(repo.Owner), url.PathEscape(repo.Name), number)
	if err := c.getAll(ctx, path, &raw); err != nil {
		if errors.Is(err, forge.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	var out []domain.Reference
	for _, ev := range raw {
		switch ev.Event {
		case "cross-referenced":
			if ev.Source == nil || ev.Source.Issue == nil {
				continue
			}
			src := ev.Source.Issue
			ref := domain.Reference{
				Kind: domain.ReferenceIssue, Ref: strconv.FormatInt(src.Number, 10),
				Title: src.Title, State: src.State, WebURL: src.HTMLURL,
				Actor: ev.Actor.Login, CreatedAt: ev.CreatedAt,
			}
			if src.PullRequest != nil {
				ref.Kind = domain.ReferencePullRequest
				if src.PullRequest.MergedAt != "" {
					ref.State = "merged"
				}
			}
			out = append(out, ref)
		case "referenced", "closed":
			if ev.CommitID == "" {
				continue
			}
			out = append(out, domain.Reference{
				Kind: domain.ReferenceCommit, Ref: ev.CommitID, State: ev.Event,
				WebURL: ev.CommitURL, Actor: ev.Actor.Login, CreatedAt: ev.CreatedAt,
			})
		}
	}
	if max > 0 && len(out) > max {
		out = out[len(out)-max:]
	}
	return out, nil
}

func issueState(s string) string {
	switch s {
	case forge.StateClosed, forge.StateAll:
		return s
	default:
		return forge.StateOpen
	}
}

// GetIssue implements forge.Forge.
func (c *Client) GetIssue(ctx context.Context, repo domain.Repository, number int64) (*domain.Issue, error) {
	var raw issueListItem
	path := fmt.Sprintf("/repos/%s/%s/issues/%d", url.PathEscape(repo.Owner), url.PathEscape(repo.Name), number)
	if _, err := c.get(ctx, path, nil, &raw); err != nil {
		return nil, err
	}
	if raw.PullRequest != nil {
		return nil, fmt.Errorf("%w: #%s is a pull request", forge.ErrNotFound, strconv.FormatInt(number, 10))
	}
	issue := mapIssueItem(raw)
	return &issue, nil
}
