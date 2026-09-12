package app

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bornholm/conclave/internal/config"
	"github.com/bornholm/conclave/internal/domain"
	"github.com/bornholm/conclave/internal/testutil"
)

// fakeForge serves one pull request from memory.
type fakeForge struct {
	pr    *domain.PullRequest
	files []domain.ChangedFile
}

func (f *fakeForge) Name() string { return "fake" }
func (f *fakeForge) GetPullRequest(_ context.Context, _ domain.Repository, n int64) (*domain.PullRequest, error) {
	if n != f.pr.Number {
		return nil, errors.New("not found")
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
	return &domain.Issue{Number: n, Title: "issue", Description: "body"}, nil
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
	for _, want := range []string{"UNTRUSTED", "Fixes #3", "+B", "ISSUE #3", "ISSUE #9", "DISCUSSION (1 entries", "comment by maint", "modified changed.txt", "WORKING DIRECTORY\n" + filepath.Join(a.WorktreeBase, res.Manifest.ID, "r1")} {
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

func TestReviewRemoteMismatch(t *testing.T) {
	a, _ := newApp(t, agentCfg(t, "r1", config.RoleReviewer, "valid"), agentCfg(t, "lead", config.RoleLead, "valid"))
	a.Config.Forge.BaseURL = "https://ghe.example.com/api/v3"
	_, err := a.Review(context.Background(), ReviewRequest{Number: 7})
	if err == nil || !strings.Contains(err.Error(), "expects ghe.example.com") {
		t.Fatalf("got %v", err)
	}
}
