package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

// TriageRequest parameterizes a triage run. Numbers wins over the query.
type TriageRequest struct {
	// Apply writes the proposed labels to the forge. Labels are only ever
	// added, and nothing else about the issue is touched.
	Apply         bool
	Numbers       []int64
	Query         forge.IssueQuery
	Revision      string
	KeepWorktrees bool
}

// TriageResult carries the consolidated triage and where artifacts live.
type TriageResult struct {
	Triage   *domain.ConsolidatedTriage
	RunDir   string
	Manifest *domain.RunManifest
}

// Triage categorizes issues and says whether they still hold.
func (a *App) Triage(ctx context.Context, req TriageRequest) (*TriageResult, error) {
	cfg := a.Config
	log := a.logger()
	if cfg.Review.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.Review.Timeout)
		defer cancel()
	}
	reviewers := cfg.TriageReviewers()
	if len(reviewers) == 0 {
		return nil, errors.New("no reviewer available for triage")
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
	headSHA, branch, err := git.ResolveRevision(ctx, req.Revision)
	if err != nil {
		return nil, err
	}

	issues, err := a.collectIssues(ctx, f, repo, req)
	if err != nil {
		return nil, err
	}
	if len(issues) == 0 {
		return nil, errors.New("no issue to triage")
	}
	labels, labelWarnings, err := a.collectLabels(ctx, f, repo)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	runID := artifact.NewTriageRunID(now, len(issues))
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
	manifest := &domain.RunManifest{
		ID: runID, Kind: "triage", StartedAt: now, Status: domain.RunRunning,
		Forge: f.Name(), Owner: repo.Owner, Repo: repo.Name, HeadSHA: headSHA,
	}
	for _, i := range issues {
		manifest.Issues = append(manifest.Issues, i.Number)
	}
	log.Info("triage started", "run", runID, "issues", len(issues), "revision", branch, "head", headSHA)
	_ = store.WriteManifest(manifest)
	_ = store.WriteJSON("context", "issues.json", issues)
	_ = store.WriteJSON("context", "labels.json", labels)

	t := &triageRun{
		cfg: cfg, git: git, store: store, manifest: manifest,
		wtBase: filepath.Join(wtBase, runID), runner: &agent.Runner{Process: a.process(), TempDir: runTmp},
		issues: issues, labels: labels, known: forge.KnownLabels(labels),
		headSHA: headSHA, branch: branch,
	}
	triage, runErr := a.executeTriage(ctx, t, req, reviewers)
	end := time.Now()
	manifest.EndedAt = &end
	if runErr != nil {
		manifest.Status = domain.RunFailed
		manifest.Error = runErr.Error()
	} else {
		manifest.Status = domain.RunSucceeded
	}
	_ = store.WriteManifest(manifest)
	if runErr != nil {
		return &TriageResult{RunDir: store.Root, Manifest: manifest}, runErr
	}
	triage.Warnings = append(labelWarnings, triage.Warnings...)
	if req.Apply {
		a.applyLabels(ctx, f, repo, triage, labels)
	}
	_ = store.WriteJSON("final", "triage.json", triage)
	var md bytes.Buffer
	if err := output.TriageMarkdown(&md, triage, output.Options{ShowAttribution: cfg.ShowAttribution()}); err == nil {
		_ = store.Write("final", "triage.md", md.Bytes())
	}
	return &TriageResult{Triage: triage, RunDir: store.Root, Manifest: manifest}, nil
}

type triageRun struct {
	cfg      *config.Config
	git      *gitrepo.Git
	store    *artifact.Store
	manifest *domain.RunManifest
	wtBase   string
	runner   *agent.Runner
	issues   []domain.Issue
	labels   []domain.Label
	known    map[string]string
	headSHA  string
	branch   string
}

