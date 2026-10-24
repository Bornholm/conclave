package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Load reads the configuration file at path, merged over the .conclave.yaml
// files of the directories above it, then defaults and validates the result.
// The farthest ancestor is merged first and the file at path last, so the
// nearest file wins; a file with "inherits: false" is the farthest one read. See
// merge for how two files combine.
func Load(path string) (*Config, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("configuration %q: %w", path, err)
	}
	doc, inherits, _, err := readFile(abs, false)
	if err != nil {
		return nil, fmt.Errorf("configuration %q: %w", path, err)
	}
	chain := []*yaml.Node{doc}
	sources := []string{abs}
	for dir := filepath.Dir(filepath.Dir(abs)); inherits; dir = filepath.Dir(dir) {
		candidate := filepath.Join(dir, DefaultFileName)
		if candidate == abs {
			// A file at the filesystem root is not its own parent.
			break
		}
		parent, parentInherits, found, err := readFile(candidate, true)
		if err != nil {
			return nil, fmt.Errorf("configuration %q, inherited by %q: %w", candidate, path, err)
		}
		if found {
			chain = append(chain, parent)
			sources = append(sources, candidate)
			inherits = parentInherits
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	merged := chain[len(chain)-1]
	for i := len(chain) - 2; i >= 0; i-- {
		merge(merged, chain[i], "")
	}
	// inherits says where the search stops, for one file: it is not part of
	// the configuration they make together.
	if i := mappingIndex(merged, "inherits"); i >= 0 {
		merged.Content = append(merged.Content[:i], merged.Content[i+2:]...)
	}
	data, err := yaml.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("configuration %q: %w", path, err)
	}
	// From here the sources read the farthest first, as everywhere else.
	slices.Reverse(sources)
	cfg, err := Decode(bytes.NewReader(data))
	if err != nil {
		if len(sources) > 1 {
			return nil, fmt.Errorf("configuration %q, merged with %s: %w", path, strings.Join(sources[:len(sources)-1], ", "), err)
		}
		return nil, fmt.Errorf("configuration %q: %w", path, err)
	}
	cfg.Sources = sources
	return cfg, nil
}

// readFile parses one configuration file on its own: unknown fields and
// another schema version are reported against the file that has them, not
// against the merged document. It returns the file's mapping and whether the
// file inherits the ones above it. An inherited file that does not exist, or
// is a directory, is not found; one that exists is checked on the descriptor
// it is read from, so the file judged is the file read.
func readFile(path string, inherited bool) (*yaml.Node, bool, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		if inherited && errors.Is(err, fs.ErrNotExist) {
			return nil, false, false, nil
		}
		if inherited && errors.Is(err, fs.ErrPermission) {
			return nil, false, false, fmt.Errorf("open: %w: add inherits: false below it to stop inheriting it", err)
		}
		return nil, false, false, fmt.Errorf("open: %w", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, false, false, fmt.Errorf("open: %w", err)
	}
	if inherited {
		if st.IsDir() {
			return nil, false, false, nil
		}
		if err := checkInherited(st); err != nil {
			return nil, false, false, err
		}
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, false, false, fmt.Errorf("read: %w", err)
	}
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			// An inherited file with nothing but comments declares
			// nothing; the file loaded must say something.
			if inherited {
				return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}, true, true, nil
			}
			return nil, false, false, errors.New("decode YAML: empty document")
		}
		return nil, false, false, fmt.Errorf("decode YAML: %w", err)
	}
	if cfg.Version != 0 && cfg.Version != CurrentVersion {
		return nil, false, false, fmt.Errorf("version: must be %d, got %d", CurrentVersion, cfg.Version)
	}
	// Agents merge by id: two with the same id in one file would be folded
	// into one by the merge, where the merged validation can no longer see
	// them.
	ids := map[string]bool{}
	for _, a := range cfg.Agents {
		if a.ID != "" && ids[a.ID] {
			return nil, false, false, fmt.Errorf("agents[%s].id: duplicate", a.ID)
		}
		ids[a.ID] = true
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, false, false, fmt.Errorf("decode YAML: %w", err)
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, false, false, errors.New("decode YAML: the document must be a mapping")
	}
	// Aliases are expanded: once merged, the anchor an alias points to may
	// belong to a subtree another file replaced.
	return expandAliases(doc.Content[0]), boolValue(cfg.Inherits, true), true, nil
}

// expandAliases replaces every alias under n by a copy of the node it points
// to, so that the tree no longer depends on where its anchors are. A
// recursive anchor never gets here: no field of Config can hold itself, so
// the typed decode before it refuses one.
func expandAliases(n *yaml.Node) *yaml.Node {
	if n.Kind == yaml.AliasNode {
		n = expandAliases(n.Alias)
	}
	c := *n
	c.Anchor = ""
	c.Content = make([]*yaml.Node, len(n.Content))
	for i, child := range n.Content {
		c.Content[i] = expandAliases(child)
	}
	return &c
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
	if err := dropDisabled(&cfg); err != nil {
		return nil, err
	}
	applyDefaults(&cfg)
	if err := Validate(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// dropDisabled leaves out the agents marked disabled. One with no command is
// only the mark, an entry of its own that matched no inherited agent: its id
// is wrong, and saying nothing would leave the agent it meant running.
func dropDisabled(cfg *Config) error {
	kept := cfg.Agents[:0]
	for _, a := range cfg.Agents {
		if !a.Disabled {
			kept = append(kept, a)
			continue
		}
		if len(a.Command) == 0 {
			return fmt.Errorf("agents[%s]: disabled, but no agent %s is declared to disable", a.ID, a.ID)
		}
		// Left out, it would only be "not a configured reviewer" there,
		// which hides why.
		for _, list := range []struct {
			field string
			ids   []string
		}{{"triage.reviewers", cfg.Triage.Reviewers}, {"plan.planners", cfg.Plan.Planners}, {"ask.respondents", cfg.Ask.Respondents}} {
			if slices.Contains(list.ids, a.ID) {
				return fmt.Errorf("agents[%s]: disabled, but %s still names it: remove it there too", a.ID, list.field)
			}
		}
	}
	cfg.Agents = kept
	return nil
}
