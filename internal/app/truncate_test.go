package app

import "testing"

func TestTruncateBytesKeepsValidUTF8(t *testing.T) {
	// "é" is two bytes: a cut at 1 byte must drop it whole.
	if got := truncateBytes("aé", 2); got != "a\n[truncated by conclave]" {
		t.Errorf("got %q", got)
	}
	if got := truncateBytes("abc", 10); got != "abc" {
		t.Errorf("untouched: %q", got)
	}
	if got := truncateBytes("ééé", 4); got != "éé\n[truncated by conclave]" {
		t.Errorf("got %q", got)
	}
}
