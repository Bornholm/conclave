package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/bornholm/conclave/internal/agent"
	"github.com/bornholm/conclave/internal/artifact"
	"github.com/bornholm/conclave/internal/config"
	"github.com/bornholm/conclave/internal/consolidation"
	"github.com/bornholm/conclave/internal/domain"
	"github.com/bornholm/conclave/internal/gitrepo"
	"github.com/bornholm/conclave/internal/output"
	"github.com/bornholm/conclave/internal/prompt"
)

// AskRequest parameterizes an ask run.
type AskRequest struct {
	Question string
	// Context is the material given with the question, typically read from
	// standard input.
	Context string
	// Project is the repository the question is about. Empty means the
	// question stands on its own: no repository is opened and the agents get
	// an empty scratch directory.
	Project       string
	Revision      string
	KeepWorktrees bool
}

// AskResult carries the consolidated answer and where artifacts live.
type AskResult struct {
	Answer   *domain.ConsolidatedAnswer
	RunDir   string
	Manifest *domain.RunManifest
}

type askRun struct {
	cfg      *config.Config
	git      *gitrepo.Git
	store    *artifact.Store
	manifest *domain.RunManifest
	wtBase   string
	runner   *agent.Runner
	question string
	context  string
	headSHA  string
	branch   string
}

func (p *askRun) hasProject() bool { return p.git != nil }

// Ask answers one question with several agents.
func (a *App) Ask(ctx context.Context, req AskRequest) (*AskResult, error) {
	cfg := a.Config
	log := a.logger()
	question := strings.TrimSpace(req.Question)
	if question == "" {
		return nil, errors.New("the question is empty")
	}
	respondents := cfg.Respondents()
	if len(respondents) == 0 {
		return nil, errors.New("no agent available to answer")
	}
	if cfg.Review.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.Review.Timeout)
		defer cancel()
	}
	question = truncateBytes(question, cfg.Ask.Limits.MaxQuestionBytes)
	questionContext := truncateBytes(strings.TrimSpace(req.Context), cfg.Ask.Limits.MaxContextBytes)

	now := time.Now()
	runID := artifact.NewAskRunID(now)
	manifest := &domain.RunManifest{ID: runID, Kind: "ask", StartedAt: now, Status: domain.RunRunning}

	// A question attached to a project is answered against a checkout of it,
	// exactly as a review or a plan would be. Without one, nothing git is
	// opened: asking a question must not require a repository.
	var git *gitrepo.Git
	var headSHA, branch, repoName string
	runsBase := a.RunsBase
	if req.Project != "" {
		var err error
		git, err = gitrepo.Open(ctx, req.Project)
		if err != nil {
			return nil, err
		}
		headSHA, branch, err = git.ResolveRevision(ctx, req.Revision)
		if err != nil {
			return nil, err
		}
		// The remote is a convenience for the report header, not a
		// requirement: a local repository with no forge is a valid project.
		if repo, err := git.Repository(ctx, cfg.Forge.Remote); err == nil {
			repoName = repo.FullName()
			manifest.Owner, manifest.Repo = repo.Owner, repo.Name
		} else {
			log.Debug("remote not resolved, the answer will not name a repository", "remote", cfg.Forge.Remote, "error", err)
		}
		manifest.HeadSHA = headSHA
		if runsBase == "" {
			runsBase = filepath.Join(git.CommonDir(), "conclave", "runs")
		}
	} else if runsBase == "" {
		runsBase = defaultRunsBase()
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
	log.Info("ask started", "run", runID, "agents", len(respondents), "project", req.Project, "revision", branch)
	_ = store.WriteManifest(manifest)
	_ = store.Write("context", "question.txt", []byte(question))
	if questionContext != "" {
		_ = store.Write("context", "context.txt", []byte(questionContext))
	}

	p := &askRun{
		cfg: cfg, git: git, store: store, manifest: manifest,
		wtBase: filepath.Join(wtBase, runID), runner: &agent.Runner{Process: a.process(), TempDir: runTmp},
		question: question, context: questionContext, headSHA: headSHA, branch: branch,
	}
	answer, runErr := a.executeAsk(ctx, p, req, respondents)
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
		return &AskResult{RunDir: store.Root, Manifest: manifest}, runErr
	}
	answer.Meta.Repository = repoName
	answer.Meta.Project = req.Project
	_ = store.WriteJSON("final", "answer.json", answer)
	var md bytes.Buffer
	if err := output.AnswerMarkdown(&md, answer, output.Options{ShowAttribution: cfg.ShowAttribution(), ShowFailedAgents: cfg.ShowFailedAgents()}); err == nil {
		_ = store.Write("final", "answer.md", md.Bytes())
	}
	return &AskResult{Answer: answer, RunDir: store.Root, Manifest: manifest}, nil
}

