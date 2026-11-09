package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bornholm/conclave/internal/agent"
	"github.com/bornholm/conclave/internal/artifact"
	"github.com/bornholm/conclave/internal/domain"
	"github.com/bornholm/conclave/internal/forge"
	"github.com/bornholm/conclave/internal/forge/factory"
	"github.com/bornholm/conclave/internal/output"
)

// LoopRequest parameterizes `conclave loop`.
type LoopRequest struct {
	// Once makes a single pass over the open pull requests.
	Once bool
	// DryRun makes a single pass and writes the comments to Out instead of
	// submitting them.
	DryRun bool
	// Include lists the pull requests taken up even though they were
	// reviewed before the loop took them up.
	Include []int64
	Out     io.Writer
}

// loopMaxFailures is how many times the loop reviews the same head of a
// pull request in vain before it waits for a new one: a review that fails
// on a head will most likely fail on it again, and each try costs a review.
const loopMaxFailures = 2

// loopMark is what the loop hides in the reviews it submits, to know on its
// next pass which round a pull request is at and what it last reviewed.
type loopMark struct {
	Round   int            `json:"round"`
	Head    string         `json:"head"`
	Run     string         `json:"run"`
	Verdict domain.Verdict `json:"verdict"`
	// Held is the verdict the CI kept back on that head, an approval given
	// as comment while the CI had not finished: the loop gives it once the
	// CI has passed, without a new push.
	Held domain.Verdict `json:"held,omitempty"`
}

var loopMarkRe = regexp.MustCompile(`<!-- conclave-loop (\{[^\n]*?\}) -->`)

func (m loopMark) String() string {
	data, _ := json.Marshal(m)
	return "<!-- conclave-loop " + string(data) + " -->"
}

// parseLoopMark reads the last mark of a review body.
func parseLoopMark(body string) (loopMark, bool) {
	all := loopMarkRe.FindAllStringSubmatch(body, -1)
	if len(all) == 0 {
		return loopMark{}, false
	}
	var m loopMark
	if err := json.Unmarshal([]byte(all[len(all)-1][1]), &m); err != nil || m.Round < 1 || m.Head == "" {
		return loopMark{}, false
	}
	return m, true
}

// loopSkipReviewed is why the loop leaves alone a pull request reviewed
// before it took it up: the one skip that a new head does not end, logged
// so that it does not look like the loop missed the push.
const loopSkipReviewed = "already manually reviewed"

// loopSkipDraft is why the loop leaves alone a draft, logged too: the pull
// request is open, and only the log tells why the loop waits on it.
const loopSkipDraft = "draft"

// loopSkipCI is why the loop leaves alone, for now, a pull request whose
// CI has not finished on its head.
const loopSkipCI = "CI still running"

// ciWait is when the loop first found the CI of a head still running.
type ciWait struct {
	Head  string    `json:"head"`
	Since time.Time `json:"since"`
}

// releasedFile is written in a run whose held approval the loop gave, and
// names the run that gave it.
const releasedFile = "released.json"

// ciWaitFile is where the loop keeps, next to the runs, when it started
// waiting for the CI of each pull request: `conclave loop --once` run from
// cron is a new process on each pass, and an in-memory wait would never
// reach ci.max_wait.
const ciWaitFile = "loop-ci-wait.json"

// loadWaits reads the waits a previous loop left. A missing or unreadable
// file is an empty one: the worst it costs is waiting ci.max_wait again.
func loadWaits(path string) map[int64]ciWait {
	waits := map[int64]ciWait{}
	data, err := os.ReadFile(path)
	if err != nil {
		return waits
	}
	var raw map[string]ciWait
	if json.Unmarshal(data, &raw) != nil {
		return waits
	}
	for k, w := range raw {
		if n, err := strconv.ParseInt(k, 10, 64); err == nil && w.Head != "" {
			waits[n] = w
		}
	}
	return waits
}

// saveWaits writes the waits for the next loop, when there is a file to
// write them to.
func (l *loop) saveWaits() {
	if l.waitFile == "" || !l.waitsChanged {
		return
	}
	raw := make(map[string]ciWait, len(l.waiting))
	for n, w := range l.waiting {
		raw[strconv.FormatInt(n, 10)] = w
	}
	data, err := json.MarshalIndent(raw, "", "  ")
	if err == nil {
		err = os.MkdirAll(filepath.Dir(l.waitFile), 0o755)
	}
	if err == nil {
		err = os.WriteFile(l.waitFile, data, 0o644)
	}
	if err != nil {
		l.app.logger().Warn("CI waits not saved, a new loop waits ci.max_wait again", "file", l.waitFile, "error", err)
		return
	}
	l.waitsChanged = false
}

