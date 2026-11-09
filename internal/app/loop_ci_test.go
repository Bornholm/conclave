package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bornholm/conclave/internal/config"
	"github.com/bornholm/conclave/internal/domain"
)

func TestDecideLoopHeldApproval(t *testing.T) {
	pr := domain.PullRequest{Number: 1, Author: "alice", State: "open", Head: domain.RepositoryRef{SHA: "h2"}}
	held := func(round int, head string) domain.ForgeReview {
		return domain.ForgeReview{Author: "me", SubmittedAt: "2026-01-01T00:00:00Z",
			Body: loopMark{Round: round, Head: head, Run: "run", Verdict: domain.VerdictComment, Held: domain.VerdictApprove}.String()}
	}
	released := domain.ForgeReview{Author: "me", SubmittedAt: "2026-01-01T00:10:00Z",
		Body: loopMark{Round: 1, Head: "h2", Run: "run2", Verdict: domain.VerdictApprove}.String()}
	at := func(rv domain.ForgeReview, when string) domain.ForgeReview { rv.SubmittedAt = when; return rv }
	untimed := func(rv domain.ForgeReview) domain.ForgeReview { return at(rv, "") }
	for _, c := range []struct {
		name      string
		reviews   []domain.ForgeReview
		maxRounds int
		skip      string
		round     int
		release   bool
	}{
		{name: "held on the head", reviews: []domain.ForgeReview{held(1, "h2")}, round: 1, release: true},
		{name: "held on the last round", reviews: []domain.ForgeReview{held(2, "h2")}, maxRounds: 2, round: 2, release: true},
		{name: "held on an older head", reviews: []domain.ForgeReview{held(1, "h1")}, round: 2},
		{name: "held on an older head, rounds done", reviews: []domain.ForgeReview{held(2, "h1")}, maxRounds: 2, skip: "2 rounds done"},
		{name: "released", reviews: []domain.ForgeReview{held(1, "h2"), released}, skip: "approved"},
		{name: "released, listed newest first", reviews: []domain.ForgeReview{released, held(1, "h2")}, skip: "approved"},
		{name: "released, no times, listed newest first", reviews: []domain.ForgeReview{untimed(released), untimed(held(1, "h2"))}, skip: "approved"},
		{name: "released, same time", reviews: []domain.ForgeReview{released, at(held(1, "h2"), released.SubmittedAt)}, skip: "approved"},
	} {
		maxRounds := c.maxRounds
		if maxRounds == 0 {
			maxRounds = 3
		}
		d := decideLoop(pr, loopUser, c.reviews, maxRounds, false)
		if d.Skip != c.skip || d.Release != c.release || (c.skip == "" && d.Round != c.round) || !d.Followed {
			t.Errorf("%s: %+v", c.name, d)
		}
		if c.release && (d.Previous == nil || d.Previous.Run != "run") {
			t.Errorf("%s: previous %+v", c.name, d.Previous)
		}
	}
}

func TestReleaseHeld(t *testing.T) {
	review := &domain.ConsolidatedReview{Verdict: domain.VerdictApprove, Warnings: []string{"a reviewer failed"}}
	pending := &domain.CIStatus{Checks: []domain.Check{{Name: "test", State: domain.CheckPending, Blocking: true}}}
	review.Warnings = append(review.Warnings, applyCI(review, pending)...)
	if review.Verdict != domain.VerdictComment || review.HeldByCI != domain.VerdictApprove || len(review.Warnings) != 2 {
		t.Fatalf("held: %+v", review)
	}
	passed := &domain.CIStatus{Checks: []domain.Check{{Name: "test", State: domain.CheckSuccess, Blocking: true}}}
	if w := releaseHeld(review, passed); len(w) != 0 || review.Verdict != domain.VerdictApprove || review.HeldByCI != "" || review.CI != passed ||
		len(review.Warnings) != 1 || review.Warnings[0] != "a reviewer failed" {
		t.Errorf("released: %+v %v", review, w)
	}

	unread := &domain.ConsolidatedReview{Verdict: domain.VerdictApprove}
	unread.Warnings = applyCI(unread, &domain.CIStatus{Error: "502"})
	failed := &domain.CIStatus{Checks: []domain.Check{{Name: "test", State: domain.CheckFailure, Blocking: true}}}
	if w := releaseHeld(unread, failed); len(w) != 1 || unread.Verdict != domain.VerdictRequestChanges || len(unread.Warnings) != 1 || !strings.Contains(unread.Warnings[0], "CI failed") {
		t.Errorf("released on a failed CI: %+v", unread)
	}

	// Changes requested by the reports are not held, and stay requested.
	asked := &domain.ConsolidatedReview{Verdict: domain.VerdictRequestChanges}
	applyCI(asked, pending)
	if asked.HeldByCI != "" {
		t.Errorf("a request for changes is held: %+v", asked)
	}
}

