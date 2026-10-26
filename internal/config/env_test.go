package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestParseEnv(t *testing.T) {
	data := "\uFEFF# comment\n\nA=1\nexport B = two words # comment\nC='single # kept \\n'\nD=\"line\\nnext \\\"q\\\" \\\\\" # comment\nE=\nF=a=b\nG= # comment\nH=#x\nI=\"C:\\Users\\bob\"\nA=last\n"
	vars, err := ParseEnv([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	want := []EnvVar{{"A", "last"}, {"B", "two words"}, {"C", "single # kept \\n"}, {"D", "line\nnext \"q\" \\"}, {"E", ""}, {"F", "a=b"}, {"G", ""}, {"H", "#x"}, {"I", `C:\Users\bob`}}
	if !slices.Equal(vars, want) {
		t.Errorf("got %q\nwant %q", vars, want)
	}
	for _, bad := range []string{"NOEQUALS", "1A=x", "A B=x", "A='open", "A=\"open", "A='x' y"} {
		if _, err := ParseEnv([]byte("OK=1\n" + bad + "\n")); err == nil || !strings.Contains(err.Error(), "line 2") {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if _, err := ParseEnv([]byte("A=" + strings.Repeat("x", 2<<20) + "\n")); err == nil || !strings.Contains(err.Error(), "1 MiB") {
		t.Errorf("a line too long: %v", err)
	}
}

func TestLoadEnv(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	// t.Setenv restores each variable, unset included, whatever LoadEnv
	// sets meanwhile.
	for _, k := range []string{"CONCLAVE_T_NEW", "CONCLAVE_T_OTHER", "CONCLAVE_T_DEF", "CONCLAVE_T_DEF2"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	t.Setenv("CONCLAVE_T_KEPT", "shell")
	t.Setenv("CONCLAVE_T_EMPTY", " ")
	write := func(name, data string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// No .env: nothing, and no error, named or not.
	for _, name := range []string{"", ".env", "./.env"} {
		cfg := &Config{EnvFile: name}
		if err := cfg.LoadEnv(); err != nil || cfg.Env.File != "" {
			t.Fatalf("missing %q: %v %+v", name, err, cfg.Env)
		}
	}

	write(".env", "CONCLAVE_T_NEW=file\nCONCLAVE_T_KEPT=file\nCONCLAVE_T_EMPTY=file\nPATH=/evil\nGIT_DIR=/evil\ngit_dir=/evil\nHome=/evil\n")
	path := os.Getenv("PATH")
	cfg := &Config{}
	if err := cfg.LoadEnv(); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("CONCLAVE_T_NEW"); got != "file" {
		t.Errorf("not set from the file: %q", got)
	}
	if got := os.Getenv("CONCLAVE_T_KEPT"); got != "shell" {
		t.Errorf("the environment did not win: %q", got)
	}
	if got := os.Getenv("CONCLAVE_T_EMPTY"); got != "file" {
		t.Errorf("an empty variable won over the file: %q", got)
	}
	if os.Getenv("PATH") != path || os.Getenv("GIT_DIR") == "/evil" {
		t.Errorf("a protected variable was set")
	}
	want := EnvReport{File: filepath.Join(dir, ".env"), Set: []string{"CONCLAVE_T_NEW", "CONCLAVE_T_EMPTY"}, Kept: []string{"CONCLAVE_T_KEPT"}, Refused: []string{"PATH", "GIT_DIR", "git_dir", "Home"}}
	if !slices.Equal(cfg.Env.Set, want.Set) || !slices.Equal(cfg.Env.Kept, want.Kept) || !slices.Equal(cfg.Env.Refused, want.Refused) || cfg.Env.File != want.File {
		t.Errorf("report %+v, want %+v", cfg.Env, want)
	}

	// The default .env that does not parse is ignored, and said to be.
	write(".env", "CONCLAVE_T_DEF=x\nCONCLAVE_T_DEF2=\"a value\nover two lines\"\n")
	cfg = &Config{}
	if err := cfg.LoadEnv(); err != nil || cfg.Env.Ignored == nil || os.Getenv("CONCLAVE_T_DEF") != "" {
		t.Errorf("a default .env that does not parse: %v %+v", err, cfg.Env)
	}

	// A default .env that cannot be read is ignored too.
	if err := os.Remove(filepath.Join(dir, ".env")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, ".env"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg = &Config{}
	if err := cfg.LoadEnv(); err != nil || cfg.Env.Ignored == nil {
		t.Errorf("a default .env that cannot be read: %v %+v", err, cfg.Env)
	}

	// A file env_file names is read instead, and must exist and parse.
	write("secrets.env", "CONCLAVE_T_OTHER=other\n")
	cfg = &Config{EnvFile: "secrets.env"}
	if err := cfg.LoadEnv(); err != nil || os.Getenv("CONCLAVE_T_OTHER") != "other" {
		t.Errorf("env_file: %v %q", err, os.Getenv("CONCLAVE_T_OTHER"))
	}
	cfg = &Config{EnvFile: "missing.env"}
	if err := cfg.LoadEnv(); err == nil || !strings.Contains(err.Error(), "missing.env") {
		t.Errorf("a missing env_file: %v", err)
	}
	write("bad.env", "not a line\n")
	cfg = &Config{EnvFile: "bad.env"}
	if err := cfg.LoadEnv(); err == nil || !strings.Contains(err.Error(), "bad.env") {
		t.Errorf("a file that does not parse: %v", err)
	}
}
