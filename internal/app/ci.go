package app

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"

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
		status.Error = oneLine(err.Error(), maxCheckFieldBytes)
		return status
	}
	// Classified on the names the forge gave, which ci.required and
	// ci.ignore are written against, then made safe to quote.
	status.Checks = sanitizeChecks(classifyChecks(a.Config.CI, checks))
	log.Debug("CI checks read", "head", sha, "checks", len(status.Checks), "state", status.State())
	return status
}

// maxCheckNameBytes caps a check name: it is a line of the prompt and of
// the review.
const maxCheckNameBytes = 200

// maxCheckFieldBytes caps a check's URL and the error of a CI that could
// not be read.
const maxCheckFieldBytes = 500

// sanitizeChecks makes what the forge said about the checks safe to quote.
// A job name and what a job writes are the author's to choose, on a pull
// request from a fork as on any other: a name is put on one line without
// backticks, like every other field quoted outside a block of untrusted
// data, and a line of output that would close the block it is quoted in is
// defused.
func sanitizeChecks(checks []domain.Check) []domain.Check {
	out := make([]domain.Check, len(checks))
	for i, c := range checks {
		c.Name = noDashes(oneLine(c.Name, maxCheckNameBytes))
		c.Conclusion = oneLine(c.Conclusion, 64)
		c.URL = oneLine(c.URL, maxCheckFieldBytes)
		c.Summary = defuseFences(c.Summary)
		anns := make([]domain.CheckAnnotation, len(c.Annotations))
		for j, an := range c.Annotations {
			an.Path = oneLine(an.Path, maxCheckNameBytes)
			an.Level = oneLine(an.Level, 32)
			an.Message = defuseFences(an.Message)
			anns[j] = an
		}
		c.Annotations = anns
		out[i] = c
	}
	return out
}

// oneLine puts s on one line, without backticks nor invisible format
// characters, in at most max bytes. strings.Fields splits on every Unicode
// space and line break.
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(strings.ReplaceAll(dropFormat(s), "`", "'")), " ")
	return forge.CutText(s, max)
}

// noDashes shortens every run of three dashes or more to two, so that a
// check name, quoted on the line before its output, cannot look like the
// marker of a block of untrusted data.
func noDashes(s string) string {
	for strings.Contains(s, "---") {
		s = strings.ReplaceAll(s, "---", "--")
	}
	return s
}

// lineBreaks turns every Unicode line break into "\n": a renderer, or a
// model, reads CR, NEL and the line and paragraph separators as one.
var lineBreaks = strings.NewReplacer("\r\n", "\n", "\r", "\n", "\v", "\n", "\f", "\n", "\u0085", "\n", "\u2028", "\n", "\u2029", "\n")

// dropFormat removes the invisible format characters (zero-width spaces and
// joiners, bidi controls, the byte order mark), which can hide a marker.
func dropFormat(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
}

// defuseFences prefixes every line that could pass for the start or the
// end of a block of untrusted data, so that quoted output cannot close the
// block it is quoted in: a line starting with "---", and any line naming an
// UNTRUSTED block, wherever it is in the line.
func defuseFences(s string) string {
	lines := strings.Split(lineBreaks.Replace(dropFormat(s)), "\n")
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "---") || strings.Contains(strings.ToUpper(l), "UNTRUSTED") {
			lines[i] = "| " + l
		}
	}
	return strings.Join(lines, "\n")
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
