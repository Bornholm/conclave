package gitlab

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
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "gitlab", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

var testRepo = domain.Repository{Host: "gitlab.example.com", Owner: "acme", Name: "proj"}

// newServer serves the API off the raw, still-escaped path: the project is
// one segment, "acme%2Fproj", and a backend that let it be decoded or
// double-escaped would miss every route here.
func newServer(t *testing.T, prefix string) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	const proj = "/api/v4/projects/acme%2Fproj"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.EscapedPath(), prefix)
		seen = append(seen, path)
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			w.Write([]byte(`{"message":"401 Unauthorized"}`))
			return
		}
		switch {
		case path == proj+"/merge_requests/42":
			if r.Method == http.MethodPut {
				w.Write([]byte(`{"iid":42}`))
				return
			}
			w.Write(fixture(t, "get-mr.json"))
		case path == proj+"/merge_requests/42/diffs":
			if r.URL.Query().Get("page") == "1" {
				w.Header().Set("X-Next-Page", "2")
				w.Write(fixture(t, "list-diffs-page-1.json"))
				return
			}
			w.Write(fixture(t, "list-diffs-page-2.json"))
		case path == proj+"/merge_requests/42/discussions":
			w.Write(fixture(t, "discussions.json"))
		case path == proj+"/merge_requests/42/approvals":
			w.Write(fixture(t, "approvals.json"))
		case path == proj+"/issues/7":
			w.Write(fixture(t, "get-issue.json"))
		case path == proj+"/issues/8":
			w.Write([]byte(`{"iid":8,"title":"Related","description":"x","state":"opened","web_url":"https://gitlab.example.com/acme/proj/-/issues/8"}`))
		case path == proj+"/issues/7/notes":
			w.Write(fixture(t, "notes.json"))
		case path == proj+"/issues/7/related_merge_requests":
			w.Write([]byte(`[{"iid":42,"title":"Add retries","state":"merged","web_url":"https://gitlab.example.com/acme/proj/-/merge_requests/42","author":{"username":"carol"},"created_at":"2024-05-02T08:00:00.000Z"}]`))
		case path == proj+"/issues":
			w.Write([]byte(`[{"iid":7,"title":"Need retries","state":"opened","author":{"username":"dave"},"labels":["bug"]}]`))
		case path == proj+"/labels":
			w.Write([]byte(`[{"id":4,"name":"bug","description":"Something is broken","color":"#d9534f"},{"id":4,"name":"bug"}]`))
		case path == proj:
			w.Write([]byte(`{"id":77,"name":"proj","path_with_namespace":"acme/proj","forked_from_project":{"path_with_namespace":"upstream/proj"}}`))
		case strings.HasPrefix(path, proj+"/repository/commits/"):
			w.Write(fixture(t, "statuses.json"))
		default:
			w.WriteHeader(404)
			w.Write([]byte(`{"message":"404 Not Found"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func newClient(t *testing.T, prefix string) (*Client, *[]string) {
	t.Helper()
	srv, seen := newServer(t, prefix)
	c, err := New(srv.URL+prefix+"/", "tok", nil)
	if err != nil {
		t.Fatal(err)
	}
	return c, seen
}

func TestGitLabFlow(t *testing.T) {
	for _, prefix := range []string{"", "/gitlab"} {
		t.Run("prefix"+prefix, func(t *testing.T) {
			c, seen := newClient(t, prefix)
			ctx := context.Background()

			pr, err := c.GetPullRequest(ctx, testRepo, 42)
			if err != nil {
				t.Fatal(err)
			}
			// The number is the iid, never the instance-wide id (90001).
			if pr.Number != 42 || pr.Author != "carol" || pr.State != "open" {
				t.Errorf("pr: %+v", pr)
			}
			if pr.Head.SHA[:4] != "dddd" || pr.Base.SHA[:4] != "bbbb" || pr.Head.IsFork {
				t.Errorf("refs: %+v %+v", pr.Head, pr.Base)
			}
			if len(pr.Labels) != 1 || pr.Labels[0] != "enhancement" {
				t.Errorf("labels: %+v", pr.Labels)
			}

			files, err := c.ListChangedFiles(ctx, testRepo, 42, 100)
			if err != nil {
				t.Fatal(err)
			}
			if len(files) != 2 {
				t.Fatalf("files: %+v", files)
			}
			if files[0].Path != "retry.go" || files[0].Additions != 2 || files[0].Deletions != 1 {
				t.Errorf("counted off the diff: %+v", files[0])
			}
			if files[1].Status != domain.FileDeleted || files[1].Path != "legacy.go" {
				t.Errorf("deleted file: %+v", files[1])
			}
			if _, err := c.ListChangedFiles(ctx, testRepo, 42, 1); !strings.Contains(err.Error(), "maximum number") {
				t.Errorf("max files: %v", err)
			}

			d, err := c.ListDiscussion(ctx, testRepo, 42, 100)
			if err != nil {
				t.Fatal(err)
			}
			// The system note is left out; the approval comes last.
			if len(d) != 4 {
				t.Fatalf("discussion: %+v", d)
			}
			if d[0].Kind != domain.CommentKindComment || d[0].Author != "dave" {
				t.Errorf("first comment: %+v", d[0])
			}
			if d[1].Kind != domain.CommentKindInline || d[1].Path != "retry.go" || d[1].Line != 12 {
				t.Errorf("inline note: %+v", d[1])
			}
			if d[3].Kind != domain.CommentKindReview || d[3].State != "approved" || d[3].Author != "frank" {
				t.Errorf("approval: %+v", d[3])
			}

			pr.Discussion = d
			issues, err := forge.LoadAssociatedIssues(ctx, c, testRepo, pr, 10)
			if err != nil {
				t.Fatal(err)
			}
			if len(issues) != 2 || issues[0].Number != 7 || issues[1].Number != 8 {
				t.Errorf("associated issues: %+v", issues)
			}
			if issues[0].State != "open" || issues[0].Title != "Need retries" {
				t.Errorf("issue: %+v", issues[0])
			}

			cmts, err := c.ListIssueComments(ctx, testRepo, 7, 100)
			if err != nil || len(cmts) != 1 || cmts[0].Author != "erin" {
				t.Errorf("issue comments: %v %+v", err, cmts)
			}

			refs, err := c.ListReferences(ctx, testRepo, 7, 10)
			if err != nil || len(refs) != 1 || refs[0].Kind != domain.ReferencePullRequest || refs[0].Ref != "42" {
				t.Errorf("references: %v %+v", err, refs)
			}

			labels, err := c.ListLabels(ctx, testRepo)
			if err != nil || len(labels) != 1 || labels[0].Description != "Something is broken" {
				t.Errorf("labels: %v %+v", err, labels)
			}

			parent, err := c.ParentRepository(ctx, testRepo)
			if err != nil || parent.Owner != "upstream" || parent.Name != "proj" {
				t.Errorf("parent: %v %+v", err, parent)
			}

			if err := c.AddIssueLabels(ctx, testRepo, 7, []domain.Label{{Name: "bug"}, {Name: "needs,comma"}}); err != nil {
				t.Errorf("add issue labels: %v", err)
			}
			if err := c.AddPullRequestLabel(ctx, testRepo, 42, "conclave/running"); err != nil {
				t.Errorf("add pr label: %v", err)
			}
			if err := c.RemovePullRequestLabel(ctx, testRepo, 42, "conclave/running"); err != nil {
				t.Errorf("remove pr label: %v", err)
			}

			for _, p := range *seen {
				if strings.Contains(p, "%252F") || strings.Contains(p, "/projects/acme/proj") {
					t.Fatalf("project path not sent as one escaped segment: %s", p)
				}
			}
		})
	}
}

func TestGitLabNotFound(t *testing.T) {
	c, _ := newClient(t, "")
	_, err := c.GetIssue(context.Background(), testRepo, 404)
	if err == nil || !strings.Contains(err.Error(), forge.ErrNotFound.Error()) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestGitLabBadAuth(t *testing.T) {
	srv, _ := newServer(t, "")
	c, _ := New(srv.URL, "bad", nil)
	_, err := c.GetPullRequest(context.Background(), testRepo, 42)
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("want 401, got %v", err)
	}
	if strings.Contains(err.Error(), "bad") {
		t.Error("the token leaked into the error")
	}
}

// TestGitLabReferencesBestEffort checks the contract: a forge that does not
// expose the endpoint leaves the triage without references, not in error.
func TestGitLabReferencesBestEffort(t *testing.T) {
	c, _ := newClient(t, "")
	refs, err := c.ListReferences(context.Background(), testRepo, 999, 10)
	if err != nil || refs != nil {
		t.Fatalf("want no references and no error, got %+v %v", refs, err)
	}
}

func TestGitLabInterfaces(t *testing.T) {
	c := &Client{}
	var _ forge.Forge = c
	var _ forge.PullRequestLabeler = c
	var _ forge.ForkResolver = c
	var _ forge.CheckReader = c
}

func TestGitLabNoReviewer(t *testing.T) {
	// GitLab has no review object: loop must say so rather than guess.
	if _, err := forge.Reviewer(&Client{}); err == nil {
		t.Fatal("expected ErrReviewsUnsupported")
	}
}

func TestMapMergeRequestFork(t *testing.T) {
	pr, err := mapMergeRequest(mergeRequest{
		IID: 3, SHA: "abc", State: "opened", Draft: true,
		SourceProjectID: 9, TargetProjectID: 77,
	}, testRepo)
	if err != nil {
		t.Fatal(err)
	}
	if !pr.Head.IsFork || pr.Head.Owner != "" || pr.State != "draft" {
		t.Errorf("fork head: %+v", pr)
	}
	if _, err := mapMergeRequest(mergeRequest{IID: 3}, testRepo); err == nil {
		t.Error("a merge request without a head SHA must be refused")
	}
}

func TestCountLines(t *testing.T) {
	added, removed := countLines("--- a/x\n+++ b/x\n@@ -1 +1,2 @@\n ctx\n+one\n+two\n-gone\n")
	if added != 2 || removed != 1 {
		t.Errorf("got %d/%d, want 2/1", added, removed)
	}
	if a, r := countLines(""); a != 0 || r != 0 {
		t.Errorf("empty diff: %d/%d", a, r)
	}
}

func TestSplitPath(t *testing.T) {
	for in, want := range map[string][2]string{
		"acme/proj":      {"acme", "proj"},
		"group/sub/proj": {"group/sub", "proj"},
		"noslash":        {"", ""},
		"/leading":       {"", ""},
		"trailing/":      {"", ""},
	} {
		owner, name, ok := splitPath(in)
		if (want[0] == "") == ok {
			t.Errorf("%q: ok = %v", in, ok)
		}
		if owner != want[0] || name != want[1] {
			t.Errorf("%q: got %q/%q, want %q/%q", in, owner, name, want[0], want[1])
		}
	}
}
