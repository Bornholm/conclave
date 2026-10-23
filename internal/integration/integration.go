// Package integration installs, into a coding agent's own configuration, the
// files that make it work with Conclave: an extension that paces a run to the
// deadline Conclave exports, and the skills that drive Conclave from a session.
//
// The files are embedded in the binary, so what is installed always matches
// the contract of the Conclave that installs it.
package integration

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed all:assets
var assets embed.FS

// skillsDir holds one directory per skill, installed whole for every agent.
const skillsDir = "assets/skills"

// File is one embedded file and where it goes, relative to the agent's root.
type File struct {
	Asset string
	Path  string
}

// Agent describes where one coding agent reads its extensions and skills.
type Agent struct {
	Name string
	// UserRoot is the agent's configuration directory for the current user.
	UserRoot func() (string, error)
	// ProjectDir is the directory, relative to a repository root, the agent
	// reads project files from.
	ProjectDir string
	// Extensions are the agent-specific files, the skills come on top.
	Extensions []File
	// SkillDirs are other directories, relative to the home directory for a
	// user install and to the repository for a project one, where the agent
	// also discovers skills. A skill of the same name there competes with the
	// installed one.
	SkillDirs []string
	// Supersedes are files, relative to the agent's root, that an extension
	// replaces: left in place, both would act on the same run.
	Supersedes []string
	// SupersededArgs are command-line arguments of the agent that only made
	// sense with what the installed files replace.
	SupersededArgs []string
}

var agents = map[string]Agent{
	"pi": {
		Name: "pi",
		UserRoot: func() (string, error) {
			if dir := os.Getenv("PI_CODING_AGENT_DIR"); dir != "" {
				// Absolute, so the install does not depend on where it is run.
				return filepath.Abs(dir)
			}
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			return filepath.Join(home, ".pi", "agent"), nil
		},
		ProjectDir: ".pi",
		Extensions: []File{{Asset: "assets/pi/extensions/conclave.ts", Path: "extensions/conclave.ts"}},
		SkillDirs:  []string{filepath.Join(".agents", "skills")},
		// The standalone deadline extension conclave.ts was derived from: both
		// would steer the run and lower its thinking.
		Supersedes: []string{"extensions/deadline.ts"},
		// Conclave 0.9.0 documented passing it that way: Pi rejects the flag
		// once the extension that registered it is gone.
		SupersededArgs: []string{"--deadline-env-prefix"},
	},
}

