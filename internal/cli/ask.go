package cli

import (
	"context"
	"errors"
	"flag"
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
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if len(positional) > 1 {
		return errors.New("usage: conclave ask [question] [--question TEXT] [--context FILE] [--project PATH]")
	}

	if set["question"] && set["q"] {
		return errors.New("the question is given twice: --question and -q are the same flag")
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

	if *project == "" && set["rev"] {
		// A revision only means something against a checkout. Saying so
		// beats answering as though the flag had been honoured.
		// --keep-worktrees is not in the same case: without a project it
		// still keeps the scratch directories, which is what it promises.
		fmt.Fprintln(stderr, "note: --rev does nothing without --project")
	}

	// The configuration is read first because it carries the limits, and a
	// limit that is applied after the whole input is in memory is not a
	// limit: `conclave ask -q "why?" --context - < /dev/zero` would grow the
	// heap until it dies.
	resolved, err := resolveConfigPath(*configPath, *project, set["config"])
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

	questionContext, err := readContext(*contextPath, cfg.Ask.Limits.MaxContextBytes)
	if err != nil {
		return err
	}
	// Standard input is the question only when nothing else gave one. It is
	// never read behind the user's back otherwise: a process started with an
	// inherited pipe nobody closes would block here forever, before printing
	// anything, although the question was already in hand.
	if text == "" {
		piped, err := readStdin(cfg.Ask.Limits.MaxQuestionBytes)
		if err != nil {
			return fmt.Errorf("read standard input: %w", err)
		}
		text = trimRead(piped, cfg.Ask.Limits.MaxQuestionBytes)
	} else if *contextPath != "-" && stdinIsRedirected() {
		// Anything piped in and not claimed is dropped, --context FILE
		// included. Saying so is the whole promise: the input never
		// disappears without a word.
		fmt.Fprintln(stderr, "note: standard input is not read when the question is given; pass --context - to use it")
	}
	if text == "" {
		return errors.New("no question: pass it as an argument, with --question, or on standard input")
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
func readContext(path string, max int) (string, error) {
	switch path {
	case "":
		return "", nil
	case "-":
		// Asked for by name, standard input is read whatever it is, a
		// terminal included: the user typing a context and ending it with
		// Ctrl-D meant exactly that. The terminal guard belongs to the
		// implicit path, where nobody asked for a read at all.
		data, err := readAllStdin(max)
		if err != nil {
			return "", fmt.Errorf("read standard input: %w", err)
		}
		return trimRead(data, max), nil
	default:
		f, err := os.Open(path)
		if err != nil {
			return "", fmt.Errorf("read context: %w", err)
		}
		defer f.Close()
		data, err := readLimited(f, max)
		if err != nil {
			return "", fmt.Errorf("read context: %w", err)
		}
		return trimRead(data, max), nil
	}
}

// trimRead trims the read and marks it when it hit the bound. The marker
// cannot be left to the app: trimming an input whose last byte is a newline
// brings the length back under the limit, and the cut would go unannounced
// precisely where it is most ordinary, at the end of a log.
func trimRead(data []byte, max int) string {
	s := strings.TrimSpace(string(data))
	if max > 0 && len(data) > max && len(s) <= max {
		s += app.TruncationMarker
	}
	return s
}

// readStdin returns what standard input holds, and nothing when it is a
// terminal: `conclave ask` with no question anywhere must fail rather than
// wait for a question nobody is going to type.
func readStdin(max int) ([]byte, error) {
	if !stdinIsRedirected() {
		return nil, nil
	}
	return readAllStdin(max)
}

// readAllStdin reads standard input, with no terminal guard.
func readAllStdin(max int) ([]byte, error) {
	if stdinReader != nil {
		return readLimited(stdinReader, max)
	}
	return readLimited(os.Stdin, max)
}

// readLimited reads at most max bytes plus one. The extra byte is what lets
// the app tell a full input from a cut one and add its truncation marker,
// while the process never holds more than the configured limit in memory.
func readLimited(r io.Reader, max int) ([]byte, error) {
	if max <= 0 {
		return io.ReadAll(r)
	}
	return io.ReadAll(io.LimitReader(r, int64(max)+1))
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
// project's own file is tried next, then the user-wide one. A path the user
// actually typed is never one of several candidates, even when it is spelled
// like the default: naming a file and getting another one is worse than the
// error.
func resolveConfigPath(path, project string, explicit bool) (string, error) {
	if explicit || path != config.DefaultFileName {
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
