package gitea

import (
	"context"
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
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "gitea", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func newServer(t *testing.T, prefix string) *httptest.Server {
	mux := http.NewServeMux()
	auth := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") != "token tok" {
			w.WriteHeader(401)
			w.Write([]byte(`{"message":"token is required"}`))
			return false
		}
		return true
	}
	mux.HandleFunc(prefix+"/api/v1/repos/acme/proj/pulls/42", func(w http.ResponseWriter, r *http.Request) {
		if auth(w, r) {
			w.Write(fixture(t, "get-pr.json"))
		}
	})
	mux.HandleFunc(prefix+"/api/v1/repos/acme/proj/pulls/42/files", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		switch r.URL.Query().Get("page") {
		case "1":
			w.Header().Set("X-HasMore", "true")
			w.Write(fixture(t, "list-files-page-1.json"))
		case "2":
			w.Header().Set("X-HasMore", "false")
			w.Write(fixture(t, "list-files-page-2.json"))
		default:
			w.WriteHeader(500)
		}
	})
	mux.HandleFunc(prefix+"/api/v1/repos/acme/proj/issues/42/comments", func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixture(t, "comments.json"))
	})
	mux.HandleFunc(prefix+"/api/v1/repos/acme/proj/pulls/42/reviews", func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixture(t, "reviews.json"))
	})
	mux.HandleFunc(prefix+"/api/v1/repos/acme/proj/pulls/42/reviews/5/comments", func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixture(t, "review-comments.json"))
	})
	mux.HandleFunc(prefix+"/api/v1/repos/acme/proj/issues/8", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"number":8,"title":"Related","body":"x","html_url":"https://git.example.com/acme/proj/issues/8"}`))
	})
	mux.HandleFunc(prefix+"/api/v1/repos/acme/proj/issues/7", func(w http.ResponseWriter, r *http.Request) {
		if auth(w, r) {
			w.Write(fixture(t, "get-issue.json"))
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestGiteaFlow(t *testing.T) {
	for _, prefix := range []string{"", "/gitea"} {
		t.Run("prefix"+prefix, func(t *testing.T) {
			srv := newServer(t, prefix)
			c, err := New(srv.URL+prefix+"/", "tok", nil)
			if err != nil {
				t.Fatal(err)
			}
			repo := domain.Repository{Owner: "acme", Name: "proj"}
			pr, err := c.GetPullRequest(context.Background(), repo, 42)
			if err != nil {
				t.Fatal(err)
			}
			if pr.Author != "carol" || pr.Head.IsFork || pr.Head.SHA[:4] != "dddd" {
				t.Errorf("pr: %+v", pr)
			}
			files, err := c.ListChangedFiles(context.Background(), repo, 42, 100)
			if err != nil {
				t.Fatal(err)
			}
			if len(files) != 2 || files[1].Status != domain.FileDeleted {
				t.Errorf("files: %+v", files)
			}
			issues, err := forge.LoadAssociatedIssues(context.Background(), c, repo, pr, 10)
			if err != nil || len(issues) != 1 || issues[0].Title != "Need retries" {
				t.Errorf("issues: %v %+v", err, issues)
			}
			d, err := c.ListDiscussion(context.Background(), repo, 42, 100)
			if err != nil || len(d) != 3 || d[0].Kind != domain.CommentKindComment || d[1].State != "changes" || d[2].Path != "retry.go" {
				t.Errorf("discussion: %v %+v", err, d)
			}
			pr.Discussion = d
			if issues, _ = forge.LoadAssociatedIssues(context.Background(), c, repo, pr, 10); len(issues) != 2 || issues[1].Number != 8 {
				t.Errorf("issues from discussion: %+v", issues)
			}
		})
	}
}

func TestGiteaBadAuth(t *testing.T) {
	srv := newServer(t, "")
	c, _ := New(srv.URL, "bad", nil)
	_, err := c.GetPullRequest(context.Background(), domain.Repository{Owner: "acme", Name: "proj"}, 42)
	if err == nil || !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "bad") {
		t.Errorf("unexpected: %v", err)
	}
}

func TestGiteaIncompatibleResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html>login</html>`))
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "tok", nil)
	_, err := c.GetPullRequest(context.Background(), domain.Repository{Owner: "acme", Name: "proj"}, 42)
	if err == nil || !strings.Contains(err.Error(), "unexpected Gitea response") {
		t.Errorf("unexpected: %v", err)
	}
}
