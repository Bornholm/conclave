package app

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateBytes(t *testing.T) {
	const marker = "\n[truncated by conclave]"
	cases := map[string]struct{ in, want string }{
		"under the limit":   {"abc", "abc"},
		"cut mid-rune":      {"aé", "a" + marker},
		"cut on a boundary": {"ééé", "éé" + marker},
		// An invalid byte before the cut must not take the content with it:
		// backing off until the whole prefix parses would delete everything
		// between that byte and the cut.
		"invalid byte kept": {"abc\xffdefghijklmnop", "abc\xffdefghi" + marker},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			max := 10
			if name == "cut mid-rune" {
				max = 2
			}
			if name == "cut on a boundary" {
				max = 5
			}
			if got := truncateBytes(tc.in, max); got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestTruncateBytesNeverLeavesAPartialRune(t *testing.T) {
	// Every cut of a run of multi-byte runes must stay valid UTF-8.
	s := strings.Repeat("é", 50)
	for max := 1; max < len(s); max++ {
		got := strings.TrimSuffix(truncateBytes(s, max), "\n[truncated by conclave]")
		if !utf8.ValidString(got) {
			t.Fatalf("max=%d left invalid UTF-8: %q", max, got)
		}
		if len(got) < max-1 {
			t.Fatalf("max=%d dropped too much: %d bytes", max, len(got))
		}
	}
}
