package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/bornholm/conclave/internal/agent"
	"github.com/bornholm/conclave/internal/artifact"
	"github.com/bornholm/conclave/internal/config"
	"github.com/bornholm/conclave/internal/consolidation"
	"github.com/bornholm/conclave/internal/domain"
	"github.com/bornholm/conclave/internal/forge"
	"github.com/bornholm/conclave/internal/forge/factory"
	"github.com/bornholm/conclave/internal/gitrepo"
	"github.com/bornholm/conclave/internal/output"
	"github.com/bornholm/conclave/internal/prompt"
)

// ReviewRequest parameterizes a run.
type ReviewRequest struct {
	Number        int64
	KeepWorktrees bool
}

// ReviewResult carries the consolidated review and where artifacts live.
type ReviewResult struct {
	Review   *domain.ConsolidatedReview
	RunDir   string
	Manifest *domain.RunManifest
}

type run struct {
	cfg       *config.Config
	git       *gitrepo.Git
	pr        *domain.PullRequest
	store     *artifact.Store
	manifest  *domain.RunManifest
	wtBase    string
	diff      []byte
	diffTrunc bool
	changed   map[string]bool
	runner    *agent.Runner
}

// Review executes the full pipeline for one pull request.
func (a *App) Review(ctx context.Context, req ReviewRequest) (*ReviewResult, error) {
	cfg := a.Config
	log := a.logger()
	if cfg.Review.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.Review.Timeout)
		defer cancel()
	}

	git, err := a.OpenRepository(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := git.Repository(ctx, cfg.Forge.Remote)
	if err != nil {
		return nil, err
	}
	if !factory.MatchesRepository(cfg.Forge, repo) {
		return nil, fmt.Errorf("remote %q points to %s but forge.base_url expects %s", cfg.Forge.Remote, repo.Host, factory.ExpectedHost(cfg.Forge))
	}
	f, err := a.forge()
	if err != nil {
		return nil, err
	}
	log.Info("fetching pull request", "forge", f.Name(), "repo", repo.FullName(), "number", req.Number)
	pr, err := f.GetPullRequest(ctx, repo, req.Number)
	if err != nil {
		if errors.Is(err, forge.ErrNotFound) {
			if hint := a.forkHint(ctx, f, repo, "pull request"); hint != "" {
				return nil, fmt.Errorf("get pull request #%d: %w\n%s", req.Number, err, hint)
			}
		}
		return nil, fmt.Errorf("get pull request #%d: %w", req.Number, err)
	}
	if label := cfg.Review.RunningLabel; label != "" {
		defer a.markRunning(ctx, f, repo, pr.Number, label)()
	}
	files, err := f.ListChangedFiles(ctx, repo, req.Number, cfg.Review.Limits.MaxFiles)
	if err != nil {
		return nil, fmt.Errorf("list changed files: %w", err)
	}
	pr.ChangedFiles = files
	if cfg.IncludeDiscussion() {
		discussion, err := f.ListDiscussion(ctx, repo, req.Number, cfg.Review.Limits.MaxComments)
		if err != nil {
			log.Warn("discussion not loaded", "error", err)
		}
		for i := range discussion {
			discussion[i].Body = truncateBytes(discussion[i].Body, cfg.Review.Limits.MaxCommentBytes)
		}
		pr.Discussion = discussion
	}
	if cfg.IncludeIssues() {
		issues, err := forge.LoadAssociatedIssues(ctx, f, repo, pr, cfg.Review.Limits.MaxIssues)
		if err != nil {
			log.Warn("associated issues partially loaded", "error", err)
		}
		for i := range issues {
			issues[i].Description = truncateBytes(issues[i].Description, cfg.Review.Limits.MaxCommentBytes)
		}
		pr.Issues = issues
	}

	log.Info("ensuring commits", "base", pr.Base.SHA, "head", pr.Head.SHA)
	if err := git.EnsureCommit(ctx, pr.Base.SHA, "", cfg.Forge.Remote, pr.Base.CloneURL); err != nil {
		return nil, fmt.Errorf("base commit: %w", err)
	}
	if err := git.EnsureCommit(ctx, pr.Head.SHA, gitrepo.PullRequestRef(pr.Number), cfg.Forge.Remote, pr.Head.CloneURL); err != nil {
		return nil, fmt.Errorf("head commit: %w", err)
	}
	mergeBase, err := git.MergeBase(ctx, pr.Base.SHA, pr.Head.SHA)
	if err != nil {
		return nil, err
	}
	pr.MergeBaseSHA = mergeBase

	now := time.Now()
	runID := artifact.NewRunID(now, pr.Number)
	runsBase := a.RunsBase
	if runsBase == "" {
		runsBase = filepath.Join(git.CommonDir(), "conclave", "runs")
	}
	store, err := artifact.Open(runsBase, runID)
	if err != nil {
		return nil, err
	}
	wtBase := a.WorktreeBase
	if wtBase == "" {
		wtBase = filepath.Join(os.TempDir(), "conclave")
	}
	runTmp := filepath.Join(wtBase, runID, "tmp")
	if err := os.MkdirAll(runTmp, 0o755); err != nil {
		return nil, fmt.Errorf("create temporary directory: %w", err)
	}
	r := &run{
		cfg: cfg, git: git, pr: pr, store: store,
		wtBase:  filepath.Join(wtBase, runID),
		changed: agent.ChangedSet(pr.ChangedFiles),
		runner:  &agent.Runner{Process: a.process(), TempDir: runTmp},
		manifest: &domain.RunManifest{
			ID: runID, StartedAt: now, Status: domain.RunRunning,
			Forge: f.Name(), Owner: repo.Owner, Repo: repo.Name, PRNumber: pr.Number,
			BaseSHA: pr.Base.SHA, HeadSHA: pr.Head.SHA, MergeBaseSHA: mergeBase,
		},
	}
	log.Info("run started", "run", runID, "dir", store.Root, "merge_base", mergeBase)
	_ = store.WriteManifest(r.manifest)
	_ = store.WriteJSON("context", "pull-request.json", pr)
	_ = store.WriteJSON("context", "changed-files.json", pr.ChangedFiles)
	_ = store.WriteJSON("context", "associated-issues.json", pr.Issues)
	_ = store.WriteJSON("context", "discussion.json", pr.Discussion)

	if cfg.IncludeDiff() {
		r.diff, r.diffTrunc, err = git.Diff(ctx, mergeBase, pr.Head.SHA, cfg.Review.Limits.MaxDiffBytes, cfg.Review.DiffExclude...)
		if err != nil {
			return nil, err
		}
		_ = store.Write("context", "diff.patch", r.diff)
	}

	review, runErr := a.execute(ctx, r, req)
	end := time.Now()
	r.manifest.EndedAt = &end
	if runErr != nil {
		r.manifest.Status = domain.RunFailed
		r.manifest.Error = runErr.Error()
	} else {
		r.manifest.Status = domain.RunSucceeded
	}
	_ = store.WriteManifest(r.manifest)
	if runErr != nil {
		return &ReviewResult{RunDir: store.Root, Manifest: r.manifest}, runErr
	}
	_ = store.WriteJSON("final", "review.json", review)
	var md bytes.Buffer
	if err := output.Markdown(&md, review, output.Options{ShowAttribution: cfg.ShowAttribution(), ShowFailedAgents: cfg.ShowFailedAgents()}); err == nil {
		_ = store.Write("final", "review.md", md.Bytes())
	}
	return &ReviewResult{Review: review, RunDir: store.Root, Manifest: r.manifest}, nil
}

