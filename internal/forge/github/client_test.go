package github

import (
	"context"
	"errors"
	"fmt"
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
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "github", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func newServer(t *testing.T) (*httptest.Server, *Client) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/repos/acme/proj/pulls/123", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" || r.Header.Get("X-GitHub-Api-Version") != APIVersion {
			w.WriteHeader(401)
			w.Write([]byte(`{"message":"Bad credentials"}`))
			return
		}
		w.Write(fixture(t, "get-pr.json"))
	})
	mux.HandleFunc("/api/v3/repos/acme/proj/pulls/123/files", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("page") {
		case "", "1":
			w.Header().Set("Link", fmt.Sprintf(`<http://%s/api/v3/repos/acme/proj/pulls/123/files?page=2&per_page=100>; rel="next"`, r.Host))
			w.Write(fixture(t, "list-files-page-1.json"))
		case "2":
			w.Write(fixture(t, "list-files-page-2.json"))
		}
	})
	mux.HandleFunc("/api/v3/repos/acme/proj/issues/45", func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixture(t, "get-issue.json"))
	})
	mux.HandleFunc("/api/v3/repos/acme/proj/issues/123/comments", func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixture(t, "comments.json"))
	})
	mux.HandleFunc("/api/v3/repos/acme/proj/pulls/123/reviews", func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixture(t, "reviews.json"))
	})
	mux.HandleFunc("/api/v3/repos/acme/proj/pulls/123/comments", func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixture(t, "review-comments.json"))
	})
	mux.HandleFunc("/api/v3/repos/acme/proj/issues/47", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"number":47,"title":"Follow-up","body":"later","html_url":"https://github.com/acme/proj/issues/47"}`))
	})
	mux.HandleFunc("/api/v3/repos/acme/proj/issues/46", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte(`{"message":"Not Found"}`))
	})
	mux.HandleFunc("/api/v3/repos/bob/proj", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"name":"proj","full_name":"bob/proj","owner":{"login":"bob"},"fork":true,"parent":{"name":"proj","full_name":"acme/proj","owner":{"login":"acme"}}}`))
	})
	mux.HandleFunc("/api/v3/repos/acme/proj", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"name":"proj","full_name":"acme/proj","owner":{"login":"acme"},"fork":false}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL+"/api/v3", "tok", nil)
	if err != nil {
		t.Fatal(err)
	}
	return srv, c
}

func TestGetPullRequestFork(t *testing.T) {
	_, c := newServer(t)
	repo := domain.Repository{Owner: "acme", Name: "proj"}
	pr, err := c.GetPullRequest(context.Background(), repo, 123)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Author != "alice" || pr.Base.SHA[:4] != "aaaa" || pr.Head.SHA[:4] != "bbbb" || !pr.Head.IsFork || pr.Head.CloneURL != "https://github.com/bob/proj.git" {
		t.Errorf("mapping: %+v", pr)
	}
	if pr.Labels[0] != "bug" || pr.Base.Branch != "main" {
		t.Errorf("labels/branch: %+v", pr)
	}
	issues, err := forge.LoadAssociatedIssues(context.Background(), c, repo, pr, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || issues[0].Number != 45 {
		t.Errorf("issues: %+v", issues)
	}
	// Issues referenced from the discussion are loaded too, capped by maxIssues.
	pr.Discussion, err = c.ListDiscussion(context.Background(), repo, 123, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(pr.Discussion) != 3 || pr.Discussion[0].Kind != domain.CommentKindComment || pr.Discussion[1].Kind != domain.CommentKindInline || pr.Discussion[2].State != "changes_requested" {
		t.Errorf("discussion: %+v", pr.Discussion)
	}
	issues, _ = forge.LoadAssociatedIssues(context.Background(), c, repo, pr, 10)
	if len(issues) != 2 || issues[1].Number != 47 {
		t.Errorf("issues with discussion: %+v", issues)
	}
	if issues, _ = forge.LoadAssociatedIssues(context.Background(), c, repo, pr, 1); len(issues) != 1 {
		t.Errorf("max_issues not honored: %+v", issues)
	}
	if d, _ := c.ListDiscussion(context.Background(), repo, 123, 1); len(d) != 1 || d[0].Kind != domain.CommentKindReview {
		t.Errorf("max_comments should keep the most recent: %+v", d)
	}
}

func TestListChangedFilesPaginated(t *testing.T) {
	_, c := newServer(t)
	files, err := c.ListChangedFiles(context.Background(), domain.Repository{Owner: "acme", Name: "proj"}, 123, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 || files[1].Status != domain.FileRenamed || files[1].PreviousPath != "internal/session/old.go" || files[2].Status != domain.FileDeleted {
		t.Errorf("files: %+v", files)
	}
	_, err = c.ListChangedFiles(context.Background(), domain.Repository{Owner: "acme", Name: "proj"}, 123, 2)
	if !errors.Is(err, forge.ErrTooManyFiles) {
		t.Errorf("expected ErrTooManyFiles, got %v", err)
	}
}

func TestBadCredentials(t *testing.T) {
	srv, _ := newServer(t)
	c, _ := New(srv.URL+"/api/v3", "wrong", nil)
	_, err := c.GetPullRequest(context.Background(), domain.Repository{Owner: "acme", Name: "proj"}, 123)
	if err == nil || !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "wrong") {
		t.Errorf("unexpected: %v", err)
	}
}

func TestParentRepository(t *testing.T) {
	_, c := newServer(t)
	ctx := context.Background()
	parent, err := c.ParentRepository(ctx, domain.Repository{Host: "ghe.example.com", Owner: "bob", Name: "proj"})
	if err != nil {
		t.Fatal(err)
	}
	if parent.Owner != "acme" || parent.Name != "proj" || parent.Host != "ghe.example.com" {
		t.Errorf("parent: %+v", parent)
	}
	if _, err := c.ParentRepository(ctx, domain.Repository{Owner: "acme", Name: "proj"}); !errors.Is(err, forge.ErrNotFork) {
		t.Errorf("expected ErrNotFork, got %v", err)
	}
}
