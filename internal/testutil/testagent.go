// Package testutil provides helpers shared by the test suites.
package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

var (
	buildOnce sync.Once
	buildPath string
	buildErr  error
)

// TestAgent builds the fake agent binary once per test process and returns its path.
func TestAgent(t testing.TB) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "conclave-testagent-")
		if err != nil {
			buildErr = err
			return
		}
		buildPath = filepath.Join(dir, "testagent")
		if runtime.GOOS == "windows" {
			buildPath += ".exe"
		}
		cmd := exec.Command("go", "build", "-o", buildPath, "github.com/bornholm/conclave/internal/testagent")
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = err
			_ = out
			t.Logf("build testagent: %s", out)
		}
	})
	if buildErr != nil {
		t.Fatalf("build testagent: %v", buildErr)
	}
	return buildPath
}
