package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bornholm/conclave/internal/agent"
	"github.com/bornholm/conclave/internal/artifact"
	"github.com/bornholm/conclave/internal/config"
	"github.com/bornholm/conclave/internal/domain"
	"github.com/bornholm/conclave/internal/forge"
	"github.com/bornholm/conclave/internal/testutil"
)

// fakeForge serves one pull request and a few issues from memory.
type fakeForge struct {
	pr     *domain.PullRequest
	files  []domain.ChangedFile
	issues []domain.Issue
	labels []domain.Label
	refs   map[int64][]domain.Reference

	// added records what AddIssueLabels received, addErr makes it fail.
	added  map[int64][]string
	addErr error

	// labelEvents records the running label calls, and whether the context
	// they got was already done.
	labelEvents []string

	// parent, when set, makes the fake forge a ForkResolver whose repository
	// is a fork of it.
	parent *domain.Repository
}

func (f *fakeForge) ParentRepository(context.Context, domain.Repository) (domain.Repository, error) {
	if f.parent == nil {
		return domain.Repository{}, forge.ErrNotFork
	}
	return *f.parent, nil
}

func (f *fakeForge) Name() string { return "fake" }
func (f *fakeForge) GetPullRequest(_ context.Context, _ domain.Repository, n int64) (*domain.PullRequest, error) {
	if n != f.pr.Number {
		return nil, fmt.Errorf("%w: pull request #%d", forge.ErrNotFound, n)
	}
	pr := *f.pr
	return &pr, nil
}
func (f *fakeForge) ListChangedFiles(context.Context, domain.Repository, int64, int) ([]domain.ChangedFile, error) {
	return f.files, nil
}
func (f *fakeForge) ListDiscussion(context.Context, domain.Repository, int64, int) ([]domain.Comment, error) {
	return []domain.Comment{{Kind: "comment", Author: "maint", CreatedAt: "t", Body: "Decided: no passthrough. Related to #9."}}, nil
}
func (f *fakeForge) GetIssue(_ context.Context, _ domain.Repository, n int64) (*domain.Issue, error) {
	for _, i := range f.issues {
		if i.Number == n {
			issue := i
			return &issue, nil
		}
	}
	return &domain.Issue{Number: n, Title: "issue", Description: "body"}, nil
}

func (f *fakeForge) ListLabels(context.Context, domain.Repository) ([]domain.Label, error) {
	return f.labels, nil
}

func (f *fakeForge) ListIssues(_ context.Context, _ domain.Repository, q forge.IssueQuery) ([]domain.Issue, error) {
	var out []domain.Issue
	for _, i := range f.issues {
		if q.State != forge.StateAll && q.State != "" && i.State != q.State {
			continue
		}
		out = append(out, i)
		if q.Limit > 0 && len(out) >= q.Limit {
			break
		}
	}
	return out, nil
}

func (f *fakeForge) ListIssueComments(_ context.Context, _ domain.Repository, n int64, _ int) ([]domain.Comment, error) {
	return []domain.Comment{{Kind: domain.CommentKindComment, Author: "bob", CreatedAt: "t", Body: "still happening"}}, nil
}

func (f *fakeForge) AddIssueLabels(_ context.Context, _ domain.Repository, n int64, labels []domain.Label) error {
	if f.addErr != nil {
		return f.addErr
	}
	if f.added == nil {
		f.added = map[int64][]string{}
	}
	for _, l := range labels {
		f.added[n] = append(f.added[n], l.Name)
	}
	return nil
}

func (f *fakeForge) AddPullRequestLabel(ctx context.Context, _ domain.Repository, n int64, name string) error {
	f.labelEvents = append(f.labelEvents, fmt.Sprintf("add #%d %s ctx=%v", n, name, ctx.Err()))
	return nil
}

func (f *fakeForge) RemovePullRequestLabel(ctx context.Context, _ domain.Repository, n int64, name string) error {
	f.labelEvents = append(f.labelEvents, fmt.Sprintf("remove #%d %s ctx=%v", n, name, ctx.Err()))
	return nil
}

func (f *fakeForge) ListReferences(_ context.Context, _ domain.Repository, n int64, _ int) ([]domain.Reference, error) {
	return f.refs[n], nil
}

// fixtureRepo creates a repository with a feature branch and returns dir, base and head SHAs.
func fixtureRepo(t *testing.T) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("init", "-q", "-b", "main")
	write("changed.txt", "a\nb\n")
	run("add", ".")
	run("commit", "-q", "-m", "base")
	base := run("rev-parse", "HEAD")
	run("checkout", "-q", "-b", "feature")
	write("changed.txt", "a\nB\n")
	run("commit", "-q", "-am", "feature")
	head := run("rev-parse", "HEAD")
	run("checkout", "-q", "main")
	run("remote", "add", "origin", "https://github.com/acme/proj.git")
	return dir, base, head
}

func agentCfg(t *testing.T, id, role, mode string) config.AgentConfig {
	return config.AgentConfig{ID: id, Role: role, Input: config.InputStdin, Command: []string{testutil.TestAgent(t)},
		Environment: map[string]string{"CONCLAVE_TESTAGENT_MODE": mode, "CONCLAVE_TESTAGENT_ID": id}}
}