// loopDecision is what the loop does with one pull request.
type loopDecision struct {
	// Skip says why the pull request is left alone, empty when it is
	// reviewed.
	Skip  string
	Round int
	// Previous is the loop's last review of the pull request, and
	// PreviousAt when it was submitted.
	Previous   *loopMark
	PreviousAt string
	// Followed says the loop has a review of its own on the pull request,
	// and needs no --include to take it up any more.
	Followed bool
	// Release says the last round held an approval back on the head, for
	// a CI that had not finished: the loop gives it once the CI has passed,
	// rather than run Round again on a head it reviewed.
	Release bool
}

// decideLoop tells whether a pull request needs a review from the loop. A
// pull request is taken up when nobody has reviewed it yet, and followed
// from then on, a new round on each new head, until the loop approves it or
// has run maxRounds rounds. Only the reviews of me are read for marks: anyone
// can write one in a review of their own. An included pull request is taken
// up whoever reviewed it before, and followed like any other from then on.
func decideLoop(pr domain.PullRequest, me string, reviews []domain.ForgeReview, maxRounds int, include bool) loopDecision {
	if strings.EqualFold(pr.Author, me) {
		return loopDecision{Skip: "opened by the authenticated user"}
	}
	if pr.State == "draft" {
		return loopDecision{Skip: loopSkipDraft}
	}
	var last *loopMark
	var lastAt string
	for _, rv := range reviews {
		if !strings.EqualFold(rv.Author, me) {
			continue
		}
		// The highest round is the last one, whatever order the forge
		// lists the reviews in.
		if m, ok := parseLoopMark(rv.Body); ok && (last == nil || m.Round > last.Round || m.Round == last.Round && sameRoundLater(m, rv.SubmittedAt, *last, lastAt)) {
			last, lastAt = &m, rv.SubmittedAt
		}
	}
	if last == nil {
		if len(reviews) > 0 && !include {
			return loopDecision{Skip: loopSkipReviewed}
		}
		return loopDecision{Round: 1}
	}
	switch {
	case last.Verdict == domain.VerdictApprove:
		return loopDecision{Skip: "approved", Followed: true}
	case last.Held != "" && last.Head == pr.Head.SHA:
		// Giving what the round decided is not a round of its own: the
		// last round's approval is given too.
		return loopDecision{Round: last.Round, Previous: last, PreviousAt: lastAt, Followed: true, Release: true}
	case last.Round >= maxRounds:
		return loopDecision{Skip: fmt.Sprintf("%d rounds done", last.Round), Followed: true}
	case last.Head == pr.Head.SHA:
		return loopDecision{Skip: "no change since the last round", Followed: true}
	}
	return loopDecision{Round: last.Round + 1, Previous: last, PreviousAt: lastAt, Followed: true}
}

// sameRoundLater reports whether a mark comes after another of the same
// round: the approval given once the CI passed after the review that held
// it. The submission times decide; when they cannot, because they are
// equal or do not parse, the mark that holds nothing is the later, since a
// release never holds, and the forge's order decides between the others.
// A held mark taken for the last would have the loop give its approval on
// every pass.
func sameRoundLater(m loopMark, at string, last loopMark, lastAt string) bool {
	t, err := time.Parse(time.RFC3339, at)
	o, lastErr := time.Parse(time.RFC3339, lastAt)
	if err == nil && lastErr == nil && !t.Equal(o) {
		return t.After(o)
	}
	if (m.Held == "") != (last.Held == "") {
		return m.Held == ""
	}
	return true
}

type loopFailure struct {
	head  string
	count int
}

// loopSubmission is a review written but not submitted: the forge refused
// it or could not be reached. It is submitted again on the next passes, as
// long as the head has not moved, rather than paid for again.
type loopSubmission struct {
	mark loopMark
	body string
	// attempts counts the submissions refused: a review the forge keeps
	// refusing, a body it will never take, is given up after
	// loopMaxFailures of them.
	attempts int
	// releases is the run whose held verdict the review gives, which is
	// told once it is submitted.
	releases string
}

