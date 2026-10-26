package config

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// DefaultEnvFile is the env file read when env_file is not set.
const DefaultEnvFile = ".env"

// EnvReport is what LoadEnv did with the env file.
type EnvReport struct {
	// File is the absolute path of the env file read, empty when there was
	// none.
	File string
	// Set lists the variables taken from the file, Kept the ones the
	// environment already held, which win, and Refused the protected ones,
	// which a file in the working directory may not set.
	Set, Kept, Refused []string
}

// LoadEnv reads the env file into the process environment, so that the
// forge token and the agents' credentials can live next to the project. A
// variable already set is left alone. The default .env may be missing, a
// file env_file names may not.
func (c *Config) LoadEnv() error {
	name := c.EnvFile
	if name == "" {
		name = DefaultEnvFile
	}
	abs, err := filepath.Abs(name)
	if err != nil {
		return fmt.Errorf("env_file %q: %w", name, err)
	}
	data, err := os.ReadFile(abs)
	if errors.Is(err, fs.ErrNotExist) && c.EnvFile == "" {
		return nil
	}
	if err != nil {
		return fmt.Errorf("env_file: %w", err)
	}
	vars, err := ParseEnv(data)
	if err != nil {
		return fmt.Errorf("env_file %s: %w", abs, err)
	}
	report := EnvReport{File: abs}
	for _, v := range vars {
		if IsProtectedEnv(v.Key) || strings.HasPrefix(v.Key, "GIT_") {
			report.Refused = append(report.Refused, v.Key)
			continue
		}
		if _, ok := os.LookupEnv(v.Key); ok {
			report.Kept = append(report.Kept, v.Key)
			continue
		}
		if err := os.Setenv(v.Key, v.Value); err != nil {
			return fmt.Errorf("env_file %s: %s: %w", abs, v.Key, err)
		}
		report.Set = append(report.Set, v.Key)
	}
	c.Env = report
	return nil
}

// EnvVar is one KEY=VALUE line of an env file.
type EnvVar struct {
	Key, Value string
}

// ParseEnv reads env file lines: KEY=VALUE, an optional "export " before
// the key, blank lines and # comments. A value in single quotes is taken as
// it is, one in double quotes understands \n, \t, \" and \\, and an
// unquoted one ends at a " #" comment. Variables are not expanded. A key
// given twice keeps its last value.
func ParseEnv(data []byte) ([]EnvVar, error) {
	var vars []EnvVar
	index := map[string]int{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if n == 1 {
			line = strings.TrimPrefix(line, "\uFEFF")
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, raw, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || !envNameRe.MatchString(key) {
			return nil, fmt.Errorf("line %d: want KEY=VALUE", n)
		}
		value, err := parseEnvValue(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("line %d: %s: %w", n, key, err)
		}
		if i, ok := index[key]; ok {
			vars[i].Value = value
			continue
		}
		index[key] = len(vars)
		vars = append(vars, EnvVar{Key: key, Value: value})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return vars, nil
}

func parseEnvValue(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	switch q := raw[0]; q {
	case '\'', '"':
		var b strings.Builder
		for i := 1; i < len(raw); i++ {
			c := raw[i]
			switch {
			case c == q:
				if rest := strings.TrimSpace(raw[i+1:]); rest != "" && !strings.HasPrefix(rest, "#") {
					return "", errors.New("text after the closing quote")
				}
				return b.String(), nil
			case c == '\\' && q == '"' && i+1 < len(raw):
				i++
				switch raw[i] {
				case 'n':
					b.WriteByte('\n')
				case 't':
					b.WriteByte('\t')
				case 'r':
					b.WriteByte('\r')
				default:
					b.WriteByte(raw[i])
				}
			default:
				b.WriteByte(c)
			}
		}
		return "", errors.New("unclosed quote")
	}
	if i := strings.Index(raw, " #"); i >= 0 {
		raw = raw[:i]
	}
	return strings.TrimSpace(raw), nil
}
