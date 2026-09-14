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

// PlanRequest parameterizes a plan run.
type PlanRequest struct {
	Number        int64
	Revision      string
	KeepWorktrees bool
}

// PlanResult carries the consolidated plan and where artifacts live.
type PlanResult struct {
	Plan     *domain.ConsolidatedPlan
	RunDir   string
	Manifest *domain.RunManifest
}

type planRun struct {
	cfg      *config.Config
	git      *gitrepo.Git
	store    *artifact.Store
	manifest *domain.RunManifest
	wtBase   string
	runner   *agent.Runner
	issue    *domain.Issue
	headSHA  string
	branch   string
}

// Plan writes the implementation plan of one issue.
func (a *App) Plan(ctx context.Context, req PlanRequest) (*PlanResult, error) {
	cfg := a.Config
	log := a.logger()
	if cfg.Review.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.Review.Timeout)
		defer cancel()
	}
	planners := cfg.Planners()
	if len(planners) == 0 {
		return nil, errors.New("no agent available to plan")
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

	log.Info("fetching issue", "forge", f.Name(), "repo", repo.FullName(), "number", req.Number)
	issue, err := a.collectPlanIssue(ctx, f, repo, req.Number)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	runID := artifact.NewPlanRunID(now, issue.Number)
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
		ID: runID, Kind: "plan", StartedAt: now, Status: domain.RunRunning,
		Forge: f.Name(), Owner: repo.Owner, Repo: repo.Name, HeadSHA: headSHA,
		Issues: []int64{issue.Number},
	}
	log.Info("plan started", "run", runID, "issue", issue.Number, "revision", branch, "head", headSHA)
	_ = store.WriteManifest(manifest)
	_ = store.WriteJSON("context", "issue.json", issue)

	p := &planRun{
		cfg: cfg, git: git, store: store, manifest: manifest,
		wtBase: filepath.Join(wtBase, runID), runner: &agent.Runner{Process: a.process(), TempDir: runTmp},
		issue: issue, headSHA: headSHA, branch: branch,
	}
	plan, runErr := a.executePlan(ctx, p, req, planners)
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
		return &PlanResult{RunDir: store.Root, Manifest: manifest}, runErr
	}
	_ = store.WriteJSON("final", "plan.json", plan)
	var md bytes.Buffer
	if err := output.PlanMarkdown(&md, plan, output.Options{ShowAttribution: cfg.ShowAttribution(), ShowFailedAgents: cfg.ShowFailedAgents()}); err == nil {
		_ = store.Write("final", "plan.md", md.Bytes())
	}
	return &PlanResult{Plan: plan, RunDir: store.Root, Manifest: manifest}, nil
}

// collectPlanIssue loads the issue with everything that says what was already
// decided about it: its discussion and the changes that mention it.
func (a *App) collectPlanIssue(ctx context.Context, f forge.Forge, repo domain.Repository, number int64) (*domain.Issue, error) {
	log := a.logger()
	limits := a.Config.Plan.Limits
	issue, err := f.GetIssue(ctx, repo, number)
	if err != nil {
		return nil, fmt.Errorf("get issue #%d: %w", number, err)
	}
	comments, err := f.ListIssueComments(ctx, repo, number, limits.MaxComments)
	if err != nil {
		log.Warn("issue comments not loaded", "issue", number, "error", err)
	}
	for i := range comments {
		comments[i].Body = truncateBytes(comments[i].Body, a.Config.Review.Limits.MaxCommentBytes)
	}
	issue.Comments = comments
	refs, err := f.ListReferences(ctx, repo, number, limits.MaxReferences)
	if err != nil {
		log.Warn("issue references not loaded", "issue", number, "error", err)
	}
	issue.References = refs
	issue.Description = truncateBytes(issue.Description, a.Config.Review.Limits.MaxCommentBytes)
	return issue, nil
}

