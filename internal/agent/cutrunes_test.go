package agent

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCutRunes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"under the limit", "abc", 10, "abc"},
		{"exact boundary", "éé", 4, "éé"},
		{"cut mid-rune", "aé", 2, "a"},
		// The bound on the back-off is what keeps an earlier bad byte from
		// taking everything behind it.
		{"invalid byte before the cut", "abc\xffdefghijkl", 10, "abc\xffdefghi"},
		{"four-byte rune split", "a\U0001F600", 3, "a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CutRunes(tc.in, tc.max); got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}

// The answer is the largest field ask carries, and the one most likely to be
// cut inside a rune: 64 KiB of free text by default.
func TestTruncateAnswerStaysValid(t *testing.T) {
	answer := strings.Repeat("é", 40) + "😀" + strings.Repeat("ü", 40)
	for max := 1; max < len(answer); max++ {
		got := strings.TrimSuffix(truncate(answer, max), "…")
		if !utf8.ValidString(got) {
			t.Fatalf("max=%d left invalid UTF-8: %q", max, got)
		}
	}
}
