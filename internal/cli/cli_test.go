package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bornholm/conclave/internal/domain"
	"github.com/bornholm/conclave/internal/integration"
	"github.com/bornholm/conclave/internal/testutil"
)

// isolated starts a configuration a test writes: it inherits nothing from
// the directories above the test's own.
const isolated = "inherits: false\n"

func TestConfigValidateListsTheInheritedFiles(t *testing.T) {
	top := t.TempDir()
	parent := filepath.Join(top, ".conclave.yaml")
	os.WriteFile(parent, []byte("version: 1\ninherits: false\nagents: [{id: rev, role: reviewer, command: [echo]}, {id: lead, role: lead, command: [echo]}]\n"), 0o644)
	os.MkdirAll(filepath.Join(top, "project"), 0o755)
	project := filepath.Join(top, "project", ".conclave.yaml")
	os.WriteFile(project, []byte("forge: {provider: github}\n"), 0o644)
	var out, errb bytes.Buffer
	Main([]string{"config", "validate", "--config", project}, &out, &errb)
	if !strings.Contains(out.String(), "valid\n✓ inherits "+parent+"\n") {
		t.Errorf("validate: %s", out.String())
	}
}

func TestConfigValidateSaysTheTokenCameFromTheEnvFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("GITEA_TOKEN", "")
	if err := os.WriteFile(".conclave.yaml", []byte(isolated+"version: 1\nforge: {provider: gitea, base_url: https://git.example.com}\nagents: [{id: rev, role: reviewer, command: [echo]}, {id: lead, role: lead, command: [echo]}]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(".env", []byte("GITEA_TOKEN=secret-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	Main([]string{"config", "validate"}, &out, &errb)
	env := filepath.Join(dir, ".env")
	for _, want := range []string{"✓ environment from " + env + ": set GITEA_TOKEN; already set none\n", "✓ token from GITEA_TOKEN, set by " + env + "\n"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String()+errb.String(), "secret-value") {
		t.Error("the token value was printed")
	}
}

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
	os.WriteFile(cfg, append([]byte(isolated), out.Bytes()...), 0o644)
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
	for _, args := range [][]string{{}, {"bogus"}, {"review"}, {"review", "abc"}, {"review", "1", "2"}, {"plan"}, {"plan", "abc"}, {"plan", "1", "2"}, {"config"}, {"agents"}, {"loop", "7"}, {"loop", "--interval", "-1m"}} {
		if code := Main(args, &out, &errb); code == 0 {
			t.Errorf("%v should fail", args)
		}
	}
}