func (a *App) executePlan(ctx context.Context, p *planRun, req PlanRequest, planners []config.AgentConfig) (*domain.ConsolidatedPlan, error) {
	log := a.logger()
	cfg := p.cfg
	lead := cfg.Lead()
	useLead := cfg.PlanUsesLead()
	keep := req.KeepWorktrees || cfg.Review.KeepWorktrees

	agents := append([]config.AgentConfig{}, planners...)
	if useLead {
		agents = append(agents, lead)
	}
	worktrees := map[string]string{}
	for _, ag := range agents {
		path := filepath.Join(p.wtBase, ag.ID)
		if err := p.git.CreateWorktree(ctx, path, p.headSHA); err != nil {
			a.cleanupPlanWorktrees(p, worktrees, false)
			return nil, fmt.Errorf("create worktree for %s: %w", ag.ID, err)
		}
		worktrees[ag.ID] = path
	}
	defer a.cleanupPlanWorktrees(p, worktrees, keep)

	outcomes, err := a.runPlanners(ctx, p, planners, worktrees)
	if err != nil {
		return nil, err
	}
	var succeeded []string
	var reports []domain.PlanReport
	for _, o := range outcomes {
		if o.Report != nil {
			succeeded = append(succeeded, o.ID)
			reports = append(reports, *o.Report)
		}
	}
	if len(succeeded) == 0 {
		return nil, fmt.Errorf("all %d planners failed: %w", len(planners), errors.Join(planErrors(outcomes)...))
	}

	meta := domain.PlanMeta{
		RunID: p.manifest.ID, Repository: p.manifest.Owner + "/" + p.manifest.Repo,
		IssueNumber: p.issue.Number, HeadSHA: p.headSHA, Branch: p.branch,
		PlannersTotal: len(planners), PlannersSucceeded: len(succeeded),
		LeadID: lead.ID, Models: map[string]string{},
	}
	var warnings []string
	for _, ex := range p.manifest.Agents {
		for _, w := range ex.Warnings {
			warnings = append(warnings, ex.ID+": "+w)
		}
		if ex.Model != "" {
			meta.Models[ex.ID] = ex.Model
		}
	}

	var plan *domain.ConsolidatedPlan
	if useLead {
		var leadWarnings []string
		plan, leadWarnings, err = a.runPlanLead(ctx, p, lead, worktrees[lead.ID], reports, succeeded)
		warnings = append(warnings, leadWarnings...)
		if err != nil {
			log.Warn("lead failed, using deterministic consolidation", "lead", lead.ID, "error", err)
			warnings = append(warnings, fmt.Sprintf("lead %s failed: %v", lead.ID, err))
			plan = consolidation.FallbackPlan(p.issue, outcomes)
		} else {
			meta.LeadUsed = true
			if last := p.manifest.Agents[len(p.manifest.Agents)-1]; last.ID == lead.ID && last.Model != "" {
				meta.Models[lead.ID] = last.Model
			}
		}
	} else {
		plan = consolidation.FallbackPlan(p.issue, outcomes)
	}
	plan.Failed = consolidation.FailedPlanners(outcomes)
	plan.Meta = meta
	plan.Warnings = append(plan.Warnings, warnings...)
	return plan, nil
}

func planErrors(outcomes []consolidation.PlanOutcome) []error {
	var errs []error
	for _, o := range outcomes {
		if o.Err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", o.ID, o.Err))
		}
	}
	return errs
}