func newApp(t *testing.T, agents ...config.AgentConfig) (*App, string) {
	t.Helper()
	dir, base, head := fixtureRepo(t)
	cfg := &config.Config{Version: 1,
		Forge: config.ForgeConfig{Provider: config.ProviderGitHub, Remote: "origin", BaseURL: config.DefaultGitHubBaseURL},
		Review: config.ReviewConfig{MaxParallel: 2, Timeout: time.Minute, AgentTimeout: 10 * time.Second, LeadTimeout: 10 * time.Second,
			Limits: config.LimitsConfig{MaxOutputBytes: 1 << 20, MaxDiffBytes: 1 << 20, MaxFiles: 100, MaxFindings: 50, MaxIssues: 10, MaxComments: 100, MaxCommentBytes: 4096}},
		Output: config.OutputConfig{Format: "markdown"}, Agents: agents}
	config.ApplyDefaults(cfg)
	f := &fakeForge{
		pr: &domain.PullRequest{Number: 7, Title: "Feature", Description: "Fixes #3", Author: "alice", State: "open",
			Base: domain.RepositoryRef{Branch: "main", SHA: base}, Head: domain.RepositoryRef{Branch: "feature", SHA: head}},
		files: []domain.ChangedFile{{Path: "changed.txt", Status: "modified", Additions: 1, Deletions: 1}},
	}
	a := &App{Config: cfg, Dir: dir, Forge: f, WorktreeBase: filepath.Join(t.TempDir(), "wt"),
		RunsBase: filepath.Join(t.TempDir(), "runs"), Logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))}
	return a, dir
}

