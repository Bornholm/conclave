package forge

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/bornholm/conclave/internal/domain"
)

// SortAndCap orders comments chronologically and keeps at most max of them.
// When trimming, the most recent comments are kept: they carry the current
// state of the discussion.
func SortAndCap(cs []domain.Comment, max int) []domain.Comment {
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].CreatedAt < cs[j].CreatedAt })
	if max > 0 && len(cs) > max {
		cs = cs[len(cs)-max:]
	}
	return cs
}

// MergeJSONArrays concatenates JSON array pages and decodes them into out,
// a pointer to a slice.
func MergeJSONArrays(pages []json.RawMessage, out any) error {
	merged := []byte("[")
	first := true
	for _, p := range pages {
		var items []json.RawMessage
		if err := json.Unmarshal(p, &items); err != nil {
			return fmt.Errorf("unexpected response: expected a JSON array: %w", err)
		}
		for _, it := range items {
			if !first {
				merged = append(merged, ',')
			}
			first = false
			merged = append(merged, it...)
		}
	}
	merged = append(merged, ']')
	return json.Unmarshal(merged, out)
}
