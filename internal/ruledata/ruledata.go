// Package ruledata keeps the rule files the routing plans rely on. A file is
// identified by its canonical address (see ruleurl): it is downloaded once
// however many plans and items use it, checked on its own schedule, validated
// by reading it with rulefmt, and stored as it arrived so that it can be
// parsed and compiled again at any time.
//
// The application, not the privileged agent, does the downloading: the agent
// never opens an outbound connection of its own.
package ruledata

import (
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
	"sort"
	"strings"
	"sync"
	"time"

	"eggbox/internal/fsutil"
	"eggbox/internal/rulefmt"
	"eggbox/internal/ruleurl"
)

// MaxSize bounds one file; the largest published lists are a few MiB.
const MaxSize = 32 << 20

// maxParallel bounds simultaneous downloads.
const maxParallel = 6

var (
	// ErrTooLarge means a file is bigger than MaxSize.
	ErrTooLarge = errors.New("the file is larger than the limit")
	// ErrNotStored means a file has not been downloaded yet.
	ErrNotStored = errors.New("the rule file has not been downloaded")
)

// Mirror says how to reach a file through another host.
type Mirror struct {
	// Name identifies the mirror in settings and in status lines.
	Name string
	// URL returns the address that serves the file with this canonical
	// address, or "" if the mirror cannot serve it.
	URL func(canonical string) string
}

// Built-in mirrors.
var (
	// GitHub is the file's own address.
	GitHub = Mirror{Name: "github", URL: func(canonical string) string { return canonical }}
	// JSDelivr is a CDN in front of GitHub repositories; it is reachable where
	// raw.githubusercontent.com is not. It only serves GitHub files.
	JSDelivr = Mirror{Name: "jsdelivr", URL: ruleurl.JSDelivr}
)

// Prefix is a mirror the user runs or trusts: it serves a file at the
// prefix followed by the file's own address.
func Prefix(prefix string) Mirror {
	return Mirror{Name: "custom", URL: func(canonical string) string { return ruleurl.WithPrefix(prefix, canonical) }}
}

// DefaultMirrors is the order downloads try mirrors in when nothing else is
// said: the file's own address, then the CDN, then the custom prefix.
func DefaultMirrors(prefix string) []Mirror {
	out := []Mirror{GitHub, JSDelivr}
	if strings.TrimSpace(prefix) != "" {
		out = append(out, Prefix(prefix))
	}
	return out
}

// MirrorsFor turns the setting ("auto", "github", "jsdelivr") and the custom
// prefix into the order downloads try.
func MirrorsFor(setting, prefix string) []Mirror {
	var out []Mirror
	switch setting {
	case "github":
		out = []Mirror{GitHub}
	case "jsdelivr":
		out = []Mirror{JSDelivr}
	default:
		out = []Mirror{GitHub, JSDelivr}
	}
	if strings.TrimSpace(prefix) != "" {
		out = append(out, Prefix(prefix))
	}
	return out
}

// Meta is what is remembered about a stored file.
type Meta struct {
	URL       string        `json:"url"`
	Format    string        `json:"format"`
	UpdatedAt time.Time     `json:"updatedAt"`
	CheckedAt time.Time     `json:"checkedAt"`
	Size      int64         `json:"size"`
	SHA256    string        `json:"sha256"`
	ETag      string        `json:"etag,omitempty"`
	Mirror    string        `json:"mirror,omitempty"`
	Stats     rulefmt.Stats `json:"stats"`
	Refs      []rulefmt.Ref `json:"refs,omitempty"`
	// LastError is why the last check failed; the stored copy is still used.
	LastError string `json:"lastError,omitempty"`
}

// Status describes one file for the interface.
type Status struct {
	URL     string
	Present bool
	Meta    Meta
}

// Outcome is the result of updating one file.
type Outcome int

const (
	// Updated: new content was downloaded and stored.
	Updated Outcome = iota
	// Unchanged: the server confirmed the stored copy is current.
	Unchanged
	// Skipped: the stored copy is recent enough.
	Skipped
	// Failed: no mirror delivered a valid file.
	Failed
)

// Result is what an update did to one file.
type Result struct {
	URL     string
	Outcome Outcome
	Mirror  string
	Err     error
}