// heldApproval has the loop review the pull request while its CI runs past
// ci.max_wait, which holds the approval back, and returns the mark.
func heldApproval(t *testing.T) (*App, *fakeForge, string, loopMark) {
	t.Helper()
	a, f, dir := newLoopApp(t, agentCfg(t, "r1", config.RoleReviewer, "approving"), agentCfg(t, "lead", config.RoleLead, "approving"))
	f.checks = []domain.Check{{Name: "test", State: domain.CheckPending}}
	zero := a.Config.CIMaxWait() * 0
	a.Config.CI.MaxWait = &zero
	if err := a.Loop(context.Background(), LoopRequest{Once: true}); err != nil {
		t.Fatal(err)
	}
	if len(f.reviews[7]) != 1 || f.reviews[7][0].State != string(domain.VerdictComment) {
		t.Fatalf("held review: %+v", f.reviews[7])
	}
	m, ok := parseLoopMark(f.reviews[7][0].Body)
	if !ok || m.Held != domain.VerdictApprove || m.Verdict != domain.VerdictComment {
		t.Fatalf("mark: %+v", m)
	}
	a.Config.CI.MaxWait = nil
	return a, f, dir, m
}

func TestLoopReleasesAHeldApprovalOnceTheCIPasses(t *testing.T) {
	a, f, _, m := heldApproval(t)
	ctx := context.Background()
	before := runs(t, a)

	// Still running: nothing, and no new wait of ci.max_wait.
	if err := a.Loop(ctx, LoopRequest{Once: true}); err != nil {
		t.Fatal(err)
	}
	if len(f.reviews[7]) != 1 || runs(t, a) != before {
		t.Fatalf("released while the CI runs: %+v", f.reviews[7])
	}

	f.checks[0].State = domain.CheckSuccess
	if err := a.Loop(ctx, LoopRequest{Once: true}); err != nil {
		t.Fatal(err)
	}
	if len(f.reviews[7]) != 2 || f.reviews[7][1].State != string(domain.VerdictApprove) {
		t.Fatalf("not released: %+v", f.reviews[7])
	}
	m2, _ := parseLoopMark(f.reviews[7][1].Body)
	if m2.Round != m.Round || m2.Head != m.Head || m2.Run == m.Run || m2.Held != "" || m2.Verdict != domain.VerdictApprove {
		t.Errorf("released mark: %+v, held %+v", m2, m)
	}
	// The agents did not run again: the release is a run of its own, with
	// the previous one's review and no report.
	if runs(t, a) != before+1 {
		t.Errorf("runs: %d, then %d", before, runs(t, a))
	}
	if reports, _ := os.ReadDir(filepath.Join(a.RunsBase, m2.Run, "reports")); len(reports) != 0 {
		t.Errorf("the agents ran again: %v", reports)
	}
	data, err := os.ReadFile(filepath.Join(a.RunsBase, m2.Run, "final", "review.json"))
	var review domain.ConsolidatedReview
	if err != nil || json.Unmarshal(data, &review) != nil || review.Meta.RunID != m2.Run || review.Verdict != domain.VerdictApprove || review.CI == nil || review.CI.State() != domain.CheckSuccess {
		t.Errorf("released review: %s %v", data, err)
	}
	prompt, _ := os.ReadFile(filepath.Join(a.RunsBase, m2.Run, "prompts", "comment-lead.txt"))
	if !strings.Contains(string(prompt), "The CI has") {
		t.Errorf("the writer is not told the review is released: %s", prompt)
	}
	held, _ := os.ReadFile(filepath.Join(a.RunsBase, m.Run, "final", "review.json"))
	if !strings.Contains(string(held), `"held_by_ci": "approve"`) {
		t.Errorf("the previous run was changed: %s", held)
	}

	// Approved: the loop is done with it.
	if err := a.Loop(ctx, LoopRequest{Once: true}); err != nil {
		t.Fatal(err)
	}
	if len(f.reviews[7]) != 2 {
		t.Errorf("reviewed again once approved: %+v", f.reviews[7])
	}
}

