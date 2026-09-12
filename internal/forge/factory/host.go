package factory

import (
	"net/url"
	"strings"
)

func parseHost(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	return strings.ToLower(u.Hostname()), nil
}

func equalHost(a, b string) bool {
	return strings.EqualFold(strings.TrimSuffix(a, "."), strings.TrimSuffix(b, "."))
}
