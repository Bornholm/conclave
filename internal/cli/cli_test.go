package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	for _, args := range [][]string{{}, {"bogus"}, {"review"}, {"review", "abc"}, {"review", "1", "2"}, {"plan"}, {"plan", "abc"}, {"plan", "1", "2"}, {"config"}, {"agents"}} {
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

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	cases := map[string]string{
		"90d":        "2026-06-14",
		"2w":         "2026-08-29",
		"3m":         "2026-06-12",
		"48h":        "2026-09-10",
		"2026-01-02": "2026-01-02",
	}
	for in, want := range cases {
		got, err := parseSince(in, now)
		if err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		if got.Format("2006-01-02") != want {
			t.Errorf("%s: got %s want %s", in, got.Format("2006-01-02"), want)
		}
	}
	if _, err := parseSince("soon", now); err == nil {
		t.Error("expected an error")
	}
}

func TestTriageUsage(t *testing.T) {
	var out, errb bytes.Buffer
	for _, args := range [][]string{
		{"triage"},
		{"triage", "abc"},
		{"triage", "--all", "12"},
		{"triage", "--all", "--state", "nope"},
		{"triage", "12", "--since", "soon", "--all"},
	} {
		errb.Reset()
		if code := Main(args, &out, &errb); code == 0 {
			t.Errorf("%v should fail", args)
		}
	}
}

func TestAskQuestionSources(t *testing.T) {
	var out, errb bytes.Buffer
	// A configuration that cannot be loaded stops the run right after the
	// question is read, which is exactly what these cases check.
	missing := []string{"--config", "/nonexistent.yaml"}
	t.Run("argument", func(t *testing.T) {
		stdinReader = strings.NewReader("")
		defer func() { stdinReader = nil }()
		errb.Reset()
		if code := Main(append([]string{"ask", "why?"}, missing...), &out, &errb); code != 1 || !strings.Contains(errb.String(), "nonexistent.yaml") {
			t.Errorf("got %d %s", code, errb.String())
		}
	})
	t.Run("stdin", func(t *testing.T) {
		stdinReader = strings.NewReader("why is it slow?")
		defer func() { stdinReader = nil }()
		errb.Reset()
		if code := Main(append([]string{"ask"}, missing...), &out, &errb); code != 1 || !strings.Contains(errb.String(), "nonexistent.yaml") {
			t.Errorf("got %d %s", code, errb.String())
		}
	})
	for name, args := range map[string][]string{
		"no question":    {"ask"},
		"question twice": {"ask", "why?", "--question", "how?"},
		"two arguments":  {"ask", "why?", "how?"},
		"bad format":     {"ask", "why?", "--format", "yaml"},
	} {
		t.Run(name, func(t *testing.T) {
			stdinReader = strings.NewReader("")
			defer func() { stdinReader = nil }()
			errb.Reset()
			if code := Main(append(args, missing...), &out, &errb); code == 0 {
				t.Errorf("%v should fail", args)
			}
		})
	}
}

func TestResolveConfigPath(t *testing.T) {
	dir := t.TempDir()
	// Isolate the lookup from whatever the machine running the tests has.
	t.Chdir(dir)
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	project := filepath.Join(dir, "repo")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveConfigPath(".conclave.yaml", project); err == nil {
		t.Error("a missing configuration must be reported, not silently defaulted")
	}
	inProject := filepath.Join(project, ".conclave.yaml")
	os.WriteFile(inProject, []byte("version: 1\n"), 0o644)
	got, err := resolveConfigPath(".conclave.yaml", project)
	if err != nil || got != inProject {
		t.Errorf("got %q %v, want the project file", got, err)
	}
	if got, _ := resolveConfigPath("/explicit.yaml", project); got != "/explicit.yaml" {
		t.Errorf("an explicit path must win: %q", got)
	}
}