// Loop reviews the open pull requests waiting for a review, and follows
// them until the loop approves them or has run loop.max_rounds rounds.
func (a *App) Loop(ctx context.Context, req LoopRequest) error {
	cfg := a.Config
	log := a.logger()
	git, err := a.OpenRepository(ctx)
	if err != nil {
		return err
	}
	repo, err := git.Repository(ctx, cfg.Forge.Remote)
	if err != nil {
		return err
	}
	if !factory.MatchesRepository(cfg.Forge, repo) {
		return fmt.Errorf("remote %q points to %s but forge.base_url expects %s", cfg.Forge.Remote, repo.Host, factory.ExpectedHost(cfg.Forge))
	}
	f, err := a.forge()
	if err != nil {
		return err
	}
	rv, err := forge.Reviewer(f)
	if err != nil {
		return err
	}
	me, err := rv.CurrentUser(ctx)
	if err != nil {
		return fmt.Errorf("authenticated user: %w", err)
	}
	runsBase := a.RunsBase
	if runsBase == "" {
		runsBase = filepath.Join(git.CommonDir(), "conclave", "runs")
	}
	// A dry run submits nothing, and labels nothing either.
	if req.DryRun && cfg.Review.RunningLabel != "" {
		dry, dryCfg := *a, *cfg
		dryCfg.Review.RunningLabel = ""
		dry.Config = &dryCfg
		a = &dry
	}
	l := &loop{app: a, forge: f, reviewer: rv, repo: repo, me: me, runsBase: runsBase, req: req,
		failures: map[int64]loopFailure{}, unsent: map[int64]loopSubmission{}, skipped: map[int64]string{},
		include: map[int64]bool{}, unheeded: map[int64]string{}, now: time.Now}
	// A dry run leaves no trace, the waits included.
	if !req.DryRun {
		l.waitFile = filepath.Join(filepath.Dir(runsBase), ciWaitFile)
	}
	l.waiting = loadWaits(l.waitFile)
	for _, n := range req.Include {
		l.include[n] = true
	}
	log.Info("loop started", "repo", repo.FullName(), "user", me, "interval", cfg.Loop.Interval, "max_rounds", cfg.Loop.MaxRounds, "writer", cfg.LoopWriter().ID, "include", req.Include)
	for {
		if err := l.pass(ctx); err != nil {
			if req.Once || req.DryRun || ctx.Err() != nil {
				return err
			}
			log.Warn("loop pass failed", "error", err)
		}
		if req.Once || req.DryRun {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(cfg.Loop.Interval):
		}
	}
}

type loop struct {
	app      *App
	forge    forge.Forge
	reviewer forge.PullRequestReviewer
	repo     domain.Repository
	me       string
	runsBase string
	req      LoopRequest
	failures map[int64]loopFailure
	unsent   map[int64]loopSubmission
	// include is the pull requests of --include the loop has not taken up
	// yet: once it has, its mark is what it follows them by.
	include map[int64]bool
	// unheeded is the reason each included pull request was last reported
	// as not taken up for: it is reported again only when the reason
	// changes, not on every pass.
	unheeded map[int64]string
	// skipped is the head and the reason with which each pull request
	// reviewed by hand or in draft was last logged: it is logged once per
	// head and reason, not on every pass.
	skipped map[int64]string
	// waiting is when the loop started waiting for the CI of each pull
	// request's head, kept in waitFile across loops; waitsChanged says it
	// has to be written again.
	waiting      map[int64]ciWait
	waitFile     string
	waitsChanged bool
	// now is the clock, replaced in tests.
	now func() time.Time
}

