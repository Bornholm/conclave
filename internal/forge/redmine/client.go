// Package redmine implements the forge interface for Redmine instances.
package redmine

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

// Client is the Redmine backend.
type Client struct {
	http *httpx.Client
}

// New creates a Redmine client. baseURL is the public instance URL, possibly
// under a sub-path (https://redmine.example.com/redmine). The JSON suffix is
// appended automatically to every API path.
func New(baseURL, token string, hc *http.Client) (*Client, error) {
	base := strings.TrimRight(baseURL, "/")
	var opts []httpx.Option
	if token != "" {
		// Redmine accepts the API key as a header or as a "key" parameter.
		// We use the X-Redmine-API-Key header (available since Redmine 1.1.0).
		opts = append(opts, httpx.WithHeader("X-Redmine-API-Key", token))
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
func (c *Client) Name() string { return "redmine" }

// get performs a GET request and decodes the JSON body into out.
func (c *Client) get(ctx context.Context, path string, q url.Values, out any) (*httpx.Response, error) {
	resp, err := c.http.Do(ctx, http.MethodGet, c.http.Resolve(path+".json", q), nil, "")
	if err != nil {
		if httpx.IsStatus(err, http.StatusNotFound) {
			return nil, fmt.Errorf("%w: %v", forge.ErrNotFound, err)
		}
		return nil, err
	}
	if out != nil {
		if err := json.Unmarshal(resp.Body, out); err != nil {
			return nil, fmt.Errorf("GET %s: unexpected Redmine response: %w", path, err)
		}
	}
	return resp, nil
}

func issuePath(number int64) string {
	return "/issues/" + strconv.FormatInt(number, 10)
}

// GetPullRequest implements forge.Forge.
// Redmine does not have pull requests, so this maps a Redmine issue as a
// pull request surrogate, treating associated changesets as the "diff".
func (c *Client) GetPullRequest(ctx context.Context, repo domain.Repository, number int64) (*domain.PullRequest, error) {
	q := url.Values{"include": {"changesets,journals"}}
	var resp issueResponse
	if _, err := c.get(ctx, issuePath(number), q, &resp); err != nil {
		return nil, err
	}
	return mapIssueToPullRequest(resp.Issue, repo)
}

// ListChangedFiles implements forge.Forge.
// Redmine does not have a direct "changed files" endpoint per issue. We
// derive the list from the changesets attached to the issue.
func (c *Client) ListChangedFiles(ctx context.Context, repo domain.Repository, number int64, maxFiles int) ([]domain.ChangedFile, error) {
	q := url.Values{"include": {"changesets"}}
	var resp issueResponse
	if _, err := c.get(ctx, issuePath(number), q, &resp); err != nil {
		return nil, err
	}
	var out []domain.ChangedFile
	for _, cs := range resp.Issue.Changesets {
		// Changesets don't expose individual file lists in the Redmine API.
		// We record the changeset as a synthetic entry to signal that files
		// were changed, without per-file granularity.
		out = append(out, domain.ChangedFile{
			Path:      fmt.Sprintf("(changeset r%d)", cs.Revision),
			Status:    domain.FileModified,
			Additions: 0,
			Deletions: 0,
		})
		if len(out) > maxFiles {
			return nil, fmt.Errorf("%w (limit %d)", forge.ErrTooManyFiles, maxFiles)
		}
	}
	return out, nil
}

// ListDiscussion implements forge.Forge.
// Redmine issue journals serve as the discussion history. Only journals with
// non-empty notes are included (pure field-change details are skipped).
func (c *Client) ListDiscussion(ctx context.Context, repo domain.Repository, number int64, maxComments int) ([]domain.Comment, error) {
	q := url.Values{"include": {"journals"}}
	var resp issueResponse
	if _, err := c.get(ctx, issuePath(number), q, &resp); err != nil {
		return nil, err
	}
	var out []domain.Comment
	for _, j := range resp.Issue.Journals {
		if strings.TrimSpace(j.Notes) == "" {
			continue
		}
		out = append(out, domain.Comment{
			Kind:      domain.CommentKindComment,
			Author:    j.User.Name,
			CreatedAt: j.CreatedOn,
			Body:      j.Notes,
		})
	}
	return forge.SortAndCap(out, maxComments), nil
}

// ListLabels implements forge.Forge.
// Redmine does not have repository-level labels in the API. Issue statuses
// and trackers act as the taxonomy instead.
func (c *Client) ListLabels(ctx context.Context, repo domain.Repository) ([]domain.Label, error) {
	// Redmine does not have a label system. Statuses and trackers are exposed
	// via separate endpoints, but we return empty to indicate no labels.
	return nil, nil
}

// AddIssueLabels implements forge.Forge.
// Redmine has no label attachment API.
func (c *Client) AddIssueLabels(ctx context.Context, repo domain.Repository, number int64, labels []domain.Label) error {
	return forge.ErrLabelsUnsupported
}

// ListIssues implements forge.Forge.
func (c *Client) ListIssues(ctx context.Context, repo domain.Repository, q forge.IssueQuery) ([]domain.Issue, error) {
	values := url.Values{
		"limit":     {"100"},
		"status_id": {issueStatus(q.State)},
		"sort":      {"updated_on:desc"},
	}
	if len(q.Labels) > 0 {
		// Redmine does not support generic labels. Instead, map to status_id or tracker_id.
		// This is best-effort; we simply ignore unknown labels.
	}
	if !q.Since.IsZero() {
		values.Set("updated_on", ">="+q.Since.UTC().Format(time.RFC3339))
	}
	// Use the project identifier from the repository as the project_id filter.
	if repo.Owner != "" {
		values.Set("project_id", repo.Owner)
	}
	var out []domain.Issue
	offset := 0
	for {
		values.Set("offset", strconv.Itoa(offset))
		var resp issuesResponse
		if _, err := c.get(ctx, "/issues", values, &resp); err != nil {
			return nil, err
		}
		for _, it := range resp.Issues {
			out = append(out, mapIssue(it))
			if q.Limit > 0 && len(out) >= q.Limit {
				return out, nil
			}
		}
		if len(resp.Issues) == 0 || offset+len(resp.Issues) >= resp.TotalCount {
			break
		}
		offset += len(resp.Issues)
	}
	return out, nil
}

// ListIssueComments implements forge.Forge.
func (c *Client) ListIssueComments(ctx context.Context, repo domain.Repository, number int64, maxComments int) ([]domain.Comment, error) {
	return c.ListDiscussion(ctx, repo, number, maxComments)
}

// ListReferences implements forge.Forge, reading changesets attached to the issue.
func (c *Client) ListReferences(ctx context.Context, repo domain.Repository, number int64, max int) ([]domain.Reference, error) {
	q := url.Values{"include": {"changesets"}}
	var resp issueResponse
	if _, err := c.get(ctx, issuePath(number), q, &resp); err != nil {
		if errors.Is(err, forge.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	var out []domain.Reference
	for _, cs := range resp.Issue.Changesets {
		out = append(out, domain.Reference{
			Kind:      domain.ReferenceCommit,
			Ref:       strconv.Itoa(cs.Revision),
			Title:     cs.Comments,
			State:     "referenced",
			Actor:     "",
			WebURL:    "",
			CreatedAt: cs.CommittedOn,
		})
	}
	if max > 0 && len(out) > max {
		out = out[len(out)-max:]
	}
	return out, nil
}

// GetIssue implements forge.Forge.
func (c *Client) GetIssue(ctx context.Context, repo domain.Repository, number int64) (*domain.Issue, error) {
	var resp issueResponse
	if _, err := c.get(ctx, issuePath(number), nil, &resp); err != nil {
		return nil, err
	}
	issue := mapIssue(resp.Issue)
	return &issue, nil
}

func issueStatus(s string) string {
	switch s {
	case forge.StateClosed, forge.StateAll:
		return s
	default:
		return "open"
	}
}