func TestLoopRunsARoundWhenTheHeldCIFails(t *testing.T) {
	a, f, _, m := heldApproval(t)
	f.checks[0].State = domain.CheckFailure
	if err := a.Loop(context.Background(), LoopRequest{Once: true}); err != nil {
		t.Fatal(err)
	}
	if len(f.reviews[7]) != 2 || f.reviews[7][1].State != string(domain.VerdictRequestChanges) {
		t.Fatalf("reviews: %+v", f.reviews[7])
	}
	m2, _ := parseLoopMark(f.reviews[7][1].Body)
	if m2.Round != m.Round+1 || m2.Held != "" {
		t.Errorf("mark: %+v", m2)
	}
	if reports, _ := os.ReadDir(filepath.Join(a.RunsBase, m2.Run, "reports")); len(reports) == 0 {
		t.Error("the agents did not look at the failure")
	}
}

func TestLoopReleasesARequestForChangesOnTheLastRound(t *testing.T) {
	a, f, _, m := heldApproval(t)
	a.Config.Loop.MaxRounds = m.Round
	f.checks[0].State = domain.CheckFailure
	if err := a.Loop(context.Background(), LoopRequest{Once: true}); err != nil {
		t.Fatal(err)
	}
	if len(f.reviews[7]) != 2 || f.reviews[7][1].State != string(domain.VerdictRequestChanges) {
		t.Fatalf("reviews: %+v", f.reviews[7])
	}
	m2, _ := parseLoopMark(f.reviews[7][1].Body)
	if m2.Round != m.Round {
		t.Errorf("a round past loop.max_rounds: %+v", m2)
	}
	if reports, _ := os.ReadDir(filepath.Join(a.RunsBase, m2.Run, "reports")); len(reports) != 0 {
		t.Errorf("the agents ran past loop.max_rounds: %v", reports)
	}
	if prompt, _ := os.ReadFile(filepath.Join(a.RunsBase, m2.Run, "prompts", "comment-lead.txt")); !strings.Contains(string(prompt), "and failed") {
		t.Errorf("the writer is not told the CI failed: %s", prompt)
	}
}

func TestLoopRunsARoundWhenTheHeldRunIsMissing(t *testing.T) {
	a, f, _, m := heldApproval(t)
	if err := os.RemoveAll(filepath.Join(a.RunsBase, m.Run)); err != nil {
		t.Fatal(err)
	}
	f.checks[0].State = domain.CheckSuccess
	if err := a.Loop(context.Background(), LoopRequest{Once: true}); err != nil {
		t.Fatal(err)
	}
	if len(f.reviews[7]) != 2 || f.reviews[7][1].State != string(domain.VerdictApprove) {
		t.Fatalf("reviews: %+v", f.reviews[7])
	}
	if m2, _ := parseLoopMark(f.reviews[7][1].Body); m2.Round != m.Round+1 {
		t.Errorf("mark: %+v", m2)
	}
}

func TestLoopRunsTheLastRoundAgainWhenTheHeldRunIsMissing(t *testing.T) {
	a, f, _, m := heldApproval(t)
	a.Config.Loop.MaxRounds = m.Round
	if err := os.RemoveAll(filepath.Join(a.RunsBase, m.Run)); err != nil {
		t.Fatal(err)
	}
	f.checks[0].State = domain.CheckSuccess
	if err := a.Loop(context.Background(), LoopRequest{Once: true}); err != nil {
		t.Fatal(err)
	}
	if len(f.reviews[7]) != 2 || f.reviews[7][1].State != string(domain.VerdictApprove) {
		t.Fatalf("reviews: %+v", f.reviews[7])
	}
	m2, _ := parseLoopMark(f.reviews[7][1].Body)
	if m2.Round != m.Round {
		t.Errorf("a round past loop.max_rounds: %+v", m2)
	}
	if reports, _ := os.ReadDir(filepath.Join(a.RunsBase, m2.Run, "reports")); len(reports) == 0 {
		t.Error("the agents did not give the verdict")
	}
}

