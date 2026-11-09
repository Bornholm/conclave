package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bornholm/conclave/internal/domain"
	"github.com/bornholm/conclave/internal/forge"
)

func TestListChecks(t *testing.T) {
	mux := http.NewServeMux()
	annotationCalls := 0
	mux.HandleFunc("/repos/acme/proj/commits/abc/check-runs", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			fmt.Fprint(w, `{"total_count":3,"check_runs":[{"id":3,"name":"lint","status":"in_progress","html_url":"https://gh/3"}]}`)
			return
		}
		w.Header().Set("Link", fmt.Sprintf(`<http://%s/repos/acme/proj/commits/abc/check-runs?page=2&per_page=100>; rel="next"`, r.Host))
		fmt.Fprint(w, `{"total_count":3,"check_runs":[
			{"id":1,"name":"build","status":"completed","conclusion":"success","html_url":"https://gh/1","output":{"title":"ok","summary":"","annotations_count":2}},
			{"id":2,"name":"test","status":"completed","conclusion":"timed_out","details_url":"https://ci/2","output":{"title":"Tests","summary":"`+strings.Repeat("x", 3000)+`","annotations_count":1}}]}`)
	})
	mux.HandleFunc("/repos/acme/proj/check-runs/2/annotations", func(w http.ResponseWriter, r *http.Request) {
		annotationCalls++
		fmt.Fprint(w, `[{"path":"a.go","start_line":3,"end_line":4,"annotation_level":"failure","title":"TestA","message":"got 1"}]`)
	})
	mux.HandleFunc("/repos/acme/proj/check-runs/1/annotations", func(w http.ResponseWriter, r *http.Request) {
		t.Error("annotations of a check that passed are fetched")
	})
	mux.HandleFunc("/repos/acme/proj/commits/abc/status", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			fmt.Fprint(w, `{"state":"failure","statuses":[{"context":"cla","state":"success"}]}`)
			return
		}
		w.Header().Set("Link", fmt.Sprintf(`<http://%s/repos/acme/proj/commits/abc/status?page=2&per_page=100>; rel="next"`, r.Host))
		fmt.Fprint(w, `{"state":"failure","statuses":[{"context":"ci/legacy","state":"error","description":"boom","target_url":"https://legacy/1"}]}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, err := New(srv.URL, "tok", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	checks, err := c.ListChecks(context.Background(), domain.Repository{Owner: "acme", Name: "proj"}, "abc")
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 5 {
		t.Fatalf("checks: %+v", checks)
	}
	by := map[string]domain.Check{}
	for _, ch := range checks {
		by[ch.Name] = ch
	}
	if ch := by["build"]; ch.State != domain.CheckSuccess || ch.Source != domain.CheckSourceRun || ch.URL != "https://gh/1" || ch.Summary != "ok" {
		t.Errorf("build: %+v", ch)
	}
	ch := by["test"]
	if ch.State != domain.CheckFailure || ch.Conclusion != "timed_out" || ch.URL != "https://ci/2" || len(ch.Summary) > forge.MaxCheckSummaryBytes+len("…") || !strings.HasPrefix(ch.Summary, "Tests\nxxx") {
		t.Errorf("test: %+v", ch)
	}
	if len(ch.Annotations) != 1 || ch.Annotations[0] != (domain.CheckAnnotation{Path: "a.go", StartLine: 3, EndLine: 4, Level: "failure", Message: "TestA: got 1"}) {
		t.Errorf("annotations: %+v", ch.Annotations)
	}
	if ch := by["lint"]; ch.State != domain.CheckPending || ch.Conclusion != "in_progress" {
		t.Errorf("lint: %+v", ch)
	}
	if ch := by["ci/legacy"]; ch.State != domain.CheckFailure || ch.Source != domain.CheckSourceStatus || ch.Summary != "boom" || ch.URL != "https://legacy/1" {
		t.Errorf("status: %+v", ch)
	}
	if by["cla"].State != domain.CheckSuccess || annotationCalls != 1 {
		t.Errorf("cla: %+v, %d annotation calls", by["cla"], annotationCalls)
	}
}

func TestListChecksError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"message":"Resource not accessible by personal access token"}`)
	}))
	defer srv.Close()
	c, err := New(srv.URL, "tok", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListChecks(context.Background(), domain.Repository{Owner: "acme", Name: "proj"}, "abc"); err == nil || !strings.Contains(err.Error(), "check runs") {
		t.Errorf("error: %v", err)
	}
}

func TestListChecksAnnotationsError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/proj/commits/abc/check-runs", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"check_runs":[{"id":2,"name":"test","status":"completed","conclusion":"failure","output":{"title":"Tests","annotations_count":1}}]}`)
	})
	mux.HandleFunc("/repos/acme/proj/check-runs/2/annotations", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})
	mux.HandleFunc("/repos/acme/proj/commits/abc/status", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"statuses":[]}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, err := New(srv.URL, "tok", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	checks, err := c.ListChecks(context.Background(), domain.Repository{Owner: "acme", Name: "proj"}, "abc")
	if err != nil {
		t.Fatalf("an annotation read failing fails the CI: %v", err)
	}
	if len(checks) != 1 || checks[0].State != domain.CheckFailure || checks[0].Annotations != nil || !strings.Contains(checks[0].Summary, "Tests\n[annotations not read: ") {
		t.Errorf("checks: %+v", checks)
	}
}

func TestListChecksPagesBounded(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Link", fmt.Sprintf(`<http://%s%s?page=%d>; rel="next"`, r.Host, r.URL.Path, calls+1))
		if strings.HasSuffix(r.URL.Path, "/check-runs") {
			fmt.Fprint(w, `{"check_runs":[]}`)
			return
		}
		fmt.Fprint(w, `{"statuses":[]}`)
	}))
	defer srv.Close()
	c, err := New(srv.URL, "tok", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListChecks(context.Background(), domain.Repository{Owner: "acme", Name: "proj"}, "abc"); err != nil {
		t.Fatal(err)
	}
	if calls != 2*maxCheckPages {
		t.Errorf("%d calls to a server that always sends a next link", calls)
	}
}
