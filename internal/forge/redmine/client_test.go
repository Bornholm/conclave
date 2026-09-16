package redmine

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bornholm/conclave/internal/domain"
	"github.com/bornholm/conclave/internal/forge"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "redmine", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func newServer(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	auth := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("X-Redmine-API-Key") != "tok" {
			w.WriteHeader(401)
			w.Write([]byte(`{"errors":["API access key is invalid or missing"]}`))
			return false
		}
		return true
	}
	mux.HandleFunc("GET /issues/42.json", func(w http.ResponseWriter, r *http.Request) {
		if auth(w, r) {
			w.Write(fixture(t, "get-issue-42.json"))
		}
	})
	mux.HandleFunc("GET /issues/7.json", func(w http.ResponseWriter, r *http.Request) {
		if auth(w, r) {
			w.Write([]byte(`{
				"issue": {
					"id": 7,
					"project": {"id": 1, "name": "ACME Project"},
					"tracker": {"id": 2, "name": "Bug"},
					"status": {"id": 5, "name": "Closed"},
					"priority": {"id": 4, "name": "Normal"},
					"author": {"id": 5, "name": "alice"},
					"subject": "Need retries",
					"description": "Transient failures cause crashes",
					"created_on": "2025-01-10T08:00:00Z",
					"updated_on": "2025-01-12T14:00:00Z",
					"closed_on": "2025-01-12T14:00:00Z",
					"journals": [],
					"changesets": []
				}
			}`))
		}
	})
	mux.HandleFunc("GET /issues.json", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		// Return paginated list
		offset := r.URL.Query().Get("offset")
		if offset == "0" {
			w.Write([]byte(`{
				"issues": [
					{"id": 7, "subject": "Need retries", "tracker": {"id": 2, "name": "Bug"}, "status": {"id": 5, "name": "Closed"}, "author": {"id": 5, "name": "alice"}, "project": {"id": 1, "name": "ACME"}, "created_on": "2025-01-10T08:00:00Z", "updated_on": "2025-01-12T14:00:00Z"},
					{"id": 8, "subject": "Another issue", "tracker": {"id": 1, "name": "Feature"}, "status": {"id": 1, "name": "New"}, "author": {"id": 6, "name": "bob"}, "project": {"id": 1, "name": "ACME"}, "created_on": "2025-01-14T09:00:00Z", "updated_on": "2025-01-14T09:00:00Z"}
				],
				"total_count": 2,
				"offset": 0,
				"limit": 100
			}`))
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestRedmineGetIssue(t *testing.T) {
	srv := newServer(t)
	c, err := New(srv.URL, "tok", nil)
	if err != nil {
		t.Fatal(err)
	}
	repo := domain.Repository{Owner: "acme", Name: "proj"}
	pr, err := c.GetPullRequest(context.Background(), repo, 42)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Author != "alice" {
		t.Errorf("author: got %q, want alice", pr.Author)
	}
	if pr.Title != "Add retry logic" {
		t.Errorf("title: got %q", pr.Title)
	}
	if pr.Number != 42 {
		t.Errorf("number: got %d, want 42", pr.Number)
	}
	if len(pr.Labels) == 0 {
		t.Errorf("labels: got %v, want non-empty", pr.Labels)
	}
}

func TestRedmineListChangedFiles(t *testing.T) {
	srv := newServer(t)
	c, _ := New(srv.URL, "tok", nil)
	repo := domain.Repository{Owner: "acme", Name: "proj"}
	files, err := c.ListChangedFiles(context.Background(), repo, 42, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Errorf("files: got %d, want 2", len(files))
	}
	if !strings.Contains(files[0].Path, "changeset") {
		t.Errorf("path: got %q", files[0].Path)
	}
}

func TestRedmineListDiscussion(t *testing.T) {
	srv := newServer(t)
	c, _ := New(srv.URL, "tok", nil)
	repo := domain.Repository{Owner: "acme", Name: "proj"}
	d, err := c.ListDiscussion(context.Background(), repo, 42, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(d) != 2 {
		t.Errorf("discussion: got %d, want 2", len(d))
	}
	if d[0].Author != "alice" {
		t.Errorf("author: got %q", d[0].Author)
	}
	if d[1].Body != "Please add tests for edge cases." {
		t.Errorf("body: got %q", d[1].Body)
	}
}

func TestRedmineGetIssueByNumber(t *testing.T) {
	srv := newServer(t)
	c, _ := New(srv.URL, "tok", nil)
	repo := domain.Repository{Owner: "acme", Name: "proj"}
	issue, err := c.GetIssue(context.Background(), repo, 7)
	if err != nil {
		t.Fatal(err)
	}
	if issue.Title != "Need retries" {
		t.Errorf("title: got %q", issue.Title)
	}
	if issue.State != "Closed" {
		t.Errorf("state: got %q", issue.State)
	}
}

func TestRedmineListIssues(t *testing.T) {
	srv := newServer(t)
	c, _ := New(srv.URL, "tok", nil)
	repo := domain.Repository{Owner: "acme", Name: "proj"}
	issues, err := c.ListIssues(context.Background(), repo, forge.IssueQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 2 {
		t.Errorf("issues: got %d, want 2", len(issues))
	}
	if issues[0].Number != 7 {
		t.Errorf("first issue number: got %d, want 7", issues[0].Number)
	}
}

func TestRedmineLoadAssociatedIssues(t *testing.T) {
	srv := newServer(t)
	c, _ := New(srv.URL, "tok", nil)
	repo := domain.Repository{Owner: "acme", Name: "proj"}
	pr := &domain.PullRequest{
		Number:      42,
		Description: "Closes #7",
	}
	issues, err := forge.LoadAssociatedIssues(context.Background(), c, repo, pr, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 {
		t.Errorf("issues: got %d, want 1", len(issues))
	}
	if issues[0].Number != 7 {
		t.Errorf("issue number: got %d, want 7", issues[0].Number)
	}
}

func TestRedmineBadAuth(t *testing.T) {
	srv := newServer(t)
	c, _ := New(srv.URL, "bad", nil)
	_, err := c.GetPullRequest(context.Background(), domain.Repository{Owner: "acme", Name: "proj"}, 42)
	if err == nil || !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "bad") {
		t.Errorf("unexpected: %v", err)
	}
}

func TestRedmineNotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /issues/999.json", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte(`{"error": "Not found"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, _ := New(srv.URL, "tok", nil)
	_, err := c.GetPullRequest(context.Background(), domain.Repository{Owner: "acme", Name: "proj"}, 999)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("unexpected: %v", err)
	}
}

func TestRedmineIncompatibleResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html>login</html>`))
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "tok", nil)
	_, err := c.GetPullRequest(context.Background(), domain.Repository{Owner: "acme", Name: "proj"}, 42)
	if err == nil || !strings.Contains(err.Error(), "unexpected Redmine response") {
		t.Errorf("unexpected: %v", err)
	}
}

func TestRedmineListLabelsEmpty(t *testing.T) {
	srv := newServer(t)
	c, _ := New(srv.URL, "tok", nil)
	labels, err := c.ListLabels(context.Background(), domain.Repository{Owner: "acme", Name: "proj"})
	if err != nil {
		t.Fatal(err)
	}
	// Redmine has no label system; returns nil
	if labels != nil {
		t.Errorf("labels: got %v, want nil", labels)
	}
}

func TestRedmineAddIssueLabelsUnsupported(t *testing.T) {
	srv := newServer(t)
	c, _ := New(srv.URL, "tok", nil)
	err := c.AddIssueLabels(context.Background(), domain.Repository{Owner: "acme", Name: "proj"}, 42, []domain.Label{
		{Name: "bug"},
	})
	if !errors.Is(err, forge.ErrLabelsUnsupported) {
		t.Errorf("expected ErrLabelsUnsupported, got %v", err)
	}
}

func TestRedmineListReferences(t *testing.T) {
	srv := newServer(t)
	c, _ := New(srv.URL, "tok", nil)
	refs, err := c.ListReferences(context.Background(), domain.Repository{Owner: "acme", Name: "proj"}, 42, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 {
		t.Errorf("refs: got %d, want 2", len(refs))
	}
	if refs[0].Ref != "1:a1b2c3d4e5f6789012345678901234567890abcd" {
		t.Errorf("first ref: got %q", refs[0].Ref)
	}
	if refs[0].Kind != domain.ReferenceCommit {
		t.Errorf("kind: got %v", refs[0].Kind)
	}
	if refs[0].Title != "Add retry logic" {
		t.Errorf("title: got %q", refs[0].Title)
	}
}
