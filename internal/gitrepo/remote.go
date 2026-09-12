// Package gitrepo wraps the git command line for the operations Conclave needs.
package gitrepo

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/bornholm/conclave/internal/domain"
)

var scpLikeRe = regexp.MustCompile(`^(?:[\w.-]+@)?([\w.-]+):(.+)$`)

// ParseRemoteURL extracts host, owner and repository name from the URL
// formats used by Git remotes:
//
//	git@github.com:owner/repo.git
//	https://github.com/owner/repo.git
//	ssh://git@git.example.com:2222/owner/repo.git
//	https://git.example.com/gitea/owner/repo
func ParseRemoteURL(raw string) (domain.Repository, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return domain.Repository{}, errors.New("empty remote URL")
	}
	var host, path string
	switch {
	case strings.Contains(raw, "://"):
		u, err := url.Parse(raw)
		if err != nil {
			return domain.Repository{}, fmt.Errorf("parse remote URL: %w", err)
		}
		host, path = u.Hostname(), u.Path
	default:
		m := scpLikeRe.FindStringSubmatch(raw)
		if m == nil {
			return domain.Repository{}, fmt.Errorf("unrecognized remote URL %q", raw)
		}
		host, path = m[1], m[2]
	}
	path = strings.Trim(path, "/")
	path = strings.TrimSuffix(path, ".git")
	segs := strings.Split(path, "/")
	if len(segs) < 2 {
		return domain.Repository{}, fmt.Errorf("remote URL %q does not contain owner/repo", raw)
	}
	repo := domain.Repository{
		Host:  strings.ToLower(host),
		Owner: segs[len(segs)-2],
		Name:  segs[len(segs)-1],
	}
	if repo.Host == "" || repo.Owner == "" || repo.Name == "" {
		return domain.Repository{}, fmt.Errorf("remote URL %q is incomplete", raw)
	}
	return repo, nil
}
