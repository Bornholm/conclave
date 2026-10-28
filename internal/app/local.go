package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/bornholm/conclave/internal/domain"
	"github.com/bornholm/conclave/internal/forge"
	"github.com/bornholm/conclave/internal/gitrepo"
)

// splitRange splits a revision range into its base and head. "a..b" and
// "a...b" both review b against its merge base with a, as a pull request
// would be; an empty side stands for HEAD, as for git. A single revision has
// no base: it is reviewed against the default branch.
func splitRange(rev string) (base, head string, isRange bool) {
	sep := "..."
	i := strings.Index(rev, sep)
	if i < 0 {
		sep = ".."
		i = strings.Index(rev, sep)
	}
	if i < 0 {
		return "", rev, false
	}
	base, head = rev[:i], rev[i+len(sep):]
	if base == "" {
		base = "HEAD"
	}
	if head == "" {
		head = "HEAD"
	}
	return base, head, true
}

// localPullRequest builds, from the local repository alone, what a pull
// request would give a review: the commits between the base and the head,
// the files they change and their messages in place of a description. No
// forge is contacted.
func (a *App) localPullRequest(ctx context.Context, git *gitrepo.Git, rev string) (*domain.PullRequest, error) {
	cfg := a.Config
	log := a.logger()
	baseRev, headRev, isRange := splitRange(rev)
	if !isRange {
		def, err := git.DefaultBranch(ctx, cfg.Forge.Remote)
		if err != nil {
			return nil, fmt.Errorf("%w: give the base as a range, such as main..%s", err, orHEAD(headRev))
		}
		baseRev = def
	}
	headSHA, headName, err := git.ResolveRevision(ctx, headRev)
	if err != nil {
		return nil, err
	}
	baseSHA, baseName, err := git.ResolveRevision(ctx, baseRev)
	if err != nil {
		return nil, err
	}
	mergeBase, err := git.MergeBase(ctx, baseSHA, headSHA)
	if err != nil {
		return nil, fmt.Errorf("%s and %s share no history: %w", baseName, headName, err)
	}
	if mergeBase == headSHA {
		return nil, fmt.Errorf("nothing to review: %s has no commit that %s does not have", headName, baseName)
	}
	if headRev == "HEAD" && git.HasUncommittedChanges(ctx) {
		log.Warn("the work tree has uncommitted changes: only the commits are reviewed")
	}
	files, err := git.ChangedFiles(ctx, mergeBase, headSHA)
	if err != nil {
		return nil, fmt.Errorf("list changed files: %w", err)
	}
	if max := cfg.Review.Limits.MaxFiles; max > 0 && len(files) > max {
		return nil, fmt.Errorf("%w (%d > %d)", forge.ErrTooManyFiles, len(files), max)
	}
	subjects, err := git.CommitSubjects(ctx, mergeBase, headSHA)
	if err != nil {
		return nil, err
	}
	messages, err := git.Log(ctx, mergeBase, headSHA)
	if err != nil {
		return nil, err
	}
	title, revision := headName, headName
	if isRange {
		revision = baseName + ".." + headName
	}
	if len(subjects) == 1 {
		title = subjects[0]
	}
	return &domain.PullRequest{
		Local:        true,
		Revision:     revision,
		Title:        title,
		Description:  truncateBytes(messages, cfg.Review.Limits.MaxCommentBytes),
		Base:         domain.RepositoryRef{Branch: baseName, SHA: baseSHA},
		Head:         domain.RepositoryRef{Branch: headName, SHA: headSHA},
		MergeBaseSHA: mergeBase,
		ChangedFiles: files,
	}, nil
}

func orHEAD(rev string) string {
	if rev == "" {
		return "HEAD"
	}
	return rev
}

// localBranch is the revision a local review reviewed, a branch or a range,
// empty for a pull request.
func localBranch(pr *domain.PullRequest) string {
	if !pr.Local {
		return ""
	}
	return pr.Revision
}
