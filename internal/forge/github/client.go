// Package github implements the forge interface for GitHub.com and GitHub Enterprise.
package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

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

// GetIssue implements forge.Forge.
func (c *Client) GetIssue(ctx context.Context, repo domain.Repository, number int64) (*domain.Issue, error) {
	var raw issue
	path := fmt.Sprintf("/repos/%s/%s/issues/%d", url.PathEscape(repo.Owner), url.PathEscape(repo.Name), number)
	if _, err := c.get(ctx, path, nil, &raw); err != nil {
		return nil, err
	}
	if raw.PullRequest != nil {
		return nil, fmt.Errorf("%w: #%s is a pull request", forge.ErrNotFound, strconv.FormatInt(number, 10))
	}
	return &domain.Issue{Number: raw.Number, Title: raw.Title, Description: raw.Body, WebURL: raw.HTMLURL}, nil
}