func TestIncludeFlag(t *testing.T) {
	var l includeList
	for _, v := range []string{"7", "#8, 9", "10,"} {
		if err := l.Set(v); err != nil {
			t.Fatalf("%q: %v", v, err)
		}
	}
	if l.String() != "7,8,9,10" {
		t.Errorf("parsed %v", l)
	}
	// Refused when parsing the flags, before the configuration is loaded.
	for _, v := range []string{"abc", "0", "-3", "", " , "} {
		var out, errb bytes.Buffer
		if code := Main([]string{"loop", "--include", v}, &out, &errb); code == 0 || !strings.Contains(errb.String(), "-include") {
			t.Errorf("--include %q: %d %s", v, code, errb.String())
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
inherits: false
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
		if !strings.Contains(err, "--rev does nothing") {
			t.Errorf("the ignored flag must be reported: %s", err)
		}
	})
	t.Run("keep-worktrees without project is honoured", func(t *testing.T) {
		// It keeps the scratch directories, which is what it promises, so
		// nothing must claim it was ignored.
		_, err := run(t, "", "ask", "why?", "--keep-worktrees", "--config", "/nonexistent.yaml")
		if strings.Contains(err, "does nothing") {
			t.Errorf("--keep-worktrees is honoured without a project: %s", err)
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

// askAgentConfig writes a configuration whose single reviewer and lead are
// the fake agent, so a run reaches the agents and comes back with an answer.
func askAgentConfig(t *testing.T) string {
	t.Helper()
	agent := testutil.TestAgent(t)
	path := filepath.Join(t.TempDir(), "c.yaml")
	body := `
version: 1
inherits: false
forge:
  provider: github
ask:
  use_lead: false
agents:
  - id: r1
    role: reviewer
    command: [` + agent + `]
    environment: {CONCLAVE_TESTAGENT_MODE: ask, CONCLAVE_TESTAGENT_ID: r1}
  - id: lead
    role: lead
    command: [` + agent + `]
    environment: {CONCLAVE_TESTAGENT_MODE: ask-lead, CONCLAVE_TESTAGENT_ID: lead}
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// The question read from standard input has machinery of its own: the
// terminal guard, the configured bound and the trim. This crosses all of it
// in one run and reads the question back out of the answer.
func TestAskQuestionFromStdinEndToEnd(t *testing.T) {
	var out, errb bytes.Buffer
	stdinReader = strings.NewReader("  why is it slow?  ")
	defer func() { stdinReader = nil }()
	code := Main([]string{"ask", "--config", askAgentConfig(t), "--format", "json"}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	var answer domain.ConsolidatedAnswer
	if err := json.Unmarshal(out.Bytes(), &answer); err != nil {
		t.Fatalf("decode: %v\n%s", err, out.String())
	}
	if answer.Question != "why is it slow?" {
		t.Errorf("the piped question must be the one answered: %q", answer.Question)
	}
	if answer.Answer == "" || len(answer.ReportedBy) != 1 {
		t.Errorf("answer: %+v", answer)
	}
}

func TestTrimReadMarksACutThatEndsInWhitespace(t *testing.T) {
	// The bound is 10, the read brings back 11 bytes, and trimming the
	// trailing newline would otherwise bring it back to 10 and hide the cut.
	if got := trimRead([]byte("0123456789\n"), 10); !strings.HasSuffix(got, "[truncated by conclave]") {
		t.Errorf("a cut must be announced: %q", got)
	}
	// An input that fits is left alone.
	if got := trimRead([]byte(" abc "), 10); got != "abc" {
		t.Errorf("untouched: %q", got)
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

func TestAgentsInstall(t *testing.T) {
	repo := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	sub := filepath.Join(repo, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	run := func(args ...string) (int, string) {
		var out, errb bytes.Buffer
		code := Main(append([]string{"agents", "install"}, args...), &out, &errb)
		return code, out.String() + errb.String()
	}

	// Flags on either side of the agent name; PATH resolves to the repository root.
	if code, out := run("--project", sub, "pi"); code != 0 || !strings.Contains(out, "✓ created") {
		t.Fatalf("install: %d %s", code, out)
	}
	skill := filepath.Join(repo, ".pi", "skills", "conclave-review", "SKILL.md")
	if _, err := os.Stat(skill); err != nil {
		t.Fatalf("not installed at the repository root: %v", err)
	}
	if code, out := run("pi", "--project", repo); code != 0 || strings.Contains(out, "created") || !strings.Contains(out, "= unchanged") {
		t.Fatalf("reinstall: %d %s", code, out)
	}

	if err := os.WriteFile(skill, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out := run("pi", "--project", repo); code != 1 || !strings.Contains(out, "✗ differs") || !strings.Contains(out, "--force") {
		t.Fatalf("edited file: %d %s", code, out)
	}
	if code, out := run("pi", "--project", repo, "--force"); code != 0 || !strings.Contains(out, "✓ replaced") {
		t.Fatalf("--force: %d %s", code, out)
	}

	// With --project, the configuration checked is the one at its root.
	stale := isolated + strings.Replace(exampleConfig, "      - --\n    input: argument", "      - --deadline-env-prefix\n      - CONCLAVE\n      - --\n    input: argument", 1)
	if err := os.WriteFile(filepath.Join(repo, ".conclave.yaml"), []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out := run("pi", "--project", sub); code != 0 || !strings.Contains(out, "agent pi still passes --deadline-env-prefix") {
		t.Fatalf("the project's configuration was not checked: %d %s", code, out)
	}

	for _, args := range [][]string{{}, {"pi", "extra"}, {"nope"}, {"pi", "--project", t.TempDir()}} {
		if code, out := run(args...); code == 0 {
			t.Errorf("%v should fail: %s", args, out)
		}
	}
}

func TestAgentsInstallWarnsAboutAStaleConfiguration(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	stale := isolated + strings.Replace(exampleConfig, "      - --\n    input: argument", "      - --extension\n      - /x/deadline.ts\n      - --deadline-env-prefix\n      - CONCLAVE\n      - --\n    input: argument", 1)
	if stale == isolated+exampleConfig {
		t.Fatal("the example no longer has the pi agent this test edits")
	}
	cfg := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(cfg, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := Main([]string{"agents", "install", "pi", "--config", cfg}, &out, &errb); code != 0 {
		t.Fatalf("install: %d %s%s", code, out.String(), errb.String())
	}
	if !strings.Contains(out.String(), "agent pi still passes /x/deadline.ts --deadline-env-prefix") {
		t.Fatalf("no warning about the stale command: %s", out.String())
	}
	out.Reset()
	if code := Main([]string{"agents", "install", "pi", "--project", ""}, &out, &errb); code == 0 {
		t.Fatal("--project with an empty path must fail")
	}
}

func TestReadResponsesIsBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "answers.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 100)), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := readResponses(path, 10)
	if err != nil || len(data) != 11 {
		t.Errorf("file: %d bytes, %v", len(data), err)
	}
	stdinReader = strings.NewReader(strings.Repeat("y", 100))
	defer func() { stdinReader = nil }()
	if data, err = readResponses("-", 10); err != nil || len(data) != 11 {
		t.Errorf("stdin: %d bytes, %v", len(data), err)
	}
}

func TestAgentsInstallSkillsOnly(t *testing.T) {
	repo := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	t.Setenv("HOME", t.TempDir())
	for agent, dir := range map[string]string{"claude": ".claude", "codex": ".agents", "opencode": ".opencode"} {
		var out, errb bytes.Buffer
		if code := Main([]string{"agents", "install", agent, "--project", repo}, &out, &errb); code != 0 {
			t.Fatalf("%s: %d %s%s", agent, code, out.String(), errb.String())
		}
		for _, skill := range []string{"conclave-review", "conclave-ask", "conclave-triage"} {
			if _, err := os.Stat(filepath.Join(repo, dir, "skills", skill, "SKILL.md")); err != nil {
				t.Errorf("%s: %v", agent, err)
			}
		}
		if strings.Contains(out.String(), "!") {
			t.Errorf("%s: unexpected warning: %s", agent, out.String())
		}
	}
	var out bytes.Buffer
	Main([]string{"help"}, &out, &out)
	if !strings.Contains(out.String(), "conclave agents install claude|codex|opencode|pi") {
		t.Errorf("usage does not list the agents:\n%s", out.String())
	}
}

// The example printed by `conclave config example` is the repository's
// .conclave.example.yaml: the two must not drift apart.
func TestExampleConfigMatchesTheRepositoryOne(t *testing.T) {
	if want := readFile(t, filepath.Join("..", "..", ".conclave.example.yaml")); string(want) != exampleConfig {
		t.Error("internal/cli/example.yaml differs from .conclave.example.yaml: copy one onto the other")
	}
}

func TestStaleCommandsSaysWhenTheConfigurationDoesNotLoad(t *testing.T) {
	pi, err := integration.Lookup("pi")
	if err != nil {
		t.Fatal(err)
	}
	top := t.TempDir()
	parent := filepath.Join(top, ".conclave.yaml")
	os.WriteFile(parent, []byte(isolated+"version: 1\n"), 0o644)
	if err := os.Chmod(parent, 0o666); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(top, "project"), 0o755)
	project := filepath.Join(top, "project", ".conclave.yaml")
	stale := strings.Replace(exampleConfig, "      - --\n    input: argument", "      - --deadline-env-prefix\n      - CONCLAVE\n      - --\n    input: argument", 1)
	os.WriteFile(project, []byte(stale), 0o644)
	// The parent others can write is refused: the commands go unchecked,
	// and the install says so instead of saying nothing.
	if w := staleCommands(pi, project); len(w) != 1 || !strings.Contains(w[0], "agent commands not checked") || !strings.Contains(w[0], parent) {
		t.Errorf("warnings: %q", w)
	}
	// Loaded, a stale command is pointed at the inherited files as well.
	os.Chmod(parent, 0o644)
	if w := staleCommands(pi, project); len(w) != 1 || !strings.Contains(w[0], "or a file it inherits ("+parent+")") {
		t.Errorf("warnings: %q", w)
	}
}
