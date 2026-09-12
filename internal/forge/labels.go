package forge

import (
	"path"
	"sort"
	"strings"

	"github.com/bornholm/conclave/internal/domain"
)

// FilterLabels keeps the labels matching one of the include patterns (all of
// them when include is empty) and drops those matching an exclude pattern.
// Patterns are shell globs, so "type/*" selects a whole family. Descriptions
// from describe override or fill in the ones the forge returned, since a
// label with no description tells an agent almost nothing.
func FilterLabels(labels []domain.Label, include, exclude []string, describe map[string]string) []domain.Label {
	out := make([]domain.Label, 0, len(labels))
	for _, l := range labels {
		if !matchAny(l.Name, include, true) || matchAny(l.Name, exclude, false) {
			continue
		}
		if d, ok := describe[l.Name]; ok {
			l.Description = d
		}
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// matchAny reports whether name matches one of the patterns. An empty pattern
// list yields empt, so callers can distinguish "no filter" from "no match".
func matchAny(name string, patterns []string, empty bool) bool {
	if len(patterns) == 0 {
		return empty
	}
	for _, p := range patterns {
		if strings.EqualFold(p, name) {
			return true
		}
		if ok, err := path.Match(p, name); err == nil && ok {
			return true
		}
	}
	return false
}

// LabelNames returns the names of the labels, in order.
func LabelNames(labels []domain.Label) []string {
	out := make([]string, len(labels))
	for i, l := range labels {
		out[i] = l.Name
	}
	return out
}

// KnownLabels indexes labels by lowercase name, to validate what an agent proposes.
func KnownLabels(labels []domain.Label) map[string]string {
	out := make(map[string]string, len(labels))
	for _, l := range labels {
		out[strings.ToLower(l.Name)] = l.Name
	}
	return out
}
