// Package ledger records every system resource the agent creates so a crashed
// or killed agent can find and remove exactly what it owns on the next start.
//
// The contract is deliberately narrow: "only delete resources we created, and
// only if they still carry our fingerprint". Nothing here ever resets the
// route table or touches resources it did not record.
package ledger

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/jtsang4/sing-box-ui/internal/fsutil"
)

// Kind classifies a recorded resource. Drivers decide how to release each kind.
type Kind string

const (
	KindTunInterface Kind = "tun-interface"
	KindDNS          Kind = "dns"
	KindRoute        Kind = "route"
	KindFile         Kind = "file"
)

// Entry is one resource owned by the agent.
type Entry struct {
	ID string `json:"id"`
	// Kind tells the platform driver how to release the resource.
	Kind Kind `json:"kind"`
	// Key is the stable identity of the resource (interface name, SCDynamicStore
	// key, file path, ...).
	Key string `json:"key"`
	// Token is the fingerprint the agent wrote into, or expects on, the resource.
	// A release only proceeds when the live value still matches it.
	Token   string            `json:"token,omitempty"`
	Meta    map[string]string `json:"meta,omitempty"`
	Created time.Time         `json:"created"`
}

// Phase says whether the previous agent run may have left system state behind.
type Phase string

const (
	// PhaseClean means nothing is applied: every resource was released.
	PhaseClean Phase = "clean"
	// PhaseActive means resources may exist. It is persisted before the first
	// resource is created and cleared only after the last one is released.
	PhaseActive Phase = "active"
)

// Session identifies the agent run that owns the entries.
type Session struct {
	ID      string    `json:"id"`
	PID     int       `json:"pid"`
	Started time.Time `json:"started"`
}

type document struct {
	Version int     `json:"version"`
	Phase   Phase   `json:"phase"`
	Session Session `json:"session"`
	Entries []Entry `json:"entries"`
}

const formatVersion = 1

// Ledger persists the resource list crash-safely.
type Ledger struct {
	mu        sync.Mutex
	path      string
	doc       document
	recovered RecoveryInfo
}

// RecoveryInfo describes what Open found from the previous run.
type RecoveryInfo struct {
	// Needed is true when the previous run did not shut down cleanly or the file was unreadable.
	Needed bool
	// Corrupt is true when the ledger could not be parsed. Entries are then
	// unknown and drivers must fall back to sweeping well-known names.
	Corrupt bool
	// Previous is the session that left state behind.
	Previous Session
	// Entries are the resources that were still recorded.
	Entries []Entry
}

// Open loads the ledger at path, creating an empty clean one if it does not exist.
func Open(path string) (*Ledger, error) {
	l := &Ledger{path: path, doc: document{Version: formatVersion, Phase: PhaseClean}}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return l, nil
	case err != nil:
		return nil, fmt.Errorf("read ledger: %w", err)
	}

	var doc document
	if err := json.Unmarshal(raw, &doc); err != nil || doc.Version != formatVersion {
		// Keep the evidence, then start over and tell the caller to sweep.
		_ = os.Rename(path, path+".corrupt-"+time.Now().UTC().Format("20060102T150405"))
		l.recovered = RecoveryInfo{Needed: true, Corrupt: true}
		l.doc.Phase = PhaseActive
		return l, l.persistLocked()
	}
	l.doc = doc
	if doc.Phase == PhaseActive || len(doc.Entries) > 0 {
		l.recovered = RecoveryInfo{
			Needed:   true,
			Previous: doc.Session,
			Entries:  append([]Entry(nil), doc.Entries...),
		}
	}
	return l, nil
}

// Recovery reports what Open found. It does not change after Open.
func (l *Ledger) Recovery() RecoveryInfo {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.recovered
}

// BeginSession marks the ledger active under a new session. It must be called
// (and is persisted) before the first resource is created.
func (l *Ledger) BeginSession(s Session) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.doc.Session = s
	l.doc.Phase = PhaseActive
	return l.persistLocked()
}

// Add records a resource. The write is durable before Add returns, so a crash
// right after creating the resource still leaves a record.
func (l *Ledger) Add(e Entry) error {
	if e.ID == "" || e.Kind == "" || e.Key == "" {
		return errors.New("ledger: entry needs id, kind and key")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if e.Created.IsZero() {
		e.Created = time.Now().UTC()
	}
	for i := range l.doc.Entries {
		if l.doc.Entries[i].ID == e.ID {
			l.doc.Entries[i] = e
			l.doc.Phase = PhaseActive
			return l.persistLocked()
		}
	}
	l.doc.Entries = append(l.doc.Entries, e)
	l.doc.Phase = PhaseActive
	return l.persistLocked()
}

// Remove forgets a released resource.
func (l *Ledger) Remove(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	kept := l.doc.Entries[:0]
	for _, e := range l.doc.Entries {
		if e.ID != id {
			kept = append(kept, e)
		}
	}
	l.doc.Entries = kept
	return l.persistLocked()
}

// Entries returns the recorded resources, newest first (the order to release them in).
func (l *Ledger) Entries() []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := append([]Entry(nil), l.doc.Entries...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out
}

// MarkClean records that nothing is applied any more. It refuses while entries remain.
func (l *Ledger) MarkClean() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.doc.Entries) > 0 {
		return fmt.Errorf("ledger: %d resources still recorded", len(l.doc.Entries))
	}
	l.doc.Phase = PhaseClean
	l.doc.Session = Session{}
	return l.persistLocked()
}

// Phase returns the current phase.
func (l *Ledger) Phase() Phase {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.doc.Phase
}

func (l *Ledger) persistLocked() error {
	l.doc.Version = formatVersion
	raw, err := json.MarshalIndent(&l.doc, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(l.path, raw, 0o600)
}