// pass goes once over the open pull requests, oldest first.
func (l *loop) pass(ctx context.Context) error {
	log := l.app.logger()
	// Also on the way out of a pass cut short: a wait lost restarts the
	// ci.max_wait clock of the next --once run.
	defer l.saveWaits()
	prs, err := l.reviewer.ListOpenPullRequests(ctx, l.repo)
	if err != nil {
		return fmt.Errorf("list pull requests: %w", err)
	}
	sort.Slice(prs, func(i, j int) bool { return prs[i].Number < prs[j].Number })
	reviewed := 0
	listed := map[int64]bool{}
	for _, pr := range prs {
		listed[pr.Number] = true
		if err := ctx.Err(); err != nil {
			return err
		}
		reviews, err := l.reviewer.ListReviews(ctx, l.repo, pr.Number)
		if err != nil {
			log.Warn("reviews not listed", "number", pr.Number, "error", err)
			continue
		}
		// A review already paid for is submitted before anything else is
		// decided: a review someone else submitted meanwhile would otherwise
		// make the pull request look already reviewed, and lose it.
		if u, ok := l.unsent[pr.Number]; ok {
			if done, submitted := l.retrySubmission(ctx, pr, reviews, u); done {
				if submitted {
					reviewed++
				}
				continue
			}
		}
		d := decideLoop(pr, l.me, reviews, l.app.Config.Loop.MaxRounds, l.include[pr.Number])
		// A pull request left alone is not waiting for its CI: a head the
		// loop already reviewed would otherwise keep its wait for as long
		// as it stays open.
		if _, ok := l.waiting[pr.Number]; ok && d.Skip != "" {
			delete(l.waiting, pr.Number)
			l.waitsChanged = true
		}
		// An included pull request the loop already follows, with no new
		// head for instance, is reported like the others it leaves alone.
		if d.Skip != "" && l.include[pr.Number] {
			l.unheed(pr.Number, d.Skip)
			if d.Followed {
				delete(l.include, pr.Number)
			}
			continue
		}
		if d.Followed {
			delete(l.include, pr.Number)
		}
		if d.Skip != "" {
			level := slog.LevelDebug
			if seen := pr.Head.SHA + " " + d.Skip; (d.Skip == loopSkipReviewed || d.Skip == loopSkipDraft) && l.skipped[pr.Number] != seen {
				l.skipped[pr.Number] = seen
				level = slog.LevelInfo
			}
			log.Log(ctx, level, "pull request skipped", "number", pr.Number, "reason", d.Skip)
			continue
		}
		// A round that succeeds clears the failures on its head: on a
		// release, they are submissions the forge kept refusing or rounds
		// it fell back to, and trying again would end the same way. A
		// release this clone could not write is not counted.
		if fl := l.failures[pr.Number]; fl.head == pr.Head.SHA && fl.count >= loopMaxFailures {
			log.Debug("pull request skipped", "number", pr.Number, "reason", "failed on this head already", "head", pr.Head.SHA)
			continue
		}
		if d.Release {
			var done bool
			done, err = l.release(ctx, pr, d)
			if !done {
				continue
			}
		} else {
			wait, ci := l.waitForCI(ctx, pr)
			if wait {
				continue
			}
			err = l.review(ctx, pr, d, ci)
		}
		reviewed++
		if err != nil {
			if ctx.Err() != nil {
				return err
			}
			fl := l.failures[pr.Number]
			if fl.head != pr.Head.SHA {
				fl = loopFailure{head: pr.Head.SHA}
			}
			fl.count++
			l.failures[pr.Number] = fl
			log.Warn("loop review failed", "number", pr.Number, "round", d.Round, "head", pr.Head.SHA, "failures", fl.count, "error", err)
			continue
		}
		delete(l.failures, pr.Number)
	}
	for number := range l.include {
		if !listed[number] {
			l.unheed(number, "not open")
		}
	}
	for number := range l.skipped {
		if !listed[number] {
			delete(l.skipped, number)
		}
	}
	for number := range l.waiting {
		if !listed[number] {
			delete(l.waiting, number)
			l.waitsChanged = true
		}
	}
	// A pull request closed or merged with a review unsubmitted.
	for number, u := range l.unsent {
		if !listed[number] {
			delete(l.unsent, number)
			log.Warn("review not submitted, given up: the pull request is no longer open", "number", number, "round", u.mark.Round, "run", u.mark.Run)
		}
	}
	log.Info("loop pass done", "open", len(prs), "reviewed", reviewed)
	return nil
}

// waitForCI reports whether the review of the pull request waits for its CI
// to finish on its head: reviewed before, the review could not approve, and
// the loop would only come back on a new head. The wait ends after
// ci.max_wait, and the review then runs anyway. A CI that could not be read
// is not waited for: waiting would not tell more. It also returns the CI it
// read, for the review to use rather than read it again.
func (l *loop) waitForCI(ctx context.Context, pr domain.PullRequest) (bool, *domain.CIStatus) {
	maxWait := l.app.Config.CIMaxWait()
	if maxWait <= 0 {
		return false, nil
	}
	ci := l.app.loadCI(ctx, l.forge, l.repo, pr.Head.SHA)
	if ci == nil || ci.Error != "" || ci.State() != domain.CheckPending {
		if _, ok := l.waiting[pr.Number]; ok {
			delete(l.waiting, pr.Number)
			l.waitsChanged = true
		}
		return false, ci
	}
	log := l.app.logger()
	w, ok := l.waiting[pr.Number]
	if !ok || w.Head != pr.Head.SHA {
		w = ciWait{Head: pr.Head.SHA, Since: l.now()}
		l.waiting[pr.Number] = w
		l.waitsChanged = true
		log.Info("pull request skipped", "number", pr.Number, "reason", loopSkipCI, "checks", checkNames(ci.Blocking(domain.CheckPending)), "max_wait", maxWait)
	}
	if waited := l.now().Sub(w.Since); waited < maxWait {
		log.Debug("pull request skipped", "number", pr.Number, "reason", loopSkipCI, "waited", waited.Round(time.Second))
		return true, nil
	}
	log.Warn("CI still running after ci.max_wait, reviewing without it: the review cannot approve", "number", pr.Number, "checks", checkNames(ci.Blocking(domain.CheckPending)), "max_wait", maxWait)
	return false, ci
}

