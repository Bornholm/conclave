package config

import (
	"errors"
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"
)

// Load reads, defaults and validates the configuration file at path.
func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open configuration %q: %w", path, err)
	}
	defer f.Close()
	cfg, err := Decode(f)
	if err != nil {
		return nil, fmt.Errorf("configuration %q: %w", path, err)
	}
	return cfg, nil
}

// Decode parses a YAML document, applies defaults and validates it.
// Unknown fields are rejected so that typos are caught early.
func Decode(r io.Reader) (*Config, error) {
	var cfg Config
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("decode YAML: empty document")
		}
		return nil, fmt.Errorf("decode YAML: %w", err)
	}
	applyDefaults(&cfg)
	if err := Validate(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