// runningLabelTimeout bounds the call that takes the running label off: it
// runs once the review is over, possibly after an interruption.
const runningLabelTimeout = 30 * time.Second

// markRunning puts the label on the pull request and returns the function
// that takes it off. Neither call fails the review: the label is a signal to
// humans, not part of the result. The removal runs on a context detached from
// ctx, which is already cancelled when the run was interrupted or timed out.
func (a *App) markRunning(ctx context.Context, f forge.Forge, repo domain.Repository, number int64, label string) func() {
	log := a.logger()
	if err := forge.AddPullRequestLabel(ctx, f, repo, number, label); err != nil {
		log.Warn("running label not added", "label", label, "error", err)
		if errors.Is(err, forge.ErrLabelsUnsupported) {
			return func() {}
		}
		// The label may have been attached before the error: removing it
		// costs one call and leaves no stale flag behind.
	}
	return func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), runningLabelTimeout)
		defer cancel()
		if err := forge.RemovePullRequestLabel(ctx, f, repo, number, label); err != nil {
			log.Warn("running label not removed", "label", label, "error", err)
		}
	}
}

func (a *App) execute(ctx context.Context, r *run, req ReviewRequest) (*domain.ConsolidatedReview, error) {
	log := a.logger()
	cfg := r.cfg
	reviewers := cfg.Reviewers()
	lead := cfg.Lead()
	keep := req.KeepWorktrees || cfg.Review.KeepWorktrees

	// Create every worktree up front so a failure is reported before agents run.
	worktrees := map[string]string{}
	all := append(append([]config.AgentConfig{}, reviewers...), lead)
	for _, ag := range all {
		path := filepath.Join(r.wtBase, ag.ID)
		if err := r.git.CreateWorktree(ctx, path, r.pr.Head.SHA); err != nil {
			a.cleanupWorktrees(r, worktrees, false)
			return nil, fmt.Errorf("create worktree for %s: %w", ag.ID, err)
		}
		worktrees[ag.ID] = path
	}
	defer a.cleanupWorktrees(r, worktrees, keep)

	outcomes, err := a.runReviewers(ctx, r, reviewers, worktrees)
	if err != nil {
		return nil, err
	}
	var succeeded []string
	var reports []domain.AgentReport
	for _, o := range outcomes {
		if o.Report != nil {
			succeeded = append(succeeded, o.ID)
			reports = append(reports, *o.Report)
		}
	}
	if len(succeeded) == 0 {
		return nil, fmt.Errorf("all %d reviewers failed: %w", len(reviewers), errors.Join(outcomeErrors(outcomes)...))
	}
	normalized := consolidation.Normalize(outcomes)
	groups := consolidation.Group(normalized)
	_ = r.store.WriteJSON("final", "groups.json", groups)

	meta := domain.ReviewMeta{
		RunID: r.manifest.ID, PRNumber: r.pr.Number, PRTitle: r.pr.Title, PRURL: r.pr.WebURL,
		HeadSHA: r.pr.Head.SHA, MergeBaseSHA: r.pr.MergeBaseSHA,
		ReviewersTotal: len(reviewers), ReviewersSucceeded: len(succeeded),
		LeadID: lead.ID, SucceededReviewers: succeeded,
	}
	var warnings []string
	meta.Models = map[string]string{}
	for _, ex := range r.manifest.Agents {
		for _, w := range ex.Warnings {
			warnings = append(warnings, ex.ID+": "+w)
		}
		if ex.Model != "" {
			meta.Models[ex.ID] = ex.Model
		}
	}

	review, leadWarnings, err := a.runLead(ctx, r, lead, worktrees[lead.ID], outcomes, succeeded, reports, groups)
	warnings = append(warnings, leadWarnings...)
	if err != nil {
		log.Warn("lead failed, using deterministic consolidation", "lead", lead.ID, "error", err)
		warnings = append(warnings, fmt.Sprintf("lead %s failed: %v", lead.ID, err))
		review = consolidation.Fallback(groups, outcomes)
	} else {
		meta.LeadUsed = true
	}
	if last := r.manifest.Agents[len(r.manifest.Agents)-1]; last.ID == lead.ID && last.Model != "" {
		meta.Models[lead.ID] = last.Model
	}
	review.Meta = meta
	review.Warnings = warnings
	return review, nil
}

