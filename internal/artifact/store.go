// Package artifact persists the inputs and outputs of a run on disk.
package artifact

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/bornholm/conclave/internal/domain"
)

var safeNameRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// NewRunID builds an identifier such as 20260911T210100Z-pr-123-a1b2c3d4.
func NewRunID(now time.Time, prNumber int64) string {
	return newID(now, fmt.Sprintf("pr-%d", prNumber))
}

// NewTriageRunID builds an identifier such as 20260911T210100Z-triage-12-a1b2c3d4,
// where the number is how many issues the run covers.
func NewTriageRunID(now time.Time, issues int) string {
	return newID(now, fmt.Sprintf("triage-%d", issues))
}

// NewPlanRunID builds an identifier such as 20260911T210100Z-plan-12-a1b2c3d4.
func NewPlanRunID(now time.Time, issue int64) string {
	return newID(now, fmt.Sprintf("plan-%d", issue))
}

func newID(now time.Time, kind string) string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%s-%s-%s", now.UTC().Format("20060102T150405Z"), kind, hex.EncodeToString(b[:]))
}

// Store is the directory tree of one run.
type Store struct {
	Root string
}

// Open creates the run directory under base.
func Open(base, runID string) (*Store, error) {
	root := filepath.Join(base, runID)
	for _, sub := range []string{"context", "prompts", "reports", "raw", "final"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o755); err != nil {
			return nil, fmt.Errorf("create run directory: %w", err)
		}
	}
	return &Store{Root: root}, nil
}

func (s *Store) path(parts ...string) string {
	clean := make([]string, len(parts))
	for i, p := range parts {
		clean[i] = safeNameRe.ReplaceAllString(p, "_")
	}
	return filepath.Join(append([]string{s.Root}, clean...)...)
}

// WriteJSON stores v as indented JSON.
func (s *Store) WriteJSON(sub, name string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path(sub, name), append(data, '\n'), 0o644)
}

// Write stores raw bytes.
func (s *Store) Write(sub, name string, data []byte) error {
	return os.WriteFile(s.path(sub, name), data, 0o644)
}

// WriteManifest stores manifest.json at the root.
func (s *Store) WriteManifest(m *domain.RunManifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.Root, "manifest.json"), append(data, '\n'), 0o644)
}
