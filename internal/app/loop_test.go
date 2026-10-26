package app

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bornholm/conclave/internal/config"
	"github.com/bornholm/conclave/internal/domain"
)

const loopUser = "me"

func (f *fakeForge) CurrentUser(context.Context) (string, error) { return loopUser, nil }

func (f *fakeForge) ListOpenPullRequests(context.Context, domain.Repository) ([]domain.PullRequest, error) {
	return append([]domain.PullRequest{*f.pr}, f.others...), nil
}

func (f *fakeForge) ListReviews(_ context.Context, _ domain.Repository, n int64) ([]domain.ForgeReview, error) {
	return f.reviews[n], nil
}

func (f *fakeForge) SubmitReview(_ context.Context, _ domain.Repository, n int64, sha string, verdict domain.Verdict, body string) error {
	if f.reviews == nil {
		f.reviews = map[int64][]domain.ForgeReview{}
	}
	f.reviews[n] = append(f.reviews[n], domain.ForgeReview{Author: loopUser, State: string(verdict), Body: body, CommitSHA: sha,
		SubmittedAt: fmt.Sprintf("2026-01-01T00:00:%02dZ", len(f.reviews[n]))})
	return nil
}

func TestDecideLoop(t *testing.T) {
	pr := domain.PullRequest{Number: 1, Author: "alice", State: "open", Head: domain.RepositoryRef{SHA: "h2"}}
	mark := func(round int, head string, v domain.Verdict) string {
		return "Text.\n\n" + loopMark{Round: round, Head: head, Run: "run", Verdict: v}.String()
	}
	draft := pr
	draft.State = "draft"
	own := pr
	own.Author = "Me"
	for _, c := range []struct {
		name      string
		pr        domain.PullRequest
		reviews   []domain.ForgeReview
		skip      string
		round     int
		maxRounds int
	}{
		{name: "new", pr: pr, round: 1},
		{name: "own", pr: own, skip: "opened by the authenticated user"},
		{name: "draft", pr: draft, skip: "draft"},
		{name: "reviewed by someone", pr: pr, reviews: []domain.ForgeReview{{Author: "bob", State: "commented"}}, skip: "already reviewed"},
		{name: "reviewed by the user by hand", pr: pr, reviews: []domain.ForgeReview{{Author: "me", Body: "LGTM"}}, skip: "already reviewed"},
		{name: "a mark in someone else's review", pr: pr, reviews: []domain.ForgeReview{{Author: "bob", Body: mark(1, "h1", domain.VerdictComment)}}, skip: "already reviewed"},
		{name: "new head", pr: pr, reviews: []domain.ForgeReview{{Author: "me", Body: mark(1, "h1", domain.VerdictRequestChanges)}, {Author: "bob"}}, round: 2},
		{name: "same head", pr: pr, reviews: []domain.ForgeReview{{Author: "me", Body: mark(1, "h2", domain.VerdictRequestChanges)}}, skip: "no change since the last round"},
		{name: "approved", pr: pr, reviews: []domain.ForgeReview{{Author: "me", Body: mark(1, "h1", domain.VerdictApprove)}}, skip: "approved"},
		{name: "rounds done", pr: pr, reviews: []domain.ForgeReview{{Author: "me", Body: mark(1, "h1", domain.VerdictComment)}, {Author: "me", Body: mark(2, "h0", domain.VerdictComment)}}, maxRounds: 2, skip: "2 rounds done"},
	} {
		maxRounds := c.maxRounds
		if maxRounds == 0 {
			maxRounds = 3
		}
		d := decideLoop(c.pr, loopUser, c.reviews, maxRounds)
		if d.Skip != c.skip || (c.skip == "" && d.Round != c.round) {
			t.Errorf("%s: %+v", c.name, d)
		}
	}
}

func TestParseLoopMark(t *testing.T) {
	m := loopMark{Round: 2, Head: "abc", Run: "r", Verdict: domain.VerdictComment}
	got, ok := parseLoopMark("body " + loopMark{Round: 1, Head: "old"}.String() + "\n" + m.String())
	if !ok || got != m {
		t.Errorf("got %+v %v", got, ok)
	}
	for _, body := range []string{"", "<!-- conclave-loop {} -->", "<!-- conclave-loop {\"round\":1} -->", "<!-- conclave-loop nope -->"} {
		if _, ok := parseLoopMark(body); ok {
			t.Errorf("%q: accepted", body)
		}
	}
}

func newLoopApp(t *testing.T, agents ...config.AgentConfig) (*App, *fakeForge, string) {
	t.Helper()
	a, dir := newApp(t, agents...)
	f := a.Forge.(*fakeForge)
	// Already reviewed by someone, and opened by the user: both left alone.
	f.others = []domain.PullRequest{
		{Number: 8, Author: "bob", State: "open", Head: domain.RepositoryRef{SHA: "x"}},
		{Number: 9, Author: loopUser, State: "open", Head: domain.RepositoryRef{SHA: "y"}},
	}
	f.reviews = map[int64][]domain.ForgeReview{8: {{Author: "carol", State: "approved"}}}
	return a, f, dir
}