// TruncationMarker is appended to text conclave had to cut.
const TruncationMarker = "\n[truncated by conclave]"

// truncateBytes cuts s to max bytes on a rune boundary. It matters most to
// `ask`, whose input is arbitrary text up to half a megabyte.
func truncateBytes(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return agent.CutRunes(s, max) + TruncationMarker
}

func outcomeErrors(outcomes []consolidation.ReviewerOutcome) []error {
	var errs []error
	for _, o := range outcomes {
		if o.Err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", o.ID, o.Err))
		}
	}
	return errs
}

func (a *App) runReviewers(ctx context.Context, r *run, reviewers []config.AgentConfig, worktrees map[string]string) ([]consolidation.ReviewerOutcome, error) {
	log := a.logger()
	outcomes := make([]consolidation.ReviewerOutcome, len(reviewers))
	execs := make([]domain.AgentExecution, len(reviewers))
	group, gctx := errgroup.WithContext(ctx)
	group.SetLimit(r.cfg.Review.MaxParallel)
	for i, rv := range reviewers {
		group.Go(func() error {
			log.Info("reviewer started", "agent", rv.ID)
			ex, report, err := a.runReviewer(gctx, r, rv, worktrees[rv.ID])
			outcomes[i] = consolidation.ReviewerOutcome{ID: rv.ID, Report: report, Err: err}
			execs[i] = ex
			if err != nil {
				log.Warn("reviewer failed", "agent", rv.ID, "error", err, "duration", ex.Duration.Round(time.Millisecond))
				if r.cfg.Review.FailFast {
					return fmt.Errorf("%s: %w", rv.ID, err)
				}
				return nil
			}
			log.Info("reviewer finished", "agent", rv.ID, "findings", len(report.Findings), "duration", ex.Duration.Round(time.Millisecond))
			return nil
		})
	}
	err := group.Wait()
	r.manifest.Agents = append(r.manifest.Agents, execs...)
	_ = r.store.WriteManifest(r.manifest)
	if err != nil {
		return outcomes, fmt.Errorf("fail_fast: %w", err)
	}
	if ctx.Err() != nil {
		return outcomes, ctx.Err()
	}
	return outcomes, nil
}