// Fetched is a file that was downloaded and read but not stored yet; the add
// dialog shows what it holds before the person decides to keep it.
type Fetched struct {
	Info   ruleurl.Info
	Data   []byte
	Parsed *rulefmt.Parsed
	Mirror string
	ETag   string
}

// Options configure a Manager.
type Options struct {
	// Dir is where files and the index live (created on first write).
	Dir string
	// Client is the HTTP client; the default has a timeout and no cookies.
	Client *http.Client
	// Mirrors is the order downloads try; DefaultMirrors("") when empty.
	Mirrors []Mirror
	// Now is the clock (tests).
	Now func() time.Time
}

// Manager downloads and stores rule files. It is safe for concurrent use.
type Manager struct {
	dir    string
	client *http.Client
	now    func() time.Time

	// files keeps a stored file from being replaced while it is read: Windows
	// refuses to rename over a file that is open (Go opens files without
	// FILE_SHARE_DELETE), so a download landing while a plan is compiled from
	// the same file failed there, and was reported as a failed update. Never
	// held while waiting for mu.
	files sync.RWMutex

	mu      sync.Mutex // guards everything below
	mirrors []Mirror
	index   map[string]Meta // by key of the canonical address
	parsed  map[string]*memo
}

type memo struct {
	sha string
	p   *rulefmt.Parsed
}

const indexVersion = 2

type indexFile struct {
	Version int             `json:"version"`
	Sources map[string]Meta `json:"sources"`
}

// New opens the store in opts.Dir.
func New(opts Options) (*Manager, error) {
	if opts.Dir == "" {
		return nil, errors.New("ruledata: no directory")
	}
	m := &Manager{dir: opts.Dir, client: opts.Client, mirrors: opts.Mirrors, now: opts.Now,
		index: map[string]Meta{}, parsed: map[string]*memo{}}
	if m.client == nil {
		m.client = &http.Client{Timeout: 90 * time.Second}
	}
	if len(m.mirrors) == 0 {
		m.mirrors = DefaultMirrors("")
	}
	if m.now == nil {
		m.now = time.Now
	}
	m.load()
	return m, nil
}

func (m *Manager) load() {
	data, err := os.ReadFile(filepath.Join(m.dir, "index.json"))
	if err != nil {
		return
	}
	var f indexFile
	if json.Unmarshal(data, &f) != nil || f.Version != indexVersion {
		// The index of an earlier version names files in another layout;
		// everything in it can be downloaded again.
		m.discardLegacy()
		return
	}
	if f.Sources != nil {
		m.index = f.Sources
	}
}

// discardLegacy removes what earlier versions stored; only caches live here.
func (m *Manager) discardLegacy() {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.Type().IsRegular() && (strings.HasSuffix(e.Name(), ".srs") || e.Name() == "index.json") {
			_ = os.Remove(filepath.Join(m.dir, e.Name()))
		}
	}
}

// SetMirrors changes the order downloads try mirrors in.
func (m *Manager) SetMirrors(mirrors []Mirror) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(mirrors) > 0 {
		m.mirrors = mirrors
	}
}

func keyOf(canonical string) string {
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:8])
}

func (m *Manager) path(key string) string { return filepath.Join(m.dir, "raw", key) }

// Statuses reports the given files (all stored ones when none are given).
func (m *Manager) Statuses(urls ...string) []Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(urls) == 0 {
		for _, meta := range m.index {
			urls = append(urls, meta.URL)
		}
		sort.Strings(urls)
	}
	out := make([]Status, 0, len(urls))
	for _, u := range urls {
		meta, ok := m.index[keyOf(u)]
		if ok {
			if st, err := os.Stat(m.path(keyOf(u))); err != nil || st.Size() == 0 {
				ok = false
			}
		}
		out = append(out, Status{URL: u, Present: ok, Meta: meta})
	}
	return out
}

// Meta returns what is known about a stored file.
func (m *Manager) Meta(canonical string) (Meta, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	meta, ok := m.index[keyOf(canonical)]
	return meta, ok
}

// Data returns the stored bytes of a file.
func (m *Manager) Data(canonical string) ([]byte, error) {
	m.mu.Lock()
	_, ok := m.index[keyOf(canonical)]
	m.mu.Unlock()
	if !ok {
		return nil, ErrNotStored
	}
	data, err := m.read(keyOf(canonical))
	if err != nil {
		return nil, ErrNotStored
	}
	return data, nil
}

