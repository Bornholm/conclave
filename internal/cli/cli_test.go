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

// askConfig writes a configuration that loads, so a case can reach the
// checks that run after the configuration is read.
func askConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(path, []byte(`
version: 1
forge:
  provider: github
agents:
  - id: rev
    role: reviewer
    command: [echo]
  - id: lead
    role: lead
    command: [echo]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAskQuestionSources(t *testing.T) {
	var out, errb bytes.Buffer
	// A configuration that cannot be loaded stops the run right after the
	// question is read, which is what the question cases check.
	missing := []string{"--config", "/nonexistent.yaml"}
	run := func(t *testing.T, stdin string, args ...string) (int, string) {
		t.Helper()
		stdinReader = strings.NewReader(stdin)
		defer func() { stdinReader = nil }()
		out.Reset()
		errb.Reset()
		return Main(args, &out, &errb), errb.String()
	}
	t.Run("argument", func(t *testing.T) {
		if code, err := run(t, "", append([]string{"ask", "why?"}, missing...)...); code != 1 || !strings.Contains(err, "nonexistent.yaml") {
			t.Errorf("got %d %s", code, err)
		}
	})
	t.Run("stdin is the question", func(t *testing.T) {
		if code, err := run(t, "why is it slow?", append([]string{"ask"}, missing...)...); code != 1 || !strings.Contains(err, "nonexistent.yaml") {
			t.Errorf("got %d %s", code, err)
		}
	})
	t.Run("stdin is not read behind the question", func(t *testing.T) {
		// The note is what tells the user their pipe was ignored. It is
		// printed once the configuration is read, so the run needs one that
		// loads; --project stops it before any agent runs.
		_, err := run(t, "a log nobody asked for", "ask", "why?", "--config", askConfig(t), "--project", "/nonexistent")
		if !strings.Contains(err, "--context -") {
			t.Errorf("the ignored input must be reported: %s", err)
		}
	})
	t.Run("a context file does not claim standard input", func(t *testing.T) {
		// --context FILE names a file, not the pipe: what was piped in is
		// still dropped, so the note still has to fire.
		file := filepath.Join(t.TempDir(), "ctx.txt")
		os.WriteFile(file, []byte("the log"), 0o644)
		_, err := run(t, "a log nobody asked for", "ask", "why?", "--context", file, "--config", askConfig(t), "--project", "/nonexistent")
		if !strings.Contains(err, "--context -") {
			t.Errorf("the dropped input must be reported: %s", err)
		}
	})
	t.Run("--context - silences the note", func(t *testing.T) {
		_, err := run(t, "the log", "ask", "why?", "--context", "-", "--config", askConfig(t), "--project", "/nonexistent")
		if strings.Contains(err, "--context -") {
			t.Errorf("standard input was claimed, no note is due: %s", err)
		}
	})
	t.Run("question given twice through the alias", func(t *testing.T) {
		if code, err := run(t, "", "ask", "-q", "a", "--question", "b", "--config", "/nonexistent.yaml"); code == 0 || !strings.Contains(err, "same flag") {
			t.Errorf("got %d %s", code, err)
		}
	})
	t.Run("missing context file", func(t *testing.T) {
		if code, err := run(t, "", "ask", "why?", "--context", "/nonexistent.ctx", "--config", askConfig(t)); code == 0 || !strings.Contains(err, "read context") {
			t.Errorf("got %d %s", code, err)
		}
	})
	t.Run("rev without project is reported", func(t *testing.T) {
		_, err := run(t, "", "ask", "why?", "--rev", "abc", "--config", "/nonexistent.yaml")
		if !strings.Contains(err, "--rev and --keep-worktrees do nothing") {
			t.Errorf("the ignored flag must be reported: %s", err)
		}
	})
	t.Run("bad format", func(t *testing.T) {
		// This one needs a configuration that loads: the format is checked
		// after the configuration is read, so a missing file would hide it.
		code, err := run(t, "", "ask", "why?", "--format", "yaml", "--config", askConfig(t))
		if code == 0 || !strings.Contains(err, `invalid --format "yaml"`) {
			t.Errorf("got %d %s", code, err)
		}
	})
	for name, args := range map[string][]string{
		"no question":      {"ask"},
		"question twice":   {"ask", "why?", "--question", "how?"},
		"two arguments":    {"ask", "why?", "how?"},
		"stdin used twice": {"ask", "--context", "-"},
	} {
		t.Run(name, func(t *testing.T) {
			if code, _ := run(t, "", append(args, missing...)...); code == 0 {
				t.Errorf("%v should fail", args)
			}
		})
	}
}

func TestReadContext(t *testing.T) {
	file := filepath.Join(t.TempDir(), "ctx.txt")
	if err := os.WriteFile(file, []byte("  the log  "), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := readContext("", 0); got != "" || err != nil {
		t.Errorf("no context: %q %v", got, err)
	}
	if got, err := readContext(file, 0); got != "the log" || err != nil {
		t.Errorf("from a file: %q %v", got, err)
	}
	// "-" is read whatever standard input is, a terminal included: the
	// terminal guard belongs to the implicit path only.
	stdinReader = strings.NewReader("typed by hand")
	defer func() { stdinReader = nil }()
	if got, err := readContext("-", 0); got != "typed by hand" || err != nil {
		t.Errorf("from standard input: %q %v", got, err)
	}
	// The limit bounds what is held in memory, plus the one byte that lets
	// the app see the input was cut.
	stdinReader = strings.NewReader(strings.Repeat("x", 100))
	if got, err := readContext("-", 10); len(got) != 11 || err != nil {
		t.Errorf("bounded read: %d bytes %v", len(got), err)
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
	if _, err := resolveConfigPath(".conclave.yaml", project, false); err == nil {
		t.Error("a missing configuration must be reported, not silently defaulted")
	}
	inProject := filepath.Join(project, ".conclave.yaml")
	os.WriteFile(inProject, []byte("version: 1\n"), 0o644)
	got, err := resolveConfigPath(".conclave.yaml", project, false)
	if err != nil || got != inProject {
		t.Errorf("got %q %v, want the project file", got, err)
	}
	if got, _ := resolveConfigPath("/explicit.yaml", project, true); got != "/explicit.yaml" {
		t.Errorf("an explicit path must win: %q", got)
	}
	// A path spelled like the default, but typed, is taken as typed: the
	// fallback chain would otherwise hand back a file nobody named.
	if got, _ := resolveConfigPath(".conclave.yaml", project, true); got != ".conclave.yaml" {
		t.Errorf("an explicitly named default must not fall back: %q", got)
	}
}