// unheed reports that an included pull request is not taken up, once until
// the reason changes: the flag asked for it, so it is said without
// --verbose.
func (l *loop) unheed(number int64, reason string) {
	if l.unheeded[number] == reason {
		return
	}
	l.unheeded[number] = reason
	l.app.logger().Warn("included pull request not taken up", "number", number, "reason", reason)
}

// retrySubmission submits again a review the forge refused, and reports
// whether the pull request is done with for this pass. The review is given
// up when the head has moved, since the next round reviews the new one,
// when the forge holds it after all, a refusal that was not one, and after
// loopMaxFailures refusals.
func (l *loop) retrySubmission(ctx context.Context, pr domain.PullRequest, reviews []domain.ForgeReview, u loopSubmission) (done, submitted bool) {
	log := l.app.logger()
	delete(l.unsent, pr.Number)
	switch {
	case u.mark.Head != pr.Head.SHA:
		log.Warn("review not submitted, given up: the head has moved", "number", pr.Number, "round", u.mark.Round, "run", u.mark.Run)
		return false, false
	case pr.State == "draft":
		// A draft is its author asking not to be reviewed yet.
		log.Warn("review not submitted, given up: the pull request is a draft now", "number", pr.Number, "round", u.mark.Round, "run", u.mark.Run)
		return true, false
	}
	for _, rv := range reviews {
		// A review given once the CI passed is of the round it was held
		// on: that round's review is not this one.
		if m, ok := parseLoopMark(rv.Body); ok && strings.EqualFold(rv.Author, l.me) && (m.Round > u.mark.Round || m.Run == u.mark.Run) {
			return false, false
		}
	}
	if err := l.submit(ctx, pr.Number, u); err != nil {
		u.attempts++
		if u.attempts >= loopMaxFailures {
			// Reviewing the head again would only end in the same refusal.
			l.failures[pr.Number] = loopFailure{head: pr.Head.SHA, count: loopMaxFailures}
			log.Warn("review not submitted, given up after repeated refusals until a new head", "number", pr.Number, "round", u.mark.Round, "run", u.mark.Run, "error", err)
			return true, false
		}
		l.unsent[pr.Number] = u
		log.Warn("review not submitted, it is tried again on the next pass", "number", pr.Number, "round", u.mark.Round, "error", err)
		return true, false
	}
	return true, true
}

// review runs one round on a pull request and submits its review.
func (l *loop) review(ctx context.Context, pr domain.PullRequest, d loopDecision, ci *domain.CIStatus) error {
	a := l.app
	cfg := a.Config
	log := a.logger()
	req := ReviewRequest{Number: pr.Number, RequestChangesFrom: domain.Severity(cfg.Loop.RequestChangesFrom), CI: ci}
	if d.Previous != nil {
		if l.hasRun(d.Previous.Run) {
			req.Since = d.Previous.Run
			req.Responses = l.responses(ctx, pr.Number, d.PreviousAt)
		} else {
			log.Warn("previous round not found in this clone, the review is a full one", "number", pr.Number, "run", d.Previous.Run)
		}
	}
	log.Info("loop review", "number", pr.Number, "title", pr.Title, "round", d.Round, "since", req.Since)
	res, err := a.Review(ctx, req)
	if err != nil {
		return err
	}
	return l.publish(ctx, pr, d, res)
}

