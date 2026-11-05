package store

import (
	"fmt"
	"os"
	"path/filepath"
)

func (s *Store) scratchRoot() string { return filepath.Join(s.root, "tmp") }

// Scratch is a throw-away directory holding resources for a dry run
// (ValidateConfig) without creating a revision.
type Scratch struct {
	dir string
}

// Scratch writes the resources into a fresh protected directory. Close it when done.
func (s *Store) Scratch(resources []Resource) (*Scratch, error) {
	if err := validateResources(resources); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.scratchRoot(), 0o700); err != nil {
		return nil, fmt.Errorf("create scratch root: %w", err)
	}
	dir, err := os.MkdirTemp(s.scratchRoot(), "scratch-")
	if err != nil {
		return nil, fmt.Errorf("create scratch dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "resources"), 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	for _, r := range resources {
		if err := writeResource(filepath.Join(dir, "resources", r.Name), r.Data); err != nil {
			_ = os.RemoveAll(dir)
			return nil, err
		}
	}
	return &Scratch{dir: dir}, nil
}

// ResourceDir is where the resources of the dry run live.
func (sc *Scratch) ResourceDir() string { return filepath.Join(sc.dir, "resources") }

// Close removes the directory.
func (sc *Scratch) Close() { _ = os.RemoveAll(sc.dir) }