// Agents lists the names of the agents that can be installed, sorted.
func Agents() []string {
	names := make([]string, 0, len(agents))
	for name := range agents {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Lookup returns the agent called name.
func Lookup(name string) (Agent, error) {
	a, ok := agents[name]
	if !ok {
		return Agent{}, fmt.Errorf("unknown agent %q (supported: %s)", name, strings.Join(Agents(), ", "))
	}
	return a, nil
}

// Status is what an install did to one file.
type Status int

const (
	// Created means the file did not exist and was written.
	Created Status = iota
	// Unchanged means the file already held the embedded content.
	Unchanged
	// Replaced means a different file was overwritten, with force.
	Replaced
	// Conflict means a different file is there and was left alone.
	Conflict
	// Failed means the file could not be compared or written; Err says why.
	Failed
)

// Result is the outcome for one file.
type Result struct {
	Path   string
	Status Status
	Err    error
}

// Options selects where to install.
type Options struct {
	// Project, when set, is the repository root to install into instead of
	// the user's configuration.
	Project string
	// Force replaces files that differ from the embedded ones.
	Force bool
}

// Report is the outcome of an install.
type Report struct {
	Root     string
	Results  []Result
	Warnings []string
}

// ErrConflict is returned, wrapped with the paths concerned, when files were
// left alone because they differ. The other files are installed.
var ErrConflict = errors.New("files differ from the bundled version and were left alone, --force replaces them")

// Install writes the agent's extensions and every skill under its root. A file
// that already holds the same content is left as is, one that differs is only
// replaced with Force: it may be the user's own edit.
func Install(a Agent, opts Options) (*Report, error) {
	root, base, err := roots(a, opts)
	if err != nil {
		return nil, err
	}
	files, err := a.files()
	if err != nil {
		return nil, err
	}
	rep := &Report{Root: root}
	// One file that cannot be written does not stop the others: each is
	// reported, and the warnings below are computed whatever happened.
	var conflicts []string
	var errs []error
	for _, f := range files {
		dst := filepath.Join(root, filepath.FromSlash(f.Path))
		// Only the user's own links are followed: a repository can commit a
		// link to any file of the user's, and --force would overwrite it.
		st, err := write(f.Asset, dst, opts.Force, opts.Project == "")
		switch {
		case err != nil:
			st = Failed
			errs = append(errs, fmt.Errorf("%s: %w", dst, err))
		case st == Conflict:
			conflicts = append(conflicts, dst)
		}
		rep.Results = append(rep.Results, Result{Path: dst, Status: st, Err: err})
	}
	// A project install leaves the user's extensions loaded, so an old one
	// there competes with the new one as much as one in the project.
	superseded := []string{root}
	if opts.Project != "" {
		if user, err := a.UserRoot(); err == nil {
			superseded = append(superseded, user)
		}
	}
	// Pi reads the user's skill directories whatever the project, so a
	// project install competes with those too.
	bases := []string{base}
	if opts.Project != "" {
		if home, err := os.UserHomeDir(); err == nil {
			bases = append(bases, home)
		}
	}
	rep.Warnings = append(a.superseded(superseded...), a.shadowed(bases...)...)
	if len(conflicts) > 0 {
		errs = append(errs, fmt.Errorf("%w: %s", ErrConflict, strings.Join(conflicts, ", ")))
	}
	return rep, errors.Join(errs...)
}

// roots returns where the files go and the directory SkillDirs are relative to.
func roots(a Agent, opts Options) (root, base string, err error) {
	if opts.Project != "" {
		return filepath.Join(opts.Project, a.ProjectDir), opts.Project, nil
	}
	root, err = a.UserRoot()
	if err != nil {
		return "", "", err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	return root, home, nil
}

// files lists the agent's extensions and every embedded skill file.
func (a Agent) files() ([]File, error) {
	files := append([]File(nil), a.Extensions...)
	err := fs.WalkDir(assets, skillsDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		files = append(files, File{Asset: p, Path: path.Join("skills", strings.TrimPrefix(p, skillsDir+"/"))})
		return nil
	})
	return files, err
}

// StaleArgs returns the arguments of an agent's command that load or configure
// what the installed files replace: a superseded file passed by path, or a flag
// only that file understood.
func (a Agent) StaleArgs(command []string) []string {
	var stale []string
	for _, arg := range command {
		for _, flag := range a.SupersededArgs {
			if arg == flag || strings.HasPrefix(arg, flag+"=") {
				stale = append(stale, arg)
			}
		}
		for _, p := range a.Supersedes {
			if filepath.Base(arg) == path.Base(p) {
				stale = append(stale, arg)
			}
		}
	}
	return stale
}

// superseded warns about files the installed extensions replace, under any of
// roots.
func (a Agent) superseded(roots ...string) []string {
	var warnings []string
	for _, root := range roots {
		for _, p := range a.Supersedes {
			old := filepath.Join(root, filepath.FromSlash(p))
			if _, err := os.Stat(old); err == nil {
				warnings = append(warnings, fmt.Sprintf("%s does what the Conclave extension now does: remove it, or both act on the same run", old))
			}
		}
	}
	return warnings
}

// shadowed warns about skills of the same name the agent would also discover
// under any of bases: it loads only one of them.
func (a Agent) shadowed(bases ...string) []string {
	skills, err := fs.ReadDir(assets, skillsDir)
	if err != nil {
		return nil
	}
	var warnings []string
	for _, base := range bases {
		for _, dir := range a.SkillDirs {
			for _, s := range skills {
				other := filepath.Join(base, dir, s.Name())
				if _, err := os.Stat(other); err == nil {
					warnings = append(warnings, fmt.Sprintf("%s also reads %s: with two skills named %s it loads only one of them", a.Name, other, s.Name()))
				}
			}
		}
	}
	return warnings
}

// write copies one embedded file to dst, through a temporary file so that a
// failed write never leaves half a file where the agent loads it. With follow,
// a symbolic link is written through, at its target: renaming over the link
// would replace it with a plain file, and leave the user's own copy, in a
// dotfiles repository for instance, behind. Without, the link itself is
// replaced.
func write(asset, dst string, force, follow bool) (Status, error) {
	data, err := assets.ReadFile(asset)
	if err != nil {
		return 0, err
	}
	if info, err := os.Lstat(dst); follow && err == nil && info.Mode()&fs.ModeSymlink != 0 {
		target, err := filepath.EvalSymlinks(dst)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", dst, err)
		}
		dst = target
	}
	status := Created
	current, err := os.ReadFile(dst)
	switch {
	case err == nil && bytes.Equal(current, data):
		return Unchanged, nil
	case err == nil && !force:
		return Conflict, nil
	case err == nil:
		status = Replaced
	case !errors.Is(err, fs.ErrNotExist):
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return 0, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".*")
	if err != nil {
		return 0, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return 0, err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return 0, err
	}
	if err := tmp.Close(); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return 0, err
	}
	return status, nil
}