// release takes up a pull request whose last round held an approval back
// for a CI that had not finished on its head, and reports whether it did
// anything. While the CI runs, the pull request is left alone; once it has
// passed, the round's review is published again with its approval, without
// running the agents on a head they reviewed. A failed CI gets a new round
// while there is one left, for the agents to look for the cause in the
// code; the round's review requests changes otherwise.
func (l *loop) release(ctx context.Context, pr domain.PullRequest, d loopDecision) (bool, error) {
	a := l.app
	cfg := a.Config
	log := a.logger()
	// Not waiting any more: the round ran.
	if _, ok := l.waiting[pr.Number]; ok {
		delete(l.waiting, pr.Number)
		l.waitsChanged = true
	}
	// A forge slow to list the release back would have it given again:
	// the held run says it was, across loops, `--once` from cron included.
	if l.hasRun(d.Previous.Run) {
		if _, err := os.Stat(filepath.Join(l.runsBase, d.Previous.Run, "final", releasedFile)); err == nil {
			log.Debug("pull request skipped", "number", pr.Number, "reason", "held approval given already")
			return false, nil
		}
	}
	// No CI to read, ci.enabled turned off or a forge that reports none,
	// releases the approval: a new round would approve without one too.
	ci := a.loadCI(ctx, l.forge, l.repo, pr.Head.SHA)
	state := ci.State()
	if state == domain.CheckPending {
		log.Debug("pull request skipped", "number", pr.Number, "reason", "approval held until the CI passes")
		return false, nil
	}
	held, err := l.heldReview(pr, d.Previous.Run)
	if err != nil {
		// Without the round's review, missing from this clone or unusable,
		// the agents give the verdict again, in a full review: in a new
		// round while one is left, in the held one run again under its own
		// number otherwise, never under a lower one, which the held mark
		// would outrank on the next pass.
		round := max(d.Round, min(d.Round+1, cfg.Loop.MaxRounds))
		log.Warn("previous round not usable, the agents give the verdict the CI held", "number", pr.Number, "run", d.Previous.Run, "round", round, "error", err)
		return true, l.review(ctx, pr, loopDecision{Round: round, Followed: true}, ci)
	}
	if state == domain.CheckFailure && d.Round < cfg.Loop.MaxRounds {
		log.Info("CI failed on a head whose approval was held, a new round looks for the cause", "number", pr.Number, "checks", checkNames(ci.Blocking(domain.CheckFailure)))
		return true, l.review(ctx, pr, loopDecision{Round: d.Round + 1, Previous: d.Previous, PreviousAt: d.PreviousAt, Followed: true}, ci)
	}
	// A failure to write the release is this clone's, not the previous
	// round's: it is tried again on every pass, costing no agent, rather
	// than paid for with the agents or counted as a failure on the head.
	res, err := l.released(pr, d.Previous.Run, held, ci)
	if err != nil {
		log.Warn("review not released, it is tried again on the next pass", "number", pr.Number, "run", d.Previous.Run, "error", err)
		return false, nil
	}
	log.Info("loop review released", "number", pr.Number, "round", d.Round, "run", d.Previous.Run, "verdict", res.Review.Verdict)
	return true, l.publish(ctx, pr, d, res)
}

// heldReview reads the review of the round that held an approval back: in
// this clone, of this pull request at its head, with a verdict held.
func (l *loop) heldReview(pr domain.PullRequest, previous string) (*domain.ConsolidatedReview, error) {
	if !l.hasRun(previous) {
		return nil, fmt.Errorf("previous round %q not found in this clone", previous)
	}
	data, err := os.ReadFile(filepath.Join(l.runsBase, previous, "final", "review.json"))
	if err != nil {
		return nil, fmt.Errorf("previous round: %w", err)
	}
	var review domain.ConsolidatedReview
	if err := json.Unmarshal(data, &review); err != nil {
		return nil, fmt.Errorf("previous round %s: %w", previous, err)
	}
	if review.Meta.PRNumber != pr.Number || review.Meta.HeadSHA != pr.Head.SHA || review.HeldByCI == "" {
		return nil, fmt.Errorf("previous round %s is not a review of #%d at %s with a verdict held by the CI", previous, pr.Number, pr.Head.SHA)
	}
	return &review, nil
}

