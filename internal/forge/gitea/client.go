// Package gitea implements the forge interface for Gitea and Forgejo instances.
package gitea

import (
	"bytes"
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

// GetIssue implements forge.Forge.
func (c *Client) GetIssue(ctx context.Context, repo domain.Repository, number int64) (*domain.Issue, error) {
	var raw issue
	if _, err := c.get(ctx, fmt.Sprintf("%s/issues/%d", repoPath(repo), number), nil, &raw); err != nil {
		return nil, err
	}
	if raw.PullRequest != nil {
		return nil, fmt.Errorf("%w: #%d is a pull request", forge.ErrNotFound, number)
	}
	return &domain.Issue{Number: raw.Number, Title: raw.Title, Description: raw.Body, WebURL: raw.HTMLURL}, nil
}
