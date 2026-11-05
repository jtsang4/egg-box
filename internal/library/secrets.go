package library

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/jtsang4/sing-box-ui/internal/fsutil"
)

// ErrNoSecret means nothing is stored under the key.
var ErrNoSecret = errors.New("no such secret")

// SecretStore keeps the values that must not sit in ordinary files: the
// subscription links, which carry the account's credentials.
type SecretStore interface {
	Get(key string) (string, error)
	Set(key, value string) error
	Delete(key string) error
}

// MemorySecrets keeps secrets in memory (tests, demos).
type MemorySecrets struct {
	mu sync.Mutex
	m  map[string]string
}

// NewMemorySecrets returns an empty store.
func NewMemorySecrets() *MemorySecrets { return &MemorySecrets{m: map[string]string{}} }

func (s *MemorySecrets) Get(key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[key]
	if !ok {
		return "", ErrNoSecret
	}
	return v, nil
}

func (s *MemorySecrets) Set(key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = value
	return nil
}

func (s *MemorySecrets) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, key)
	return nil
}

// FileSecrets keeps secrets in one file only the user can read. It is the
// fallback for systems without a usable keychain.
type FileSecrets struct {
	path string
	mu   sync.Mutex
}

// NewFileSecrets stores secrets in path.
func NewFileSecrets(path string) *FileSecrets { return &FileSecrets{path: path} }

func (s *FileSecrets) load() (map[string]string, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func (s *FileSecrets) save(m map[string]string) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(s.path, data, 0o600)
}

func (s *FileSecrets) Get(key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.load()
	if err != nil {
		return "", err
	}
	v, ok := m[key]
	if !ok {
		return "", ErrNoSecret
	}
	return v, nil
}

func (s *FileSecrets) Set(key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.load()
	if err != nil {
		return err
	}
	m[key] = value
	return s.save(m)
}

func (s *FileSecrets) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.load()
	if err != nil {
		return err
	}
	if _, ok := m[key]; !ok {
		return nil
	}
	delete(m, key)
	return s.save(m)
}

// Chain reads from the stores in order and writes to the first that accepts
// the value, so a keychain that refuses access degrades to a protected file
// instead of losing the subscription.
type Chain []SecretStore

func (c Chain) Get(key string) (string, error) {
	var last error = ErrNoSecret
	for _, s := range c {
		v, err := s.Get(key)
		if err == nil {
			return v, nil
		}
		if !errors.Is(err, ErrNoSecret) {
			last = err
		}
	}
	return "", last
}

func (c Chain) Set(key, value string) error {
	var last error
	for i, s := range c {
		if err := s.Set(key, value); err != nil {
			last = err
			continue
		}
		// Do not leave an older copy in a store that is read first.
		for _, earlier := range c[:i] {
			_ = earlier.Delete(key)
		}
		return nil
	}
	if last == nil {
		last = errors.New("no secret store is configured")
	}
	return last
}

func (c Chain) Delete(key string) error {
	var last error
	for _, s := range c {
		if err := s.Delete(key); err != nil {
			last = err
		}
	}
	return last
}