// defaultRunsBase is where the artifacts of a question without a project go,
// since there is no repository to put them next to.
func defaultRunsBase() string {
	if dir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(dir, "conclave", "runs")
	}
	return filepath.Join(os.TempDir(), "conclave", "runs")
}

func (a *App) executeAsk(ctx context.Context, p *askRun, req AskRequest, respondents []config.AgentConfig) (*domain.ConsolidatedAnswer, error) {
	log := a.logger()
	cfg := p.cfg
	lead := cfg.Lead()
	useLead := cfg.AskUsesLead()
	keep := req.KeepWorktrees || cfg.Review.KeepWorktrees

	agents := append([]config.AgentConfig{}, respondents...)
	if useLead {
		agents = append(agents, lead)
	}
	worktrees := map[string]string{}
	for _, ag := range agents {
		path := filepath.Join(p.wtBase, ag.ID)
		if err := a.createAskWorkdir(ctx, p, path); err != nil {
			a.cleanupAskWorkdirs(p, worktrees, false)
			return nil, fmt.Errorf("create working directory for %s: %w", ag.ID, err)
		}
		worktrees[ag.ID] = path
	}
	defer a.cleanupAskWorkdirs(p, worktrees, keep)

	outcomes, err := a.runRespondents(ctx, p, respondents, worktrees)
	if err != nil {
		return nil, err
	}
	var succeeded []string
	var reports []domain.AnswerReport
	for _, o := range outcomes {
		if o.Report != nil {
			succeeded = append(succeeded, o.ID)
			reports = append(reports, *o.Report)
		}
	}
	if len(succeeded) == 0 {
		return nil, fmt.Errorf("all %d agents failed to answer: %w", len(respondents), errors.Join(answerErrors(outcomes)...))
	}

	meta := domain.AnswerMeta{
		RunID: p.manifest.ID, HeadSHA: p.headSHA, Branch: p.branch,
		RespondentsTotal: len(respondents), RespondentsSucceeded: len(succeeded),
		Models: map[string]string{},
	}
	// LeadID is what the output reads as "a lead was expected here". A run
	// configured with use_lead: false expected none, and must not be
	// reported as one where the lead was unavailable.
	if useLead {
		meta.LeadID = lead.ID
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

	var answer *domain.ConsolidatedAnswer
	if useLead {
		var leadWarnings []string
		answer, leadWarnings, err = a.runAskLead(ctx, p, lead, worktrees[lead.ID], reports, succeeded)
		warnings = append(warnings, leadWarnings...)
		if err != nil {
			log.Warn("lead failed, using deterministic consolidation", "lead", lead.ID, "error", err)
			warnings = append(warnings, fmt.Sprintf("lead %s failed: %v", lead.ID, err))
			answer = consolidation.FallbackAnswer(p.question, outcomes)
		} else {
			meta.LeadUsed = true
		}
		// The model is captured on both paths: an artifact of a failed
		// consolidation has to say which model failed it.
		if last := p.manifest.Agents[len(p.manifest.Agents)-1]; last.ID == lead.ID && last.Model != "" {
			meta.Models[lead.ID] = last.Model
		}
	} else {
		answer = consolidation.FallbackAnswer(p.question, outcomes)
	}
	answer.Failed = consolidation.FailedRespondents(outcomes)
	answer.Meta = meta
	answer.Warnings = append(answer.Warnings, warnings...)
	return answer, nil
}

// createAskWorkdir gives an agent a disposable worktree of the project, or an
// empty directory when the question is not about one.
func (a *App) createAskWorkdir(ctx context.Context, p *askRun, path string) error {
	if p.hasProject() {
		return p.git.CreateWorktree(ctx, path, p.headSHA)
	}
	return os.MkdirAll(path, 0o755)
}

func answerErrors(outcomes []consolidation.AnswerOutcome) []error {
	var errs []error
	for _, o := range outcomes {
		if o.Err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", o.ID, o.Err))
		}
	}
	return errs
}

