package app

import (
	"context"
	"fmt"

	"github.com/bornholm/conclave/internal/domain"
	"github.com/bornholm/conclave/internal/forge"
)

// forkHint turns a not-found error into an actionable message when the
// configured remote points to a fork: a pull request opened from a fork is
// numbered in the upstream repository, not in the fork, so the number the
// user passed simply does not exist where conclave looked. It returns an
// empty string when the repository is not a fork, or when the forge cannot
// tell, in which case the caller keeps the bare error.
func (a *App) forkHint(ctx context.Context, f forge.Forge, repo domain.Repository, what string) string {
	parent, err := forge.ParentRepository(ctx, f, repo)
	if err != nil {
		return ""
	}
	return fmt.Sprintf(
		"hint: %s is a fork of %s, and a %s opened from a fork lives in the upstream repository. "+
			"Add a remote pointing to %s and set forge.remote to its name (currently %q).",
		repo.FullName(), parent.FullName(), what, parent.FullName(), a.Config.Forge.Remote)
}