func TestReviewEndToEnd(t *testing.T) {
	a, _ := newApp(t,
		agentCfg(t, "r1", config.RoleReviewer, "valid"),
		agentCfg(t, "r2", config.RoleReviewer, "exit-1"),
		agentCfg(t, "r3", config.RoleReviewer, "envelope"),
		agentCfg(t, "r4", config.RoleReviewer, "pi-json"),
		agentCfg(t, "lead", config.RoleLead, "valid"))
	a.Config.Agents[3].Output = config.OutputPiJSON
	a.Config.Agents[0].Model = "configured/model"
	res, err := a.Review(context.Background(), ReviewRequest{Number: 7})
	if err != nil {
		t.Fatal(err)
	}
	rv := res.Review
	if rv.Meta.Models["r1"] != "configured/model" || rv.Meta.Models["r4"] != "openrouter/fake/model" || rv.Meta.Models["r3"] != "" {
		t.Errorf("models: %v", rv.Meta.Models)
	}
	if _, err := os.Stat(filepath.Join(res.RunDir, "raw", "r4.trace.jsonl")); err != nil {
		t.Errorf("pi trace artifact: %v", err)
	}
	if rv.Verdict != domain.VerdictRequestChanges || !rv.Meta.LeadUsed || rv.Meta.ReviewersSucceeded != 3 || rv.Meta.ReviewersTotal != 4 {
		t.Errorf("meta: %+v verdict=%s", rv.Meta, rv.Verdict)
	}
	if len(rv.Findings) != 1 || rv.Findings[0].ReportedBy[0] != "r1" || rv.Findings[0].File != "changed.txt" {
		t.Errorf("findings: %+v", rv.Findings)
	}
	if len(rv.FailedReviewers) != 1 || rv.FailedReviewers[0].ID != "r2" || !strings.Contains(rv.FailedReviewers[0].Reason, "code 1") {
		t.Errorf("failed: %+v", rv.FailedReviewers)
	}
	if len(rv.Warnings) < 2 || !strings.Contains(strings.Join(rv.Warnings, "\n"), "rejected") {
		t.Errorf("warnings should mention the rejected finding: %v", rv.Warnings)
	}
	// Artifacts.
	for _, p := range []string{"manifest.json", "context/pull-request.json", "context/discussion.json", "context/diff.patch", "prompts/reviewer-r1.txt", "prompts/lead.txt",
		"reports/r1.json", "reports/r3.json", "raw/r2.stderr", "raw/lead.stdout", "final/review.json", "final/review.md", "final/groups.json"} {
		if _, err := os.Stat(filepath.Join(res.RunDir, p)); err != nil {
			t.Errorf("artifact %s: %v", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(res.RunDir, "reports", "r2.json")); err == nil {
		t.Error("failed reviewer must not have a report")
	}
	if res.Manifest.Status != domain.RunSucceeded || len(res.Manifest.Agents) != 5 {
		t.Errorf("manifest: %+v", res.Manifest)
	}
	// Prompt content and security boundary.
	p, _ := os.ReadFile(filepath.Join(res.RunDir, "prompts", "reviewer-r1.txt"))
	for _, want := range []string{"UNTRUSTED", "Fixes #3", "+B", "TIME BUDGET\nYou have 10 seconds", "ISSUE #3", "ISSUE #9", "DISCUSSION (1 entries", "comment by maint", "modified changed.txt", "WORKING DIRECTORY\n" + filepath.Join(a.WorktreeBase, res.Manifest.ID, "r1")} {
		if !strings.Contains(string(p), want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	// Worktrees removed.
	entries, _ := os.ReadDir(a.WorktreeBase)
	if len(entries) != 0 {
		t.Errorf("worktrees left behind: %v", entries)
	}
}

func TestReviewKeepWorktreesAndLeadFallback(t *testing.T) {
	a, _ := newApp(t,
		agentCfg(t, "r1", config.RoleReviewer, "valid"),
		agentCfg(t, "lead", config.RoleLead, "invalid-json"))
	res, err := a.Review(context.Background(), ReviewRequest{Number: 7, KeepWorktrees: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Review.Meta.LeadUsed || len(res.Review.Findings) != 1 || !strings.Contains(res.Review.Summary, "Deterministic") {
		t.Errorf("fallback: %+v", res.Review)
	}
	wt := filepath.Join(a.WorktreeBase, res.Manifest.ID, "r1")
	if _, err := os.Stat(filepath.Join(wt, "changed.txt")); err != nil {
		t.Errorf("worktree should be kept: %v", err)
	}
	head, _ := exec.Command("git", "-C", wt, "rev-parse", "HEAD").Output()
	if strings.TrimSpace(string(head)) != res.Manifest.HeadSHA {
		t.Errorf("worktree head mismatch")
	}
}

func TestReviewLeadImpostor(t *testing.T) {
	a, _ := newApp(t,
		agentCfg(t, "r1", config.RoleReviewer, "valid"),
		agentCfg(t, "lead", config.RoleLead, "lead-impostor"))
	res, err := a.Review(context.Background(), ReviewRequest{Number: 7})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Review.Findings) != 0 || !res.Review.Meta.LeadUsed {
		t.Errorf("finding attributed to a ghost reviewer must be dropped: %+v", res.Review.Findings)
	}
}

func TestReviewAllReviewersFail(t *testing.T) {
	a, _ := newApp(t,
		agentCfg(t, "r1", config.RoleReviewer, "exit-1"),
		agentCfg(t, "lead", config.RoleLead, "valid"))
	res, err := a.Review(context.Background(), ReviewRequest{Number: 7})
	if err == nil || !strings.Contains(err.Error(), "all 1 reviewers failed") {
		t.Fatalf("expected failure, got %v", err)
	}
	if res == nil || res.Manifest.Status != domain.RunFailed {
		t.Errorf("manifest should record the failure")
	}
	if entries, _ := os.ReadDir(a.WorktreeBase); len(entries) != 0 {
		t.Errorf("worktrees left behind after failure")
	}
}

func TestReviewFailFastAndCancel(t *testing.T) {
	a, _ := newApp(t,
		agentCfg(t, "r1", config.RoleReviewer, "timeout"),
		agentCfg(t, "r2", config.RoleReviewer, "exit-1"),
		agentCfg(t, "lead", config.RoleLead, "valid"))
	a.Config.Review.FailFast = true
	a.Process = nil
	start := time.Now()
	_, err := a.Review(context.Background(), ReviewRequest{Number: 7})
	if err == nil || !strings.Contains(err.Error(), "fail_fast") {
		t.Fatalf("expected fail_fast error, got %v", err)
	}
	if time.Since(start) > 9*time.Second {
		t.Error("fail_fast did not cancel the slow reviewer")
	}

	a2, _ := newApp(t, agentCfg(t, "r1", config.RoleReviewer, "timeout"), agentCfg(t, "lead", config.RoleLead, "valid"))
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(500 * time.Millisecond); cancel() }()
	_, err = a2.Review(ctx, ReviewRequest{Number: 7})
	if err == nil {
		t.Fatal("expected cancellation")
	}
	if entries, _ := os.ReadDir(a2.WorktreeBase); len(entries) != 0 {
		t.Errorf("worktrees left behind after cancel")
	}
}

func TestReviewForkHint(t *testing.T) {
	a, _ := newApp(t, agentCfg(t, "r1", config.RoleReviewer, "valid"), agentCfg(t, "lead", config.RoleLead, "valid"))
	f := a.Forge.(*fakeForge)
	f.parent = &domain.Repository{Host: "github.com", Owner: "upstream", Name: "proj"}
	_, err := a.Review(context.Background(), ReviewRequest{Number: 54})
	if err == nil || !strings.Contains(err.Error(), "acme/proj is a fork of upstream/proj") {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(err.Error(), `forge.remote to its name (currently "origin")`) {
		t.Errorf("hint does not name the configured remote: %v", err)
	}

	// Not a fork: the bare not-found error is kept.
	f.parent = nil
	if _, err := a.Review(context.Background(), ReviewRequest{Number: 54}); err == nil || strings.Contains(err.Error(), "hint:") {
		t.Fatalf("got %v", err)
	}
}

func TestReviewRemoteMismatch(t *testing.T) {
	a, _ := newApp(t, agentCfg(t, "r1", config.RoleReviewer, "valid"), agentCfg(t, "lead", config.RoleLead, "valid"))
	a.Config.Forge.BaseURL = "https://ghe.example.com/api/v3"
	_, err := a.Review(context.Background(), ReviewRequest{Number: 7})
	if err == nil || !strings.Contains(err.Error(), "expects ghe.example.com") {
		t.Fatalf("got %v", err)
	}
}

// TestReviewAsksAgainForAMissingReport: a reviewer that ends on prose is run
// once more with its own words and the output contract, and its report is
// kept; with retries disabled it fails as before.
func TestReviewAsksAgainForAMissingReport(t *testing.T) {
	a, _ := newApp(t,
		agentCfg(t, "r1", config.RoleReviewer, "prose-then-report"),
		agentCfg(t, "lead", config.RoleLead, "valid"))
	res, err := a.Review(context.Background(), ReviewRequest{Number: 7})
	if err != nil {
		t.Fatal(err)
	}
	if res.Review.Meta.ReviewersSucceeded != 1 {
		t.Fatalf("the retried reviewer must count as succeeded: %+v", res.Review.Meta)
	}
	var r1 domain.AgentExecution
	for _, ex := range res.Manifest.Agents {
		if ex.ID == "r1" {
			r1 = ex
		}
	}
	if !r1.Succeeded || r1.ReportRetries != 1 {
		t.Errorf("manifest: %+v", r1)
	}
	retry, err := os.ReadFile(filepath.Join(res.RunDir, "prompts", "reviewer-r1.retry1.txt"))
	if err != nil || !strings.Contains(string(retry), "B should stay lowercase") || !strings.Contains(string(retry), "OUTPUT CONTRACT") {
		t.Errorf("retry prompt must hand back the first run and the contract: %v\n%s", err, retry)
	}
	for _, p := range []string{"raw/r1.stdout", "raw/r1.retry1.stdout", "reports/r1.json"} {
		if _, err := os.Stat(filepath.Join(res.RunDir, p)); err != nil {
			t.Errorf("artifact %s: %v", p, err)
		}
	}

	none := 0
	a, _ = newApp(t,
		agentCfg(t, "r1", config.RoleReviewer, "prose-then-report"),
		agentCfg(t, "lead", config.RoleLead, "valid"))
	a.Config.Review.ReportRetries = &none
	if _, err := a.Review(context.Background(), ReviewRequest{Number: 7}); err == nil || !strings.Contains(err.Error(), "no JSON object found") {
		t.Fatalf("without retries the reviewer must fail as before: %v", err)
	}
}

// TestReviewDoesNotAskAgainAfterACutShortRun: a reviewer that stopped after
// a word has no review to convert, so it fails instead of being asked for a
// report it would make up.
// TestReviewDoesNotAskAgainAfterACutShortRun: a reviewer stopped by its
// timeout that wrote nothing usable is not asked again. It did not end without
// a report through its own fault, and the context window explanation does not
// apply: the run was stopped.
func TestReviewDoesNotAskAgainAfterAPointlessCutShortRun(t *testing.T) {
	cfg := agentCfg(t, "r1", config.RoleReviewer, "timeout-silent")
	cfg.Timeout = 700 * time.Millisecond
	a, _ := newApp(t, cfg,
		agentCfg(t, "r2", config.RoleReviewer, "valid"),
		agentCfg(t, "lead", config.RoleLead, "valid"))
	res, err := a.Review(context.Background(), ReviewRequest{Number: 7})
	if err != nil {
		t.Fatal(err)
	}
	var r1 domain.AgentExecution
	for _, ex := range res.Manifest.Agents {
		if ex.ID == "r1" {
			r1 = ex
		}
	}
	if r1.Succeeded || r1.Salvaged || !strings.Contains(r1.Error, "timed out") {
		t.Errorf("manifest: %+v", r1)
	}
	if r1.ReportRetries != 0 {
		t.Errorf("a run that wrote nothing to convert must not be asked again: %+v", r1)
	}
}

// TestReviewSalvagesAReportFromACutShortRun: the agent wrote its report, then
// kept going until it was stopped. The report is usable, and the run is a
// success with a warning rather than a lost review.
func TestReviewSalvagesAReportFromACutShortRun(t *testing.T) {
	cfg := agentCfg(t, "r1", config.RoleReviewer, "timeout-with-report")
	cfg.Output = config.OutputPiJSON
	cfg.Timeout = 700 * time.Millisecond
	a, _ := newApp(t, cfg,
		agentCfg(t, "r2", config.RoleReviewer, "valid"),
		agentCfg(t, "lead", config.RoleLead, "valid"))
	res, err := a.Review(context.Background(), ReviewRequest{Number: 7})
	if err != nil {
		t.Fatal(err)
	}
	var r1 domain.AgentExecution
	for _, ex := range res.Manifest.Agents {
		if ex.ID == "r1" {
			r1 = ex
		}
	}
	if !r1.Succeeded || !r1.Salvaged {
		t.Errorf("manifest: %+v", r1)
	}
	if r1.Error != "" {
		t.Errorf("a salvaged run must not report an error: %q", r1.Error)
	}
	if !strings.Contains(strings.Join(r1.Warnings, "\n"), "time budget") {
		t.Errorf("the warning must say the report was interrupted: %v", r1.Warnings)
	}
	if _, err := os.Stat(filepath.Join(res.RunDir, "reports", "r1.json")); err != nil {
		t.Errorf("the salvaged report was not written: %v", err)
	}
	if res.Review.Meta.ReviewersSucceeded != 2 {
		t.Errorf("reviewers succeeded = %d, want 2", res.Review.Meta.ReviewersSucceeded)
	}
}

// TestReviewAsksAgainForACutShortRunWithATranscript: the agent wrote prose but
// no report before it was stopped. A retry on its own deadline, with what it
// wrote, can still turn that into a report.
func TestReviewAsksAgainForACutShortRunWithATranscript(t *testing.T) {
	cfg := agentCfg(t, "r1", config.RoleReviewer, "timeout-no-report")
	cfg.Timeout = 700 * time.Millisecond
	a, _ := newApp(t, cfg,
		agentCfg(t, "r2", config.RoleReviewer, "valid"),
		agentCfg(t, "lead", config.RoleLead, "valid"))
	res, err := a.Review(context.Background(), ReviewRequest{Number: 7})
	if err != nil {
		t.Fatal(err)
	}
	// The retry asks for the report and the test agent answers it, so the
	// reviewer ends up with a usable report after being stopped once.
	if _, err := os.Stat(filepath.Join(res.RunDir, "prompts", "reviewer-r1.retry1.txt")); err != nil {
		t.Errorf("the reviewer was not asked again: %v", err)
	}
	p, _ := os.ReadFile(filepath.Join(res.RunDir, "prompts", "reviewer-r1.retry1.txt"))
	if !strings.Contains(string(p), "still reading the changed files") {
		t.Errorf("the retry prompt lacks the transcript: %s", p)
	}
}

// TestReviewDoesNotAskAgainAfterAModelAPIError: a reviewer whose provider
// refused every call (quota, budget) exits 0 without a word; asking it again
// would hit the same refusal, so it fails with the provider's message.
func TestReviewDoesNotAskAgainAfterAModelAPIError(t *testing.T) {
	r1cfg := agentCfg(t, "r1", config.RoleReviewer, "pi-api-error")
	r1cfg.Output = config.OutputPiJSON
	a, _ := newApp(t, r1cfg,
		agentCfg(t, "r2", config.RoleReviewer, "valid"),
		agentCfg(t, "lead", config.RoleLead, "valid"))
	res, err := a.Review(context.Background(), ReviewRequest{Number: 7})
	if err != nil {
		t.Fatal(err)
	}
	var r1 domain.AgentExecution
	for _, ex := range res.Manifest.Agents {
		if ex.ID == "r1" {
			r1 = ex
		}
	}
	if r1.Succeeded || r1.ReportRetries != 0 || !strings.Contains(r1.Error, "429: budget exceeded") || r1.Model != "p/m" {
		t.Errorf("manifest: %+v", r1)
	}
	if _, err := os.Stat(filepath.Join(res.RunDir, "prompts", "reviewer-r1.retry1.txt")); !os.IsNotExist(err) {
		t.Errorf("the reviewer was asked again: %v", err)
	}
}

// TestReviewGivesALargeDiffAsAFile: a diff over max_inline_diff_bytes is
// written in the reviewer worktree and indexed in the prompt instead.
func TestReviewGivesALargeDiffAsAFile(t *testing.T) {
	a, _ := newApp(t, agentCfg(t, "r1", config.RoleReviewer, "valid"), agentCfg(t, "lead", config.RoleLead, "valid"))
	a.Config.Review.Limits.MaxInlineDiffBytes = 10
	res, err := a.Review(context.Background(), ReviewRequest{Number: 7, KeepWorktrees: true})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := os.ReadFile(filepath.Join(res.RunDir, "prompts", "reviewer-r1.txt"))
	for _, want := range []string{"DIFF FILE (git diff", "in the file " + DiffFileName, "- changed.txt: 1-"} {
		if !strings.Contains(string(p), want) {
			t.Errorf("prompt lacks %q:\n%s", want, p)
		}
	}
	if strings.Contains(string(p), "BEGIN UNTRUSTED DIFF") {
		t.Error("the diff is still inlined")
	}
	diff, err := os.ReadFile(filepath.Join(a.WorktreeBase, res.Manifest.ID, "r1", DiffFileName))
	if err != nil || !strings.Contains(string(diff), "+B") {
		t.Errorf("diff file: %v\n%s", err, diff)
	}
}

// TestReviewExcludesPathsFromTheDiff: excluded paths leave the diff given to
// the reviewers, and the prompt says so.
func TestReviewExcludesPathsFromTheDiff(t *testing.T) {
	a, _ := newApp(t,
		agentCfg(t, "r1", config.RoleReviewer, "valid"),
		agentCfg(t, "lead", config.RoleLead, "valid"))
	a.Config.Review.DiffExclude = []string{"changed.*"}
	res, err := a.Review(context.Background(), ReviewRequest{Number: 7})
	if err != nil {
		t.Fatal(err)
	}
	diff, _ := os.ReadFile(filepath.Join(res.RunDir, "context", "diff.patch"))
	if strings.Contains(string(diff), "+B") {
		t.Errorf("excluded path left in the diff:\n%s", diff)
	}
	p, _ := os.ReadFile(filepath.Join(res.RunDir, "prompts", "reviewer-r1.txt"))
	if !strings.Contains(string(p), "left out of the diff") || !strings.Contains(string(p), "- changed.*") || !strings.Contains(string(p), "modified changed.txt") {
		t.Errorf("prompt must name the excluded patterns and keep the changed file listed")
	}
}

func TestReviewRunningLabel(t *testing.T) {
	want := []string{"add #7 conclave/running ctx=<nil>", "remove #7 conclave/running ctx=<nil>"}
	for _, tc := range []struct {
		name, mode string
		cancel     bool
	}{
		{name: "success", mode: "valid"},
		{name: "failure", mode: "exit-1"},
		{name: "interrupted", mode: "timeout", cancel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := newApp(t, agentCfg(t, "r1", config.RoleReviewer, tc.mode), agentCfg(t, "lead", config.RoleLead, "valid"))
			a.Config.Review.RunningLabel = "conclave/running"
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				go func() { time.Sleep(500 * time.Millisecond); cancel() }()
			}
			_, err := a.Review(ctx, ReviewRequest{Number: 7})
			if (err == nil) != (tc.mode == "valid") {
				t.Fatalf("unexpected outcome: %v", err)
			}
			if got := a.Forge.(*fakeForge).labelEvents; strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Errorf("label calls: %q, want %q", got, want)
			}
		})
	}

	a, _ := newApp(t, agentCfg(t, "r1", config.RoleReviewer, "valid"), agentCfg(t, "lead", config.RoleLead, "valid"))
	if _, err := a.Review(context.Background(), ReviewRequest{Number: 7}); err != nil {
		t.Fatal(err)
	}
	if got := a.Forge.(*fakeForge).labelEvents; len(got) != 0 {
		t.Errorf("no running label configured, yet: %q", got)
	}
}

// TestReviewSalvagesAReportCutMidMessage: the run was stopped while the model
// was still writing its report, so the JSON has no end. It is closed and used,
// and the run carries the warning that says findings after the cut are missing.
func TestReviewSalvagesAReportCutMidMessage(t *testing.T) {
	cfg := agentCfg(t, "r1", config.RoleReviewer, "pi-truncated")
	cfg.Output = config.OutputPiJSON
	a, _ := newApp(t, cfg,
		agentCfg(t, "r2", config.RoleReviewer, "valid"),
		agentCfg(t, "lead", config.RoleLead, "valid"))
	res, err := a.Review(context.Background(), ReviewRequest{Number: 7})
	if err != nil {
		t.Fatal(err)
	}
	var r1 domain.AgentExecution
	for _, ex := range res.Manifest.Agents {
		if ex.ID == "r1" {
			r1 = ex
		}
	}
	if !r1.Succeeded || !r1.Salvaged {
		t.Fatalf("manifest: %+v", r1)
	}
	warnings := strings.Join(r1.Warnings, "\n")
	for _, want := range []string{"stopped while still writing", "cut off while being written", "cut before its verdict"} {
		if !strings.Contains(warnings, want) {
			t.Errorf("the warning must mention %q: %v", want, r1.Warnings)
		}
	}
	// The repair must be what made this usable, not a fixture that happened to
	// emit a complete report: the cut loses the findings that came after it.
	raw, err := os.ReadFile(filepath.Join(res.RunDir, "reports", "r1.json"))
	if err != nil {
		t.Fatalf("the salvaged report was not written: %v", err)
	}
	var rep domain.AgentReport
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Findings) != 1 {
		t.Errorf("a report cut inside the first finding must keep exactly one, got %d", len(rep.Findings))
	}
	// A partial report must never be able to approve what it could not read.
	if rep.Verdict != domain.VerdictComment {
		t.Errorf("verdict = %q, want comment on a report cut before its verdict", rep.Verdict)
	}
}

// TestWithGraceKeepsCancellation: the grace period replaces the reviewer's
// deadline, not its cancellation. A retry nothing can interrupt would outlive
// Ctrl-C and review.timeout, and the run could not finish before it did.
func TestWithGraceKeepsCancellation(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	retry, cancelRetry := withGrace(parent, time.Minute)
	defer cancelRetry()

	if _, ok := retry.Deadline(); !ok {
		t.Error("the grace period must set a later deadline")
	}
	if retry.Err() != nil {
		t.Fatalf("a fresh retry context is already done: %v", retry.Err())
	}
	// The parent's cancellation still reaches the retry.
	cancelParent()
	select {
	case <-retry.Done():
	case <-time.After(2 * time.Second):
		t.Error("cancelling the parent must cancel the retry")
	}
}

// TestReviewSalvagedReportKeepsItsFindings: the salvage queries git for each
// finding's path, so it must run while its context is still live. Releasing the
// retry's context first made every PathExistsAt call fail, and the run was then
// recorded as succeeded with a salvaged report holding no findings at all -
// a clean-looking review of nothing.
func TestReviewSalvagedReportKeepsItsFindings(t *testing.T) {
	cfg := agentCfg(t, "r1", config.RoleReviewer, "pi-truncated")
	cfg.Output = config.OutputPiJSON
	a, _ := newApp(t, cfg,
		agentCfg(t, "r2", config.RoleReviewer, "valid"),
		agentCfg(t, "lead", config.RoleLead, "valid"))
	res, err := a.Review(context.Background(), ReviewRequest{Number: 7})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(res.RunDir, "reports", "r1.json"))
	if err != nil {
		t.Fatalf("the salvaged report was not written: %v", err)
	}
	var rep domain.AgentReport
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Findings) == 0 {
		t.Error("a salvaged report whose findings all vanished reads like a clean review: the context was released before the salvage")
	}
	for _, f := range rep.Findings {
		if strings.Contains(strings.ToUpper(f.File), "DOES NOT EXIST") {
			t.Errorf("a finding survived with an unresolved path: %+v", f)
		}
	}
}

