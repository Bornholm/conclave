package gitrepo

import (
	"context"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// fixtureRepo builds: base commit on main, feature branch with a changed,
// renamed and deleted file. It returns the repo, base and head SHAs.
func fixtureRepo(t *testing.T) (*Git, string, string) {
	t.Helper()
	dir := t.TempDir()
	ctx := context.Background()
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
	write("old.txt", "same content that is long enough to be detected as rename\n")
	write("deleted.txt", "gone\n")
	run("add", ".")
	run("commit", "-q", "-m", "base")
	base := run("rev-parse", "HEAD")
	run("checkout", "-q", "-b", "feature")
	write("changed.txt", "a\nB\n")
	run("mv", "old.txt", "new.txt")
	run("rm", "-q", "deleted.txt")
	run("add", ".")
	run("commit", "-q", "-m", "feature")
	head := run("rev-parse", "HEAD")
	run("checkout", "-q", "main")
	write("main-only.txt", "x\n")
	run("add", ".")
	run("commit", "-q", "-m", "main moves on")
	newBase := run("rev-parse", "HEAD")
	run("remote", "add", "origin", "https://github.com/acme/proj.git")

	g, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = base
	return g, newBase, head
}

func TestRepositoryOperations(t *testing.T) {
	g, base, head := fixtureRepo(t)
	ctx := context.Background()

	repo, err := g.Repository(ctx, "origin")
	if err != nil || repo.Owner != "acme" || repo.Name != "proj" {
		t.Fatalf("repository: %v %+v", err, repo)
	}
	if _, err := g.Repository(ctx, "nope"); err == nil {
		t.Error("expected missing remote error")
	}

	mb, err := g.MergeBase(ctx, base, head)
	if err != nil || mb == base || mb == head {
		t.Fatalf("merge-base: %v %s", err, mb)
	}
	if err := g.EnsureCommit(ctx, head, "", ""); err != nil {
		t.Errorf("ensure existing commit: %v", err)
	}
	if err := g.EnsureCommit(ctx, "0123456789012345678901234567890123456789", "", ""); err == nil {
		t.Error("expected failure for unknown commit")
	}
	if err := g.EnsureCommit(ctx, "not-a-sha", "", ""); err == nil {
		t.Error("expected failure for invalid sha")
	}

	diff, truncated, err := g.Diff(ctx, mb, head, 0)
	if err != nil || truncated {
		t.Fatalf("diff: %v", err)
	}
	for _, want := range []string{"rename from old.txt", "deleted file", "+B"} {
		if !strings.Contains(string(diff), want) {
			t.Errorf("diff lacks %q:\n%s", want, diff)
		}
	}
	if _, truncated, _ := g.Diff(ctx, mb, head, 20); !truncated {
		t.Error("expected truncation")
	}
	// Excluded paths leave the diff, the others stay.
	excluded, _, err := g.Diff(ctx, mb, head, 0, "changed.*", "*deleted*")
	if err != nil {
		t.Fatalf("diff with exclusions: %v", err)
	}
	if strings.Contains(string(excluded), "+B") || strings.Contains(string(excluded), "deleted file") || !strings.Contains(string(excluded), "new.txt") {
		t.Errorf("exclusions not applied:\n%s", excluded)
	}
	names, _ := g.DiffNames(ctx, mb, head)
	if len(names) != 3 {
		t.Errorf("names: %v", names)
	}
	if !g.PathExistsAt(ctx, head, "new.txt") || g.PathExistsAt(ctx, head, "deleted.txt") {
		t.Error("PathExistsAt")
	}
	if content, err := g.FileAt(ctx, head, "changed.txt", 100); err != nil || string(content) != "a\nB\n" {
		t.Errorf("FileAt: %v %q", err, content)
	}
	if _, err := g.FileAt(ctx, head, "changed.txt", 2); err == nil || !strings.Contains(err.Error(), "larger than 2") {
		t.Errorf("FileAt over the limit: %v", err)
	}
	if _, err := g.FileAt(ctx, head, "deleted.txt", 100); err == nil {
		t.Error("FileAt of a deleted file")
	}

	wt1 := filepath.Join(t.TempDir(), "wt", "a")
	wt2 := filepath.Join(t.TempDir(), "wt", "b")
	for _, wt := range []string{wt1, wt2} {
		if err := g.CreateWorktree(ctx, wt, head); err != nil {
			t.Fatalf("worktree: %v", err)
		}
		if h, _ := g.WorktreeHead(ctx, wt); h != head {
			t.Errorf("worktree head %s != %s", h, head)
		}
		if _, err := os.Stat(filepath.Join(wt, "new.txt")); err != nil {
			t.Errorf("worktree content: %v", err)
		}
		headFile, _ := os.ReadFile(filepath.Join(wt, ".git"))
		if !strings.HasPrefix(string(headFile), "gitdir:") {
			t.Error("worktree .git should be a file")
		}
	}
	// Modifying a worktree must not touch the main tree.
	os.WriteFile(filepath.Join(wt1, "changed.txt"), []byte("hacked"), 0o644)
	if data, _ := os.ReadFile(filepath.Join(g.Root(), "changed.txt")); string(data) == "hacked" {
		t.Error("main repository modified")
	}
	if h, _ := g.run(ctx, "rev-parse", "HEAD"); strings.TrimSpace(h) != base {
		t.Error("main HEAD moved")
	}
	for _, wt := range []string{wt1, wt2} {
		if err := g.RemoveWorktree(ctx, wt); err != nil {
			t.Errorf("remove: %v", err)
		}
		if _, err := os.Stat(wt); !os.IsNotExist(err) {
			t.Errorf("worktree %s still exists", wt)
		}
	}
	if err := g.RemoveWorktree(ctx, wt1); err != nil {
		t.Errorf("remove twice should be idempotent: %v", err)
	}
}

func TestEnsureCommitFetchesPullRef(t *testing.T) {
	// upstream has the PR head under refs/pull/1/head; local clone lacks it.
	upstream, _, head := fixtureRepo(t)
	ctx := context.Background()
	if _, err := upstream.run(ctx, "update-ref", "refs/pull/1/head", head); err != nil {
		t.Fatal(err)
	}
	if _, err := upstream.run(ctx, "branch", "-D", "feature"); err != nil {
		t.Fatal(err)
	}
	local := t.TempDir()
	cmd := exec.Command("git", "clone", "-q", "--no-local", "--branch", "main", upstream.Root(), local)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone: %v %s", err, out)
	}
	g, err := Open(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	if g.HasCommit(ctx, head) {
		t.Fatal("clone should not have the PR head yet")
	}
	if err := g.EnsureCommit(ctx, head, PullRequestRef(1), "origin"); err != nil {
		t.Fatal(err)
	}
	if !g.HasCommit(ctx, head) {
		t.Error("PR head not fetched")
	}
}

func TestOpenNotARepository(t *testing.T) {
	if _, err := Open(context.Background(), t.TempDir()); err == nil {
		t.Fatal("expected error")
	}
}

func TestRedactArgs(t *testing.T) {
	got := redactArgs([]string{"fetch", "https://user:tok@host/x.git"})
	if got[1] != "https://***@host/x.git" {
		t.Errorf("got %q", got[1])
	}
}

// TestCreateWorktreeCleansUpAFailedAdd: git registers the worktree before a
// failing post-checkout hook; the failure must not leave it listed, and the
// error must say more than git's progress line.
func TestCreateWorktreeCleansUpAFailedAdd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell hook")
	}
	g, _, head := fixtureRepo(t)
	ctx := context.Background()
	hook := filepath.Join(g.CommonDir(), "hooks", "post-checkout")
	if err := os.MkdirAll(filepath.Dir(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(t.TempDir(), "wt", "r1")
	err := g.CreateWorktree(ctx, wt, head)
	if err == nil {
		t.Fatal("expected the failing hook to fail the add")
	}
	if !strings.Contains(err.Error(), "exit status 3") {
		t.Errorf("error lacks the exit status: %v", err)
	}
	if _, statErr := os.Stat(wt); !os.IsNotExist(statErr) {
		t.Errorf("worktree directory left behind: %v", statErr)
	}
	out, _ := g.run(ctx, "worktree", "list", "--porcelain")
	if strings.Contains(out, wt) {
		t.Errorf("worktree still registered:\n%s", out)
	}
}

func TestChangedFilesAndLog(t *testing.T) {
	g, newBase, head := fixtureRepo(t)
	ctx := context.Background()
	if _, err := g.run(ctx, "checkout", "-q", "feature"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(g.Root(), "added.bin"), []byte{0, 1, 2, 0}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := g.run(ctx, "add", "added.bin"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.run(ctx, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "binary\n\nwith a body"); err != nil {
		t.Fatal(err)
	}
	tip, _, err := g.ResolveRevision(ctx, "feature")
	if err != nil {
		t.Fatal(err)
	}
	mb, err := g.MergeBase(ctx, newBase, head)
	if err != nil {
		t.Fatal(err)
	}
	files, err := g.ChangedFiles(ctx, mb, tip)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range files {
		got[f.Path] = fmt.Sprintf("%s %s +%d -%d", f.Status, f.PreviousPath, f.Additions, f.Deletions)
	}
	want := map[string]string{
		"added.bin":   "added  +0 -0",
		"changed.txt": "modified  +1 -1",
		"deleted.txt": "deleted  +0 -1",
		"new.txt":     "renamed old.txt +0 -0",
	}
	if !maps.Equal(got, want) {
		t.Errorf("changed files:\n got %v\nwant %v", got, want)
	}
	subjects, err := g.CommitSubjects(ctx, mb, tip, 10)
	if err != nil || !slices.Equal(subjects, []string{"binary", "feature"}) {
		t.Errorf("subjects = %q, %v", subjects, err)
	}
	log, err := g.Log(ctx, mb, tip, 10)
	if err != nil || !strings.Contains(log, "with a body") || !strings.Contains(log, "    feature\n") || strings.Contains(log, "main moves on") {
		t.Errorf("log = %q, %v", log, err)
	}
	if log, err := g.Log(ctx, mb, tip, 1); err != nil || strings.Contains(log, "    feature\n") {
		t.Errorf("log bounded to one commit = %q, %v", log, err)
	}
}

func TestDefaultBranch(t *testing.T) {
	g, _, _ := fixtureRepo(t)
	ctx := context.Background()
	if b, err := g.DefaultBranch(ctx, "origin"); err != nil || b != "main" {
		t.Errorf("without a remote HEAD: %q, %v", b, err)
	}
	// The remote's HEAD wins over a local main.
	for _, args := range [][]string{
		{"update-ref", "refs/remotes/origin/trunk", "main"},
		{"symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/trunk"},
	} {
		if _, err := g.run(ctx, args...); err != nil {
			t.Fatal(err)
		}
	}
	if b, err := g.DefaultBranch(ctx, "origin"); err != nil || b != "origin/trunk" {
		t.Errorf("with a remote HEAD: %q, %v", b, err)
	}
	if _, err := g.run(ctx, "branch", "-q", "-m", "main", "dev"); err != nil {
		t.Fatal(err)
	}
	if b, err := g.DefaultBranch(ctx, ""); err == nil {
		t.Errorf("no main, no master, no remote: got %q", b)
	}
}
