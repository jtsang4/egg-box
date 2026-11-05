// Package ruledata keeps the binary rule sets (geosite, geoip) the routing
// presets rely on: it downloads them from the sing-box project's rule-set
// branches or a CDN mirror of them, validates what arrived and stores it for
// the config generator to hand to the agent as resources.
//
// The application, not the privileged agent, does the downloading: the agent
// never opens an outbound connection of its own.
package ruledata

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"eggbox/internal/fsutil"
)

// MaxSize bounds one rule set; the largest published ones are a few MiB.
const MaxSize = 32 << 20

// Mirror says where a rule set can be fetched from.
type Mirror struct {
	// Name identifies the mirror in settings and in status lines.
	Name string
	// URL returns the address of a rule set file ("geosite-cn.srs") of a
	// repository ("sing-geosite"), or "" if the mirror cannot serve it.
	URL func(repo, file string) string
}

// Built-in mirrors.
var (
	// GitHub serves the files straight from the repositories' rule-set branch.
	GitHub = Mirror{Name: "github", URL: func(repo, file string) string {
		return "https://raw.githubusercontent.com/SagerNet/" + repo + "/rule-set/" + url.PathEscape(file)
	}}
	// JSDelivr is a CDN in front of the same branch; it is reachable where
	// raw.githubusercontent.com is not.
	JSDelivr = Mirror{Name: "jsdelivr", URL: func(repo, file string) string {
		return "https://cdn.jsdelivr.net/gh/SagerNet/" + repo + "@rule-set/" + url.PathEscape(file)
	}}
)

// DefaultMirrors is the order downloads try mirrors in.
func DefaultMirrors() []Mirror { return []Mirror{GitHub, JSDelivr} }

// MirrorByName finds a built-in mirror.
func MirrorByName(name string) (Mirror, bool) {
	for _, m := range DefaultMirrors() {
		if m.Name == name {
			return m, true
		}
	}
	return Mirror{}, false
}

var validID = regexp.MustCompile(`^(geosite|geoip)-[A-Za-z0-9!._\-]{1,80}$`)

// repoOf maps a rule set id to the repository that publishes it.
func repoOf(id string) (string, bool) {
	if !validID.MatchString(id) || strings.Contains(id, "..") {
		return "", false
	}
	if strings.HasPrefix(id, "geoip-") {
		return "sing-geoip", true
	}
	return "sing-geosite", true
}

func fileOf(id string) string { return id + ".srs" }

// storeName is the file a rule set is kept in; "!" is not safe in every file system.
func storeName(id string) string {
	return strings.NewReplacer("!", "not-").Replace(id) + ".srs"
}

// Meta is what is remembered about a stored rule set.
type Meta struct {
	UpdatedAt time.Time `json:"updatedAt"`
	CheckedAt time.Time `json:"checkedAt"`
	Size      int64     `json:"size"`
	SHA256    string    `json:"sha256"`
	ETag      string    `json:"etag,omitempty"`
	Source    string    `json:"source,omitempty"`
}

// Status describes one rule set for the interface.
type Status struct {
	ID      string
	Present bool
	Meta    Meta
}

// Outcome is the result of updating one rule set.
type Outcome int

const (
	// Updated: new content was downloaded and stored.
	Updated Outcome = iota
	// Unchanged: the mirror confirmed the stored copy is current.
	Unchanged
	// Skipped: the stored copy is recent enough.
	Skipped
	// Failed: no mirror delivered a valid file.
	Failed
)

// Result is what an update did to one rule set.
type Result struct {
	ID      string
	Outcome Outcome
	Source  string
	Err     error
}

// Manager downloads and stores rule sets. It is safe for concurrent use.
type Manager struct {
	dir     string
	client  *http.Client
	mirrors []Mirror
	now     func() time.Time

	mu    sync.Mutex // serializes updates and index writes
	index map[string]Meta
}

// Options configure a Manager.
type Options struct {
	// Dir is where files and the index live (created on first write).
	Dir string
	// Client is the HTTP client; the default has a timeout and no cookies.
	Client *http.Client
	// Mirrors is the order downloads try; DefaultMirrors when empty.
	Mirrors []Mirror
	// Now is the clock (tests).
	Now func() time.Time
}

// New opens the store in opts.Dir.
func New(opts Options) (*Manager, error) {
	if opts.Dir == "" {
		return nil, errors.New("ruledata: no directory")
	}
	m := &Manager{dir: opts.Dir, client: opts.Client, mirrors: opts.Mirrors, now: opts.Now, index: map[string]Meta{}}
	if m.client == nil {
		m.client = &http.Client{Timeout: 90 * time.Second}
	}
	if len(m.mirrors) == 0 {
		m.mirrors = DefaultMirrors()
	}
	if m.now == nil {
		m.now = time.Now
	}
	if data, err := os.ReadFile(filepath.Join(m.dir, "index.json")); err == nil {
		// A damaged index only costs a re-download.
		_ = json.Unmarshal(data, &m.index)
	}
	if m.index == nil {
		m.index = map[string]Meta{}
	}
	return m, nil
}