// collectIssues resolves the issues to triage, then loads their comments and
// references. Explicit numbers win over the query.
func (a *App) collectIssues(ctx context.Context, f forge.Forge, repo domain.Repository, req TriageRequest) ([]domain.Issue, error) {
	log := a.logger()
	limits := a.Config.Triage.Limits
	var issues []domain.Issue
	if len(req.Numbers) > 0 {
		for _, n := range req.Numbers {
			issue, err := f.GetIssue(ctx, repo, n)
			if err != nil {
				return nil, fmt.Errorf("get issue #%d: %w", n, err)
			}
			issues = append(issues, *issue)
		}
	} else {
		q := req.Query
		if q.Limit == 0 || q.Limit > limits.MaxIssues {
			q.Limit = limits.MaxIssues
		}
		found, err := f.ListIssues(ctx, repo, q)
		if err != nil {
			return nil, fmt.Errorf("list issues: %w", err)
		}
		issues = found
	}
	if len(issues) > limits.MaxIssues {
		return nil, fmt.Errorf("%d issues exceed the limit of %d (triage.limits.max_issues)", len(issues), limits.MaxIssues)
	}
	for i := range issues {
		comments, err := f.ListIssueComments(ctx, repo, issues[i].Number, limits.MaxComments)
		if err != nil {
			log.Warn("issue comments not loaded", "issue", issues[i].Number, "error", err)
		}
		for j := range comments {
			comments[j].Body = truncateBytes(comments[j].Body, a.Config.Review.Limits.MaxCommentBytes)
		}
		issues[i].Comments = comments
		refs, err := f.ListReferences(ctx, repo, issues[i].Number, limits.MaxReferences)
		if err != nil {
			log.Warn("issue references not loaded", "issue", issues[i].Number, "error", err)
		}
		issues[i].References = refs
		issues[i].Description = truncateBytes(issues[i].Description, a.Config.Review.Limits.MaxCommentBytes)
	}
	return issues, nil
}

// collectLabels builds the taxonomy an agent may pick from.
func (a *App) collectLabels(ctx context.Context, f forge.Forge, repo domain.Repository) ([]domain.Label, []string, error) {
	cfg := a.Config.Triage.Labels
	var raw []domain.Label
	var warnings []string
	if cfg.Source == config.LabelSourceList {
		for _, name := range cfg.List {
			raw = append(raw, domain.Label{Name: name, Description: cfg.Describe[name]})
		}
		return forge.FilterLabels(raw, cfg.Include, cfg.Exclude, cfg.Describe), nil, nil
	}
	found, err := f.ListLabels(ctx, repo)
	if err != nil {
		return nil, nil, fmt.Errorf("list labels: %w", err)
	}
	labels := forge.FilterLabels(found, cfg.Include, cfg.Exclude, cfg.Describe)
	if len(labels) == 0 {
		warnings = append(warnings, "the repository defines no usable label, no category was proposed")
	}
	return labels, warnings, nil
}

