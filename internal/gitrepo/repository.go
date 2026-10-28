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
	"strconv"
	"strings"
	"time"

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
		// Keep the exit status beside stderr: git may print only a progress
		// line ("Preparing worktree") before failing, or be killed.
		msg := err.Error()
		if s := strings.TrimSpace(stderr.String()); s != "" {
			msg = s + " (" + msg + ")"
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

// IsAncestor reports whether ancestor is reachable from descendant: false as
// well when either commit is unknown.
func (g *Git) IsAncestor(ctx context.Context, ancestor, descendant string) bool {
	_, err := g.run(ctx, "merge-base", "--is-ancestor", ancestor, descendant)
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

// ResolveRevision turns a revision (a branch, a tag, HEAD, a SHA) into a
// commit SHA, and returns a readable name for it.
func (g *Git) ResolveRevision(ctx context.Context, rev string) (sha string, name string, err error) {
	if rev == "" {
		rev = "HEAD"
	}
	out, err := g.run(ctx, "rev-parse", "--verify", rev+"^{commit}")
	if err != nil {
		return "", "", fmt.Errorf("resolve revision %q: %w", rev, err)
	}
	sha = strings.TrimSpace(out)
	name = rev
	if rev == "HEAD" {
		if branch, err := g.run(ctx, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
			if b := strings.TrimSpace(branch); b != "" && b != "HEAD" {
				name = b
			}
		}
	}
	return sha, name, nil
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
func (g *Git) Diff(ctx context.Context, baseSHA, headSHA string, limit int64, exclude ...string) ([]byte, bool, error) {
	args := []string{"diff", "--find-renames", "--no-color", "--no-ext-diff", baseSHA, headSHA}
	if len(exclude) > 0 {
		// Excluded paths are pathspecs, so a "*" matches across directories:
		// "*_templ.go" drops every generated templ file of the tree.
		args = append(args, "--", ".")
		for _, pattern := range exclude {
			args = append(args, ":(exclude)"+pattern)
		}
	}
	out, err := g.run(ctx, args...)
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

// ChangedFiles lists the files changed between two commits, with their
// status and line counts, as a forge lists those of a pull request. A binary
// file counts no lines.
func (g *Git) ChangedFiles(ctx context.Context, baseSHA, headSHA string) ([]domain.ChangedFile, error) {
	out, err := g.run(ctx, "diff", "--name-status", "-z", "--find-renames", "--no-ext-diff", baseSHA, headSHA)
	if err != nil {
		return nil, err
	}
	var files []domain.ChangedFile
	fields := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	for i := 0; i < len(fields) && fields[i] != ""; {
		status := fields[i]
		f := domain.ChangedFile{Status: fileStatus(status)}
		if (status[0] == 'R' || status[0] == 'C') && i+2 < len(fields) {
			f.PreviousPath, f.Path = fields[i+1], fields[i+2]
			i += 3
		} else if i+1 < len(fields) {
			f.Path = fields[i+1]
			i += 2
		} else {
			return nil, fmt.Errorf("git diff --name-status: truncated entry %q", status)
		}
		files = append(files, f)
	}
	counts, err := g.lineCounts(ctx, baseSHA, headSHA)
	if err != nil {
		return nil, err
	}
	for i := range files {
		c := counts[files[i].Path]
		files[i].Additions, files[i].Deletions = c[0], c[1]
	}
	return files, nil
}

// lineCounts maps each path changed between two commits to its added and
// deleted line counts.
func (g *Git) lineCounts(ctx context.Context, baseSHA, headSHA string) (map[string][2]int, error) {
	out, err := g.run(ctx, "diff", "--numstat", "-z", "--find-renames", "--no-ext-diff", baseSHA, headSHA)
	if err != nil {
		return nil, err
	}
	counts := map[string][2]int{}
	fields := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	for i := 0; i < len(fields) && fields[i] != ""; i++ {
		parts := strings.SplitN(fields[i], "\t", 3)
		if len(parts) != 3 {
			return nil, fmt.Errorf("git diff --numstat: unexpected entry %q", fields[i])
		}
		path := parts[2]
		// A rename leaves the path empty and gives the old and new paths as
		// the next two fields.
		if path == "" && i+2 < len(fields) {
			path = fields[i+2]
			i += 2
		}
		// "-" stands for a binary file: no line counts.
		add, _ := strconv.Atoi(parts[0])
		del, _ := strconv.Atoi(parts[1])
		counts[path] = [2]int{add, del}
	}
	return counts, nil
}

func fileStatus(code string) string {
	switch code[0] {
	case 'A':
		return domain.FileAdded
	case 'M':
		return domain.FileModified
	case 'D':
		return domain.FileDeleted
	case 'R':
		return domain.FileRenamed
	case 'C':
		return domain.FileCopied
	}
	return domain.FileChanged
}

// Log returns the commits reachable from headSHA but not from baseSHA, newest
// first, with their author, date and full message: at most max of them, so
// that a long divergence is not read whole only to be cut.
func (g *Git) Log(ctx context.Context, baseSHA, headSHA string, max int) (string, error) {
	return g.run(ctx, "log", "--no-color", "--no-decorate", "--format=medium", "--max-count="+strconv.Itoa(max), baseSHA+".."+headSHA)
}

// CommitSubjects returns the subject of each commit reachable from headSHA
// but not from baseSHA, newest first, at most max of them.
func (g *Git) CommitSubjects(ctx context.Context, baseSHA, headSHA string, max int) ([]string, error) {
	out, err := g.run(ctx, "log", "--no-color", "--format=%s", "--max-count="+strconv.Itoa(max), baseSHA+".."+headSHA)
	if err != nil {
		return nil, err
	}
	out = strings.TrimSuffix(out, "\n")
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// DefaultBranch returns the branch a local review is measured against when
// none is given: the one the remote's HEAD points to, else a local main or
// master, else the remote's.
func (g *Git) DefaultBranch(ctx context.Context, remote string) (string, error) {
	if remote != "" {
		if out, err := g.run(ctx, "symbolic-ref", "--quiet", "--short", "refs/remotes/"+remote+"/HEAD"); err == nil {
			if b := strings.TrimSpace(out); b != "" {
				return b, nil
			}
		}
	}
	candidates := []string{"refs/heads/main", "refs/heads/master"}
	if remote != "" {
		candidates = append(candidates, "refs/remotes/"+remote+"/main", "refs/remotes/"+remote+"/master")
	}
	for _, ref := range candidates {
		if _, err := g.run(ctx, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err == nil {
			return strings.TrimPrefix(strings.TrimPrefix(ref, "refs/heads/"), "refs/remotes/"), nil
		}
	}
	return "", errors.New("no default branch found: the remote names no HEAD and there is no main or master branch")
}

// HasUncommittedChanges reports whether the work tree or the index differ
// from HEAD, untracked files aside.
func (g *Git) HasUncommittedChanges(ctx context.Context) bool {
	out, err := g.run(ctx, "status", "--porcelain", "--untracked-files=no")
	return err == nil && strings.TrimSpace(out) != ""
}

// CreateWorktree adds a detached worktree at path checked out at sha.
func (g *Git) CreateWorktree(ctx context.Context, path, sha string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if _, err := g.run(ctx, "worktree", "add", "--detach", path, sha); err != nil {
		// git may have registered the worktree before failing (a failed
		// checkout or post-checkout hook): remove it, or it stays listed.
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		_ = g.RemoveWorktree(cctx, path)
		return err
	}
	return nil
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

// FileAt returns the content of path in the tree of the given commit, or an
// error when it is not a file there or is larger than limit bytes.
func (g *Git) FileAt(ctx context.Context, sha, path string, limit int64) ([]byte, error) {
	obj := sha + ":" + path
	size, err := g.run(ctx, "cat-file", "-s", obj)
	if err != nil {
		return nil, err
	}
	n, err := strconv.ParseInt(strings.TrimSpace(size), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("%s: size %q: %w", path, strings.TrimSpace(size), err)
	}
	if n > limit {
		return nil, fmt.Errorf("%s: %d bytes, larger than %d", path, n, limit)
	}
	out, err := g.run(ctx, "cat-file", "blob", obj)
	if err != nil {
		return nil, err
	}
	return []byte(out), nil
}

// PullRequestRef returns the hidden ref forges expose for a pull request head.
func PullRequestRef(number int64) string {
	return fmt.Sprintf("+refs/pull/%d/head:refs/conclave/pull/%d/head", number, number)
}