// TestVerdictIsAddedOnlyToAReviewerReport: ask, plan and triage run the same
// reviewer and lead agents with other schemas, and a triage consolidation cut
// after its summary keeps exactly the fields a cut lead review keeps. The shape
// cannot tell them apart, so the command decides: only a review adds a verdict.
func TestVerdictIsAddedOnlyToAReviewerReport(t *testing.T) {
	cut := []byte(`{"schema_version":"1","summary":"the timeout path is never taken`)
	ag := config.AgentConfig{ID: "r", Output: config.OutputAuto}
	for _, tc := range []struct {
		name     string
		verdicts bool
	}{{"review", true}, {"triage", false}} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := artifact.Open(t.TempDir(), "run")
			if err != nil {
				t.Fatal(err)
			}
			var ex domain.AgentExecution
			ad, err := (&App{}).adapt(&run{store: store, verdicts: tc.verdicts}, ag, cut, &ex)
			if err != nil {
				t.Fatal(err)
			}
			var obj map[string]any
			if err := json.Unmarshal(ad.Report, &obj); err != nil {
				t.Fatalf("the cut report must be closed: %v: %s", err, ad.Report)
			}
			if _, has := obj["verdict"]; has != tc.verdicts {
				t.Errorf("verdict added = %v, want %v: %s", has, tc.verdicts, ad.Report)
			}
			if slices.Contains(ex.Warnings, cutOffVerdictWarning) != tc.verdicts {
				t.Errorf("the verdict warning must follow the verdict: %v", ex.Warnings)
			}
		})
	}
}

