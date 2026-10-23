package integration

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pi(t *testing.T) Agent {
	t.Helper()
	a, err := Lookup("pi")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func statuses(rep *Report) map[string]Status {
	m := map[string]Status{}
	for _, r := range rep.Results {
		m[r.Path] = r.Status
	}
	return m
}

func TestInstallIntoTheUserConfiguration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PI_CODING_AGENT_DIR", "")
	rep, err := Install(pi(t), Options{})
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, ".pi", "agent")
	if rep.Root != root {
		t.Fatalf("root = %s, want %s", rep.Root, root)
	}
	for _, p := range []string{
		"extensions/conclave.ts",
		"skills/conclave-review/SKILL.md",
		"skills/conclave-ask/SKILL.md",
		"skills/conclave-triage/SKILL.md",
	} {
		dst := filepath.Join(root, filepath.FromSlash(p))
		if statuses(rep)[dst] != Created {
			t.Errorf("%s: status %v, want created", p, statuses(rep)[dst])
		}
		info, err := os.Stat(dst)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o644 {
			t.Errorf("%s: mode %v, want 0644", p, info.Mode().Perm())
		}
	}
	if len(rep.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", rep.Warnings)
	}
}

func TestInstallHonoursPiCodingAgentDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dir)
	rep, err := Install(pi(t), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Root != dir {
		t.Fatalf("root = %s, want %s", rep.Root, dir)
	}
	if _, err := os.Stat(filepath.Join(dir, "extensions", "conclave.ts")); err != nil {
		t.Fatal(err)
	}
}

func TestInstallIntoAProject(t *testing.T) {
	repo := t.TempDir()
	if _, err := Install(pi(t), Options{Project: repo}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".pi", "skills", "conclave-review", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
}

func TestReinstallLeavesAnEditedFileAloneUnlessForced(t *testing.T) {
	repo := t.TempDir()
	if _, err := Install(pi(t), Options{Project: repo}); err != nil {
		t.Fatal(err)
	}
	edited := filepath.Join(repo, ".pi", "skills", "conclave-ask", "SKILL.md")
	if err := os.WriteFile(edited, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	rep, err := Install(pi(t), Options{Project: repo})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	for path, st := range statuses(rep) {
		want := Unchanged
		if path == edited {
			want = Conflict
		}
		if st != want {
			t.Errorf("%s: status %v, want %v", path, st, want)
		}
	}
	if got, _ := os.ReadFile(edited); string(got) != "mine" {
		t.Fatalf("edited file overwritten without --force: %q", got)
	}

	rep, err = Install(pi(t), Options{Project: repo, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if statuses(rep)[edited] != Replaced {
		t.Errorf("status %v, want replaced", statuses(rep)[edited])
	}
	want, _ := assets.ReadFile("assets/skills/conclave-ask/SKILL.md")
	if got, _ := os.ReadFile(edited); !bytes.Equal(got, want) {
		t.Fatal("--force did not restore the bundled file")
	}
}

func TestInstallWarnsAboutCompetingFiles(t *testing.T) {
	repo := t.TempDir()
	for _, dir := range []string{
		filepath.Join(repo, ".agents", "skills", "conclave-review"),
		filepath.Join(repo, ".pi", "extensions"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, ".pi", "extensions", "deadline.ts"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := Install(pi(t), Options{Project: repo})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(rep.Warnings, "\n")
	if !strings.Contains(joined, "deadline.ts") || !strings.Contains(joined, filepath.Join(".agents", "skills", "conclave-review")) {
		t.Fatalf("warnings = %v", rep.Warnings)
	}
}

func TestEverySkillNamesItsDirectory(t *testing.T) {
	skills, err := fs.ReadDir(assets, skillsDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) == 0 {
		t.Fatal("no skill embedded")
	}
	for _, s := range skills {
		data, err := assets.ReadFile(skillsDir + "/" + s.Name() + "/SKILL.md")
		if err != nil {
			t.Fatal(err)
		}
		head, _, ok := strings.Cut(strings.TrimPrefix(string(data), "---\n"), "\n---\n")
		if !strings.HasPrefix(string(data), "---\n") || !ok {
			t.Fatalf("%s: no frontmatter", s.Name())
		}
		if !strings.Contains(head, "\nname: "+s.Name()+"\n") && !strings.HasPrefix(head, "name: "+s.Name()+"\n") {
			t.Errorf("%s: frontmatter name does not match the directory", s.Name())
		}
		if !strings.Contains(head, "\ndescription: ") {
			t.Errorf("%s: no description, pi would not load it", s.Name())
		}
	}
}

func TestUnknownAgent(t *testing.T) {
	if _, err := Lookup("nope"); err == nil || !strings.Contains(err.Error(), "pi") {
		t.Fatalf("err = %v", err)
	}
}

func TestProjectInstallWarnsAboutTheUsersOldExtension(t *testing.T) {
	user := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", user)
	if err := os.MkdirAll(filepath.Join(user, "extensions"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(user, "extensions", "deadline.ts")
	if err := os.WriteFile(old, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := Install(pi(t), Options{Project: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(rep.Warnings, "\n"), old) {
		t.Fatalf("warnings = %v, want one about %s", rep.Warnings, old)
	}
}

func TestRelativePiCodingAgentDirIsMadeAbsolute(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("PI_CODING_AGENT_DIR", "agent")
	rep, err := Install(pi(t), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(rep.Root) {
		t.Fatalf("root %s is relative", rep.Root)
	}
}

func TestForceWritesThroughASymlink(t *testing.T) {
	repo := t.TempDir()
	dotfiles := t.TempDir()
	mine := filepath.Join(dotfiles, "SKILL.md")
	if err := os.WriteFile(mine, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(repo, ".pi", "skills", "conclave-ask", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(mine, link); err != nil {
		t.Fatal(err)
	}

	_, err := Install(pi(t), Options{Project: repo})
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "conclave-ask") {
		t.Fatalf("err = %v, want ErrConflict naming the file", err)
	}
	if _, err := Install(pi(t), Options{Project: repo, Force: true}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&fs.ModeSymlink == 0 {
		t.Fatal("--force replaced the symlink with a plain file")
	}
	want, _ := assets.ReadFile("assets/skills/conclave-ask/SKILL.md")
	if got, _ := os.ReadFile(mine); !bytes.Equal(got, want) {
		t.Fatal("the link target was not updated")
	}
}
