// Package store keeps the agent's durable state in its protected data
// directory: configuration revisions (with a last-known-good pointer), the
// persisted settings and the user's connection intent.
//
// Layout beneath the root:
//
//	config/meta.json            current + last good revision pointers
//	config/rev-000007/          one promoted revision
//	    config.json
//	    resources/<name>        files the configuration references
//	config/rev-000008.pending/  being prepared, never visible as a revision
//	settings.json
//	intent.json
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jtsang4/sing-box-ui/internal/fsutil"
)

// Limits protect the agent from absurd inputs coming from the (unprivileged) GUI.
const (
	MaxConfigBytes   = 8 << 20
	MaxResourceBytes = 16 << 20
	MaxResourceTotal = 64 << 20
	MaxResources     = 64
)

var resourceName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// Errors callers can match with errors.Is.
var (
	ErrRevisionConflict = errors.New("revision conflict")
	ErrNoRevision       = errors.New("no such revision")
	ErrInvalidResource  = errors.New("invalid resource")
)

// Resource is a file the configuration references by name.
type Resource struct {
	Name   string
	Data   []byte
	SHA256 string // optional, lowercase hex
}

// Revision describes a promoted (or pending) configuration revision on disk.
type Revision struct {
	N           uint64    `json:"n"`
	ProfileID   string    `json:"profile_id,omitempty"`
	ProfileName string    `json:"profile_name,omitempty"`
	Created     time.Time `json:"created"`
	Dir         string    `json:"-"`
}

// ConfigPath is the stored configuration file of the revision. It holds the
// configuration as the GUI sent it; the policy layer normalizes it again every
// time it is started, with the platform plan of that moment.
func (r Revision) ConfigPath() string { return filepath.Join(r.Dir, "config.json") }

// ResourceDir holds the resources of the revision.
func (r Revision) ResourceDir() string { return filepath.Join(r.Dir, "resources") }

type meta struct {
	Version  int                 `json:"version"`
	Current  uint64              `json:"current"`
	LastGood uint64              `json:"last_good"`
	Revs     map[uint64]Revision `json:"revs"`
}

// Store is safe for concurrent use.
type Store struct {
	mu   sync.Mutex
	root string
	meta meta
}

// Open prepares the directory tree under root and loads the revision pointers.
func Open(root string) (*Store, error) {
	s := &Store{root: root, meta: meta{Version: 1, Revs: map[uint64]Revision{}}}
	if err := os.MkdirAll(s.configDir(), 0o700); err != nil {
		return nil, fmt.Errorf("create config dir: %w", err)
	}
	raw, err := os.ReadFile(s.metaPath())
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, fmt.Errorf("read revision metadata: %w", err)
	default:
		if err := json.Unmarshal(raw, &s.meta); err != nil {
			return nil, fmt.Errorf("revision metadata is unreadable: %w", err)
		}
		if s.meta.Revs == nil {
			s.meta.Revs = map[uint64]Revision{}
		}
	}
	s.sweepLocked()
	_ = os.RemoveAll(s.scratchRoot())
	return s, nil
}

func (s *Store) configDir() string { return filepath.Join(s.root, "config") }
func (s *Store) metaPath() string  { return filepath.Join(s.configDir(), "meta.json") }
func (s *Store) revDir(n uint64) string {
	return filepath.Join(s.configDir(), fmt.Sprintf("rev-%06d", n))
}
func (s *Store) pendingDir(n uint64) string { return s.revDir(n) + ".pending" }

// Root returns the data root the store was opened with.
func (s *Store) Root() string { return s.root }

// Current returns the revision the user last accepted.
func (s *Store) Current() (Revision, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.revisionLocked(s.meta.Current)
}

// LastGood returns the newest revision that is known to have started successfully.
func (s *Store) LastGood() (Revision, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.revisionLocked(s.meta.LastGood)
}

// Get returns a specific promoted revision.
func (s *Store) Get(n uint64) (Revision, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.revisionLocked(n)
}