// Parsed reads a stored file. The result is kept until the file changes, so
// compiling a plan again does not read every list again; callers must not
// modify it.
func (m *Manager) Parsed(canonical string) (*rulefmt.Parsed, error) {
	key := keyOf(canonical)
	m.mu.Lock()
	meta, ok := m.index[key]
	if c := m.parsed[key]; ok && c != nil && c.sha == meta.SHA256 {
		m.mu.Unlock()
		return c.p, nil
	}
	m.mu.Unlock()
	if !ok {
		return nil, ErrNotStored
	}
	data, err := m.read(key)
	if err != nil {
		return nil, ErrNotStored
	}
	p, err := rulefmt.Parse(data, rulefmt.Hint{Source: canonical})
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.parsed[key] = &memo{sha: meta.SHA256, p: p}
	m.mu.Unlock()
	return p, nil
}

// read reads a stored file, which is not replaced meanwhile.
func (m *Manager) read(key string) ([]byte, error) {
	m.files.RLock()
	defer m.files.RUnlock()
	return os.ReadFile(m.path(key))
}

// Deps lists the files a stored file refers to by name: a Clash list that
// says GEOSITE,google needs the geosite file for google. They are stored by
// Update together with the file.
func (m *Manager) Deps(canonical string) []string {
	meta, ok := m.Meta(canonical)
	if !ok {
		return nil
	}
	return refURLs(meta.Refs)
}

func refURLs(refs []rulefmt.Ref) []string {
	var out []string
	seen := map[string]bool{}
	for _, r := range refs {
		var u string
		switch r.Kind {
		case rulefmt.RefGeoSite:
			u = ruleurl.GeoSite(r.Name)
		case rulefmt.RefGeoIP:
			u = ruleurl.GeoIP(r.Name)
		case rulefmt.RefASN:
			u = ruleurl.ASN(r.Name)
		}
		if u != "" && !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	return out
}

// UpdateOptions tune an update.
type UpdateOptions struct {
	// MaxAge skips files checked more recently than this; 0 checks all.
	MaxAge time.Duration
	// Force downloads even when the server says the copy is current.
	Force bool
}

// Update brings the files up to date, up to six at a time, and the files they
// refer to as well. It reports one Result per file, in the order given, then
// one per file reached through a reference, and returns early only when ctx
// ends.
func (m *Manager) Update(ctx context.Context, urls []string, opts UpdateOptions) []Result {
	results := m.updateAll(ctx, dedupe(urls), opts)
	// Files named by references (one level of GEOSITE/GEOIP/IP-ASN lines;
	// the datasets themselves do not refer to others).
	var deps []string
	seen := map[string]bool{}
	for _, u := range urls {
		seen[u] = true
	}
	for _, r := range results {
		if r.Outcome == Failed {
			continue
		}
		for _, d := range m.Deps(r.URL) {
			if !seen[d] {
				seen[d] = true
				deps = append(deps, d)
			}
		}
	}
	if len(deps) > 0 {
		results = append(results, m.updateAll(ctx, deps, opts)...)
	}
	return results
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, u := range in {
		if !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	return out
}

func (m *Manager) updateAll(ctx context.Context, urls []string, opts UpdateOptions) []Result {
	results := make([]Result, len(urls))
	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup
	for i, u := range urls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ctx.Err() != nil {
				results[i] = Result{URL: u, Outcome: Failed, Err: ctx.Err()}
				return
			}
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[i] = Result{URL: u, Outcome: Failed, Err: ctx.Err()}
				return
			}
			results[i] = m.updateOne(ctx, u, opts)
		}()
	}
	wg.Wait()
	return results
}

func (m *Manager) mirrorList() []Mirror {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Mirror(nil), m.mirrors...)
}

// candidates are the addresses to try for a file, in order, without repeats.
// A file no mirror can serve is tried at its own address.
func candidates(mirrors []Mirror, canonical string) []candidate {
	var out []candidate
	seen := map[string]bool{}
	for _, mir := range mirrors {
		if u := mir.URL(canonical); u != "" && !seen[u] {
			seen[u] = true
			out = append(out, candidate{mir.Name, u})
		}
	}
	if len(out) == 0 {
		out = append(out, candidate{GitHub.Name, canonical})
	}
	return out
}