// SetMirrors changes the order downloads try mirrors in.
func (m *Manager) SetMirrors(mirrors []Mirror) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(mirrors) > 0 {
		m.mirrors = mirrors
	}
}

// Statuses reports the given rule sets (all known ones when none are given).
func (m *Manager) Statuses(ids ...string) []Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(ids) == 0 {
		for id := range m.index {
			ids = append(ids, id)
		}
		sort.Strings(ids)
	}
	out := make([]Status, 0, len(ids))
	for _, id := range ids {
		meta, ok := m.index[id]
		if ok {
			if st, err := os.Stat(filepath.Join(m.dir, storeName(id))); err != nil || st.Size() == 0 {
				ok = false
			}
		}
		out = append(out, Status{ID: id, Present: ok, Meta: meta})
	}
	return out
}

// Load returns the stored rule sets among ids and the ids that are not stored.
func (m *Manager) Load(ids ...string) (sets map[string][]byte, missing []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sets = map[string][]byte{}
	for _, id := range ids {
		data, err := os.ReadFile(filepath.Join(m.dir, storeName(id)))
		if err != nil || !validSRS(data) {
			missing = append(missing, id)
			continue
		}
		sets[id] = data
	}
	return sets, missing
}

// UpdateOptions tune an update.
type UpdateOptions struct {
	// MaxAge skips rule sets checked more recently than this; 0 checks all.
	MaxAge time.Duration
	// Force downloads even when the mirror says the copy is current.
	Force bool
}

// Update brings the rule sets up to date. It reports one Result per id and
// returns early only when ctx ends.
func (m *Manager) Update(ctx context.Context, ids []string, opts UpdateOptions) []Result {
	m.mu.Lock()
	defer m.mu.Unlock()
	results := make([]Result, 0, len(ids))
	for _, id := range ids {
		if ctx.Err() != nil {
			results = append(results, Result{ID: id, Outcome: Failed, Err: ctx.Err()})
			continue
		}
		results = append(results, m.updateOne(ctx, id, opts))
	}
	return results
}

func (m *Manager) updateOne(ctx context.Context, id string, opts UpdateOptions) Result {
	repo, ok := repoOf(id)
	if !ok {
		return Result{ID: id, Outcome: Failed, Err: fmt.Errorf("%q is not a rule set the application can download", id)}
	}
	meta, have := m.index[id]
	path := filepath.Join(m.dir, storeName(id))
	if st, err := os.Stat(path); err != nil || st.Size() == 0 {
		have, meta = false, Meta{}
	}
	if have && !opts.Force && opts.MaxAge > 0 && m.now().Sub(meta.CheckedAt) < opts.MaxAge {
		return Result{ID: id, Outcome: Skipped, Source: meta.Source}
	}

	var lastErr error
	for _, mirror := range m.mirrors {
		u := mirror.URL(repo, fileOf(id))
		if u == "" {
			continue
		}
		etag := ""
		if have && !opts.Force {
			etag = meta.ETag
		}
		data, newTag, notModified, err := m.fetch(ctx, u, etag)
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", mirror.Name, err)
			continue
		}
		now := m.now()
		if notModified {
			meta.CheckedAt = now
			m.index[id] = meta
			m.saveIndex()
			return Result{ID: id, Outcome: Unchanged, Source: meta.Source}
		}
		if err := os.MkdirAll(m.dir, 0o700); err != nil {
			return Result{ID: id, Outcome: Failed, Err: err}
		}
		if err := fsutil.WriteFileAtomic(path, data, 0o600); err != nil {
			return Result{ID: id, Outcome: Failed, Err: err}
		}
		sum := sha256.Sum256(data)
		m.index[id] = Meta{UpdatedAt: now, CheckedAt: now, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:]), ETag: newTag, Source: mirror.Name}
		m.saveIndex()
		return Result{ID: id, Outcome: Updated, Source: mirror.Name}
	}
	if lastErr == nil {
		lastErr = errors.New("no mirror offers this rule set")
	}
	return Result{ID: id, Outcome: Failed, Err: lastErr}
}

func (m *Manager) saveIndex() {
	data, err := json.MarshalIndent(m.index, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(m.dir, 0o700); err == nil {
		_ = fsutil.WriteFileAtomic(filepath.Join(m.dir, "index.json"), data, 0o600)
	}
}

func (m *Manager) fetch(ctx context.Context, rawURL, etag string) (data []byte, newTag string, notModified bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", false, err
	}
	req.Header.Set("User-Agent", "egg-box")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, "", false, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotModified && etag != "":
		return nil, etag, true, nil
	case resp.StatusCode != http.StatusOK:
		return nil, "", false, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > MaxSize {
		return nil, "", false, errors.New("the file is larger than the limit")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxSize+1))
	if err != nil {
		return nil, "", false, err
	}
	if len(body) > MaxSize {
		return nil, "", false, errors.New("the file is larger than the limit")
	}
	if !validSRS(body) {
		return nil, "", false, errors.New("the file is not a binary rule set")
	}
	return body, resp.Header.Get("ETag"), false, nil
}

// srsMagic starts every binary rule set.
var srsMagic = []byte("SRS")

func validSRS(b []byte) bool { return len(b) > 8 && bytes.HasPrefix(b, srsMagic) }
