package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDoSendsAuthOnlyToBaseHost(t *testing.T) {
	var gotAuth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = append(gotAuth, r.Header.Get("Authorization"))
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = append(gotAuth, r.Header.Get("Authorization"))
		w.Write([]byte(`{}`))
	}))
	defer other.Close()

	c, err := New(srv.URL+"/api/", WithToken("token", "secret"))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]bool
	if _, err := c.GetJSON(context.Background(), "/x", nil, &out); err != nil || !out["ok"] {
		t.Fatalf("get: %v %v", err, out)
	}
	if _, err := c.Do(context.Background(), "GET", other.URL+"/y", nil, ""); err != nil {
		t.Fatal(err)
	}
	if gotAuth[0] != "token secret" || gotAuth[1] != "" {
		t.Errorf("auth headers: %q", gotAuth)
	}
}

func TestDoErrorsHideToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(403)
		w.Write([]byte(`{"message":"API rate limit exceeded"}`))
	}))
	defer srv.Close()
	c, _ := New(srv.URL, WithToken("Bearer", "supersecret"))
	_, err := c.Do(context.Background(), "GET", "/repos", nil, "")
	if err == nil || !IsStatus(err, 403) || strings.Contains(err.Error(), "supersecret") {
		t.Fatalf("unexpected: %v", err)
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("rate limit not detected: %v", err)
	}
}

func TestDoLimitsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(make([]byte, 100))
	}))
	defer srv.Close()
	c, _ := New(srv.URL, WithMaxResponseBytes(10))
	if _, err := c.Do(context.Background(), "GET", "/", nil, ""); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestDoContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	c, _ := New(srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Do(ctx, "GET", "/", nil, ""); err == nil {
		t.Fatal("expected cancellation error")
	}
}

func TestNextLink(t *testing.T) {
	h := http.Header{}
	h.Set("Link", `<https://x/a?page=2>; rel="next", <https://x/a?page=5>; rel="last"`)
	if got := NextLink(h); got != "https://x/a?page=2" {
		t.Errorf("got %q", got)
	}
	if NextLink(http.Header{}) != "" {
		t.Error("empty header")
	}
}

func TestResolveSubpath(t *testing.T) {
	c, _ := New("https://git.example.com/gitea/")
	if got := c.Resolve("/api/v1/repos", nil); got != "https://git.example.com/gitea/api/v1/repos" {
		t.Errorf("got %q", got)
	}
}
