//go:build unix

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRefusesAParentOthersCanWrite(t *testing.T) {
	top := t.TempDir()
	parent := filepath.Join(top, DefaultFileName)
	writeConfig(t, parent, "version: 1\ninherits: false\n")
	if err := os.Chmod(parent, 0o666); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(top, "project", DefaultFileName)
	writeConfig(t, project, validYAML)
	if _, err := Load(project); err == nil || !strings.Contains(err.Error(), parent) || !strings.Contains(err.Error(), "writable by others") {
		t.Errorf("a parent others can write must be refused: %v", err)
	}
	// The project's own file is the user's choice: it is not checked, and
	// inherits: false below the parent leaves it unread.
	if err := os.Chmod(project, 0o666); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, project, "inherits: false\n"+validYAML)
	if _, err := Load(project); err != nil {
		t.Errorf("inherits: false must leave the parent unread: %v", err)
	}
}

func TestOwnerAllowed(t *testing.T) {
	for _, tc := range []struct {
		uid  uint32
		self int
		want bool
	}{
		{1000, 1000, true},
		{0, 1000, true},
		{1001, 1000, false},
		// Running as root, another user's file is still refused.
		{1000, 0, false},
		{0, 0, true},
	} {
		if got := ownerAllowed(tc.uid, tc.self); got != tc.want {
			t.Errorf("ownerAllowed(%d, %d) = %v", tc.uid, tc.self, got)
		}
	}
}
