package gitrepo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
	names, _ := g.DiffNames(ctx, mb, head)
	if len(names) != 3 {
		t.Errorf("names: %v", names)
	}
	if !g.PathExistsAt(ctx, head, "new.txt") || g.PathExistsAt(ctx, head, "deleted.txt") {
		t.Error("PathExistsAt")
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
