package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
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
	if f.submitErrs > 0 {
		f.submitErrs--
		return errors.New("502 Bad Gateway")
	}
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
		{name: "marks listed newest first", pr: pr, reviews: []domain.ForgeReview{{Author: "me", Body: mark(2, "h1", domain.VerdictComment)}, {Author: "me", Body: mark(1, "h0", domain.VerdictComment)}}, round: 3},
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
	if err != nil || !strings.Contains(string(prev), m.Run) || !strings.Contains(string(prev), "maint (comment, 2026-01-01T00:01:00Z)") {
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
	if len(f.reviews[7]) != 1 || !strings.Contains(f.reviews[7][0].Body, "## Conclave review") {
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

// runs counts the review runs under the runs base.
func runs(t *testing.T, a *App) int {
	t.Helper()
	entries, _ := os.ReadDir(a.RunsBase)
	return len(entries)
}

func newLoop(a *App, f *fakeForge) *loop {
	return &loop{app: a, forge: f, reviewer: f, me: loopUser, runsBase: a.RunsBase, failures: map[int64]loopFailure{}, unsent: map[int64]loopSubmission{}}
}

func TestLoopRetriesASubmissionWithoutReviewingAgain(t *testing.T) {
	a, f, dir := newLoopApp(t, agentCfg(t, "r1", config.RoleReviewer, "valid"), agentCfg(t, "lead", config.RoleLead, "valid"))
	f.submitErrs = 1
	ctx := context.Background()
	l := newLoop(a, f)
	for range 3 {
		if err := l.pass(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.reviews[7]) != 1 || runs(t, a) != 1 {
		t.Fatalf("submitted=%d runs=%d: the review was not kept for the next passes", len(f.reviews[7]), runs(t, a))
	}
	// A review left unsubmitted when the head moves is dropped: the new head
	// gets a review of its own.
	f.submitErrs = 1
	advanceHead(t, a, dir)
	if err := l.pass(ctx); err != nil {
		t.Fatal(err)
	}
	// One more commit on the feature branch.
	for _, args := range [][]string{{"checkout", "-q", "feature"}, {"commit", "-q", "--allow-empty", "-m", "again"}, {"checkout", "-q", "main"}} {
		git := exec.Command("git", append([]string{"-C", dir}, args...)...)
		git.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := git.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	head, _ := exec.Command("git", "-C", dir, "rev-parse", "feature").Output()
	f.pr.Head.SHA = strings.TrimSpace(string(head))
	if err := l.pass(ctx); err != nil {
		t.Fatal(err)
	}
	m, _ := parseLoopMark(f.reviews[7][len(f.reviews[7])-1].Body)
	if runs(t, a) != 3 || len(f.reviews[7]) != 2 || m.Head != f.pr.Head.SHA {
		t.Errorf("runs=%d submitted=%d mark=%+v", runs(t, a), len(f.reviews[7]), m)
	}
}

func TestLoopGivesUpAHeadAfterTwoFailures(t *testing.T) {
	a, f, dir := newLoopApp(t, agentCfg(t, "r1", config.RoleReviewer, "exit-1"), agentCfg(t, "lead", config.RoleLead, "valid"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l := newLoop(a, f)
	for range 3 {
		if err := l.pass(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := runs(t, a); n != loopMaxFailures {
		t.Fatalf("%d reviews of a head that keeps failing, want %d", n, loopMaxFailures)
	}
	advanceHead(t, a, dir)
	if err := l.pass(ctx); err != nil {
		t.Fatal(err)
	}
	if n := runs(t, a); n != loopMaxFailures+1 {
		t.Errorf("a new head is not tried: %d runs", n)
	}
	if len(f.reviews[7]) != 0 {
		t.Errorf("a failed review was submitted")
	}
}

func TestLoopFullReviewWhenThePreviousRunIsMissing(t *testing.T) {
	a, f, dir := newLoopApp(t, agentCfg(t, "r1", config.RoleReviewer, "valid"), agentCfg(t, "lead", config.RoleLead, "valid"))
	ctx := context.Background()
	if err := a.Loop(ctx, LoopRequest{Once: true}); err != nil {
		t.Fatal(err)
	}
	m, _ := parseLoopMark(f.reviews[7][0].Body)
	if err := os.RemoveAll(filepath.Join(a.RunsBase, m.Run)); err != nil {
		t.Fatal(err)
	}
	advanceHead(t, a, dir)
	if err := a.Loop(ctx, LoopRequest{Once: true}); err != nil {
		t.Fatal(err)
	}
	m2, _ := parseLoopMark(f.reviews[7][1].Body)
	if m2.Round != 2 {
		t.Errorf("round: %+v", m2)
	}
	if _, err := os.Stat(filepath.Join(a.RunsBase, m2.Run, "context", "previous-review.json")); err == nil {
		t.Error("the review was incremental against a run that is not there")
	}
	l := &loop{runsBase: a.RunsBase}
	for _, id := range []string{"../" + m2.Run, "a/b", ".", "..", "", m2.Run + "/../" + m2.Run} {
		if l.hasRun(id) {
			t.Errorf("%q accepted as a run id", id)
		}
	}
	if !l.hasRun(m2.Run) {
		t.Error("the run is not found")
	}
}

func TestLoopRejectsACommentThatDropsAFinding(t *testing.T) {
	a, f, _ := newLoopApp(t, agentCfg(t, "r1", config.RoleReviewer, "valid"), agentCfg(t, "lead", config.RoleLead, "valid"),
		agentCfg(t, "w", config.RoleReviewer, "writer-drops"))
	a.Config.Loop.Writer = "w"
	a.Config.Loop.MaxCommentBytes = 300
	if err := a.Loop(context.Background(), LoopRequest{Once: true}); err != nil {
		t.Fatal(err)
	}
	body := f.reviews[7][0].Body
	m, ok := parseLoopMark(body)
	if !strings.HasPrefix(body, "Automatic review 1 of at most 3.\n\n## Conclave review") || !ok || len(body) > 300 {
		t.Errorf("a comment without the high finding was published, or the fallback is not capped, mark included: %d bytes %q", len(body), body)
	}
	// The run keeps what the author read, and the comment turned down.
	published, _ := os.ReadFile(filepath.Join(a.RunsBase, m.Run, "final", "comment.md"))
	rejected, _ := os.ReadFile(filepath.Join(a.RunsBase, m.Run, "final", "comment-rejected.md"))
	if string(published) != body || !strings.Contains(string(rejected), "Commentaire en français") {
		t.Errorf("published %q, rejected %q", published, rejected)
	}
}

func TestLoopDryRunLabelsNothing(t *testing.T) {
	a, f, _ := newLoopApp(t, agentCfg(t, "r1", config.RoleReviewer, "valid"), agentCfg(t, "lead", config.RoleLead, "valid"))
	a.Config.Review.RunningLabel = "conclave/running"
	if err := a.Loop(context.Background(), LoopRequest{DryRun: true, Out: &bytes.Buffer{}}); err != nil {
		t.Fatal(err)
	}
	if len(f.labelEvents) != 0 || a.Config.Review.RunningLabel != "conclave/running" {
		t.Errorf("label events %v, label %q", f.labelEvents, a.Config.Review.RunningLabel)
	}
}

func TestLoopSubmitsAnUnsentReviewDespiteAReviewSubmittedMeanwhile(t *testing.T) {
	a, f, _ := newLoopApp(t, agentCfg(t, "r1", config.RoleReviewer, "valid"), agentCfg(t, "lead", config.RoleLead, "valid"))
	f.submitErrs = 1
	ctx := context.Background()
	l := newLoop(a, f)
	if err := l.pass(ctx); err != nil {
		t.Fatal(err)
	}
	f.reviews[7] = append(f.reviews[7], domain.ForgeReview{Author: "carol", State: "commented", SubmittedAt: "2026-01-01T00:00:30Z"})
	if err := l.pass(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.reviews[7]) != 2 || f.reviews[7][1].Author != loopUser || runs(t, a) != 1 {
		t.Errorf("reviews=%+v runs=%d", f.reviews[7], runs(t, a))
	}
}

func TestLoopGivesUpASubmissionTheForgeKeepsRefusing(t *testing.T) {
	a, f, _ := newLoopApp(t, agentCfg(t, "r1", config.RoleReviewer, "valid"), agentCfg(t, "lead", config.RoleLead, "valid"))
	f.submitErrs = 100
	ctx := context.Background()
	l := newLoop(a, f)
	for range 4 {
		if err := l.pass(ctx); err != nil {
			t.Fatal(err)
		}
	}
	// One review, two refusals, then nothing until a new head.
	if f.submitErrs != 100-loopMaxFailures || runs(t, a) != 1 || len(l.unsent) != 0 {
		t.Errorf("submissions tried=%d runs=%d unsent=%d", 100-f.submitErrs, runs(t, a), len(l.unsent))
	}
	// A pull request closed with a review unsent drops it.
	b, g, _ := newLoopApp(t, agentCfg(t, "r1", config.RoleReviewer, "valid"), agentCfg(t, "lead", config.RoleLead, "valid"))
	g.submitErrs = 1
	m := newLoop(b, g)
	if err := m.pass(ctx); err != nil || len(m.unsent) != 1 {
		t.Fatalf("unsent=%d %v", len(m.unsent), err)
	}
	g.pr.Number = 70
	if err := m.pass(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.unsent[7]; ok {
		t.Error("the review of a closed pull request is kept")
	}
}

func TestMissingFindings(t *testing.T) {
	review := &domain.ConsolidatedReview{Findings: []domain.ConsolidatedFinding{
		{Severity: domain.SeverityHigh, File: "a.go"},
		{Severity: domain.SeverityMedium, File: "b.go"},
		{Severity: domain.SeverityMedium, File: "b.go"},
		{Severity: domain.SeverityLow, File: "c.go"},
		{Severity: domain.SeverityCritical, File: "d.go", OutOfScope: true},
	}}
	if got := missingFindings(review, "Voir a.go:3."); len(got) != 1 || got[0] != "b.go" {
		t.Errorf("missing: %v", got)
	}
}

func TestLoopDropsAnUnsentReviewOnADraftOrOnceTheForgeHasIt(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name   string
		change func(f *fakeForge, mark loopMark)
	}{
		{"draft", func(f *fakeForge, _ loopMark) { f.pr.State = "draft" }},
		// The refusal was not one: the forge holds the review.
		{"held", func(f *fakeForge, m loopMark) {
			f.reviews[7] = append(f.reviews[7], domain.ForgeReview{Author: loopUser, Body: m.String(), SubmittedAt: "2026-01-01T00:00:10Z"})
		}},
	} {
		a, f, _ := newLoopApp(t, agentCfg(t, "r1", config.RoleReviewer, "valid"), agentCfg(t, "lead", config.RoleLead, "valid"))
		f.submitErrs = 1
		l := newLoop(a, f)
		if err := l.pass(ctx); err != nil {
			t.Fatal(err)
		}
		c.change(f, l.unsent[7].mark)
		if err := l.pass(ctx); err != nil {
			t.Fatal(err)
		}
		if _, ok := l.unsent[7]; ok || runs(t, a) != 1 || len(f.reviews[7]) > 1 {
			t.Errorf("%s: unsent=%v runs=%d reviews=%d", c.name, ok, runs(t, a), len(f.reviews[7]))
		}
	}
}

type discussionForge struct {
	*fakeForge
	discussion []domain.Comment
}

func (f discussionForge) ListDiscussion(context.Context, domain.Repository, int64, int) ([]domain.Comment, error) {
	return f.discussion, nil
}

func TestLoopResponsesKeepTheNewestWithinTheCap(t *testing.T) {
	a, f, _ := newLoopApp(t, agentCfg(t, "r1", config.RoleReviewer, "valid"), agentCfg(t, "lead", config.RoleLead, "valid"))
	a.Config.Review.Limits.MaxCommentBytes = 300
	l := newLoop(a, f)
	l.forge = discussionForge{f, []domain.Comment{
		{Kind: "comment", Author: "alice", CreatedAt: "2026-01-01T00:00:00Z", Body: "before the round"},
		{Kind: "comment", Author: "alice", CreatedAt: "2026-01-02T00:00:00Z", Body: strings.Repeat("old ", 200)},
		{Kind: "comment", Author: loopUser, CreatedAt: "2026-01-03T00:00:00Z", Body: "mine"},
		{Kind: "comment", Author: "alice", CreatedAt: "2026-01-04T00:00:00Z", Body: "newest answer"},
	}}
	got := l.responses(context.Background(), 7, "2026-01-01T12:00:00Z")
	if !strings.Contains(got, "newest answer") || strings.Contains(got, "before the round") || strings.Contains(got, "mine") ||
		!strings.HasPrefix(got, "alice (comment, 2026-01-02") || len(got) > 300+len("…") {
		t.Errorf("%d bytes:\n%s", len(got), got)
	}
	// A previous round without a time opens the window.
	if got := l.responses(context.Background(), 7, ""); !strings.Contains(got, "newest answer") {
		t.Errorf("open window: %s", got)
	}
}
