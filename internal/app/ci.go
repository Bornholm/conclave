package app

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/bornholm/conclave/internal/config"
	"github.com/bornholm/conclave/internal/domain"
	"github.com/bornholm/conclave/internal/forge"
)

// loadCI reads the checks of the pull request head and says which ones
// decide the verdict. It returns nil when ci.enabled is false or the forge
// reports no CI: the review is then decided as before. A forge that does
// report CI but could not be read gives a status with Error set, on which
// the review cannot approve.
func (a *App) loadCI(ctx context.Context, f forge.Forge, repo domain.Repository, sha string) *domain.CIStatus {
	if !a.Config.CIEnabled() {
		return nil
	}
	log := a.logger()
	checks, err := forge.ListChecks(ctx, f, repo, sha)
	if errors.Is(err, forge.ErrChecksUnsupported) {
		log.Debug("CI not read", "forge", f.Name(), "reason", err)
		return nil
	}
	status := &domain.CIStatus{HeadSHA: sha}
	if err != nil {
		log.Warn("CI checks not read", "head", sha, "error", err)
		status.Error = err.Error()
		return status
	}
	status.Checks = classifyChecks(a.Config.CI, checks)
	log.Debug("CI checks read", "head", sha, "checks", len(status.Checks), "state", status.State())
	return status
}

// classifyChecks marks the checks that decide the verdict: every one but the
// ignored ones, or only the required ones when ci.required is set. A
// required pattern no check matches yet is added as pending: the CI may not
// have started.
func classifyChecks(cfg config.CIConfig, checks []domain.Check) []domain.Check {
	out := make([]domain.Check, 0, len(checks))
	matched := make([]bool, len(cfg.Required))
	for _, c := range checks {
		c.Blocking = len(cfg.Required) == 0
		for i, p := range cfg.Required {
			if matchName(p, c.Name) {
				c.Blocking, matched[i] = true, true
			}
		}
		if matchesAny(cfg.Ignore, c.Name) {
			c.Blocking = false
		}
		out = append(out, c)
	}
	for i, p := range cfg.Required {
		if !matched[i] && !matchesAny(cfg.Ignore, p) {
			out = append(out, domain.Check{
				Name: p, State: domain.CheckPending, Blocking: true,
				Summary: "named in ci.required, not reported on this head yet",
			})
		}
	}
	return out
}

func matchesAny(patterns []string, name string) bool {
	for _, p := range patterns {
		if matchName(p, name) {
			return true
		}
	}
	return false
}

// matchName matches a check name against a pattern in which "*" stands for
// any run of characters, slashes included, since check names such as
// "CI / test (push)" are not paths, and "?" for one character.
func matchName(pattern, name string) bool {
	var b strings.Builder
	b.WriteString("^")
	for _, r := range pattern {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	return err == nil && re.MatchString(name)
}

// applyCI makes the verdict follow the CI of the head reviewed, and says
// why: a failed blocking check requests changes, and a check still running
// or a CI that could not be read keeps the review from approving.
func applyCI(review *domain.ConsolidatedReview, status *domain.CIStatus) []string {
	if status == nil {
		return nil
	}
	switch status.State() {
	case domain.CheckFailure:
		names := checkNames(status.Blocking(domain.CheckFailure))
		if review.Verdict == domain.VerdictRequestChanges {
			return []string{fmt.Sprintf("CI failed on the head reviewed: %s", names)}
		}
		review.Verdict = domain.VerdictRequestChanges
		return []string{fmt.Sprintf("CI failed on the head reviewed (%s): the verdict is request_changes", names)}
	case domain.CheckPending:
		if review.Verdict != domain.VerdictApprove {
			return nil
		}
		review.Verdict = domain.VerdictComment
		if status.Error != "" {
			return []string{fmt.Sprintf("the CI checks could not be read (%s): a review that cannot tell the CI passed is reported as comment, set ci.enabled to false to review without them", status.Error)}
		}
		return []string{fmt.Sprintf("CI still running on the head reviewed (%s): the reports approved, but a review before the CI has passed is reported as comment", checkNames(status.Blocking(domain.CheckPending)))}
	}
	return nil
}

func checkNames(checks []domain.Check) string {
	names := make([]string, len(checks))
	for i, c := range checks {
		names[i] = c.Name
	}
	return strings.Join(names, ", ")
}