func (s *Store) revisionLocked(n uint64) (Revision, bool) {
	if n == 0 {
		return Revision{}, false
	}
	r, ok := s.meta.Revs[n]
	if !ok {
		return Revision{}, false
	}
	r.Dir = s.revDir(n)
	return r, true
}

// CurrentN returns the current revision number (0 when none).
func (s *Store) CurrentN() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.meta.Current
}

// Staged is a revision under preparation. Exactly one of Promote or Discard
// must follow.
type Staged struct {
	N           uint64
	ProfileID   string
	ProfileName string
	dir         string // pending directory
	final       string // directory the revision will live in once promoted
	done        bool
}

// FinalResourceDir is where the resources will be after Promote. The policy
// layer uses it to rewrite "res:<name>" references before the move happens.
func (st *Staged) FinalResourceDir() string { return filepath.Join(st.final, "resources") }

// PendingResourceDir is where the resources are right now. A dry-run of the
// core (which opens rule-set files while it is being built) must use this
// directory, because the final one does not exist before Promote.
func (st *Staged) PendingResourceDir() string { return filepath.Join(st.dir, "resources") }

// WriteConfig stores the normalized configuration in the pending directory.
func (st *Staged) WriteConfig(data []byte) error {
	if len(data) == 0 || len(data) > MaxConfigBytes {
		return fmt.Errorf("configuration size %d is outside 1..%d bytes", len(data), MaxConfigBytes)
	}
	return fsutil.WriteFileAtomic(filepath.Join(st.dir, "config.json"), data, 0o600)
}

// Stage allocates revision expected+1 and writes the resources. It fails with
// ErrRevisionConflict when expected is not the stored revision, so two writers
// (or a stale retry) can never silently overwrite each other.
func (s *Store) Stage(expected uint64, profileID, profileName string, resources []Resource) (*Staged, error) {
	if err := validateResources(resources); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if expected != s.meta.Current {
		return nil, fmt.Errorf("%w: expected %d, stored %d", ErrRevisionConflict, expected, s.meta.Current)
	}
	n := s.meta.Current + 1
	// A burned number from an earlier failed attempt may have left a pending dir.
	_ = os.RemoveAll(s.pendingDir(n))
	_ = os.RemoveAll(s.revDir(n))

	dir := s.pendingDir(n)
	if err := os.MkdirAll(filepath.Join(dir, "resources"), 0o700); err != nil {
		return nil, fmt.Errorf("create revision dir: %w", err)
	}
	for _, r := range resources {
		if err := writeResource(filepath.Join(dir, "resources", r.Name), r.Data); err != nil {
			_ = os.RemoveAll(dir)
			return nil, err
		}
	}
	return &Staged{N: n, ProfileID: profileID, ProfileName: profileName, dir: dir, final: s.revDir(n)}, nil
}

// Promote makes a staged revision current. The previous current revision is
// kept so a failed start can roll back to it.
func (s *Store) Promote(st *Staged) (Revision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st.done {
		return Revision{}, errors.New("staged revision already finished")
	}
	if _, err := os.Stat(filepath.Join(st.dir, "config.json")); err != nil {
		return Revision{}, fmt.Errorf("staged revision has no configuration: %w", err)
	}
	if err := os.Rename(st.dir, st.final); err != nil {
		return Revision{}, fmt.Errorf("promote revision: %w", err)
	}
	st.done = true

	rev := Revision{N: st.N, ProfileID: st.ProfileID, ProfileName: st.ProfileName, Created: time.Now().UTC()}
	next := s.meta
	next.Revs = cloneRevs(s.meta.Revs)
	next.Revs[st.N] = rev
	next.Current = st.N
	if err := s.persistLocked(next); err != nil {
		_ = os.Rename(st.final, st.dir)
		st.done = false
		return Revision{}, err
	}
	s.meta = next
	rev.Dir = st.final
	s.pruneLocked()
	return rev, nil
}

// Discard throws a staged revision away.
func (s *Store) Discard(st *Staged) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st == nil || st.done {
		return
	}
	st.done = true
	_ = os.RemoveAll(st.dir)
}

