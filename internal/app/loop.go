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
		if m, ok := parseLoopMark(rv.Body); ok {
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
	l := &loop{app: a, forge: f, reviewer: rv, repo: repo, me: me, runsBase: runsBase, req: req, failures: map[int64]loopFailure{}}
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
	for _, pr := range prs {
		if err := ctx.Err(); err != nil {
			return err
		}
		reviews, err := l.reviewer.ListReviews(ctx, l.repo, pr.Number)
		if err != nil {
			log.Warn("reviews not listed", "number", pr.Number, "error", err)
			continue
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
	log.Info("loop pass done", "open", len(prs), "reviewed", reviewed)
	return nil
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
		body = strings.TrimSpace(md.String())
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
	if err := l.reviewer.SubmitReview(ctx, l.repo, pr.Number, review.Meta.HeadSHA, review.Verdict, body); err != nil {
		return fmt.Errorf("submit review: %w (run %s)", err, res.RunDir)
	}
	log.Info("review submitted", "number", pr.Number, "round", d.Round, "verdict", review.Verdict, "run", review.Meta.RunID)
	return nil
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
	var b strings.Builder
	for _, c := range discussion {
		if strings.EqualFold(c.Author, l.me) || c.CreatedAt <= since || strings.TrimSpace(c.Body) == "" {
			continue
		}
		where := ""
		if c.Path != "" {
			where = fmt.Sprintf(" on %s:%d", c.Path, c.Line)
		}
		fmt.Fprintf(&b, "%s (%s%s, %s):\n%s\n\n", c.Author, c.Kind, where, c.CreatedAt, strings.TrimSpace(c.Body))
	}
	return strings.TrimSpace(b.String())
}