// TestAdaptKeepsAReportThatParses: the repair is for a report that was cut. A
// complete one followed by an unterminated fragment carrying the marker, a
// quoted schema or a second attempt, must not be replaced by that fragment.
func TestAdaptKeepsAReportThatParses(t *testing.T) {
	report := `{"schema_version":"1","reviewer":{"id":"r"},"summary":"s","findings":[],"verdict":"request_changes"}`
	stdout := []byte(report + "\nThe schema starts with {\"schema_version\":\"1\",\"summ")
	store, err := artifact.Open(t.TempDir(), "run")
	if err != nil {
		t.Fatal(err)
	}
	var ex domain.AgentExecution
	ad, err := (&App{}).adapt(&run{store: store, verdicts: true}, config.AgentConfig{ID: "r", Output: config.OutputAuto}, stdout, &ex)
	if err != nil {
		t.Fatal(err)
	}
	got, err := agent.ExtractJSON(ad.Report, "schema_version")
	if err != nil || string(got) != report {
		t.Errorf("the complete report must stand: %s (%v)", got, err)
	}
	if slices.Contains(ex.Warnings, cutOffReportWarning) {
		t.Errorf("a report that parses was not cut off: %v", ex.Warnings)
	}
}

// TestReviewWarnsAboutACutLeadReport: the reviewers' warnings are read from the
// manifest before the lead runs, so the lead's own, adapt()'s included, reach
// the review only through what runLead returns.
func TestReviewWarnsAboutACutLeadReport(t *testing.T) {
	a, _ := newApp(t,
		agentCfg(t, "r1", config.RoleReviewer, "valid"),
		agentCfg(t, "lead", config.RoleLead, "lead-cut"))
	res, err := a.Review(context.Background(), ReviewRequest{Number: 7})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Review.Meta.LeadUsed {
		t.Fatalf("the cut consolidation must still be used: %v", res.Review.Warnings)
	}
	if !slices.Contains(res.Review.Warnings, cutOffReportWarning) {
		t.Errorf("the review must say the lead's report was cut off: %v", res.Review.Warnings)
	}
}