// MarkGood records that revision n started successfully.
func (s *Store) MarkGood(n uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.meta.Revs[n]; !ok {
		return fmt.Errorf("%w: %d", ErrNoRevision, n)
	}
	if s.meta.LastGood == n {
		return nil
	}
	next := s.meta
	next.LastGood = n
	if err := s.persistLocked(next); err != nil {
		return err
	}
	s.meta = next
	s.pruneLocked()
	return nil
}

// Revert makes an older revision current again after a failed apply.
func (s *Store) Revert(n uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.meta.Revs[n]; !ok {
		return fmt.Errorf("%w: %d", ErrNoRevision, n)
	}
	next := s.meta
	next.Revs = cloneRevs(s.meta.Revs)
	failed := s.meta.Current
	next.Current = n
	if failed > n {
		delete(next.Revs, failed)
	}
	if err := s.persistLocked(next); err != nil {
		return err
	}
	s.meta = next
	if failed > n {
		_ = os.RemoveAll(s.revDir(failed))
	}
	return nil
}

func (s *Store) persistLocked(m meta) error {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(s.metaPath(), raw, 0o600)
}

// pruneLocked keeps the current and last-good revisions only.
func (s *Store) pruneLocked() {
	keep := map[uint64]bool{s.meta.Current: true, s.meta.LastGood: true}
	changed := false
	for n := range s.meta.Revs {
		if !keep[n] {
			delete(s.meta.Revs, n)
			_ = os.RemoveAll(s.revDir(n))
			changed = true
		}
	}
	if changed {
		_ = s.persistLocked(s.meta)
	}
}

// sweepLocked removes directories nobody points to (crashes mid-apply).
func (s *Store) sweepLocked() {
	entries, err := os.ReadDir(s.configDir())
	if err != nil {
		return
	}
	known := map[string]bool{}
	for n := range s.meta.Revs {
		known[filepath.Base(s.revDir(n))] = true
	}
	for _, e := range entries {
		name := e.Name()
		if name == "meta.json" || !strings.HasPrefix(name, "rev-") || known[name] {
			continue
		}
		_ = os.RemoveAll(filepath.Join(s.configDir(), name))
	}
}

func cloneRevs(in map[uint64]Revision) map[uint64]Revision {
	out := make(map[uint64]Revision, len(in)+1)
	for k, v := range in {
		out[k] = v
	}
	return out
}

func validateResources(rs []Resource) error {
	if len(rs) > MaxResources {
		return fmt.Errorf("%w: %d resources exceed the limit of %d", ErrInvalidResource, len(rs), MaxResources)
	}
	seen := map[string]bool{}
	total := 0
	for _, r := range rs {
		if !resourceName.MatchString(r.Name) || r.Name == "." || r.Name == ".." {
			return fmt.Errorf("%w: name %q", ErrInvalidResource, r.Name)
		}
		if seen[strings.ToLower(r.Name)] {
			return fmt.Errorf("%w: duplicate name %q", ErrInvalidResource, r.Name)
		}
		seen[strings.ToLower(r.Name)] = true
		if len(r.Data) > MaxResourceBytes {
			return fmt.Errorf("%w: %q is %d bytes, limit %d", ErrInvalidResource, r.Name, len(r.Data), MaxResourceBytes)
		}
		total += len(r.Data)
		if total > MaxResourceTotal {
			return fmt.Errorf("%w: resources exceed %d bytes in total", ErrInvalidResource, MaxResourceTotal)
		}
		if r.SHA256 != "" {
			sum := sha256.Sum256(r.Data)
			if !strings.EqualFold(hex.EncodeToString(sum[:]), r.SHA256) {
				return fmt.Errorf("%w: %q does not match its sha256", ErrInvalidResource, r.Name)
			}
		}
	}
	return nil
}

// ResourceRef is the prefix used inside configurations to point at a resource.
const ResourceRef = "res:"

func writeResource(path string, data []byte) error {
	// O_EXCL refuses to follow or replace anything that already exists there.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("write resource: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("write resource: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync resource: %w", err)
	}
	return f.Close()
}
