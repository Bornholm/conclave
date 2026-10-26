package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/bornholm/conclave/internal/agent"
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
	Out    io.Writer
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
}

// decideLoop tells whether a pull request needs a review from the loop. A
// pull request is taken up when nobody has reviewed it yet, and followed
// from then on, a new round on each new head, until the loop approves it or
// has run maxRounds rounds. Only the reviews of me are read for marks: anyone
// can write one in a review of their own.
func decideLoop(pr domain.PullRequest, me string, reviews []domain.ForgeReview, maxRounds int) loopDecision {
	if strings.EqualFold(pr.Author, me) {
		return loopDecision{Skip: "opened by the authenticated user"}
	}
	if pr.State == "draft" {
		return loopDecision{Skip: "draft"}
	}
	var last *loopMark
	var lastAt string
	for _, rv := range reviews {
		if !strings.EqualFold(rv.Author, me) {
			continue
		}
		// The highest round is the last one, whatever order the forge
		// lists the reviews in.
		if m, ok := parseLoopMark(rv.Body); ok && (last == nil || m.Round >= last.Round) {
			last, lastAt = &m, rv.SubmittedAt
		}
	}
	if last == nil {
		if len(reviews) > 0 {
			return loopDecision{Skip: "already reviewed"}
		}
		return loopDecision{Round: 1}
	}
	switch {
	case last.Verdict == domain.VerdictApprove:
		return loopDecision{Skip: "approved"}
	case last.Round >= maxRounds:
		return loopDecision{Skip: fmt.Sprintf("%d rounds done", last.Round)}
	case last.Head == pr.Head.SHA:
		return loopDecision{Skip: "no change since the last round"}
	}
	return loopDecision{Round: last.Round + 1, Previous: last, PreviousAt: lastAt}
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
		failures: map[int64]loopFailure{}, unsent: map[int64]loopSubmission{}}
	log.Info("loop started", "repo", repo.FullName(), "user", me, "interval", cfg.Loop.Interval, "max_rounds", cfg.Loop.MaxRounds, "writer", cfg.LoopWriter().ID)
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
}

// pass goes once over the open pull requests, oldest first.
func (l *loop) pass(ctx context.Context) error {
	log := l.app.logger()
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
			if l.retrySubmission(ctx, pr, reviews, u) {
				reviewed++
				continue
			}
		}
		d := decideLoop(pr, l.me, reviews, l.app.Config.Loop.MaxRounds)
		if d.Skip != "" {
			log.Debug("pull request skipped", "number", pr.Number, "reason", d.Skip)
			continue
		}
		if fl := l.failures[pr.Number]; fl.head == pr.Head.SHA && fl.count >= loopMaxFailures {
			log.Debug("pull request skipped", "number", pr.Number, "reason", "failed on this head already", "head", pr.Head.SHA)
			continue
		}
		reviewed++
		if err := l.review(ctx, pr, d); err != nil {
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

// retrySubmission submits again a review the forge refused, and reports
// whether the pull request is done with for this pass. The review is given
// up when the head has moved, since the next round reviews the new one,
// when the forge holds it after all, a refusal that was not one, and after
// loopMaxFailures refusals.
func (l *loop) retrySubmission(ctx context.Context, pr domain.PullRequest, reviews []domain.ForgeReview, u loopSubmission) bool {
	log := l.app.logger()
	delete(l.unsent, pr.Number)
	if u.mark.Head != pr.Head.SHA {
		log.Warn("review not submitted, given up: the head has moved", "number", pr.Number, "round", u.mark.Round, "run", u.mark.Run)
		return false
	}
	for _, rv := range reviews {
		if m, ok := parseLoopMark(rv.Body); ok && strings.EqualFold(rv.Author, l.me) && m.Round >= u.mark.Round {
			return false
		}
	}
	if err := l.submit(ctx, pr.Number, u); err != nil {
		u.attempts++
		if u.attempts >= loopMaxFailures {
			// Reviewing the head again would only end in the same refusal.
			l.failures[pr.Number] = loopFailure{head: pr.Head.SHA, count: loopMaxFailures}
			log.Warn("review not submitted, given up after repeated refusals until a new head", "number", pr.Number, "round", u.mark.Round, "run", u.mark.Run, "error", err)
			return true
		}
		l.unsent[pr.Number] = u
		log.Warn("review not submitted, it is tried again on the next pass", "number", pr.Number, "round", u.mark.Round, "error", err)
	}
	return true
}

// review runs one round on a pull request and submits its review.
func (l *loop) review(ctx context.Context, pr domain.PullRequest, d loopDecision) error {
	a := l.app
	cfg := a.Config
	log := a.logger()
	req := ReviewRequest{Number: pr.Number}
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
	review := res.Review
	body := ""
	comment, err := a.WriteComment(ctx, CommentRequest{Result: res, PR: &pr, Round: d.Round, MaxRounds: cfg.Loop.MaxRounds})
	if err != nil {
		if ctx.Err() != nil {
			return err
		}
		// The review is done and paid for: it is published as Conclave
		// renders it rather than lost.
		log.Warn("writer failed, the review is published as Conclave renders it", "number", pr.Number, "error", err)
		var md bytes.Buffer
		if err := output.Markdown(&md, review, output.Options{}); err != nil {
			return err
		}
		// The whole review is the longest body the loop submits: capped like
		// the writer's, or the forge refuses it.
		body = agent.TruncateField(roundNotice(d.Round, cfg.Loop.MaxRounds, review.Verdict)+"\n\n"+md.String(), cfg.Loop.MaxCommentBytes)
	} else {
		for _, w := range comment.Warnings {
			log.Warn("writer", "number", pr.Number, "warning", w)
		}
		body = comment.Body
	}
	mark := loopMark{Round: d.Round, Head: review.Meta.HeadSHA, Run: review.Meta.RunID, Verdict: review.Verdict}
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
	l.app.logger().Info("review submitted", "number", number, "round", u.mark.Round, "verdict", u.mark.Verdict, "run", u.mark.Run)
	return nil
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
	max := cfg.Review.Limits.MaxCommentBytes
	var b strings.Builder
	for _, c := range discussion {
		// Review.limits.max_comment_bytes cuts the responses, with a
		// warning: one byte past it is all that has to be collected.
		if b.Len() > max {
			break
		}
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
		fmt.Fprintf(&b, "%s (%s%s, %s):\n%s\n\n", c.Author, c.Kind, where, c.CreatedAt, agent.TruncateField(c.Body, max))
	}
	return strings.TrimSpace(b.String())
}