// runPlanners runs every planner over the issue, at most plan.max_parallel at
// a time.
func (a *App) runPlanners(ctx context.Context, p *planRun, planners []config.AgentConfig, worktrees map[string]string) ([]consolidation.PlanOutcome, error) {
	log := a.logger()
	outcomes := make([]consolidation.PlanOutcome, len(planners))
	execs := make([]domain.AgentExecution, len(planners))
	group, gctx := errgroup.WithContext(ctx)
	group.SetLimit(p.cfg.Plan.MaxParallel)
	for i, pl := range planners {
		group.Go(func() error {
			log.Info("planner started", "agent", pl.ID, "issue", p.issue.Number)
			ex, report, err := a.runPlanner(gctx, p, pl, worktrees[pl.ID])
			outcomes[i] = consolidation.PlanOutcome{ID: pl.ID, Report: report, Err: err}
			execs[i] = ex
			if err != nil {
				log.Warn("planner failed", "agent", pl.ID, "error", err, "duration", ex.Duration.Round(time.Millisecond))
				return nil
			}
			log.Info("planner done", "agent", pl.ID, "steps", len(report.Steps),
				"effort", report.Effort, "duration", ex.Duration.Round(time.Millisecond))
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return outcomes, err
	}
	p.manifest.Agents = append(p.manifest.Agents, execs...)
	_ = p.store.WriteManifest(p.manifest)
	if ctx.Err() != nil {
		return outcomes, ctx.Err()
	}
	return outcomes, nil
}

func (a *App) runPlanner(ctx context.Context, p *planRun, pl config.AgentConfig, worktree string) (domain.AgentExecution, *domain.PlanReport, error) {
	ex := domain.AgentExecution{ID: pl.ID, Role: pl.Role, Worktree: worktree}
	pr, err := prompt.Plan(prompt.PlanInput{
		AgentID: pl.ID, Worktree: worktree, Branch: p.branch, HeadSHA: p.headSHA,
		Issue: p.issue, Specialties: pl.Specialties, MaxSteps: p.cfg.Plan.Limits.MaxSteps,
	})
	if err != nil {
		ex.Error = err.Error()
		return ex, nil, err
	}
	name := "plan-" + pl.ID
	_ = p.store.Write("prompts", name+".txt", pr)
	res := p.runner.Run(ctx, p.cfg, pl, worktree, pr)
	ex.Duration = res.Duration
	if res.Result != nil {
		ex.ExitCode = res.Result.ExitCode
		_ = p.store.Write("raw", name+".stdout", res.Result.Stdout)
		_ = p.store.Write("raw", name+".stderr", res.Result.Stderr)
	}
	if res.Err != nil {
		ex.Error = res.Err.Error()
		return ex, nil, res.Err
	}
	ad, err := a.adapt(&run{store: p.store}, pl, res.Result.Stdout, &ex)
	if err != nil {
		return ex, nil, err
	}
	report, warnings, err := agent.ParsePlanReport(ad.Report, pl.ID, p.issue.Number,
		agent.PlanLimits{MaxSteps: p.cfg.Plan.Limits.MaxSteps, MaxFieldBytes: agent.DefaultLimits.MaxFieldBytes})
	ex.Warnings = warnings
	if err != nil {
		ex.Error = err.Error()
		return ex, nil, fmt.Errorf("invalid plan: %w", err)
	}
	report.Reviewer.Model = ex.Model
	ex.Succeeded = true
	_ = p.store.WriteJSON("reports", name+".json", report)
	return ex, report, nil
}

func (a *App) runPlanLead(ctx context.Context, p *planRun, lead config.AgentConfig, worktree string, reports []domain.PlanReport, succeeded []string) (*domain.ConsolidatedPlan, []string, error) {
	log := a.logger()
	ex := domain.AgentExecution{ID: lead.ID, Role: lead.Role, Worktree: worktree}
	defer func() {
		p.manifest.Agents = append(p.manifest.Agents, ex)
		_ = p.store.WriteManifest(p.manifest)
	}()
	pr, err := prompt.LeadPlan(prompt.LeadPlanInput{
		AgentID: lead.ID, Worktree: worktree, Branch: p.branch, HeadSHA: p.headSHA,
		Issue: p.issue, Reports: reports, SucceededPlanners: succeeded,
		MaxSteps: p.cfg.Plan.Limits.MaxSteps,
	})
	if err != nil {
		ex.Error = err.Error()
		return nil, nil, err
	}
	_ = p.store.Write("prompts", "lead-plan.txt", pr)
	log.Info("lead started", "agent", lead.ID, "plans", len(reports))
	res := p.runner.Run(ctx, p.cfg, lead, worktree, pr)
	ex.Duration = res.Duration
	if res.Result != nil {
		ex.ExitCode = res.Result.ExitCode
		_ = p.store.Write("raw", "lead-plan.stdout", res.Result.Stdout)
		_ = p.store.Write("raw", "lead-plan.stderr", res.Result.Stderr)
	}
	if res.Err != nil {
		ex.Error = res.Err.Error()
		return nil, nil, res.Err
	}
	ad, err := a.adapt(&run{store: p.store}, lead, res.Result.Stdout, &ex)
	if err != nil {
		return nil, nil, err
	}
	plan, warnings, err := consolidation.ParseLeadPlan(ad.Report, p.issue, succeeded, p.cfg.Plan.Limits.MaxSteps)
	ex.Warnings = warnings
	if err != nil {
		ex.Error = err.Error()
		return nil, warnings, fmt.Errorf("invalid lead plan: %w", err)
	}
	ex.Succeeded = true
	log.Info("lead finished", "agent", lead.ID, "steps", len(plan.Steps), "duration", ex.Duration.Round(time.Millisecond))
	return plan, warnings, nil
}

func (a *App) cleanupPlanWorktrees(p *planRun, worktrees map[string]string, keep bool) {
	log := a.logger()
	if keep {
		log.Info("keeping worktrees", "dir", p.wtBase)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for id, path := range worktrees {
		if err := p.git.RemoveWorktree(ctx, path); err != nil {
			log.Warn("worktree cleanup failed", "agent", id, "path", path, "error", err)
		}
	}
	if err := os.RemoveAll(p.wtBase); err != nil {
		log.Warn("scratch cleanup failed", "path", p.wtBase, "error", err)
	}
}