func (a *App) runReviewer(ctx context.Context, r *run, rv config.AgentConfig, worktree string) (domain.AgentExecution, *domain.AgentReport, error) {
	ex := domain.AgentExecution{ID: rv.ID, Role: rv.Role, Worktree: worktree}
	p, err := prompt.Reviewer(prompt.ReviewerInput{
		AgentID: rv.ID, Worktree: worktree, Specialties: rv.Specialties, PR: r.pr,
		MergeBaseSHA: r.pr.MergeBaseSHA, HeadSHA: r.pr.Head.SHA,
		IncludeFiles: r.cfg.IncludeChangedFiles(), Diff: string(r.diff), DiffTruncated: r.diffTrunc,
		DiffExcluded: r.cfg.Review.DiffExclude, MaxFindings: r.cfg.Review.Limits.MaxFindings,
	})
	if err != nil {
		ex.Error = err.Error()
		return ex, nil, err
	}
	report, transcript, err := a.reviewerAttempt(ctx, r, rv, worktree, rv.ID, p, &ex)
	// A reviewer that ran to its end but left no usable report is asked once
	// more for the report alone, with what it wrote. Timeouts and crashes are
	// not retried: they would most likely happen again.
	for attempt := 1; isNoUsableReport(err) && attempt <= r.cfg.ReviewReportRetries() && ctx.Err() == nil; attempt++ {
		name := fmt.Sprintf("%s.retry%d", rv.ID, attempt)
		a.logger().Warn("reviewer returned no usable report, asking again", "agent", rv.ID, "attempt", attempt, "reason", ex.Error)
		retry, perr := prompt.ReviewerRetry(prompt.ReviewerRetryInput{
			AgentID: rv.ID, Worktree: worktree, PR: r.pr, MergeBaseSHA: r.pr.MergeBaseSHA, HeadSHA: r.pr.Head.SHA,
			Problem: "Conclave could not use it: " + ex.Error + ".", Transcript: string(transcript),
			MaxFindings: r.cfg.Review.Limits.MaxFindings,
		})
		if perr != nil {
			break
		}
		ex.ReportRetries = attempt
		ex.Error = ""
		var more []byte
		report, more, err = a.reviewerAttempt(ctx, r, rv, worktree, name, retry, &ex)
		if len(more) > 0 {
			transcript = more
		}
	}
	if err != nil {
		return ex, nil, err
	}
	ex.Succeeded = true
	_ = r.store.WriteJSON("reports", rv.ID+".json", report)
	return ex, report, nil
}

// noUsableReport marks a run that ended normally but produced no report
// Conclave can use: no text, no JSON, or a JSON that fails validation. It
// keeps the message of the error it wraps, which the report shows.
type noUsableReport struct{ err error }

func (e noUsableReport) Error() string { return e.err.Error() }
func (e noUsableReport) Unwrap() error { return e.err }

func isNoUsableReport(err error) bool {
	var target noUsableReport
	return errors.As(err, &target)
}

// reviewerAttempt runs a reviewer once with the given prompt, storing its
// prompt and raw output under name. It returns the report, or the agent's
// transcript for a retry when the run produced no usable report.
func (a *App) reviewerAttempt(ctx context.Context, r *run, rv config.AgentConfig, worktree, name string, p []byte, ex *domain.AgentExecution) (*domain.AgentReport, []byte, error) {
	_ = r.store.Write("prompts", "reviewer-"+name+".txt", p)
	res := r.runner.Run(ctx, r.cfg, rv, worktree, p)
	ex.Duration += res.Duration
	if res.Result != nil {
		ex.ExitCode = res.Result.ExitCode
		_ = r.store.Write("raw", name+".stdout", res.Result.Stdout)
		_ = r.store.Write("raw", name+".stderr", res.Result.Stderr)
	}
	if res.Err != nil {
		ex.Error = res.Err.Error()
		return nil, nil, res.Err
	}
	stored := rv
	stored.ID = name
	ad, err := a.adapt(r, stored, res.Result.Stdout, ex)
	if err != nil {
		var transcript []byte
		if ad != nil {
			transcript = ad.Transcript
		}
		return nil, transcript, noUsableReport{err}
	}
	exists := func(p string) bool { return r.git.PathExistsAt(ctx, r.pr.Head.SHA, p) }
	report, warnings, err := agent.ParseReport(ad.Report, rv.ID, r.changed, exists, agent.Limits{MaxFindings: r.cfg.Review.Limits.MaxFindings, MaxFieldBytes: agent.DefaultLimits.MaxFieldBytes})
	ex.Warnings = append(ex.Warnings, warnings...)
	if err != nil {
		ex.Error = err.Error()
		return nil, ad.Transcript, noUsableReport{fmt.Errorf("invalid report: %w", err)}
	}
	// The self-reported model is untrusted: prefer configuration, then detection.
	report.Reviewer.Model = ex.Model
	return report, nil, nil
}