// released writes, in a run of its own, the review a previous run held
// with the verdict the CI held and the CI read since. The previous run is
// left as it was: it is what the agents said, and what was published then.
// A run that could not be written whole is removed.
func (l *loop) released(pr domain.PullRequest, previous string, review *domain.ConsolidatedReview, ci *domain.CIStatus) (_ *ReviewResult, err error) {
	cfg := l.app.Config
	started := l.now()
	id := artifact.NewRunID(started, pr.Number)
	store, err := artifact.Open(l.runsBase, id)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(store.Root)
		}
	}()
	// The run's agents are the previous run's: none ran in this one. What
	// ties it to the pull request is set whatever the previous manifest
	// says, or when it cannot be read: `--since last` finds the run by it.
	manifest, _ := runManifest(filepath.Join(l.runsBase, previous))
	manifest.Forge, manifest.Owner, manifest.Repo, manifest.PRNumber, manifest.HeadSHA = l.forge.Name(), l.repo.Owner, l.repo.Name, pr.Number, pr.Head.SHA
	manifest.ID, manifest.StartedAt, manifest.Status, manifest.Error, manifest.Agents = id, started, domain.RunSucceeded, "", nil
	ended := l.now()
	manifest.EndedAt = &ended
	review.Meta.RunID = id
	releaseHeld(review, ci)
	if ci != nil {
		_ = store.WriteJSON("context", "checks.json", ci)
	}
	// The next round is measured against it.
	if err := store.WriteJSON("final", "review.json", review); err != nil {
		return nil, err
	}
	var md bytes.Buffer
	if err := output.Markdown(&md, review, output.Options{ShowAttribution: cfg.ShowAttribution(), ShowFailedAgents: cfg.ShowFailedAgents()}); err == nil {
		_ = store.Write("final", "review.md", md.Bytes())
	}
	if err := store.WriteManifest(&manifest); err != nil {
		return nil, err
	}
	return &ReviewResult{Review: review, RunDir: store.Root, Manifest: &manifest}, nil
}

// publish has the writer turn a review into a comment and submits it, with
// the loop's mark.
func (l *loop) publish(ctx context.Context, pr domain.PullRequest, d loopDecision, res *ReviewResult) error {
	a := l.app
	cfg := a.Config
	log := a.logger()
	from := domain.Severity(cfg.Loop.RequestChangesFrom)
	review := res.Review
	body := ""
	comment, err := a.WriteComment(ctx, CommentRequest{Result: res, PR: &pr, Round: d.Round, MaxRounds: cfg.Loop.MaxRounds, RequestChangesFrom: from, Released: d.Release})
	if err != nil {
		if ctx.Err() != nil {
			return err
		}
		// The review is done and paid for: it is published as Conclave
		// renders it rather than lost.
		log.Warn("writer failed, the review is published as Conclave renders it", "number", pr.Number, "error", err)
		var md bytes.Buffer
		if err := output.Markdown(&md, authorView(review), output.Options{}); err != nil {
			return err
		}
		// The whole review is the longest body the loop submits: capped like
		// the writer's, or the forge refuses it.
		notice := roundNotice(d.Round, cfg.Loop.MaxRounds, review.Verdict)
		if d.Release {
			notice = "The CI has finished since the previous comment, which published this review without the verdict it held back. " + notice
		}
		body = agent.TruncateField(notice+"\n\n"+md.String(), commentBytes(cfg))
	} else {
		for _, w := range comment.Warnings {
			log.Warn("writer", "number", pr.Number, "warning", w)
		}
		body = comment.Body
	}
	mark := loopMark{Round: d.Round, Head: review.Meta.HeadSHA, Run: review.Meta.RunID, Verdict: review.Verdict, Held: review.HeldByCI}
	// The comment was cut to leave the mark its room: the body the forge
	// receives is the one that was checked.
	body += "\n\n" + mark.String() + "\n"
	if l.req.DryRun {
		out := l.req.Out
		if out == nil {
			out = os.Stdout
		}
		_, err := fmt.Fprintf(out, "<!-- pull request #%d, round %d, verdict %s, not submitted -->\n%s\n", pr.Number, d.Round, review.Verdict, body)
		return err
	}
	u := loopSubmission{mark: mark, body: body, attempts: 1}
	if d.Release {
		u.releases = d.Previous.Run
	}
	if err := l.submit(ctx, pr.Number, u); err != nil {
		// The review is done: only the submission is tried again.
		l.unsent[pr.Number] = u
		log.Warn("review not submitted, it is tried again on the next pass", "number", pr.Number, "round", d.Round, "run", res.RunDir, "error", err)
	}
	return nil
}

