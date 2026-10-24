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
	writeConfig(t, parent, "version: 1\n")
	if err := os.Chmod(parent, 0o666); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(top, "project", DefaultFileName)
	writeConfig(t, project, validYAML)
	if _, err := Load(project); err == nil || !strings.Contains(err.Error(), parent) || !strings.Contains(err.Error(), "writable by others") {
		t.Errorf("a parent others can write must be refused: %v", err)
	}
	// The project's own file is the user's choice: it is not checked, and
	// root: true below the parent leaves it unread.
	if err := os.Chmod(project, 0o666); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, project, "root: true\n"+validYAML)
	if _, err := Load(project); err != nil {
		t.Errorf("root: true must leave the parent unread: %v", err)
	}
}
