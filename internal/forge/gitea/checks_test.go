package gitea

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bornholm/conclave/internal/domain"
)

func TestListChecks(t *testing.T) {
	pages := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/repos/acme/proj/commits/abc/status") {
			http.NotFound(w, r)
			return
		}
		pages++
		if r.URL.Query().Get("page") == "1" {
			var b strings.Builder
			b.WriteString(`{"state":"failure","statuses":[`)
			for i := 0; i < checkPageSize; i++ {
				if i > 0 {
					b.WriteString(",")
				}
				state := "success"
				switch i {
				case 0:
					state = "failure"
				case 1:
					state = "pending"
				case 2:
					state = "warning"
				}
				fmt.Fprintf(&b, `{"context":"job %d","status":%q,"description":"d%d","target_url":"https://ci/%d"}`, i, state, i, i)
			}
			b.WriteString("]}")
			fmt.Fprint(w, b.String())
			return
		}
		fmt.Fprint(w, `{"state":"failure","statuses":[{"context":"last","status":"error"}]}`)
	}))
	defer srv.Close()
	c, err := New(srv.URL, "tok", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	checks, err := c.ListChecks(context.Background(), domain.Repository{Owner: "acme", Name: "proj"}, "abc")
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != checkPageSize+1 || pages != 2 {
		t.Fatalf("%d checks over %d pages", len(checks), pages)
	}
	if ch := checks[0]; ch.Name != "job 0" || ch.State != domain.CheckFailure || ch.Summary != "d0" || ch.URL != "https://ci/0" || ch.Source != domain.CheckSourceStatus {
		t.Errorf("first: %+v", ch)
	}
	if checks[1].State != domain.CheckPending || checks[2].State != domain.CheckSuccess || checks[len(checks)-1].State != domain.CheckFailure {
		t.Errorf("states: %+v", checks)
	}
}

func TestListChecksPagingIgnored(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var b strings.Builder
		b.WriteString(`{"statuses":[`)
		for i := 0; i < checkPageSize; i++ {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `{"context":"job %d","status":"success"}`, i)
		}
		b.WriteString("]}")
		fmt.Fprint(w, b.String())
	}))
	defer srv.Close()
	c, err := New(srv.URL, "tok", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	checks, err := c.ListChecks(context.Background(), domain.Repository{Owner: "acme", Name: "proj"}, "abc")
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != checkPageSize || calls != 2 {
		t.Errorf("%d checks in %d calls from a server that ignores paging", len(checks), calls)
	}
}