func (a *App) runLead(ctx context.Context, r *run, lead config.AgentConfig, worktree string, outcomes []consolidation.ReviewerOutcome, succeeded []string, reports []domain.AgentReport, groups []domain.FindingGroup) (*domain.ConsolidatedReview, []string, error) {
	log := a.logger()
	ex := domain.AgentExecution{ID: lead.ID, Role: lead.Role, Worktree: worktree}
	defer func() {
		r.manifest.Agents = append(r.manifest.Agents, ex)
		_ = r.store.WriteManifest(r.manifest)
	}()
	p, err := prompt.Lead(prompt.LeadInput{
		AgentID: lead.ID, Worktree: worktree, PR: r.pr, MergeBaseSHA: r.pr.MergeBaseSHA, HeadSHA: r.pr.Head.SHA,
		SucceededReviewers: succeeded, FailedReviewers: consolidation.FailedReviewers(outcomes),
		Reports: reports, Groups: groups, MaxFindings: r.cfg.Review.Limits.MaxFindings,
	})
	if err != nil {
		ex.Error = err.Error()
		return nil, nil, err
	}
	_ = r.store.Write("prompts", "lead.txt", p)
	log.Info("lead started", "agent", lead.ID)
	res := r.runner.Run(ctx, r.cfg, lead, worktree, p)
	ex.Duration = res.Duration
	if res.Result != nil {
		ex.ExitCode = res.Result.ExitCode
		_ = r.store.Write("raw", lead.ID+".stdout", res.Result.Stdout)
		_ = r.store.Write("raw", lead.ID+".stderr", res.Result.Stderr)
	}
	if res.Err != nil {
		ex.Error = res.Err.Error()
		return nil, nil, res.Err
	}
	ad, err := a.adapt(r, lead, res.Result.Stdout, &ex)
	if err != nil {
		return nil, nil, err
	}
	exists := func(p string) bool { return r.git.PathExistsAt(ctx, r.pr.Head.SHA, p) }
	review, warnings, err := consolidation.ParseLeadReview(ad.Report, succeeded, outcomes, r.changed, exists, r.cfg.Review.Limits.MaxFindings)
	ex.Warnings = warnings
	if err != nil {
		ex.Error = err.Error()
		return nil, warnings, fmt.Errorf("invalid lead review: %w", err)
	}
	ex.Succeeded = true
	log.Info("lead finished", "agent", lead.ID, "findings", len(review.Findings), "duration", ex.Duration.Round(time.Millisecond))
	return review, warnings, nil
}

// adapt applies the agent's output format, stores the tool trace and records
// the model: the configured value wins, otherwise the one detected in the output.
func (a *App) adapt(r *run, ag config.AgentConfig, stdout []byte, ex *domain.AgentExecution) (*agent.Adapted, error) {
	ad, err := agent.Adapt(ag.Output, stdout)
	if ad != nil && ad.Trace != nil {
		_ = r.store.Write("raw", ag.ID+".trace.jsonl", ad.Trace)
	}
	if err != nil {
		ex.Error = err.Error()
		// The partial result still carries the transcript a retry needs.
		return ad, fmt.Errorf("invalid output: %w", err)
	}
	ex.ToolCalls = ad.ToolCalls
	ex.Model = ag.Model
	if ex.Model == "" {
		ex.Model = ad.Model
	}
	return ad, nil
}

func (a *App) cleanupWorktrees(r *run, worktrees map[string]string, keep bool) {
	log := a.logger()
	if keep {
		log.Info("keeping worktrees", "dir", r.wtBase)
		return
	}
	// Use a fresh context: cleanup must run even after cancellation.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for id, path := range worktrees {
		if err := r.git.RemoveWorktree(ctx, path); err != nil {
			log.Warn("worktree cleanup failed", "agent", id, "path", path, "error", err)
		}
	}
	// Remove the run's scratch directory (agent TMPDIR, prompt files).
	if err := os.RemoveAll(r.wtBase); err != nil {
		log.Warn("scratch cleanup failed", "path", r.wtBase, "error", err)
	}
}
