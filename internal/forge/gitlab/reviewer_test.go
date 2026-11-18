package gitlab

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bornholm/conclave/internal/domain"
)

const mrPath = "/api/v4/projects/acme%2Fproj/merge_requests"

type call struct{ method, path, query, body string }

// reviewClient serves canned answers keyed by "METHOD escaped-path" and
// records every request, in order, with its body.
func reviewClient(t *testing.T, routes map[string]string) (*Client, *[]call) {
	t.Helper()
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		calls = append(calls, call{r.Method, r.URL.EscapedPath(), r.URL.RawQuery, string(body)})
		resp, ok := routes[r.Method+" "+r.URL.EscapedPath()]
		if !ok {
			w.WriteHeader(404)
			w.Write([]byte(`{"message":"404 Not Found"}`))
			return
		}
		if r.Method == http.MethodPost {
			w.WriteHeader(201)
		}
		w.Write([]byte(resp))
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL+"/", "tok", nil)
	if err != nil {
		t.Fatal(err)
	}
	return c, &calls
}

func TestCurrentUser(t *testing.T) {
	c, _ := reviewClient(t, map[string]string{"GET /api/v4/user": `{"id":1,"username":"conclave-bot"}`})
	me, err := c.CurrentUser(context.Background())
	if err != nil || me != "conclave-bot" {
		t.Fatalf("CurrentUser = %q, %v", me, err)
	}
}

func TestListOpenPullRequests(t *testing.T) {
	c, calls := reviewClient(t, map[string]string{"GET " + mrPath: `[
		{"iid":1,"title":"a","state":"opened","author":{"username":"alice"},"sha":"h1"},
		{"iid":2,"title":"b","state":"opened","author":{"username":"bob"},"sha":"h2","draft":true}]`})
	prs, err := c.ListOpenPullRequests(context.Background(), testRepo)
	if err != nil || len(prs) != 2 {
		t.Fatalf("ListOpenPullRequests = %+v, %v", prs, err)
	}
	if prs[0].Number != 1 || prs[0].Author != "alice" || prs[0].State != "open" || prs[0].Head.SHA != "h1" || prs[1].State != "draft" {
		t.Errorf("ListOpenPullRequests = %+v", prs)
	}
	if q := (*calls)[0].query; !strings.Contains(q, "state=opened") {
		t.Errorf("query %q does not ask for the open ones", q)
	}
}