// submit submits a review the loop wrote.
func (l *loop) submit(ctx context.Context, number int64, u loopSubmission) error {
	if err := l.reviewer.SubmitReview(ctx, l.repo, number, u.mark.Head, u.mark.Verdict, u.body); err != nil {
		return fmt.Errorf("submit review: %w", err)
	}
	// The mark is on the forge: the loop follows the pull request by it.
	delete(l.include, number)
	// The run keeps what the author reads, once it is theirs to read.
	if store, err := artifact.Open(l.runsBase, u.mark.Run); err == nil {
		_ = store.Write("final", "comment.md", []byte(u.body))
	}
	if u.releases != "" && l.hasRun(u.releases) {
		data, _ := json.Marshal(map[string]string{"run": u.mark.Run})
		if err := os.WriteFile(filepath.Join(l.runsBase, u.releases, "final", releasedFile), data, 0o644); err != nil {
			l.app.logger().Warn("release not recorded, a forge slow to list it may have it given again", "number", number, "run", u.releases, "error", err)
		}
	}
	l.app.logger().Info("review submitted", "number", number, "round", u.mark.Round, "verdict", u.mark.Verdict, "run", u.mark.Run)
	return nil
}

// authorView is the review without how it was produced, which the writer
// is told to leave out: agent ids, models, run ids and the run's warnings.
func authorView(review *domain.ConsolidatedReview) *domain.ConsolidatedReview {
	v := *review
	v.Warnings, v.FailedReviewers = nil, nil
	v.Meta.Models, v.Meta.RunID = nil, ""
	if v.Meta.Since != nil {
		since := *v.Meta.Since
		since.RunID = ""
		v.Meta.Since = &since
	}
	return &v
}

// roundNotice is what the writer is asked to tell the author about the
// round, for a review published without it.
func roundNotice(round, maxRounds int, verdict domain.Verdict) string {
	notice := fmt.Sprintf("Automatic review %d of at most %d.", round, maxRounds)
	if round >= maxRounds && verdict != domain.VerdictApprove {
		notice += " It is the last one: no further automatic review will follow, a maintainer takes it from here."
	}
	return notice
}

var runIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// hasRun reports whether the run a mark names is in this clone. The name
// comes from a review body: it is checked to be a plain run id before it
// becomes a path.
func (l *loop) hasRun(id string) bool {
	if !runIDRe.MatchString(id) {
		return false
	}
	_, err := os.Stat(filepath.Join(l.runsBase, id, "final", "review.json"))
	return err == nil
}

// responses gathers what others wrote on the pull request since the loop's
// last review: the author's answers, which an incremental review weighs.
func (l *loop) responses(ctx context.Context, number int64, since string) string {
	cfg := l.app.Config
	discussion, err := l.forge.ListDiscussion(ctx, l.repo, number, cfg.Review.Limits.MaxComments)
	if err != nil {
		l.app.logger().Warn("responses not loaded", "number", number, "error", err)
		return ""
	}
	// A time that does not parse leaves the window open rather than shut:
	// the review cuts the responses to its limit, and an answer weighed
	// twice costs less than one never read.
	sinceTime, sinceErr := time.Parse(time.RFC3339, since)
	if sinceErr != nil {
		l.app.logger().Warn("previous round without a submission time, every comment is passed as a response", "number", number, "submitted_at", since)
	}
	// The newest answers are the ones the round is for: they are collected
	// first, each within the room left, and given oldest first, within the
	// review's own cap.
	room := cfg.Review.Limits.MaxCommentBytes
	var entries []string
	for i := len(discussion) - 1; i >= 0 && room > 0; i-- {
		c := discussion[i]
		if strings.EqualFold(c.Author, l.me) || strings.TrimSpace(c.Body) == "" {
			continue
		}
		if at, err := time.Parse(time.RFC3339, c.CreatedAt); sinceErr == nil && err == nil && !at.After(sinceTime) {
			continue
		}
		where := ""
		if c.Path != "" {
			where = fmt.Sprintf(" on %s:%d", c.Path, c.Line)
		}
		head := fmt.Sprintf("%s (%s%s, %s):\n", c.Author, c.Kind, where, c.CreatedAt)
		// One long answer does not crowd out the others: each gets at
		// most a quarter of the cap.
		// The ellipsis of a cut and the separator count too: the review
		// cuts what is over its limit from the end, where the newest
		// answer is.
		const extra = len("…") + len("\n\n")
		if len(head)+extra >= room {
			break
		}
		entry := head + agent.TruncateField(c.Body, min(room-len(head)-extra, max(cfg.Review.Limits.MaxCommentBytes/4, 1))) + "\n\n"
		room -= len(entry)
		entries = append(entries, entry)
	}
	slices.Reverse(entries)
	b := strings.Join(entries, "")
	return strings.TrimSpace(b)
}