func (a *App) executeTriage(ctx context.Context, t *triageRun, req TriageRequest, reviewers []config.AgentConfig) (*domain.ConsolidatedTriage, error) {
	log := a.logger()
	cfg := t.cfg
	lead := cfg.Lead()
	useLead := cfg.TriageUsesLead()
	keep := req.KeepWorktrees || cfg.Review.KeepWorktrees

	agents := append([]config.AgentConfig{}, reviewers...)
	if useLead {
		agents = append(agents, lead)
	}
	worktrees := map[string]string{}
	for _, ag := range agents {
		path := filepath.Join(t.wtBase, ag.ID)
		if err := t.git.CreateWorktree(ctx, path, t.headSHA); err != nil {
			a.cleanupTriageWorktrees(t, worktrees, false)
			return nil, fmt.Errorf("create worktree for %s: %w", ag.ID, err)
		}
		worktrees[ag.ID] = path
	}
	defer a.cleanupTriageWorktrees(t, worktrees, keep)

	outcomes, err := a.runTriageAgents(ctx, t, reviewers, worktrees)
	if err != nil {
		return nil, err
	}
	var reports []domain.TriageReport
	succeeded := map[string]bool{}
	for _, o := range outcomes {
		if o.Report != nil {
			reports = append(reports, *o.Report)
			succeeded[o.Agent] = true
		}
	}
	if len(reports) == 0 {
		return nil, fmt.Errorf("no issue could be triaged: %w", errors.Join(triageErrors(outcomes)...))
	}
	succeededIDs := make([]string, 0, len(succeeded))
	for id := range succeeded {
		succeededIDs = append(succeededIDs, id)
	}
	sort.Strings(succeededIDs)

	meta := domain.TriageMeta{
		RunID: t.manifest.ID, Repository: t.manifest.Owner + "/" + t.manifest.Repo,
		HeadSHA: t.headSHA, Branch: t.branch, Total: len(t.issues),
		LeadID: lead.ID, Models: map[string]string{},
	}
	var warnings []string
	for _, ex := range t.manifest.Agents {
		for _, w := range ex.Warnings {
			warnings = append(warnings, ex.ID+": "+w)
		}
		if ex.Model != "" {
			meta.Models[ex.ID] = ex.Model
		}
	}

	var triage *domain.ConsolidatedTriage
	if useLead {
		var leadWarnings []string
		triage, leadWarnings, err = a.runTriageLead(ctx, t, lead, worktrees[lead.ID], reports, succeededIDs)
		warnings = append(warnings, leadWarnings...)
		if err != nil {
			log.Warn("lead failed, using deterministic consolidation", "lead", lead.ID, "error", err)
			warnings = append(warnings, fmt.Sprintf("lead %s failed: %v", lead.ID, err))
			triage = consolidation.FallbackTriage(t.issues, outcomes)
		} else {
			meta.LeadUsed = true
			if last := t.manifest.Agents[len(t.manifest.Agents)-1]; last.ID == lead.ID && last.Model != "" {
				meta.Models[lead.ID] = last.Model
			}
		}
	} else {
		triage = consolidation.FallbackTriage(t.issues, outcomes)
		triage.Summary = fmt.Sprintf("Triage of %d issue(s) by %s.", len(triage.Issues), joinIDs(succeededIDs))
	}
	warnings = append(warnings, consolidation.ApplyStatusLabels(triage, cfg.Triage.StatusLabels, t.known, cfg.Triage.Labels.MaxLabels)...)
	triage.Failed = consolidation.FailedIssues(t.issues, outcomes)
	meta.Succeeded = len(triage.Issues)
	triage.Meta = meta
	triage.Warnings = append(triage.Warnings, warnings...)
	return triage, nil
}

func joinIDs(ids []string) string {
	if len(ids) == 0 {
		return "no agent"
	}
	out := ids[0]
	for _, id := range ids[1:] {
		out += ", " + id
	}
	return out
}

func triageErrors(outcomes []consolidation.TriageOutcome) []error {
	var errs []error
	for _, o := range outcomes {
		if o.Err != nil {
			errs = append(errs, fmt.Errorf("#%d %s: %w", o.Number, o.Agent, o.Err))
		}
	}
	return errs
}

