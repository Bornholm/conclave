package gitrepo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bornholm/conclave/internal/domain"
)

var shaRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// ErrNotARepository is returned when the directory is not inside a Git work tree.
var ErrNotARepository = errors.New("not a git repository")

// Git runs git commands against one repository. All commands use argument
// vectors, never a shell.
type Git struct {
	root      string
	commonDir string
	gitBin    string
}

// Open locates the repository containing dir.
func Open(ctx context.Context, dir string) (*Git, error) {
	bin, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("git executable not found: %w", err)
	}
	g := &Git{gitBin: bin, root: dir}
	root, err := g.run(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrNotARepository, dir)
	}
	g.root = strings.TrimSpace(root)
	common, err := g.run(ctx, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, err
	}
	g.commonDir = strings.TrimSpace(common)
	return g, nil
}

// Root returns the work tree root.
func (g *Git) Root() string { return g.root }

// CommonDir returns the shared .git directory (also for linked worktrees).
func (g *Git) CommonDir() string { return g.commonDir }

// run executes git in the repository root and returns stdout.
func (g *Git) run(ctx context.Context, args ...string) (string, error) {
	return g.runIn(ctx, g.root, args...)
}

func (g *Git) runIn(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, g.gitBin, args...)
	cmd.Dir = dir
	cmd.Env = append(cleanGitEnv(os.Environ()), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return stdout.String(), fmt.Errorf("git %s: %s", strings.Join(redactArgs(args), " "), msg)
	}
	return stdout.String(), nil
}

// cleanGitEnv drops variables that would redirect git to another repository.
func cleanGitEnv(env []string) []string {
	out := env[:0:0]
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		switch name {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR",
			"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE":
			continue
		}
		out = append(out, kv)
	}
	return out
}

// redactArgs hides credentials that may appear in clone URLs.
func redactArgs(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		if i := strings.Index(a, "://"); i >= 0 {
			if at := strings.Index(a, "@"); at > i {
				a = a[:i+3] + "***@" + a[at+1:]
			}
		}
		out[i] = a
	}
	return out
}

// RemoteURL returns the fetch URL of the named remote.
func (g *Git) RemoteURL(ctx context.Context, name string) (string, error) {
	out, err := g.run(ctx, "remote", "get-url", name)
	if err != nil {
		return "", fmt.Errorf("remote %q not found: %w", name, err)
	}
	return strings.TrimSpace(out), nil
}

// Repository resolves the named remote into a repository identity.
func (g *Git) Repository(ctx context.Context, remote string) (domain.Repository, error) {
	u, err := g.RemoteURL(ctx, remote)
	if err != nil {
		return domain.Repository{}, err
	}
	return ParseRemoteURL(u)
}

// HasCommit reports whether the commit exists locally.
func (g *Git) HasCommit(ctx context.Context, sha string) bool {
	_, err := g.run(ctx, "cat-file", "-e", sha+"^{commit}")
	return err == nil
}

// Fetch fetches refspecs from a remote name or URL.
func (g *Git) Fetch(ctx context.Context, remote string, refspecs ...string) error {
	args := append([]string{"fetch", "--no-tags", "--no-write-fetch-head", remote}, refspecs...)
	_, err := g.run(ctx, args...)
	return err
}

// EnsureCommit makes sha available locally. It tries, in order: the local
// object store, the pull request ref on the configured remote, then a direct
// fetch of the SHA from each candidate remote or URL.
func (g *Git) EnsureCommit(ctx context.Context, sha string, prRef string, remotes ...string) error {
	if !shaRe.MatchString(sha) {
		return fmt.Errorf("invalid commit SHA %q", sha)
	}
	if g.HasCommit(ctx, sha) {
		return nil
	}
	var errs []error
	if prRef != "" && len(remotes) > 0 {
		if err := g.Fetch(ctx, remotes[0], prRef); err != nil {
			errs = append(errs, err)
		} else if g.HasCommit(ctx, sha) {
			return nil
		}
	}
	for _, r := range remotes {
		if r == "" {
			continue
		}
		if err := g.Fetch(ctx, r, sha); err != nil {
			errs = append(errs, err)
			continue
		}
		if g.HasCommit(ctx, sha) {
			return nil
		}
	}
	return fmt.Errorf("commit %s unavailable after fetch: %w", sha, errors.Join(errs...))
}

// MergeBase returns the best common ancestor of two commits.
func (g *Git) MergeBase(ctx context.Context, baseSHA, headSHA string) (string, error) {
	out, err := g.run(ctx, "merge-base", baseSHA, headSHA)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// Diff returns the unified diff between two commits, truncated to limit bytes
// (with a marker) when limit > 0.
func (g *Git) Diff(ctx context.Context, baseSHA, headSHA string, limit int64) ([]byte, bool, error) {
	out, err := g.run(ctx, "diff", "--find-renames", "--no-color", "--no-ext-diff", baseSHA, headSHA)
	if err != nil {
		return nil, false, err
	}
	data := []byte(out)
	if limit > 0 && int64(len(data)) > limit {
		cut := data[:limit]
		if i := bytes.LastIndexByte(cut, '\n'); i > 0 {
			cut = cut[:i+1]
		}
		return append(cut, []byte("\n[diff truncated by conclave: output exceeded configured max_diff_bytes]\n")...), true, nil
	}
	return data, false, nil
}

// DiffNames lists the paths changed between two commits.
func (g *Git) DiffNames(ctx context.Context, baseSHA, headSHA string) ([]string, error) {
	out, err := g.run(ctx, "diff", "--name-only", "--find-renames", baseSHA, headSHA)
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}

// CreateWorktree adds a detached worktree at path checked out at sha.
func (g *Git) CreateWorktree(ctx context.Context, path, sha string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	_, err := g.run(ctx, "worktree", "add", "--detach", path, sha)
	return err
}

// RemoveWorktree deletes a worktree and its administrative files.
func (g *Git) RemoveWorktree(ctx context.Context, path string) error {
	if _, err := g.run(ctx, "worktree", "remove", "--force", path); err != nil {
		// Fall back to a manual cleanup so a broken worktree never blocks the run.
		_ = os.RemoveAll(path)
		_, _ = g.run(ctx, "worktree", "prune")
		if _, statErr := os.Stat(path); statErr == nil {
			return err
		}
	}
	return nil
}

// WorktreeHead returns the commit checked out in a worktree.
func (g *Git) WorktreeHead(ctx context.Context, path string) (string, error) {
	out, err := g.runIn(ctx, path, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// PathExistsAt reports whether path exists in the tree of the given commit.
func (g *Git) PathExistsAt(ctx context.Context, sha, path string) bool {
	_, err := g.run(ctx, "cat-file", "-e", sha+":"+path)
	return err == nil
}

// PullRequestRef returns the hidden ref forges expose for a pull request head.
func PullRequestRef(number int64) string {
	return fmt.Sprintf("+refs/pull/%d/head:refs/conclave/pull/%d/head", number, number)
}
