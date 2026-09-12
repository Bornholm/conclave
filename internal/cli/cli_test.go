package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMainCommands(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Main([]string{"version"}, &out, &errb); code != 0 || !strings.Contains(out.String(), "conclave") {
		t.Errorf("version: %d %s", code, out.String())
	}
	out.Reset()
	if code := Main([]string{"config", "example"}, &out, &errb); code != 0 || !strings.Contains(out.String(), "version: 1") {
		t.Errorf("example: %d", code)
	}
	cfg := filepath.Join(t.TempDir(), "c.yaml")
	os.WriteFile(cfg, out.Bytes(), 0o644)
	out.Reset()
	bad := strings.Replace(string(readFile(t, cfg)), "max_parallel", "max_paralel", 1)
	badPath := filepath.Join(t.TempDir(), "bad.yaml")
	os.WriteFile(badPath, []byte(bad), 0o644)
	if code := Main([]string{"config", "validate", "--config", badPath}, &out, &errb); code == 0 || !strings.Contains(out.String(), "max_paralel") {
		t.Errorf("validate bad: %d %s", code, out.String())
	}
	// Flags after the positional argument must be accepted; a missing config then fails loading.
	errb.Reset()
	if code := Main([]string{"review", "13", "--config", "/nonexistent.yaml"}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "nonexistent.yaml") {
		t.Errorf("interspersed flags: %d %s", code, errb.String())
	}
	for _, args := range [][]string{{}, {"bogus"}, {"review"}, {"review", "abc"}, {"review", "1", "2"}, {"config"}, {"agents"}} {
		if code := Main(args, &out, &errb); code == 0 {
			t.Errorf("%v should fail", args)
		}
	}
}

func readFile(t *testing.T, p string) []byte {
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
