// Package library is the application's persistent state: the subscriptions and
// their nodes, the routing preferences, the user's custom rules and the
// application settings.
//
// Everything is written under one directory, readable only by the user. The
// subscription links are credentials and live in a SecretStore (the system
// keychain in the application); the node files hold server credentials and
// are protected the same way as the rest of the directory.
package library

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"eggbox/internal/configgen"
	"eggbox/internal/fsutil"
	"eggbox/internal/subscription"
)

// Kind says where a subscription's nodes come from.
type Kind string

const (
	// KindRemote is fetched from a link and refreshed from time to time.
	KindRemote Kind = "remote"
	// KindLocal was imported once (pasted links, a file) and never changes by itself.
	KindLocal Kind = "local"
)

// formatVersion is the version of library.json this code writes.
const formatVersion = 1

// DefaultIntervalHours is how often a new subscription refreshes.
const DefaultIntervalHours = 24

// Subscription is a source of nodes and what is known about it.
type Subscription struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Kind    Kind   `json:"kind"`
	Enabled bool   `json:"enabled"`
	// Host is the link's host name, for display. The rest of the link is secret.
	Host string `json:"host,omitempty"`
	// IntervalHours is the automatic refresh interval; 0 refreshes manually only.
	IntervalHours int `json:"intervalHours"`
	// IntervalAuto follows the interval the provider asks for, until the user sets one.
	IntervalAuto bool `json:"intervalAuto"`
	// UserAgent is what the download announces; empty uses the default.
	UserAgent string `json:"userAgent,omitempty"`
	// Include and Exclude are regular expressions on node names, case-insensitive.
	// A node is used when it matches Include (if set) and not Exclude (if set).
	Include string `json:"include,omitempty"`
	Exclude string `json:"exclude,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
	// UpdatedAt is when the content last changed; CheckedAt when it was last tried.
	UpdatedAt    time.Time           `json:"updatedAt,omitempty"`
	CheckedAt    time.Time           `json:"checkedAt,omitempty"`
	LastError    string              `json:"lastError,omitempty"`
	NodeCount    int                 `json:"nodeCount"`
	SkippedCount int                 `json:"skippedCount,omitempty"`
	Format       string              `json:"format,omitempty"`
	Notices      []string            `json:"notices,omitempty"`
	Usage        *subscription.Usage `json:"usage,omitempty"`
	HomePage     string              `json:"homePage,omitempty"`

	ETag         string `json:"etag,omitempty"`
	LastModified string `json:"lastModified,omitempty"`
}

// Settings are the application's own preferences.
type Settings struct {
	// Language is "" (follow the system), "en" or "zh".
	Language string `json:"language"`
	// Theme is "" (follow the system), "light" or "dark".
	Theme string `json:"theme"`
	// SelectedNode is the Key of the node chosen in the Proxy group; "" is automatic.
	SelectedNode string `json:"selectedNode"`
	// RuleMirror is "auto", "github" or "jsdelivr".
	RuleMirror string `json:"ruleMirror"`
	// RuleUpdateHours is how often rule data is checked; 0 never.
	RuleUpdateHours int `json:"ruleUpdateHours"`
	// AutoRefresh refreshes subscriptions on their intervals.
	AutoRefresh bool `json:"autoRefresh"`
	// ReapplyAfterRefresh applies refreshed nodes to a running connection.
	ReapplyAfterRefresh bool `json:"reapplyAfterRefresh"`
	// LaunchAtLogin starts the application when the user signs in.
	LaunchAtLogin bool `json:"launchAtLogin"`
	// OnboardingDone is set once the first-run introduction was completed.
	OnboardingDone bool `json:"onboardingDone"`
	// CloseToTray keeps the application running in the tray when its window closes.
	CloseToTray bool `json:"closeToTray"`
	// Notifications shows system notifications for connection changes.
	Notifications bool `json:"notifications"`
	// SortNodes is "default", "latency" or "name".
	SortNodes string `json:"sortNodes"`
}

// DefaultSettings are the settings of a fresh installation.
func DefaultSettings() Settings {
	return Settings{
		RuleMirror:          "auto",
		RuleUpdateHours:     72,
		AutoRefresh:         true,
		ReapplyAfterRefresh: true,
		CloseToTray:         true,
		Notifications:       true,
		SortNodes:           "default",
	}
}

type fileData struct {
	Version       int                    `json:"version"`
	Subscriptions []*Subscription        `json:"subscriptions"`
	Prefs         configgen.Prefs        `json:"prefs"`
	Rules         []configgen.CustomRule `json:"rules"`
	Settings      Settings               `json:"settings"`
}

type nodeFile struct {
	Nodes []subscription.Node `json:"nodes"`
}

// Library is safe for concurrent use.
type Library struct {
	dir     string
	secrets SecretStore
	now     func() time.Time

	mu        sync.RWMutex
	data      fileData
	nodes     map[string][]subscription.Node
	listeners []func()
}

// Options configure Open.
type Options struct {
	// Secrets keeps the subscription links; required.
	Secrets SecretStore
	// Now is the clock (tests).
	Now func() time.Time
}

// Open reads the library in dir, creating it when it does not exist.
func Open(dir string, opts Options) (*Library, error) {
	if dir == "" {
		return nil, errors.New("library: no directory")
	}
	if opts.Secrets == nil {
		return nil, errors.New("library: no secret store")
	}
	l := &Library{dir: dir, secrets: opts.Secrets, now: opts.Now, nodes: map[string][]subscription.Node{}}
	if l.now == nil {
		l.now = time.Now
	}
	l.data = fileData{Version: formatVersion, Prefs: configgen.DefaultPrefs(), Settings: DefaultSettings()}
	raw, err := os.ReadFile(filepath.Join(dir, "library.json"))
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, fmt.Errorf("read the library: %w", err)
	default:
		if err := json.Unmarshal(raw, &l.data); err != nil {
			return nil, fmt.Errorf("the library file is damaged: %w", err)
		}
		if l.data.Version > formatVersion {
			return nil, fmt.Errorf("the library was written by a newer version of the application (format %d)", l.data.Version)
		}
		l.data.Version = formatVersion
		l.data.Prefs, _ = l.data.Prefs.Normalize()
	}
	return l, nil
}

// OnChange registers a function called after every change, from the goroutine
// that made it and outside the library's lock.
func (l *Library) OnChange(fn func()) {
	l.mu.Lock()
	l.listeners = append(l.listeners, fn)
	l.mu.Unlock()
}

// mutate runs fn under the write lock, saves when it succeeds and notifies.
func (l *Library) mutate(fn func() error) error {
	l.mu.Lock()
	err := fn()
	if err == nil {
		err = l.saveLocked()
	}
	listeners := append([]func(){}, l.listeners...)
	l.mu.Unlock()
	if err == nil {
		for _, f := range listeners {
			f()
		}
	}
	return err
}

func (l *Library) saveLocked() error {
	if err := os.MkdirAll(l.dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(l.data, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(filepath.Join(l.dir, "library.json"), data, 0o600)
}

// ------------------------------------------------------------- subscriptions

func newID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "s" + hex.EncodeToString(b[:])
}

func secretKey(id string) string { return "subscription/" + id }

func (l *Library) find(id string) *Subscription {
	for _, s := range l.data.Subscriptions {
		if s.ID == id {
			return s
		}
	}
	return nil
}

func clone(s *Subscription) Subscription {
	c := *s
	c.Notices = append([]string(nil), s.Notices...)
	if s.Usage != nil {
		u := *s.Usage
		c.Usage = &u
	}
	return c
}

// Subscriptions lists the subscriptions in display order.
func (l *Library) Subscriptions() []Subscription {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]Subscription, 0, len(l.data.Subscriptions))
	for _, s := range l.data.Subscriptions {
		out = append(out, clone(s))
	}
	return out
}

// Subscription returns one subscription.
func (l *Library) Subscription(id string) (Subscription, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if s := l.find(id); s != nil {
		return clone(s), true
	}
	return Subscription{}, false
}

// ErrNotFound means no subscription has the id.
var ErrNotFound = errors.New("no such subscription")

// RemoteOptions are the choices made when adding a link.
type RemoteOptions struct {
	UserAgent string
	Include   string
	Exclude   string
}

// ValidateFilter checks an include/exclude expression.
func ValidateFilter(expr string) error {
	if expr == "" {
		return nil
	}
	_, err := regexp.Compile("(?i)" + expr)
	return err
}

// AddRemote adds a subscription link. The link is validated and stored as a
// secret; nothing is downloaded yet.
func (l *Library) AddRemote(name, rawURL string, o RemoteOptions) (Subscription, error) {
	rawURL = strings.TrimSpace(rawURL)
	if err := subscription.ValidateURL(rawURL); err != nil {
		return Subscription{}, err
	}
	for _, f := range []string{o.Include, o.Exclude} {
		if err := ValidateFilter(f); err != nil {
			return Subscription{}, fmt.Errorf("the node filter is not a valid regular expression: %w", err)
		}
	}
	u, _ := url.Parse(rawURL)
	sub := &Subscription{
		ID: newID(), Kind: KindRemote, Enabled: true, Host: u.Hostname(),
		Name:          strings.TrimSpace(name),
		IntervalHours: DefaultIntervalHours, IntervalAuto: true,
		UserAgent: strings.TrimSpace(o.UserAgent), Include: o.Include, Exclude: o.Exclude,
		CreatedAt: l.now().UTC(),
	}
	if sub.Name == "" {
		sub.Name = u.Hostname()
	}
	if err := l.secrets.Set(secretKey(sub.ID), rawURL); err != nil {
		return Subscription{}, fmt.Errorf("could not store the link securely: %w", err)
	}
	err := l.mutate(func() error {
		l.data.Subscriptions = append(l.data.Subscriptions, sub)
		return nil
	})
	if err != nil {
		_ = l.secrets.Delete(secretKey(sub.ID))
		return Subscription{}, err
	}
	return clone(sub), nil
}

// AddLocal adds a subscription whose nodes were imported once.
func (l *Library) AddLocal(name string, res *subscription.Result) (Subscription, error) {
	if res == nil || len(res.Nodes) == 0 {
		return Subscription{}, subscription.ErrNoNodes
	}
	sub := &Subscription{
		ID: newID(), Kind: KindLocal, Enabled: true, Name: strings.TrimSpace(name),
		CreatedAt: l.now().UTC(),
	}
	if sub.Name == "" {
		sub.Name = "Imported nodes"
	}
	applyResult(sub, res, l.now().UTC())
	if err := l.writeNodes(sub.ID, res.Nodes); err != nil {
		return Subscription{}, err
	}
	err := l.mutate(func() error {
		l.data.Subscriptions = append(l.data.Subscriptions, sub)
		l.nodes[sub.ID] = res.Nodes
		return nil
	})
	if err != nil {
		_ = os.Remove(l.nodePath(sub.ID))
		return Subscription{}, err
	}
	return clone(sub), nil
}

// Edit changes a subscription's settings. fn may change Name, Enabled,
// IntervalHours, UserAgent, Include and Exclude; everything else is kept.
func (l *Library) Edit(id string, fn func(*Subscription)) error {
	return l.mutate(func() error {
		s := l.find(id)
		if s == nil {
			return ErrNotFound
		}
		edited := clone(s)
		fn(&edited)
		for _, f := range []string{edited.Include, edited.Exclude} {
			if err := ValidateFilter(f); err != nil {
				return fmt.Errorf("the node filter is not a valid regular expression: %w", err)
			}
		}
		if edited.IntervalHours < 0 || edited.IntervalHours > 24*90 {
			return errors.New("the refresh interval is out of range")
		}
		s.Name = strings.TrimSpace(edited.Name)
		if s.Name == "" {
			s.Name = s.Host
		}
		if edited.IntervalHours != s.IntervalHours {
			s.IntervalAuto = false
		}
		s.Enabled, s.IntervalHours = edited.Enabled, edited.IntervalHours
		s.UserAgent = strings.TrimSpace(edited.UserAgent)
		s.Include, s.Exclude = edited.Include, edited.Exclude
		return nil
	})
}

// Remove deletes a subscription, its nodes and its link.
func (l *Library) Remove(id string) error {
	err := l.mutate(func() error {
		for i, s := range l.data.Subscriptions {
			if s.ID == id {
				l.data.Subscriptions = append(l.data.Subscriptions[:i], l.data.Subscriptions[i+1:]...)
				delete(l.nodes, id)
				return nil
			}
		}
		return ErrNotFound
	})
	if err != nil {
		return err
	}
	_ = os.Remove(l.nodePath(id))
	_ = l.secrets.Delete(secretKey(id))
	return nil
}

// Move places a subscription at index in the display order.
func (l *Library) Move(id string, index int) error {
	return l.mutate(func() error {
		cur := -1
		for i, s := range l.data.Subscriptions {
			if s.ID == id {
				cur = i
			}
		}
		if cur < 0 {
			return ErrNotFound
		}
		index = max(0, min(index, len(l.data.Subscriptions)-1))
		s := l.data.Subscriptions[cur]
		rest := append(append([]*Subscription{}, l.data.Subscriptions[:cur]...), l.data.Subscriptions[cur+1:]...)
		l.data.Subscriptions = append(append(append([]*Subscription{}, rest[:index]...), s), rest[index:]...)
		return nil
	})
}

// Link returns a remote subscription's link.
func (l *Library) Link(id string) (string, error) {
	if _, ok := l.Subscription(id); !ok {
		return "", ErrNotFound
	}
	return l.secrets.Get(secretKey(id))
}

// SetLink replaces a remote subscription's link.
func (l *Library) SetLink(id, rawURL string) error {
	rawURL = strings.TrimSpace(rawURL)
	if err := subscription.ValidateURL(rawURL); err != nil {
		return err
	}
	u, _ := url.Parse(rawURL)
	if _, ok := l.Subscription(id); !ok {
		return ErrNotFound
	}
	if err := l.secrets.Set(secretKey(id), rawURL); err != nil {
		return fmt.Errorf("could not store the link securely: %w", err)
	}
	return l.mutate(func() error {
		s := l.find(id)
		if s == nil {
			return ErrNotFound
		}
		s.Host, s.ETag, s.LastModified = u.Hostname(), "", ""
		return nil
	})
}

// --------------------------------------------------------------------- nodes

func (l *Library) nodePath(id string) string { return filepath.Join(l.dir, "nodes", id+".json") }

func (l *Library) writeNodes(id string, nodes []subscription.Node) error {
	data, err := json.Marshal(nodeFile{Nodes: nodes})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(l.nodePath(id)), 0o700); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(l.nodePath(id), data, 0o600)
}

// Nodes returns a subscription's nodes as last stored, before filters.
func (l *Library) Nodes(id string) ([]subscription.Node, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.nodesLocked(id)
}

func (l *Library) nodesLocked(id string) ([]subscription.Node, error) {
	if l.find(id) == nil {
		return nil, ErrNotFound
	}
	if n, ok := l.nodes[id]; ok {
		return cloneNodes(n), nil
	}
	raw, err := os.ReadFile(l.nodePath(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f nodeFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("the node file is damaged: %w", err)
	}
	l.nodes[id] = f.Nodes
	return cloneNodes(f.Nodes), nil
}

func cloneNodes(in []subscription.Node) []subscription.Node {
	out := make([]subscription.Node, len(in))
	for i, n := range in {
		out[i] = n.Clone()
	}
	return out
}

// ActiveNode is a node that will be part of the generated configuration.
type ActiveNode struct {
	Node    subscription.Node
	SubID   string
	SubName string
}

// Key identifies the node's server (see subscription.Node.Key).
func (a ActiveNode) Key() string { return a.Node.Key() }

// ActiveNodes returns the nodes of the enabled subscriptions, in subscription
// order, with each subscription's filters applied and duplicates between
// subscriptions removed. Names are unique across the result.
func (l *Library) ActiveNodes() ([]ActiveNode, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var all []subscription.Node
	var owner []*Subscription
	for _, s := range l.data.Subscriptions {
		if !s.Enabled {
			continue
		}
		nodes, err := l.nodesLocked(s.ID)
		if err != nil {
			return nil, err
		}
		for _, n := range FilterNodes(nodes, s.Include, s.Exclude) {
			all = append(all, n)
			owner = append(owner, s)
		}
	}
	// Dedupe keeps order, so map survivors back to their owners by key.
	byKey := map[string]*Subscription{}
	for i, n := range all {
		if _, ok := byKey[n.Key()]; !ok {
			byKey[n.Key()] = owner[i]
		}
	}
	kept, _ := subscription.Dedupe(all)
	out := make([]ActiveNode, 0, len(kept))
	for _, n := range kept {
		o := byKey[n.Key()]
		out = append(out, ActiveNode{Node: n, SubID: o.ID, SubName: o.Name})
	}
	return out, nil
}

// FilterNodes applies include/exclude expressions (case-insensitive) to names.
// An expression that does not compile is ignored.
func FilterNodes(nodes []subscription.Node, include, exclude string) []subscription.Node {
	var inc, exc *regexp.Regexp
	if include != "" {
		inc, _ = regexp.Compile("(?i)" + include)
	}
	if exclude != "" {
		exc, _ = regexp.Compile("(?i)" + exclude)
	}
	if inc == nil && exc == nil {
		return nodes
	}
	out := make([]subscription.Node, 0, len(nodes))
	for _, n := range nodes {
		if inc != nil && !inc.MatchString(n.Name) {
			continue
		}
		if exc != nil && exc.MatchString(n.Name) {
			continue
		}
		out = append(out, n)
	}
	return out
}

// ------------------------------------------------------ preferences and rules

// Prefs returns the routing preferences.
func (l *Library) Prefs() configgen.Prefs {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.data.Prefs
}

// SetPrefs stores routing preferences, normalized. It returns what was adjusted.
func (l *Library) SetPrefs(p configgen.Prefs) (notes []string, err error) {
	p, notes = p.Normalize()
	return notes, l.mutate(func() error {
		l.data.Prefs = p
		return nil
	})
}

// Rules returns the custom rules in match order.
func (l *Library) Rules() []configgen.CustomRule {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return append([]configgen.CustomRule(nil), l.data.Rules...)
}

// SetRules replaces the custom rules. Invalid rules are refused outright.
func (l *Library) SetRules(rules []configgen.CustomRule) error {
	clean := make([]configgen.CustomRule, 0, len(rules))
	for i, r := range rules {
		r.Value = strings.TrimSpace(r.Value)
		if err := r.Valid(); err != nil {
			return fmt.Errorf("rule %d: %w", i+1, err)
		}
		clean = append(clean, r)
	}
	return l.mutate(func() error {
		l.data.Rules = clean
		return nil
	})
}

// Settings returns the application settings.
func (l *Library) Settings() Settings {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.data.Settings
}

// UpdateSettings changes the application settings.
func (l *Library) UpdateSettings(fn func(*Settings)) error {
	return l.mutate(func() error {
		s := l.data.Settings
		fn(&s)
		switch s.Language {
		case "", "en", "zh":
		default:
			return fmt.Errorf("unsupported language %q", s.Language)
		}
		switch s.Theme {
		case "", "light", "dark":
		default:
			return fmt.Errorf("unsupported theme %q", s.Theme)
		}
		switch s.RuleMirror {
		case "auto", "github", "jsdelivr":
		default:
			return fmt.Errorf("unsupported rule mirror %q", s.RuleMirror)
		}
		switch s.SortNodes {
		case "default", "latency", "name":
		default:
			return fmt.Errorf("unsupported sort order %q", s.SortNodes)
		}
		if s.RuleUpdateHours < 0 || s.RuleUpdateHours > 24*90 {
			return errors.New("the rule data interval is out of range")
		}
		l.data.Settings = s
		return nil
	})
}

// -------------------------------------------------------------------- due

// RetryAfterFailure is how soon a failed refresh is tried again.
const RetryAfterFailure = 15 * time.Minute

// Due lists the remote subscriptions that should be refreshed now.
func (l *Library) Due() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	now := l.now()
	var due []string
	for _, s := range l.data.Subscriptions {
		if s.Kind != KindRemote || !s.Enabled || s.IntervalHours <= 0 {
			continue
		}
		wait := time.Duration(s.IntervalHours) * time.Hour
		if s.LastError != "" {
			wait = min(wait, RetryAfterFailure)
		}
		if s.CheckedAt.IsZero() || now.Sub(s.CheckedAt) >= wait {
			due = append(due, s.ID)
		}
	}
	sort.Strings(due)
	return due
}