// runRespondents asks every agent the question, at most ask.max_parallel at
// a time.
func (a *App) runRespondents(ctx context.Context, p *askRun, respondents []config.AgentConfig, worktrees map[string]string) ([]consolidation.AnswerOutcome, error) {
	log := a.logger()
	outcomes := make([]consolidation.AnswerOutcome, len(respondents))
	execs := make([]domain.AgentExecution, len(respondents))
	group, gctx := errgroup.WithContext(ctx)
	group.SetLimit(p.cfg.Ask.MaxParallel)
	for i, ag := range respondents {
		group.Go(func() error {
			log.Info("agent started", "agent", ag.ID)
			ex, report, err := a.runRespondent(gctx, p, ag, worktrees[ag.ID])
			outcomes[i] = consolidation.AnswerOutcome{ID: ag.ID, Report: report, Err: err}
			execs[i] = ex
			if err != nil {
				log.Warn("agent failed", "agent", ag.ID, "error", err, "duration", ex.Duration.Round(time.Millisecond))
				return nil
			}
			log.Info("agent answered", "agent", ag.ID, "confidence", report.Confidence,
				"duration", ex.Duration.Round(time.Millisecond))
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

func (a *App) runRespondent(ctx context.Context, p *askRun, ag config.AgentConfig, worktree string) (domain.AgentExecution, *domain.AnswerReport, error) {
	ex := domain.AgentExecution{ID: ag.ID, Role: ag.Role, Worktree: worktree}
	pr, err := prompt.Ask(prompt.AskInput{
		AgentID: ag.ID, Worktree: worktree, Specialties: ag.Specialties,
		HasProject: p.hasProject(), Branch: p.branch, HeadSHA: p.headSHA,
		Question: p.question, Context: p.context,
	})
	if err != nil {
		ex.Error = err.Error()
		return ex, nil, err
	}
	name := "ask-" + ag.ID
	_ = p.store.Write("prompts", name+".txt", pr)
	res := p.runner.Run(ctx, p.cfg, ag, worktree, pr)
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
	ad, err := a.adapt(&run{store: p.store}, ag, res.Result.Stdout, &ex)
	if err != nil {
		return ex, nil, err
	}
	report, warnings, err := agent.ParseAnswerReport(ad.Report, ag.ID, p.answerLimits())
	ex.Warnings = warnings
	if err != nil {
		ex.Error = err.Error()
		return ex, nil, fmt.Errorf("invalid answer: %w", err)
	}
	report.Reviewer.Model = ex.Model
	ex.Succeeded = true
	_ = p.store.WriteJSON("reports", name+".json", report)
	return ex, report, nil
}

func (a *App) runAskLead(ctx context.Context, p *askRun, lead config.AgentConfig, worktree string, reports []domain.AnswerReport, succeeded []string) (*domain.ConsolidatedAnswer, []string, error) {
	log := a.logger()
	ex := domain.AgentExecution{ID: lead.ID, Role: lead.Role, Worktree: worktree}
	defer func() {
		p.manifest.Agents = append(p.manifest.Agents, ex)
		_ = p.store.WriteManifest(p.manifest)
	}()
	pr, err := prompt.LeadAsk(prompt.LeadAskInput{
		AgentID: lead.ID, Worktree: worktree, HasProject: p.hasProject(),
		Branch: p.branch, HeadSHA: p.headSHA, Question: p.question, Context: p.context,
		Reports: reports, SucceededRespondents: succeeded,
	})
	if err != nil {
		ex.Error = err.Error()
		return nil, nil, err
	}
	_ = p.store.Write("prompts", "lead-ask.txt", pr)
	log.Info("lead started", "agent", lead.ID, "answers", len(reports))
	res := p.runner.Run(ctx, p.cfg, lead, worktree, pr)
	ex.Duration = res.Duration
	if res.Result != nil {
		ex.ExitCode = res.Result.ExitCode
		_ = p.store.Write("raw", "lead-ask.stdout", res.Result.Stdout)
		_ = p.store.Write("raw", "lead-ask.stderr", res.Result.Stderr)
	}
	if res.Err != nil {
		ex.Error = res.Err.Error()
		return nil, nil, res.Err
	}
	ad, err := a.adapt(&run{store: p.store}, lead, res.Result.Stdout, &ex)
	if err != nil {
		return nil, nil, err
	}
	answer, warnings, err := consolidation.ParseLeadAnswer(ad.Report, p.question, succeeded, p.answerLimits())
	ex.Warnings = warnings
	if err != nil {
		ex.Error = err.Error()
		return nil, warnings, fmt.Errorf("invalid lead answer: %w", err)
	}
	ex.Succeeded = true
	log.Info("lead finished", "agent", lead.ID, "duration", ex.Duration.Round(time.Millisecond))
	return answer, warnings, nil
}

func (p *askRun) answerLimits() agent.AnswerLimits {
	return agent.AnswerLimits{
		MaxAnswerBytes: p.cfg.Ask.Limits.MaxAnswerBytes,
		MaxFieldBytes:  agent.DefaultLimits.MaxFieldBytes,
		MaxKeyPoints:   p.cfg.Ask.Limits.MaxKeyPoints,
		MaxReferences:  p.cfg.Ask.Limits.MaxReferences,
	}
}

func (a *App) cleanupAskWorkdirs(p *askRun, worktrees map[string]string, keep bool) {
	log := a.logger()
	if keep {
		log.Info("keeping working directories", "dir", p.wtBase)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if p.hasProject() {
		for id, path := range worktrees {
			if err := p.git.RemoveWorktree(ctx, path); err != nil {
				log.Warn("worktree cleanup failed", "agent", id, "path", path, "error", err)
			}
		}
	}
	if err := os.RemoveAll(p.wtBase); err != nil {
		log.Warn("scratch cleanup failed", "path", p.wtBase, "error", err)
	}
}
