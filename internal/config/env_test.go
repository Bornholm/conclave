package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestParseEnv(t *testing.T) {
	data := "\uFEFF# comment\n\nA=1\nexport B = two words # comment\nC='single # kept \\n'\nD=\"line\\nnext \\\"q\\\" \\\\\" # comment\nE=\nF=a=b\nA=last\n"
	vars, err := ParseEnv([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	want := []EnvVar{{"A", "last"}, {"B", "two words"}, {"C", "single # kept \\n"}, {"D", "line\nnext \"q\" \\"}, {"E", ""}, {"F", "a=b"}}
	if !slices.Equal(vars, want) {
		t.Errorf("got %q\nwant %q", vars, want)
	}
	for _, bad := range []string{"NOEQUALS", "1A=x", "A B=x", "A='open", "A=\"open", "A='x' y"} {
		if _, err := ParseEnv([]byte("OK=1\n" + bad + "\n")); err == nil || !strings.Contains(err.Error(), "line 2") {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestLoadEnv(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	unset := func(keys ...string) {
		for _, k := range keys {
			t.Setenv(k, "")
			os.Unsetenv(k)
		}
	}
	unset("CONCLAVE_T_NEW", "CONCLAVE_T_OTHER")
	t.Setenv("CONCLAVE_T_KEPT", "shell")

	// No .env: nothing, and no error.
	cfg := &Config{}
	if err := cfg.LoadEnv(); err != nil || cfg.Env.File != "" {
		t.Fatalf("missing .env: %v %+v", err, cfg.Env)
	}

	os.WriteFile(filepath.Join(dir, ".env"), []byte("CONCLAVE_T_NEW=file\nCONCLAVE_T_KEPT=file\nPATH=/evil\nGIT_DIR=/evil\n"), 0o600)
	path := os.Getenv("PATH")
	cfg = &Config{}
	if err := cfg.LoadEnv(); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("CONCLAVE_T_NEW"); got != "file" {
		t.Errorf("not set from the file: %q", got)
	}
	if got := os.Getenv("CONCLAVE_T_KEPT"); got != "shell" {
		t.Errorf("the environment did not win: %q", got)
	}
	if os.Getenv("PATH") != path || os.Getenv("GIT_DIR") == "/evil" {
		t.Errorf("a protected variable was set")
	}
	want := EnvReport{File: filepath.Join(dir, ".env"), Set: []string{"CONCLAVE_T_NEW"}, Kept: []string{"CONCLAVE_T_KEPT"}, Refused: []string{"PATH", "GIT_DIR"}}
	if !slices.Equal(cfg.Env.Set, want.Set) || !slices.Equal(cfg.Env.Kept, want.Kept) || !slices.Equal(cfg.Env.Refused, want.Refused) || cfg.Env.File != want.File {
		t.Errorf("report %+v, want %+v", cfg.Env, want)
	}

	// A file env_file names is read instead, and must exist.
	os.WriteFile(filepath.Join(dir, "secrets.env"), []byte("CONCLAVE_T_OTHER=other\n"), 0o600)
	cfg = &Config{EnvFile: "secrets.env"}
	if err := cfg.LoadEnv(); err != nil || os.Getenv("CONCLAVE_T_OTHER") != "other" {
		t.Errorf("env_file: %v %q", err, os.Getenv("CONCLAVE_T_OTHER"))
	}
	cfg = &Config{EnvFile: "missing.env"}
	if err := cfg.LoadEnv(); err == nil {
		t.Error("a missing env_file is not an error")
	}
	os.WriteFile(filepath.Join(dir, "bad.env"), []byte("not a line\n"), 0o600)
	cfg = &Config{EnvFile: "bad.env"}
	if err := cfg.LoadEnv(); err == nil || !strings.Contains(err.Error(), "bad.env") {
		t.Errorf("a file that does not parse: %v", err)
	}
}