func TestReleasedRunIsFoundBySinceLast(t *testing.T) {
	a, f, _, m := heldApproval(t)
	// A previous manifest that cannot be read leaves the release findable.
	if err := os.WriteFile(filepath.Join(a.RunsBase, m.Run, "manifest.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.checks[0].State = domain.CheckSuccess
	if err := a.Loop(context.Background(), LoopRequest{Once: true}); err != nil {
		t.Fatal(err)
	}
	m2, _ := parseLoopMark(f.reviews[7][len(f.reviews[7])-1].Body)
	path, err := lastReview(a.RunsBase, domain.Repository{Owner: "acme", Name: "proj"}, 7, "")
	if err != nil || filepath.Base(filepath.Dir(filepath.Dir(path))) != m2.Run {
		t.Errorf("--since last: %s %v, want %s", path, err, m2.Run)
	}
}

func TestLoopRetriesAReleaseTheForgeRefused(t *testing.T) {
	a, f, _, _ := heldApproval(t)
	f.checks[0].State = domain.CheckSuccess
	f.submitErrs = 1
	l := newLoop(a, f)
	ctx := context.Background()
	if err := l.pass(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.reviews[7]) != 1 || len(l.unsent) != 1 {
		t.Fatalf("refused: %+v %+v", f.reviews[7], l.unsent)
	}
	// The held review is of the same round: it is not the one refused.
	if err := l.pass(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.reviews[7]) != 2 || f.reviews[7][1].State != string(domain.VerdictApprove) {
		t.Errorf("not submitted again: %+v", f.reviews[7])
	}
}

func TestLoopGivesUpAReleaseTheForgeKeepsRefusing(t *testing.T) {
	a, f, _, _ := heldApproval(t)
	f.checks[0].State = domain.CheckSuccess
	f.submitErrs = 100
	l := newLoop(a, f)
	ctx := context.Background()
	before := runs(t, a)
	for range 2*loopMaxFailures + 2 {
		if err := l.pass(ctx); err != nil {
			t.Fatal(err)
		}
	}
	// One release written, submitted loopMaxFailures times, then left
	// until a new head: not a new run on every pass.
	if len(f.reviews[7]) != 1 || runs(t, a) != before+1 || len(l.unsent) != 0 {
		t.Errorf("reviews %d, runs %d then %d, unsent %+v", len(f.reviews[7]), before, runs(t, a), l.unsent)
	}
}

func TestLoopHoldsTheApprovalWhileTheCIIsUnread(t *testing.T) {
	a, f, _, _ := heldApproval(t)
	f.checksErr = errors.New("403 Forbidden")
	before := runs(t, a)
	for range 3 {
		if err := a.Loop(context.Background(), LoopRequest{Once: true}); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.reviews[7]) != 1 || runs(t, a) != before {
		t.Errorf("released on a CI it could not read: reviews %d, runs %d then %d", len(f.reviews[7]), before, runs(t, a))
	}
	f.checksErr = nil
	f.checks[0].State = domain.CheckSuccess
	if err := a.Loop(context.Background(), LoopRequest{Once: true}); err != nil {
		t.Fatal(err)
	}
	if len(f.reviews[7]) != 2 || f.reviews[7][1].State != string(domain.VerdictApprove) {
		t.Errorf("not released once readable: %+v", f.reviews[7])
	}
}

func TestLoopRunsARoundWhenTheHeldReviewIsUnusable(t *testing.T) {
	for _, c := range []struct {
		name      string
		maxRounds func(held int) int
	}{
		{"rounds left", func(held int) int { return held + 1 }},
		// loop.max_rounds lowered under the held round since: the round is
		// run again under its own number, not under a lower one that the
		// held mark would outrank on every pass.
		{"max_rounds lowered", func(held int) int { return held - 1 }},
	} {
		a, f, _, m := heldApproval(t)
		a.Config.Loop.MaxRounds = c.maxRounds(m.Round)
		if err := os.WriteFile(filepath.Join(a.RunsBase, m.Run, "final", "review.json"), []byte("{"), 0o644); err != nil {
			t.Fatal(err)
		}
		f.checks[0].State = domain.CheckSuccess
		if err := a.Loop(context.Background(), LoopRequest{Once: true}); err != nil {
			t.Fatal(err)
		}
		if len(f.reviews[7]) != 2 || f.reviews[7][1].State != string(domain.VerdictApprove) {
			t.Fatalf("%s: reviews: %+v", c.name, f.reviews[7])
		}
		if m2, _ := parseLoopMark(f.reviews[7][1].Body); m2.Round < m.Round {
			t.Errorf("%s: mark %+v under the held round %d", c.name, m2, m.Round)
		}
		if d := decideLoop(*f.pr, loopUser, f.reviews[7], a.Config.Loop.MaxRounds, false); d.Skip != "approved" {
			t.Errorf("%s: the next pass: %+v", c.name, d)
		}
	}
}

func TestLoopRunsAFullRoundWhenTheHeldReviewIsUnusableAndTheCIFailed(t *testing.T) {
	a, f, _, m := heldApproval(t)
	if err := os.WriteFile(filepath.Join(a.RunsBase, m.Run, "final", "review.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.checks[0].State = domain.CheckFailure
	if err := a.Loop(context.Background(), LoopRequest{Once: true}); err != nil {
		t.Fatal(err)
	}
	if len(f.reviews[7]) != 2 || f.reviews[7][1].State != string(domain.VerdictRequestChanges) {
		t.Fatalf("reviews: %+v", f.reviews[7])
	}
	if m2, _ := parseLoopMark(f.reviews[7][1].Body); m2.Round != m.Round+1 {
		t.Errorf("mark: %+v", m2)
	}
}

func TestLoopRetriesAReleaseItCouldNotWrite(t *testing.T) {
	a, f, _, m := heldApproval(t)
	f.checks[0].State = domain.CheckSuccess
	l := newLoop(a, f)
	// The runs directory cannot take a new run: the release is this
	// clone's failure, retried, not a round of the agents.
	if err := os.Chmod(a.RunsBase, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(a.RunsBase, 0o755) })
	before := runs(t, a)
	for range loopMaxFailures + 1 {
		if err := l.pass(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.reviews[7]) != 1 || runs(t, a) != before || l.failures[7].count != 0 {
		t.Fatalf("reviews %+v, runs %d then %d, failures %+v", f.reviews[7], before, runs(t, a), l.failures[7])
	}

	_ = os.Chmod(a.RunsBase, 0o755)
	if err := l.pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.reviews[7]) != 2 || f.reviews[7][1].State != string(domain.VerdictApprove) {
		t.Errorf("not released once writable: %+v", f.reviews[7])
	}
	if m2, _ := parseLoopMark(f.reviews[7][1].Body); m2.Round != m.Round {
		t.Errorf("mark: %+v", m2)
	}
}

// lateForge lists the reviews as they were before the last submission, a
// forge slow to list a new review.
type lateForge struct {
	*fakeForge
	frozen []domain.ForgeReview
}

func (f *lateForge) ListReviews(context.Context, domain.Repository, int64) ([]domain.ForgeReview, error) {
	return f.frozen, nil
}

func TestLoopGivesAHeldApprovalOnceWhenTheForgeListsItLate(t *testing.T) {
	a, f, _, _ := heldApproval(t)
	f.checks[0].State = domain.CheckSuccess
	late := &lateForge{fakeForge: f, frozen: append([]domain.ForgeReview(nil), f.reviews[7]...)}
	before := runs(t, a)
	// A new loop on each pass, as `--once` from cron.
	for range 3 {
		l := newLoop(a, f)
		l.reviewer = late
		if err := l.pass(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.reviews[7]) != 2 || runs(t, a) != before+1 {
		t.Errorf("released %d times, runs %d then %d", len(f.reviews[7])-1, before, runs(t, a))
	}
}

func TestLoopReleasesWithoutACIToRead(t *testing.T) {
	a, f, _, _ := heldApproval(t)
	// ci.enabled turned off since: a new round would approve without the
	// CI too.
	off := false
	a.Config.CI.Enabled = &off
	if err := a.Loop(context.Background(), LoopRequest{Once: true}); err != nil {
		t.Fatal(err)
	}
	if len(f.reviews[7]) != 2 || f.reviews[7][1].State != string(domain.VerdictApprove) {
		t.Errorf("reviews: %+v", f.reviews[7])
	}
}

func TestLoopRunsTheRoundOfAFailedHeldCIOnceWhenTheForgeListsItLate(t *testing.T) {
	a, f, _, m := heldApproval(t)
	f.checks[0].State = domain.CheckFailure
	late := &lateForge{fakeForge: f, frozen: append([]domain.ForgeReview(nil), f.reviews[7]...)}
	for range 3 {
		l := newLoop(a, f)
		l.reviewer = late
		if err := l.pass(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.reviews[7]) != 2 {
		t.Errorf("the round ran %d times", len(f.reviews[7])-1)
	}
	if _, err := os.Stat(newLoop(a, f).releasedPath(m.Run)); err != nil {
		t.Errorf("the held run does not say its round ran: %v", err)
	}
}

func TestLoopIgnoresAHeldVerdictTheLeadWrote(t *testing.T) {
	a, f, _ := newLoopApp(t, agentCfg(t, "r1", config.RoleReviewer, "approving"), agentCfg(t, "lead", config.RoleLead, "lead-held"))
	a.Config.Loop.RequestChangesFrom = string(domain.SeverityHigh)
	f.checks = []domain.Check{{Name: "test", State: domain.CheckPending}}
	zero := a.Config.CIMaxWait() * 0
	a.Config.CI.MaxWait = &zero
	if err := a.Loop(context.Background(), LoopRequest{Once: true}); err != nil {
		t.Fatal(err)
	}
	if len(f.reviews[7]) != 1 {
		t.Fatalf("reviews: %+v", f.reviews[7])
	}
	if m, _ := parseLoopMark(f.reviews[7][0].Body); m.Held != "" || m.Verdict != domain.VerdictComment {
		t.Errorf("the lead's held verdict reached the mark: %+v", m)
	}
}

func TestLoopFallbackNoticeOfARelease(t *testing.T) {
	for _, c := range []struct {
		name  string
		state domain.CheckState
		last  bool
		want  string
	}{
		{"passed", domain.CheckSuccess, false, "The CI has passed since the previous comment"},
		{"failed on the last round", domain.CheckFailure, true, "and it failed: changes are requested"},
		{"no check reported", "", false, "No CI is read on this head any more"},
	} {
		a, f, _, m := heldApproval(t)
		a.Config.Agents = append(a.Config.Agents, agentCfg(t, "w", config.RoleReviewer, "exit-1"))
		a.Config.Loop.Writer = "w"
		if c.last {
			a.Config.Loop.MaxRounds = m.Round
		}
		if c.state == "" {
			f.checks = nil
		} else {
			f.checks[0].State = c.state
		}
		l := newLoop(a, f)
		if err := l.pass(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(f.reviews[7]) != 2 || !strings.Contains(f.reviews[7][1].Body, c.want) || c.state == "" && strings.Contains(f.reviews[7][1].Body, "has passed") {
			t.Errorf("%s: %+v", c.name, f.reviews[7])
		}
	}
}

func TestLoopRunsTheRoundOfAMissingHeldRunOnceWhenTheForgeListsItLate(t *testing.T) {
	a, f, _, m := heldApproval(t)
	if err := os.RemoveAll(filepath.Join(a.RunsBase, m.Run)); err != nil {
		t.Fatal(err)
	}
	f.checks[0].State = domain.CheckSuccess
	late := &lateForge{fakeForge: f, frozen: append([]domain.ForgeReview(nil), f.reviews[7]...)}
	for range 3 {
		l := newLoop(a, f)
		l.reviewer = late
		if err := l.pass(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.reviews[7]) != 2 {
		t.Errorf("the round ran %d times", len(f.reviews[7])-1)
	}
	if _, err := os.Stat(filepath.Join(a.RunsBase, m.Run)); err == nil {
		t.Error("a run that is not in this clone got a directory")
	}
}

func TestLoopTellsTheWriterNoCIIsRead(t *testing.T) {
	a, f, _, _ := heldApproval(t)
	off := false
	a.Config.CI.Enabled = &off
	if err := a.Loop(context.Background(), LoopRequest{Once: true}); err != nil {
		t.Fatal(err)
	}
	m2, _ := parseLoopMark(f.reviews[7][len(f.reviews[7])-1].Body)
	prompt, _ := os.ReadFile(filepath.Join(a.RunsBase, m2.Run, "prompts", "comment-lead.txt"))
	if !strings.Contains(string(prompt), "No CI is") || strings.Contains(string(prompt), "The CI has passed since") {
		t.Errorf("the writer is told the CI passed: %s", prompt)
	}
}
