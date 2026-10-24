package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Load reads the configuration file at path, merged over the .conclave.yaml
// files of the directories above it, then defaults and validates the result.
// The farthest ancestor is merged first and the file at path last, so the
// nearest file wins; a file with "root: true" is the farthest one read. See
// merge for how two files combine.
func Load(path string) (*Config, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("configuration %q: %w", path, err)
	}
	doc, root, err := readFile(abs)
	if err != nil {
		return nil, fmt.Errorf("configuration %q: %w", path, err)
	}
	chain := []*yaml.Node{doc}
	sources := []string{abs}
	for dir := filepath.Dir(filepath.Dir(abs)); !root; dir = filepath.Dir(dir) {
		candidate := filepath.Join(dir, DefaultFileName)
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			if err := checkInherited(st); err != nil {
				return nil, fmt.Errorf("configuration %q, inherited by %q: %w", candidate, path, err)
			}
			var parent *yaml.Node
			if parent, root, err = readFile(candidate); err != nil {
				return nil, fmt.Errorf("configuration %q, inherited by %q: %w", candidate, path, err)
			}
			chain = append(chain, parent)
			sources = append(sources, candidate)
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	merged := chain[len(chain)-1]
	for i := len(chain) - 2; i >= 0; i-- {
		merge(merged, chain[i], "")
	}
	data, err := yaml.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("configuration %q: %w", path, err)
	}
	cfg, err := Decode(bytes.NewReader(data))
	if err != nil {
		if len(sources) > 1 {
			return nil, fmt.Errorf("configuration %q, merged with %s: %w", path, strings.Join(sources[1:], ", "), err)
		}
		return nil, fmt.Errorf("configuration %q: %w", path, err)
	}
	for i, j := 0, len(sources)-1; i < j; i, j = i+1, j-1 {
		sources[i], sources[j] = sources[j], sources[i]
	}
	cfg.Sources = sources
	return cfg, nil
}

// readFile parses one configuration file on its own: unknown fields and
// another schema version are reported against the file that has them, not
// against the merged document. It returns the file's mapping and whether the
// file says it is the root.
func readFile(path string) (*yaml.Node, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, fmt.Errorf("open: %w", err)
	}
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, false, errors.New("decode YAML: empty document")
		}
		return nil, false, fmt.Errorf("decode YAML: %w", err)
	}
	if cfg.Version != 0 && cfg.Version != CurrentVersion {
		return nil, false, fmt.Errorf("version: must be %d, got %d", CurrentVersion, cfg.Version)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, false, fmt.Errorf("decode YAML: %w", err)
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, false, errors.New("decode YAML: the document must be a mapping")
	}
	return doc.Content[0], cfg.Root, nil
}

// merge merges src over dst, in place. A mapping merges key by key, so a file
// sets only the fields it names; anything else, a list included, replaces
// what dst had. The one exception is the agents list, merged by id: an agent
// whose id dst already has is merged over it, a new one is added after.
func merge(dst, src *yaml.Node, path string) {
	for i := 0; i+1 < len(src.Content); i += 2 {
		key, value := src.Content[i], src.Content[i+1]
		name := path + key.Value
		j := mappingIndex(dst, key.Value)
		switch {
		case j < 0:
			dst.Content = append(dst.Content, key, value)
		case name == "agents" && dst.Content[j+1].Kind == yaml.SequenceNode && value.Kind == yaml.SequenceNode:
			mergeAgents(dst.Content[j+1], value)
		case dst.Content[j+1].Kind == yaml.MappingNode && value.Kind == yaml.MappingNode:
			merge(dst.Content[j+1], value, name+".")
		default:
			dst.Content[j+1] = value
		}
	}
}

func mergeAgents(dst, src *yaml.Node) {
	for _, agent := range src.Content {
		id := agentID(agent)
		k := -1
		for i, a := range dst.Content {
			if id != "" && agentID(a) == id {
				k = i
				break
			}
		}
		if k < 0 || agent.Kind != yaml.MappingNode || dst.Content[k].Kind != yaml.MappingNode {
			if k < 0 {
				dst.Content = append(dst.Content, agent)
			} else {
				dst.Content[k] = agent
			}
			continue
		}
		merge(dst.Content[k], agent, "agents[].")
	}
}

func agentID(n *yaml.Node) string {
	if n.Kind != yaml.MappingNode {
		return ""
	}
	if i := mappingIndex(n, "id"); i >= 0 {
		return n.Content[i+1].Value
	}
	return ""
}

func mappingIndex(n *yaml.Node, key string) int {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return i
		}
	}
	return -1
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