type candidate struct{ mirror, url string }

func (m *Manager) updateOne(ctx context.Context, canonical string, opts UpdateOptions) Result {
	info, err := ruleurl.Canonical(canonical)
	if err != nil || info.URL != canonical {
		return Result{URL: canonical, Outcome: Failed, Err: fmt.Errorf("%q is not a canonical rule file address", ruleurl.Display(canonical))}
	}
	key := keyOf(canonical)
	m.mu.Lock()
	meta, have := m.index[key]
	m.mu.Unlock()
	if have {
		if st, err := os.Stat(m.path(key)); err != nil || st.Size() == 0 {
			have, meta = false, Meta{}
		}
	}
	if have && !opts.Force && opts.MaxAge > 0 && m.now().Sub(meta.CheckedAt) < opts.MaxAge {
		return Result{URL: canonical, Outcome: Skipped, Mirror: meta.Mirror}
	}

	var failure error
	for _, c := range candidates(m.mirrorList(), canonical) {
		etag := ""
		if have && !opts.Force {
			etag = meta.ETag
		}
		data, newTag, notModified, err := m.get(ctx, c.url, etag)
		if err != nil {
			failure = worse(failure, fmt.Errorf("%s: %w", c.mirror, err))
			continue
		}
		now := m.now()
		if notModified {
			meta.CheckedAt, meta.LastError, meta.Mirror = now, "", c.mirror
			m.mu.Lock()
			m.index[key] = meta
			m.saveIndexLocked()
			m.mu.Unlock()
			return Result{URL: canonical, Outcome: Unchanged, Mirror: c.mirror}
		}
		parsed, err := rulefmt.Parse(data, rulefmt.Hint{Source: canonical})
		if err != nil {
			failure = worse(failure, fmt.Errorf("%s: %w", c.mirror, err))
			continue
		}
		sum := sha256.Sum256(data)
		next := Meta{URL: canonical, Format: string(parsed.Format), UpdatedAt: now, CheckedAt: now, Size: int64(len(data)),
			SHA256: hex.EncodeToString(sum[:]), ETag: newTag, Mirror: c.mirror, Stats: parsed.Stats, Refs: parsed.Refs}
		if have && next.SHA256 == meta.SHA256 {
			// The same bytes again (a server without ETags): nothing changed.
			meta.CheckedAt, meta.LastError, meta.ETag, meta.Mirror = now, "", newTag, c.mirror
			m.mu.Lock()
			m.index[key] = meta
			m.saveIndexLocked()
			m.mu.Unlock()
			return Result{URL: canonical, Outcome: Unchanged, Mirror: c.mirror}
		}
		if err := m.store(key, data, next, parsed); err != nil {
			return Result{URL: canonical, Outcome: Failed, Err: err}
		}
		return Result{URL: canonical, Outcome: Updated, Mirror: c.mirror}
	}
	if failure == nil {
		failure = errors.New("no mirror offers this file")
	}
	if have {
		// Keep using the stored copy; remember why it could not be refreshed.
		meta.LastError = Describe(failure)
		m.mu.Lock()
		m.index[key] = meta
		m.saveIndexLocked()
		m.mu.Unlock()
	}
	return Result{URL: canonical, Outcome: Failed, Err: failure}
}

func (m *Manager) store(key string, data []byte, meta Meta, parsed *rulefmt.Parsed) error {
	if err := os.MkdirAll(filepath.Join(m.dir, "raw"), 0o700); err != nil {
		return err
	}
	m.files.Lock()
	err := fsutil.WriteFileAtomic(m.path(key), data, 0o600)
	m.files.Unlock()
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.index[key] = meta
	if parsed != nil {
		m.parsed[key] = &memo{sha: meta.SHA256, p: parsed}
	}
	m.saveIndexLocked()
	return nil
}

// ParseError means a file was downloaded but holds no rules the client can
// read. Body is what came, so a caller can tell what it was instead (a
// subscription pasted as a rule list).
type ParseError struct {
	Err  error
	Body []byte
}

func (e *ParseError) Error() string { return e.Err.Error() }
func (e *ParseError) Unwrap() error { return e.Err }