// TestReviewReportGraceZeroDisablesTheRetry: report_grace: 0 is documented as
// disabling the retry of a run that ran out of time. It must not launch one on a
// context that expires immediately, which turned a timeout into a confusing
// "context deadline exceeded" while still burning a prompt and a raw file.
func TestReviewReportGraceZeroDisablesTheRetry(t *testing.T) {
	zero := time.Duration(0)
	cfg := agentCfg(t, "r1", config.RoleReviewer, "timeout-no-report")
	cfg.Timeout = 700 * time.Millisecond
	a, _ := newApp(t, cfg,
		agentCfg(t, "r2", config.RoleReviewer, "valid"),
		agentCfg(t, "lead", config.RoleLead, "valid"))
	a.Config.Review.ReportGrace = &zero
	res, err := a.Review(context.Background(), ReviewRequest{Number: 7})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(res.RunDir, "prompts", "reviewer-r1.retry1.txt")); !os.IsNotExist(err) {
		t.Errorf("a zero grace period must not start a retry: %v", err)
	}
	var r1 domain.AgentExecution
	for _, ex := range res.Manifest.Agents {
		if ex.ID == "r1" {
			r1 = ex
		}
	}
	if strings.Contains(r1.Error, "context deadline exceeded") {
		t.Errorf("a disabled retry must leave the timeout readable: %q", r1.Error)
	}
	// The first attempt was stopped with only prose written, so the reviewer is
	// asked to convert it; with the retry disabled, that prose is what failed.
	if r1.ReportRetries != 0 || !strings.Contains(r1.Error, "no JSON object") {
		t.Errorf("manifest: %+v", r1)
	}
}

