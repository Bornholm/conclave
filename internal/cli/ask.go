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
	contextPath := fs.String("context", "", "file holding the context to answer against, \"-\" for standard input")
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
		return errors.New("usage: conclave ask [question] [--question TEXT] [--context FILE] [--project PATH]")
	}

	text := strings.TrimSpace(*question)
	if len(positional) == 1 {
		if text != "" {
			return errors.New("the question is given twice: as an argument and with --question")
		}
		text = strings.TrimSpace(positional[0])
	}
	if text == "" && *contextPath == "-" {
		return errors.New("standard input cannot be both the question and the context")
	}
	questionContext, err := readContext(*contextPath)
	if err != nil {
		return err
	}
	// Standard input is the question only when nothing else gave one. It is
	// never read behind the user's back otherwise: a process started with an
	// inherited pipe nobody closes would block here forever, before printing
	// anything, although the question was already in hand.
	if text == "" {
		piped, err := readStdin()
		if err != nil {
			return fmt.Errorf("read standard input: %w", err)
		}
		text = strings.TrimSpace(string(piped))
	} else if *contextPath == "" && stdinIsRedirected() {
		fmt.Fprintln(stderr, "note: standard input is not read when the question is given; pass --context - to use it")
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

// readContext loads the material the question must be answered against, from
// a file or, for "-", from standard input. It is read only when asked for,
// which is what keeps a question given on the command line from blocking on
// an inherited pipe.
func readContext(path string) (string, error) {
	switch path {
	case "":
		return "", nil
	case "-":
		data, err := readStdin()
		if err != nil {
			return "", fmt.Errorf("read standard input: %w", err)
		}
		return strings.TrimSpace(string(data)), nil
	default:
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read context: %w", err)
		}
		return strings.TrimSpace(string(data)), nil
	}
}

// readStdin returns what standard input holds, and nothing when it is a
// terminal: `conclave ask` with no question anywhere must fail rather than
// wait for a question nobody is going to type.
func readStdin() ([]byte, error) {
	if stdinReader != nil {
		return io.ReadAll(stdinReader)
	}
	if !stdinIsRedirected() {
		return nil, nil
	}
	return io.ReadAll(os.Stdin)
}

// stdinIsRedirected reports whether standard input is something other than a
// terminal. It only stats the descriptor, so it never blocks.
func stdinIsRedirected() bool {
	if stdinReader != nil {
		return true
	}
	st, err := os.Stdin.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice == 0
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