// Fetch downloads a file the person pasted an address of and reads it, so
// the interface can show what it holds before it is kept. Nothing is stored.
func (m *Manager) Fetch(ctx context.Context, raw string) (*Fetched, error) {
	info, err := ruleurl.Canonical(raw)
	if err != nil {
		return nil, err
	}
	var failure error
	var unreadable []byte
	for _, c := range candidates(m.mirrorList(), info.URL) {
		data, tag, _, err := m.get(ctx, c.url, "")
		if err != nil {
			failure = worse(failure, fmt.Errorf("%s: %w", c.mirror, err))
			continue
		}
		parsed, err := rulefmt.Parse(data, rulefmt.Hint{Source: info.URL})
		if err != nil {
			failure = worse(failure, fmt.Errorf("%s: %w", c.mirror, err))
			unreadable = data
			continue
		}
		return &Fetched{Info: info, Data: data, Parsed: parsed, Mirror: c.mirror, ETag: tag}, nil
	}
	if failure == nil {
		failure = errors.New("no mirror offers this file")
	}
	if unreadable != nil && rank(failure) == 3 {
		return nil, &ParseError{Err: failure, Body: unreadable}
	}
	return nil, failure
}

// Keep stores a fetched file.
func (m *Manager) Keep(f *Fetched) error {
	now := m.now()
	sum := sha256.Sum256(f.Data)
	meta := Meta{URL: f.Info.URL, Format: string(f.Parsed.Format), UpdatedAt: now, CheckedAt: now, Size: int64(len(f.Data)),
		SHA256: hex.EncodeToString(sum[:]), ETag: f.ETag, Mirror: f.Mirror, Stats: f.Parsed.Stats, Refs: f.Parsed.Refs}
	return m.store(keyOf(f.Info.URL), f.Data, meta, f.Parsed)
}

// Prune forgets every stored file not in keep.
func (m *Manager) Prune(keep map[string]bool) (removed int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, meta := range m.index {
		if keep[meta.URL] {
			continue
		}
		m.files.Lock()
		_ = os.Remove(m.path(key))
		m.files.Unlock()
		delete(m.index, key)
		delete(m.parsed, key)
		removed++
	}
	if removed > 0 {
		m.saveIndexLocked()
	}
	return removed
}

func (m *Manager) saveIndexLocked() {
	data, err := json.MarshalIndent(indexFile{Version: indexVersion, Sources: m.index}, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(m.dir, 0o700); err == nil {
		_ = fsutil.WriteFileAtomic(filepath.Join(m.dir, "index.json"), data, 0o600)
	}
}

// get downloads one address. An address may carry a token in its query; the
// errors returned never repeat it.
func (m *Manager) get(ctx context.Context, rawURL, etag string) (data []byte, newTag string, notModified bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", false, errors.New("the address cannot be requested")
	}
	req.Header.Set("User-Agent", "egg-box")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, "", false, cleanErr(err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotModified && etag != "":
		return nil, etag, true, nil
	case resp.StatusCode != http.StatusOK:
		return nil, "", false, statusError(resp.StatusCode)
	}
	if resp.ContentLength > MaxSize {
		return nil, "", false, ErrTooLarge
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxSize+1))
	if err != nil {
		return nil, "", false, cleanErr(err)
	}
	if len(body) > MaxSize {
		return nil, "", false, ErrTooLarge
	}
	return body, resp.Header.Get("ETag"), false, nil
}

// StatusError is an HTTP answer other than 200.
type StatusError int

func (e StatusError) Error() string { return fmt.Sprintf("HTTP %d", int(e)) }

func statusError(code int) error { return StatusError(code) }

// cleanErr removes the address from a transport error.
func cleanErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// worse keeps the more informative of two failures: what the file is (a
// format nobody can read) says more than what the server answered, which says
// more than that the network failed.
func worse(a, b error) error {
	if a == nil {
		return b
	}
	if rank(b) >= rank(a) {
		return b
	}
	return a
}

func rank(err error) int {
	var se StatusError
	switch {
	case errors.Is(err, rulefmt.ErrMRS), errors.Is(err, rulefmt.ErrEmpty), errors.Is(err, rulefmt.ErrUnrecognized),
		errors.Is(err, rulefmt.ErrMalformed), errors.Is(err, ErrTooLarge):
		return 3
	case errors.As(err, &se):
		return 2
	}
	return 1
}

// Describe is a short reason for a failure, safe to show and to keep in
// reports: errors here never repeat the address of the file.
func Describe(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