// TestSalvageRefusesToReportAnUnvalidatedReport: a finding whose path cannot be
// checked is dropped, so a context that died while git was being queried leaves
// an empty report that reads like a clean review. The salvage must fail instead
// of returning success with nothing in it.
func TestSalvageRefusesToReportAnUnvalidatedReport(t *testing.T) {
	a, _ := newApp(t, agentCfg(t, "r1", config.RoleReviewer, "valid"), agentCfg(t, "lead", config.RoleLead, "valid"))
	dead, cancel := context.WithCancel(context.Background())
	cancel()

	var ex domain.AgentExecution
	report, _, err := a.salvageTimedOut(dead, &run{cfg: a.Config, git: nil, changed: nil, store: nil},
		a.Config.Agents[0], t.TempDir(), "r1", time.Minute, []byte(validPiStreamFor("r1")), &ex)
	if err == nil {
		t.Errorf("a report that could not be validated must not be reported as salvaged: %+v", report)
	}
	if ex.Salvaged {
		t.Error("the run must not be marked salvaged when its findings were dropped")
	}
}

// validPiStreamFor renders a pi stream carrying one finding on changed.txt.
func validPiStreamFor(id string) string {
	rep := `{"schema_version":"1","reviewer":{"id":"` + id + `"},"summary":"s",` +
		`"findings":[{"id":"f1","category":"correctness","severity":"high","confidence":0.9,` +
		`"file":"changed.txt","start_line":1,"end_line":1,"title":"t","description":"d"}],` +
		`"verdict":"comment"}`
	b, _ := json.Marshal(rep)
	return `{"type":"message_start","message":{"role":"assistant","content":[]}}` + "\n" +
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":` + string(b) + `}}` + "\n"
}

// TestSalvageReportsTheConfiguredTimeout: the run's budget must be reported from
// the configuration. Recomputing it from a context whose deadline has passed
// yields a negative duration, and the message then blames the reviewer for dying
// before it started. The salvage here fails on the markers of its own report, so
// the message it builds is still the one under test.
func TestSalvageReportsTheConfiguredTimeout(t *testing.T) {
	a, _ := newApp(t, agentCfg(t, "r1", config.RoleReviewer, "valid"), agentCfg(t, "lead", config.RoleLead, "valid"))
	var ex domain.AgentExecution
	_, _, err := a.salvageTimedOut(context.Background(), &run{cfg: a.Config, store: nil},
		a.Config.Agents[0], t.TempDir(), "r1", 15*time.Minute,
		[]byte(validPiStreamFor("someone-else")), &ex)
	if err == nil {
		t.Fatal("a report naming another reviewer must be refused")
	}
	if strings.Contains(err.Error(), "timed out after -") {
		t.Errorf("a negative timeout was reported: %v", err)
	}
	if !strings.Contains(err.Error(), "timed out after 15m0s") {
		t.Errorf("the message must name the configured timeout, got %v", err)
	}
}

// TestReviewSalvagesARunStoppedByTheReviewTimeout: a reviewer killed because
// review.timeout expired produces a plain context error, not a TimedOut result.
// Its output used to be discarded, then salvaged only to be thrown away with the
// whole review: the published review is what must carry its findings.
func TestReviewSalvagesARunStoppedByTheReviewTimeout(t *testing.T) {
	cfg := agentCfg(t, "r1", config.RoleReviewer, "timeout-with-report")
	cfg.Output = config.OutputPiJSON
	// A generous agent timeout, so the run is stopped by the review's own.
	cfg.Timeout = time.Minute
	a, _ := newApp(t, cfg,
		agentCfg(t, "r2", config.RoleReviewer, "valid"),
		agentCfg(t, "lead", config.RoleLead, "valid"))
	a.Config.Review.Timeout = 2 * time.Second
	res, err := a.Review(context.Background(), ReviewRequest{Number: 7})
	if err != nil {
		t.Fatalf("a review whose reviewers left reports must be published: %v", err)
	}
	var r1 domain.AgentExecution
	for _, ex := range res.Manifest.Agents {
		if ex.ID == "r1" {
			r1 = ex
		}
		if ex.ID == "lead" {
			t.Errorf("no lead can run once review.timeout has expired: %+v", ex)
		}
	}
	if !r1.Salvaged {
		t.Errorf("a run stopped by review.timeout must be salvageable too: %+v", r1)
	}
	if res.Review.Meta.ReviewersSucceeded != 2 || res.Review.Meta.LeadUsed {
		t.Errorf("both reports, no lead: %+v", res.Review.Meta)
	}
	var reportedByR1 bool
	for _, f := range res.Review.Findings {
		reportedByR1 = reportedByR1 || slices.Contains(f.ReportedBy, "r1")
	}
	if !reportedByR1 {
		t.Errorf("the salvaged report must reach the review: %+v", res.Review.Findings)
	}
	if res.Review.Verdict == domain.VerdictApprove {
		t.Errorf("a review cut short by review.timeout must not approve: %s", res.Review.Verdict)
	}
	if !slices.ContainsFunc(res.Review.Warnings, func(w string) bool { return strings.Contains(w, "review.timeout expired before lead") }) {
		t.Errorf("the review must say why the lead did not run: %v", res.Review.Warnings)
	}
}