func TestLoopReviewsAndFollowsUp(t *testing.T) {
	a, f, dir := newLoopApp(t, agentCfg(t, "r1", config.RoleReviewer, "valid"), agentCfg(t, "lead", config.RoleLead, "valid"))
	ctx := context.Background()
	if err := a.Loop(ctx, LoopRequest{Once: true}); err != nil {
		t.Fatal(err)
	}
	reviews := f.reviews[7]
	if len(reviews) != 1 || len(f.reviews[8]) != 1 || len(f.reviews[9]) != 0 {
		t.Fatalf("reviews: %+v", f.reviews)
	}
	first := reviews[0]
	m, ok := parseLoopMark(first.Body)
	if !ok || m.Round != 1 || m.Head != f.pr.Head.SHA || m.Verdict != domain.VerdictRequestChanges || first.State != "request_changes" || first.CommitSHA != f.pr.Head.SHA {
		t.Errorf("first review: %+v mark=%+v", first, m)
	}
	if !strings.HasPrefix(first.Body, "Commentaire en français. Verdict request_changes, round 1/3.") || strings.Contains(first.Body, "Follow-up") {
		t.Errorf("first comment: %q", first.Body)
	}
	if _, err := os.Stat(filepath.Join(a.RunsBase, m.Run, "final", "comment.md")); err != nil {
		t.Errorf("the comment is not kept with the run: %v", err)
	}

	// Nothing moved: nothing to do.
	if err := a.Loop(ctx, LoopRequest{Once: true}); err != nil {
		t.Fatal(err)
	}
	if len(f.reviews[7]) != 1 {
		t.Fatalf("reviewed again an unchanged head: %d reviews", len(f.reviews[7]))
	}

	// A new head: the second round is incremental, and weighs what was said
	// on the pull request since the first.
	advanceHead(t, a, dir)
	if err := a.Loop(ctx, LoopRequest{Once: true}); err != nil {
		t.Fatal(err)
	}
	if len(f.reviews[7]) != 2 {
		t.Fatalf("no second round: %+v", f.reviews[7])
	}
	second := f.reviews[7][1]
	m2, _ := parseLoopMark(second.Body)
	if m2.Round != 2 || m2.Head != f.pr.Head.SHA || !strings.Contains(second.Body, "round 2/3. Follow-up.") {
		t.Errorf("second review: %q", second.Body)
	}
	prev, err := os.ReadFile(filepath.Join(a.RunsBase, m2.Run, "context", "previous-review.json"))
	if err != nil || !strings.Contains(string(prev), m.Run) || !strings.Contains(string(prev), "maint (comment, t)") {
		t.Errorf("the second round is not measured against the first, with the responses: %s %v", prev, err)
	}
}

func TestLoopLastRoundAndApproval(t *testing.T) {
	a, f, dir := newLoopApp(t, agentCfg(t, "r1", config.RoleReviewer, "valid"), agentCfg(t, "lead", config.RoleLead, "valid"))
	a.Config.Loop.MaxRounds = 1
	ctx := context.Background()
	if err := a.Loop(ctx, LoopRequest{Once: true}); err != nil {
		t.Fatal(err)
	}
	if len(f.reviews[7]) != 1 || !strings.Contains(f.reviews[7][0].Body, "Last round.") {
		t.Fatalf("the last round does not say so: %+v", f.reviews[7])
	}
	advanceHead(t, a, dir)
	if err := a.Loop(ctx, LoopRequest{Once: true}); err != nil {
		t.Fatal(err)
	}
	if len(f.reviews[7]) != 1 {
		t.Errorf("reviewed past loop.max_rounds")
	}

	// An approval is submitted as one, and ends the loop on the pull request.
	b, g, dir2 := newLoopApp(t, agentCfg(t, "r1", config.RoleReviewer, "approving"), agentCfg(t, "lead", config.RoleLead, "approving"))
	if err := b.Loop(ctx, LoopRequest{Once: true}); err != nil {
		t.Fatal(err)
	}
	if len(g.reviews[7]) != 1 || g.reviews[7][0].State != "approve" || strings.Contains(g.reviews[7][0].Body, "Last round.") {
		t.Fatalf("approval: %+v", g.reviews[7])
	}
	advanceHead(t, b, dir2)
	if err := b.Loop(ctx, LoopRequest{Once: true}); err != nil {
		t.Fatal(err)
	}
	if len(g.reviews[7]) != 1 {
		t.Errorf("reviewed again an approved pull request")
	}
}

func TestLoopPublishesTheReviewWhenTheWriterFails(t *testing.T) {
	a, f, _ := newLoopApp(t, agentCfg(t, "r1", config.RoleReviewer, "valid"), agentCfg(t, "lead", config.RoleLead, "valid"),
		agentCfg(t, "w", config.RoleReviewer, "exit-1"))
	// w fails as a reviewer too, which the review survives.
	a.Config.Loop.Writer = "w"
	if err := a.Loop(context.Background(), LoopRequest{Once: true}); err != nil {
		t.Fatal(err)
	}
	if len(f.reviews[7]) != 1 || !strings.HasPrefix(f.reviews[7][0].Body, "## Conclave review") {
		t.Errorf("fallback: %+v", f.reviews[7])
	}
}

func TestLoopDryRun(t *testing.T) {
	a, f, _ := newLoopApp(t, agentCfg(t, "r1", config.RoleReviewer, "valid"), agentCfg(t, "lead", config.RoleLead, "valid"))
	var out bytes.Buffer
	if err := a.Loop(context.Background(), LoopRequest{DryRun: true, Out: &out}); err != nil {
		t.Fatal(err)
	}
	if len(f.reviews[7]) != 0 || !strings.Contains(out.String(), "pull request #7, round 1, verdict request_changes, not submitted") || !strings.Contains(out.String(), "Commentaire en français") {
		t.Errorf("dry run: submitted=%d out=%s", len(f.reviews[7]), out.String())
	}
}
