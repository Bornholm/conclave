package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/bornholm/conclave/internal/app"
	"github.com/bornholm/conclave/internal/config"
	"github.com/bornholm/conclave/internal/output"
)

// stdinReader overrides standard input in tests. When nil, standard input is
// read only if something is piped into it.
var stdinReader io.Reader

func runAsk(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("ask", stderr)
	question := fs.String("question", "", "the question to ask (default: read from standard input)")
	fs.StringVar(question, "q", "", "shorthand for --question")
	project := fs.String("project", "", "path of the repository the question is about (\".\" for the current one)")
	configPath := fs.String("config", config.DefaultFileName, "configuration file")
	format := fs.String("format", "", "output format: markdown or json (default: configuration)")
	rev := fs.String("rev", "HEAD", "revision the agents read, with --project")
	keep := fs.Bool("keep-worktrees", false, "keep the temporary working directories after the run")
	verbose := fs.Bool("verbose", false, "verbose logging on stderr")
	positional, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(positional) > 1 {
		return errors.New("usage: conclave ask [question] [--question TEXT] [--project PATH]")
	}

	text := strings.TrimSpace(*question)
	if len(positional) == 1 {
		if text != "" {
			return errors.New("the question is given twice: as an argument and with --question")
		}
		text = strings.TrimSpace(positional[0])
	}
	piped, err := readStdin()
	if err != nil {
		return fmt.Errorf("read standard input: %w", err)
	}
	// Standard input is the question when nothing else gave one, and the
	// context it must be answered against otherwise. That is what lets a
	// large input be piped in under a short question.
	var questionContext string
	if text == "" {
		text = strings.TrimSpace(string(piped))
	} else {
		questionContext = strings.TrimSpace(string(piped))
	}
	if text == "" {
		return errors.New("no question: pass it as an argument, with --question, or on standard input")
	}

	resolved, err := resolveConfigPath(*configPath, *project)
	if err != nil {
		return err
	}
	cfg, err := config.Load(resolved)
	if err != nil {
		return err
	}
	if *format != "" {
		if *format != config.FormatMarkdown && *format != config.FormatJSON {
			return fmt.Errorf("invalid --format %q", *format)
		}
		cfg.Output.Format = *format
	}
	logger := newLogger(stderr, *verbose)
	for _, w := range config.Warnings(cfg) {
		logger.Warn(w)
	}
	a := &app.App{Config: cfg, Logger: logger}
	res, err := a.Ask(ctx, app.AskRequest{
		Question: text, Context: questionContext,
		Project: *project, Revision: *rev, KeepWorktrees: *keep,
	})
	if err != nil {
		if res != nil && res.RunDir != "" {
			fmt.Fprintln(stderr, "artifacts:", res.RunDir)
		}
		return err
	}
	if cfg.Output.Format == config.FormatJSON {
		return output.AnswerJSON(stdout, res.Answer)
	}
	return output.AnswerMarkdown(stdout, res.Answer, output.Options{
		ShowAttribution: cfg.ShowAttribution(), ShowFailedAgents: cfg.ShowFailedAgents(),
	})
}

// readStdin returns what was piped into the process, and nothing when
// standard input is a terminal: an interactive `conclave ask "..."` must not
// hang waiting for a context nobody is going to type.
func readStdin() ([]byte, error) {
	if stdinReader != nil {
		return io.ReadAll(stdinReader)
	}
	st, err := os.Stdin.Stat()
	if err != nil || st.Mode()&os.ModeCharDevice != 0 {
		return nil, nil
	}
	return io.ReadAll(os.Stdin)
}

// resolveConfigPath finds the configuration of an ask run. A question needs
// no repository, so the working directory may hold no .conclave.yaml: the
// project's own file is tried next, then the user-wide one.
func resolveConfigPath(path, project string) (string, error) {
	if path != config.DefaultFileName {
		return path, nil
	}
	candidates := []string{path}
	if project != "" {
		candidates = append(candidates, filepath.Join(project, config.DefaultFileName))
	}
	if dir, err := os.UserConfigDir(); err == nil {
		candidates = append(candidates, filepath.Join(dir, "conclave", "config.yaml"))
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c, nil
		}
	}
	return "", fmt.Errorf("no configuration found, looked at: %s", strings.Join(candidates, ", "))
}