// TestReviewCutShortByTheTimeoutDoesNotApprove: no lead weighed the reports, and
// a reviewer stopped may have lost the findings that mattered, so approving
// reports still yield a comment.
func TestReviewCutShortByTheTimeoutDoesNotApprove(t *testing.T) {
	cfg := agentCfg(t, "r1", config.RoleReviewer, "timeout-approving")
	cfg.Output = config.OutputPiJSON
	cfg.Timeout = time.Minute
	a, _ := newApp(t, cfg,
		agentCfg(t, "r2", config.RoleReviewer, "approving"),
		agentCfg(t, "lead", config.RoleLead, "valid"))
	a.Config.Review.Timeout = 2 * time.Second
	res, err := a.Review(context.Background(), ReviewRequest{Number: 7})
	if err != nil {
		t.Fatal(err)
	}
	if res.Review.Verdict != domain.VerdictComment {
		t.Errorf("verdict = %s, want comment", res.Review.Verdict)
	}
}

// TestReviewTimeoutWithoutReportFails: the salvage only rescues reports that
// exist. A review.timeout that left none is still a failed review.
func TestReviewTimeoutWithoutReportFails(t *testing.T) {
	r1 := agentCfg(t, "r1", config.RoleReviewer, "timeout-silent")
	r2 := agentCfg(t, "r2", config.RoleReviewer, "timeout-silent")
	for _, c := range []*config.AgentConfig{&r1, &r2} {
		c.Output = config.OutputPiJSON
		c.Timeout = time.Minute
	}
	a, _ := newApp(t, r1, r2, agentCfg(t, "lead", config.RoleLead, "valid"))
	a.Config.Review.Timeout = 2 * time.Second
	if _, err := a.Review(context.Background(), ReviewRequest{Number: 7}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a review.timeout with no report must fail on the deadline, got %v", err)
	}
}

// TestRetryTimeoutIsNotZero: a retry of a run that ended normally, then stopped
// by its own deadline, used to be recorded as "timed out after 0s".
func TestRetryTimeoutIsNotZero(t *testing.T) {
	cfg := agentCfg(t, "r1", config.RoleReviewer, "prose-then-timeout")
	cfg.Output = config.OutputPiJSON
	cfg.Timeout = 2 * time.Second
	a, _ := newApp(t, cfg,
		agentCfg(t, "r2", config.RoleReviewer, "valid"),
		agentCfg(t, "lead", config.RoleLead, "valid"))
	res, err := a.Review(context.Background(), ReviewRequest{Number: 7})
	if err != nil {
		t.Fatal(err)
	}
	var r1 domain.AgentExecution
	for _, ex := range res.Manifest.Agents {
		if ex.ID == "r1" {
			r1 = ex
		}
	}
	if strings.Contains(r1.Error, "timed out after 0s") {
		t.Errorf("a stopped retry must report the budget it ran on: %+v", r1)
	}
}

// TestIsStoppedIgnoresCancellation: Ctrl-C means stop. Treating it as a deadline
// salvaged the run, cleared its error and kept querying git for two minutes after
// the user asked to stop.
func TestIsStoppedIgnoresCancellation(t *testing.T) {
	if isStopped(context.Canceled) {
		t.Error("a user cancellation must not be salvaged")
	}
	if isStopped(fmt.Errorf("run: %w", context.Canceled)) {
		t.Error("a wrapped cancellation must not be salvaged either")
	}
	if !isStopped(context.DeadlineExceeded) {
		t.Error("an expired deadline is what the salvage is for")
	}
	if !isStopped(cutShort{err: errors.New("timed out after 15m0s")}) {
		t.Error("a run stopped by its own budget is salvageable")
	}
}

// TestSalvagedLeadIsRecordedAsSucceeded: the consolidation is published and the
// review reports LeadUsed, so recording the lead as a failed agent in the
// manifest contradicts what the caller did with its output.
func TestSalvagedLeadIsRecordedAsSucceeded(t *testing.T) {
	lead := agentCfg(t, "lead", config.RoleLead, "lead-timeout")
	lead.Output = config.OutputPiJSON
	lead.Timeout = 700 * time.Millisecond
	a, _ := newApp(t, agentCfg(t, "r1", config.RoleReviewer, "valid"), lead)
	res, err := a.Review(context.Background(), ReviewRequest{Number: 7})
	if err != nil {
		t.Fatal(err)
	}
	var ex domain.AgentExecution
	for _, e := range res.Manifest.Agents {
		if e.ID == "lead" {
			ex = e
		}
	}
	// The fixture always outlives the lead's timeout: a missing salvage is the
	// regression this test exists for, not a reason to skip it.
	if !ex.Salvaged {
		t.Fatalf("the lead was stopped after writing its consolidation, which must be salvaged: %+v", ex)
	}
	if !ex.Succeeded {
		t.Errorf("a published consolidation must not be recorded as a failed agent: %+v", ex)
	}
}

// TestSalvageContextHonoursCancellation: the docs promise that a Ctrl-C stops a
// salvage where it is. A plain WithoutCancel kept reading for two minutes after
// it, which is what the helper exists to prevent.
func TestSalvageContextHonoursCancellation(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	salvageCtx, cancel := salvageContext(parent)
	defer cancel()

	if _, ok := salvageCtx.Deadline(); !ok {
		t.Error("the salvage window must have a deadline of its own")
	}
	cancelParent()
	select {
	case <-salvageCtx.Done():
	case <-time.After(2 * time.Second):
		t.Error("cancelling the run must cancel the salvage")
	}
}