// runTriageAgents runs every reviewer over every issue, at most
// triage.max_parallel at a time. The parallelism is over issues, not over
// agents: a batch is many small independent jobs.
func (a *App) runTriageAgents(ctx context.Context, t *triageRun, reviewers []config.AgentConfig, worktrees map[string]string) ([]consolidation.TriageOutcome, error) {
	log := a.logger()
	type job struct {
		issue    domain.Issue
		reviewer config.AgentConfig
	}
	var jobs []job
	for _, issue := range t.issues {
		for _, rv := range reviewers {
			jobs = append(jobs, job{issue, rv})
		}
	}
	outcomes := make([]consolidation.TriageOutcome, len(jobs))
	execs := make([]domain.AgentExecution, len(jobs))
	group, gctx := errgroup.WithContext(ctx)
	group.SetLimit(t.cfg.Triage.MaxParallel)
	for i, j := range jobs {
		group.Go(func() error {
			ex, report, err := a.runTriageAgent(gctx, t, j.reviewer, worktrees[j.reviewer.ID], j.issue)
			outcomes[i] = consolidation.TriageOutcome{Number: j.issue.Number, Agent: j.reviewer.ID, Report: report, Err: err}
			execs[i] = ex
			if err != nil {
				log.Warn("triage failed", "agent", j.reviewer.ID, "issue", j.issue.Number, "error", err)
				return nil
			}
			log.Info("triage done", "agent", j.reviewer.ID, "issue", j.issue.Number,
				"status", report.Status, "labels", report.Labels, "duration", ex.Duration.Round(time.Millisecond))
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return outcomes, err
	}
	t.manifest.Agents = append(t.manifest.Agents, execs...)
	_ = t.store.WriteManifest(t.manifest)
	if ctx.Err() != nil {
		return outcomes, ctx.Err()
	}
	return outcomes, nil
}

func (a *App) runTriageAgent(ctx context.Context, t *triageRun, rv config.AgentConfig, worktree string, issue domain.Issue) (domain.AgentExecution, *domain.TriageReport, error) {
	ex := domain.AgentExecution{ID: rv.ID, Role: rv.Role, Worktree: worktree}
	others := make([]domain.Issue, 0, len(t.issues))
	for _, o := range t.issues {
		if o.Number != issue.Number {
			others = append(others, domain.Issue{Number: o.Number, Title: o.Title})
		}
	}
	p, err := prompt.Triage(prompt.TriageInput{
		AgentID: rv.ID, Worktree: worktree, Branch: t.branch, HeadSHA: t.headSHA,
		Issue: &issue, Labels: t.labels, MaxLabels: t.cfg.Triage.Labels.MaxLabels, OtherIssues: others,
	})
	if err != nil {
		ex.Error = err.Error()
		return ex, nil, err
	}
	name := fmt.Sprintf("triage-%d-%s", issue.Number, rv.ID)
	_ = t.store.Write("prompts", name+".txt", p)
	res := t.runner.Run(ctx, t.cfg, rv, worktree, p)
	ex.Duration = res.Duration
	if res.Result != nil {
		ex.ExitCode = res.Result.ExitCode
		_ = t.store.Write("raw", name+".stdout", res.Result.Stdout)
		_ = t.store.Write("raw", name+".stderr", res.Result.Stderr)
	}
	if res.Err != nil {
		ex.Error = res.Err.Error()
		return ex, nil, res.Err
	}
	ad, err := a.adapt(&run{store: t.store}, rv, res.Result.Stdout, &ex)
	if err != nil {
		return ex, nil, err
	}
	report, warnings, err := agent.ParseTriageReport(ad.Report, rv.ID, issue.Number, t.known,
		agent.TriageLimits{MaxLabels: t.cfg.Triage.Labels.MaxLabels, MaxFieldBytes: agent.DefaultLimits.MaxFieldBytes})
	ex.Warnings = warnings
	if err != nil {
		ex.Error = err.Error()
		return ex, nil, fmt.Errorf("invalid report: %w", err)
	}
	report.Reviewer.Model = ex.Model
	ex.Succeeded = true
	_ = t.store.WriteJSON("reports", name+".json", report)
	return ex, report, nil
}

func (a *App) runTriageLead(ctx context.Context, t *triageRun, lead config.AgentConfig, worktree string, reports []domain.TriageReport, succeeded []string) (*domain.ConsolidatedTriage, []string, error) {
	log := a.logger()
	ex := domain.AgentExecution{ID: lead.ID, Role: lead.Role, Worktree: worktree}
	defer func() {
		t.manifest.Agents = append(t.manifest.Agents, ex)
		_ = t.store.WriteManifest(t.manifest)
	}()
	p, err := prompt.LeadTriage(prompt.LeadTriageInput{
		AgentID: lead.ID, Worktree: worktree, Branch: t.branch, HeadSHA: t.headSHA,
		Labels: t.labels, MaxLabels: t.cfg.Triage.Labels.MaxLabels,
		Issues: t.issues, Reports: reports, SucceededReviewers: succeeded,
	})
	if err != nil {
		ex.Error = err.Error()
		return nil, nil, err
	}
	_ = t.store.Write("prompts", "lead-triage.txt", p)
	log.Info("lead started", "agent", lead.ID, "issues", len(t.issues))
	res := t.runner.Run(ctx, t.cfg, lead, worktree, p)
	ex.Duration = res.Duration
	if res.Result != nil {
		ex.ExitCode = res.Result.ExitCode
		_ = t.store.Write("raw", "lead-triage.stdout", res.Result.Stdout)
		_ = t.store.Write("raw", "lead-triage.stderr", res.Result.Stderr)
	}
	if res.Err != nil {
		ex.Error = res.Err.Error()
		return nil, nil, res.Err
	}
	ad, err := a.adapt(&run{store: t.store}, lead, res.Result.Stdout, &ex)
	if err != nil {
		return nil, nil, err
	}
	triage, warnings, err := consolidation.ParseLeadTriage(ad.Report, t.issues, succeeded, t.known, t.cfg.Triage.Labels.MaxLabels)
	ex.Warnings = warnings
	if err != nil {
		ex.Error = err.Error()
		return nil, warnings, fmt.Errorf("invalid lead triage: %w", err)
	}
	ex.Succeeded = true
	log.Info("lead finished", "agent", lead.ID, "issues", len(triage.Issues), "duration", ex.Duration.Round(time.Millisecond))
	return triage, warnings, nil
}

// applyLabels adds the proposed labels to the issues on the forge. It adds
// and never removes, skips an issue the run is not confident enough about,
// and records on each issue what it did.
func (a *App) applyLabels(ctx context.Context, f forge.Forge, repo domain.Repository, triage *domain.ConsolidatedTriage, labels []domain.Label) {
	log := a.logger()
	byName := make(map[string]domain.Label, len(labels))
	for _, l := range labels {
		byName[l.Name] = l
	}
	floor := a.Config.Triage.MinApplyConfidence
	for i := range triage.Issues {
		issue := &triage.Issues[i]
		missing := issue.AddedLabels()
		if len(missing) == 0 {
			continue
		}
		if issue.Confidence < floor {
			issue.ApplyError = fmt.Sprintf("skipped: confidence %.0f%% is below the %.0f%% floor",
				issue.Confidence*100, floor*100)
			log.Info("labels not applied", "issue", issue.Number, "reason", "low confidence")
			continue
		}
		toAdd := make([]domain.Label, 0, len(missing))
		for _, name := range missing {
			if l, ok := byName[name]; ok {
				toAdd = append(toAdd, l)
			}
		}
		if err := f.AddIssueLabels(ctx, repo, issue.Number, toAdd); err != nil {
			issue.ApplyError = err.Error()
			log.Warn("labels not applied", "issue", issue.Number, "error", err)
			continue
		}
		issue.Applied = missing
		issue.CurrentLabels = append(issue.CurrentLabels, missing...)
		log.Info("labels applied", "issue", issue.Number, "labels", missing)
	}
}

func (a *App) cleanupTriageWorktrees(t *triageRun, worktrees map[string]string, keep bool) {
	log := a.logger()
	if keep {
		log.Info("keeping worktrees", "dir", t.wtBase)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for id, path := range worktrees {
		if err := t.git.RemoveWorktree(ctx, path); err != nil {
			log.Warn("worktree cleanup failed", "agent", id, "path", path, "error", err)
		}
	}
	if err := os.RemoveAll(t.wtBase); err != nil {
		log.Warn("scratch cleanup failed", "path", t.wtBase, "error", err)
	}
}