// GitLab has no review object. What stands for one, as a review on GitHub
// would, is an approval, an inline comment on the diff, or a note carrying
// the loop's mark. A plain comment is conversation, and a system note is
// the activity log: counting them would make every merge request with a
// question in it look reviewed by hand, and the loop would never take it.
func TestListReviews(t *testing.T) {
	mark := `LGTM\n\n<!-- conclave-loop {\"round\":1,\"head\":\"h1\",\"run\":\"r1\",\"verdict\":\"comment\"} -->`
	c, _ := reviewClient(t, map[string]string{
		"GET " + mrPath + "/42/discussions": `[
			{"id":"a","notes":[
				{"id":1,"body":"added 1 commit","author":{"username":"bob"},"created_at":"2026-01-01T10:00:00.000Z","system":true},
				{"id":2,"body":"Can we rename this?","author":{"username":"carol"},"created_at":"2026-01-01T11:00:00.000Z","system":false},
				{"id":3,"type":"DiffNote","body":"off by one","author":{"username":"carol"},"created_at":"2026-01-01T12:00:00.000Z","system":false}]},
			{"id":"b","notes":[
				{"id":4,"type":"DiscussionNote","body":"` + mark + `","author":{"username":"conclave-bot"},"created_at":"2026-01-02T09:00:00.000Z","system":false}]}]`,
		"GET " + mrPath + "/42/approvals": `{"approved_by":[{"user":{"username":"dave"},"approved_at":"2026-01-01T11:30:00.000Z"}]}`,
	})
	got, err := c.ListReviews(context.Background(), testRepo, 42)
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.ForgeReview{
		{Author: "dave", State: "approved", SubmittedAt: "2026-01-01T11:30:00.000Z"},
		{Author: "carol", State: "commented", Body: "off by one", SubmittedAt: "2026-01-01T12:00:00.000Z"},
		{Author: "conclave-bot", State: "commented", Body: strings.ReplaceAll(strings.ReplaceAll(mark, `\n`, "\n"), `\"`, `"`), SubmittedAt: "2026-01-02T09:00:00.000Z"},
	}
	if len(got) != len(want) {
		t.Fatalf("ListReviews = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("review %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// The approvals say whether someone already stood for the merge request:
// read wrong, the loop would take up one a human approved. Not knowing is
// an error, not "nobody".
func TestListReviewsApprovalsUnreadable(t *testing.T) {
	c, _ := reviewClient(t, map[string]string{"GET " + mrPath + "/42/discussions": `[]`})
	if _, err := c.ListReviews(context.Background(), testRepo, 42); err == nil {
		t.Fatal("want an error when the approvals cannot be read")
	}
}

// GitLab's API has no request for changes: that verdict and a comment are
// the note alone, never an approval.
func TestSubmitReviewWithoutApproval(t *testing.T) {
	for _, v := range []domain.Verdict{domain.VerdictComment, domain.VerdictRequestChanges} {
		c, calls := reviewClient(t, map[string]string{"POST " + mrPath + "/42/notes": `{"id":9}`})
		if err := c.SubmitReview(context.Background(), testRepo, 42, "h3", v, "fix it"); err != nil {
			t.Fatalf("%s: %v", v, err)
		}
		want := []call{{"POST", mrPath + "/42/notes", "", `{"body":"fix it"}`}}
		if len(*calls) != 1 || (*calls)[0] != want[0] {
			t.Errorf("%s: calls = %+v, want %+v", v, *calls, want)
		}
	}
}

// An approval is the note, then the approval pinned to the head reviewed,
// so that GitLab refuses it when the head moved meanwhile.
func TestSubmitReviewApprove(t *testing.T) {
	c, calls := reviewClient(t, map[string]string{
		"POST " + mrPath + "/42/notes":   `{"id":9}`,
		"POST " + mrPath + "/42/approve": `{"approved":true}`,
	})
	if err := c.SubmitReview(context.Background(), testRepo, 42, "h3", domain.VerdictApprove, "ok"); err != nil {
		t.Fatal(err)
	}
	want := []call{
		{"POST", mrPath + "/42/notes", "", `{"body":"ok"}`},
		{"POST", mrPath + "/42/approve", "", `{"sha":"h3"}`},
	}
	if len(*calls) != len(want) {
		t.Fatalf("calls = %+v, want %+v", *calls, want)
	}
	for i := range want {
		if (*calls)[i] != want[i] {
			t.Errorf("call %d = %+v, want %+v", i, (*calls)[i], want[i])
		}
	}
}

// The note goes first: an approval refused afterwards leaves the merge
// request unapproved, which is the safe side. The other order could leave
// an approval with no review to explain it.
func TestSubmitReviewApproveRefused(t *testing.T) {
	c, calls := reviewClient(t, map[string]string{"POST " + mrPath + "/42/notes": `{"id":9}`})
	if err := c.SubmitReview(context.Background(), testRepo, 42, "h3", domain.VerdictApprove, "ok"); err == nil {
		t.Fatal("want an error when the approval is refused")
	}
	if len(*calls) != 2 || (*calls)[0].path != mrPath+"/42/notes" {
		t.Errorf("calls = %+v, want the note before the approval", *calls)
	}
}

// A self-managed instance with a time zone set writes offsets, not UTC:
// the 12:00+02:00 approval came before the 11:00Z comment.
func TestListReviewsOrdersByInstant(t *testing.T) {
	c, _ := reviewClient(t, map[string]string{
		"GET " + mrPath + "/42/discussions": `[{"id":"a","notes":[{"id":1,"type":"DiffNote","body":"x","author":{"username":"carol"},"created_at":"2026-01-01T11:00:00.000Z"}]}]`,
		"GET " + mrPath + "/42/approvals":   `{"approved_by":[{"user":{"username":"dave"},"approved_at":"2026-01-01T12:00:00.000+02:00"}]}`,
	})
	got, err := c.ListReviews(context.Background(), testRepo, 42)
	if err != nil || len(got) != 2 || got[0].Author != "dave" {
		t.Fatalf("ListReviews = %+v, %v, want dave's approval first", got, err)
	}
}

// As on GitHub and Gitea, a verdict the forge cannot express is refused
// before anything is published.
func TestSubmitReviewUnknownVerdict(t *testing.T) {
	c, calls := reviewClient(t, map[string]string{"POST " + mrPath + "/42/notes": `{"id":9}`})
	if err := c.SubmitReview(context.Background(), testRepo, 42, "h3", domain.Verdict("maybe"), "x"); err == nil {
		t.Fatal("want an error for an unknown verdict")
	}
	if len(*calls) != 0 {
		t.Errorf("calls = %+v, want none", *calls)
	}
}
