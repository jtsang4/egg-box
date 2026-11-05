package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jtsang4/sing-box-ui/internal/fsutil"
)

// Settings are the agent-side preferences. Start-at-boot of the service and
// start-at-login of the GUI are deliberately NOT here: they belong to the
// operating system (service start type, login items) and are separate switches.
type Settings struct {
	AutoConnect              bool   `json:"auto_connect"`
	ResumeAfterCrash         bool   `json:"resume_after_crash"`
	ReconnectOnNetworkChange bool   `json:"reconnect_on_network_change"`
	LogLevel                 string `json:"log_level"`
}

// DefaultSettings are what a fresh installation uses.
func DefaultSettings() Settings {
	return Settings{ResumeAfterCrash: true, ReconnectOnNetworkChange: true, LogLevel: "info"}
}

// ValidLogLevel reports whether level is one the core understands.
func ValidLogLevel(level string) bool {
	switch level {
	case "trace", "debug", "info", "warn", "error":
		return true
	}
	return false
}

// Intent is what the user asked for, kept across agent restarts so a crash,
// reboot or upgrade can restore it (after cleanup, and subject to Settings).
type Intent struct {
	Connected          bool      `json:"connected"`
	Revision           uint64    `json:"revision"`
	ResumeAfterUpgrade bool      `json:"resume_after_upgrade"`
	Updated            time.Time `json:"updated"`
}

// File persists one JSON document atomically.
type File[T any] struct {
	mu       sync.Mutex
	path     string
	defaults T
}

// OpenFile returns a persisted value. A missing file yields defaults; a corrupt
// file is quarantined and also yields defaults, so a damaged settings file can
// never stop the agent from starting.
func OpenFile[T any](root, name string, defaults T) (*File[T], error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	return &File[T]{path: filepath.Join(root, name), defaults: defaults}, nil
}

// Load reads the current value.
func (f *File[T]) Load() T {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.loadLocked()
}

func (f *File[T]) loadLocked() T {
	raw, err := os.ReadFile(f.path)
	if err != nil {
		return f.defaults
	}
	v := f.defaults
	if err := json.Unmarshal(raw, &v); err != nil {
		_ = os.Rename(f.path, f.path+".corrupt-"+time.Now().UTC().Format("20060102T150405"))
		return f.defaults
	}
	return v
}

// Save replaces the value.
func (f *File[T]) Save(v T) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.saveLocked(v)
}

func (f *File[T]) saveLocked(v T) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(f.path, raw, 0o600)
}

// Update applies fn to the current value and persists the result.
func (f *File[T]) Update(fn func(*T) error) (T, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v := f.loadLocked()
	if err := fn(&v); err != nil {
		return v, err
	}
	if err := f.saveLocked(v); err != nil {
		return v, errors.Join(errors.New("persist state"), err)
	}
	return v, nil
}
