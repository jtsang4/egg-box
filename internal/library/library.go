// Package library is the application's persistent state: the subscriptions and
// their nodes, the routing plans and exceptions, the network preferences and
// the application settings.
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
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"eggbox/internal/configgen"
	"eggbox/internal/fsutil"
	"eggbox/internal/routing"
	"eggbox/internal/ruleurl"
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

// formatVersion is the version of library.json this code writes. Version 2
// holds the routing state (plans, exceptions, the main exit) that replaced the
// presets and the list of custom rules; see migrate. The routing state has a
// version of its own (routing.FormatVersion), upgraded where it is read, so
// that what only it changes does not keep an older application from opening
// the file.
const formatVersion = 2

// DefaultIntervalHours is how often a new subscription refreshes.
const DefaultIntervalHours = 24

// DefaultRuleUpdateHours is how often rule data is checked unless the person
// chose otherwise; community rule sets are published daily.
const DefaultRuleUpdateHours = 24

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
	// NoAuto keeps all of the subscription's nodes out of the automatic choice
	// (the fastest-node groups); they can still be chosen by hand.
	NoAuto bool `json:"noAuto,omitempty"`
	// Alias is a short name shown after a node's name when two nodes would
	// otherwise look alike ("HK 01 · Aurora").
	Alias string `json:"alias,omitempty"`
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
	// RuleMirror is "auto", "github" or "jsdelivr".
	RuleMirror string `json:"ruleMirror"`
	// RuleMirrorPrefix is a mirror the person runs or trusts, tried last
	// ("https://gh-proxy.example/"); empty uses none.
	RuleMirrorPrefix string `json:"ruleMirrorPrefix,omitempty"`
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
		RuleUpdateHours:     DefaultRuleUpdateHours,
		AutoRefresh:         true,
		ReapplyAfterRefresh: true,
		CloseToTray:         true,
		Notifications:       true,
		SortNodes:           "default",
	}
}

type fileData struct {
	Version       int             `json:"version"`
	Subscriptions []*Subscription `json:"subscriptions"`
	Prefs         configgen.Prefs `json:"prefs"`
	Routing       routing.State   `json:"routing"`
	Settings      Settings        `json:"settings"`
	// Notices wait for the interface to show them once.
	Notices []Notice `json:"notices,omitempty"`
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
	// version counts the changes made since the library was opened.
	version atomic.Uint64
}

// Version changes whenever something stored in the library did. Readers that
// keep a copy of a part of it (the routing state, which is expensive to copy)
// can tell from it whether their copy is still current.
func (l *Library) Version() uint64 { return l.version.Load() }

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
	l.data = fileData{Version: formatVersion, Prefs: configgen.DefaultPrefs(), Routing: routing.NewState(), Settings: DefaultSettings()}
	raw, err := os.ReadFile(filepath.Join(dir, "library.json"))
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, fmt.Errorf("read the library: %w", err)
	default:
		var head struct {
			Version int `json:"version"`
		}
		if err := json.Unmarshal(raw, &head); err != nil {
			return nil, fmt.Errorf("the library file is damaged: %w", err)
		}
		if head.Version > formatVersion {
			return nil, fmt.Errorf("the library was written by a newer version of the application (format %d)", head.Version)
		}
		if err := json.Unmarshal(raw, &l.data); err != nil {
			return nil, fmt.Errorf("the library file is damaged: %w", err)
		}
		if head.Version < formatVersion {
			if err := l.migrate(raw, head.Version); err != nil {
				return nil, fmt.Errorf("upgrade the library: %w", err)
			}
			// Keep what the older version wrote next to the upgraded file, once.
			backup := filepath.Join(dir, fmt.Sprintf("library.v%d.json", head.Version))
			if _, err := os.Stat(backup); errors.Is(err, os.ErrNotExist) {
				if err := fsutil.WriteFileAtomic(backup, raw, 0o600); err != nil {
					return nil, fmt.Errorf("keep a copy of the old library: %w", err)
				}
			}
			l.data.Version = formatVersion
			l.data.Prefs, _ = l.data.Prefs.Normalize()
			if err := l.saveLocked(); err != nil {
				return nil, fmt.Errorf("save the upgraded library: %w", err)
			}
		}
		l.data.Version = formatVersion
		l.data.Prefs, _ = l.data.Prefs.Normalize()
		// What the upgrade adds is worked out again on every open, so a file
		// that cannot be written now is not a reason not to open it: the next
		// change saves it.
		if l.data.Routing.Upgrade() {
			_ = l.saveLocked()
		}
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
	// A change that could not be saved is still a change in memory.
	l.version.Add(1)
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
	// The copy handed back is made under the lock: the schedule may pick the new
	// subscription up (it was never refreshed, so it is due) the moment it exists.
	var added Subscription
	err := l.mutate(func() error {
		l.data.Subscriptions = append(l.data.Subscriptions, sub)
		added = clone(sub)
		return nil
	})
	if err != nil {
		_ = l.secrets.Delete(secretKey(sub.ID))
		return Subscription{}, err
	}
	return added, nil
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
	var added Subscription
	err := l.mutate(func() error {
		l.data.Subscriptions = append(l.data.Subscriptions, sub)
		l.nodes[sub.ID] = res.Nodes
		added = clone(sub)
		return nil
	})
	if err != nil {
		_ = os.Remove(l.nodePath(sub.ID))
		return Subscription{}, err
	}
	return added, nil
}

// Errors of EditNode.
var (
	// ErrNotImported means the subscription is fetched from a link: its nodes
	// are the provider's, and the next refresh would undo an edit.
	ErrNotImported = errors.New("only imported nodes can be edited")
	// ErrNoNode means the subscription has no node with that key.
	ErrNoNode = errors.New("no such node")
	// ErrNodeTaken means another node of the same import already is that
	// server, or already has that name.
	ErrNodeTaken = errors.New("another node of this import already has that name or server")
	// ErrBadLink means what was given is not one share link of a supported
	// protocol. It says no more: what the parser says may quote the link, and
	// links carry credentials.
	ErrBadLink = errors.New("that is not a share link of a supported protocol")
)

// EditNode replaces the node whose key is key in an imported subscription
// with the one a share link describes (see subscription.EditedLink), and
// returns it as stored. The import itself and its other nodes stay. When the
// node gets another name, every choice made of it in the routing state follows
// it, in the same change.
func (l *Library) EditNode(id, key, link string) (subscription.Node, error) {
	var next subscription.Node
	err := l.mutate(func() error {
		s := l.find(id)
		if s == nil {
			return ErrNotFound
		}
		if s.Kind != KindLocal {
			return ErrNotImported
		}
		nodes, err := l.nodesLocked(id)
		if err != nil {
			return err
		}
		at := slices.IndexFunc(nodes, func(n subscription.Node) bool { return n.Key() == key })
		if at < 0 {
			return ErrNoNode
		}
		was := nodes[at]
		if next, err = subscription.EditedLink(was, link); err != nil {
			return ErrBadLink
		}
		for i, n := range nodes {
			if i != at && (n.Key() == next.Key() || strings.EqualFold(n.Name, next.Name)) {
				return ErrNodeTaken
			}
		}
		nodes[at] = next
		if err := l.writeNodes(id, nodes); err != nil {
			return err
		}
		l.nodes[id] = nodes
		if was.Name != next.Name {
			// A node is known by its import and its name (routing.NodeID): what
			// was chosen for a node of the new name that is gone now applies
			// to this one, as after a refresh that brings that name back.
			l.data.Routing.RenameNode(id, was.Name, routing.NodeID{
				Sub: id, Name: next.Name, Hash: next.Key(), Region: subscription.Region(next.Name)})
		}
		return nil
	})
	if err != nil {
		return subscription.Node{}, err
	}
	return next.Clone(), nil
}

// Edit changes a subscription's settings. fn may change Name, Alias, Enabled,
// NoAuto, IntervalHours, UserAgent, Include and Exclude; everything else is kept.
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
		s.NoAuto = edited.NoAuto
		s.Alias = cleanAlias(edited.Alias)
		s.UserAgent = strings.TrimSpace(edited.UserAgent)
		s.Include, s.Exclude = edited.Include, edited.Exclude
		return nil
	})
}

// cleanAlias keeps an alias short and on one line.
func cleanAlias(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 12 {
		s = string(r[:12])
	}
	return s
}

// Remove deletes a subscription, its nodes and its link.
func (l *Library) Remove(id string) error {
	err := l.mutate(func() error {
		for i, s := range l.data.Subscriptions {
			if s.ID == id {
				l.data.Subscriptions = append(l.data.Subscriptions[:i], l.data.Subscriptions[i+1:]...)
				delete(l.nodes, id)
				// The plan that came with it stays, as the person's own.
				if l.data.Routing.PlanByID(routing.SubPlanID(id)) != nil {
					_, _ = l.data.Routing.DetachPlan(routing.SubPlanID(id), s.Name)
				}
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
	// SubAlias is the subscription's short name, when it has one.
	SubAlias string
	// Auto is false when the subscription is kept out of the automatic choice.
	Auto bool
	// Imported is set for a node of an import, which can be edited.
	Imported bool
}

// Key identifies the node's server (see subscription.Node.Key).
func (a ActiveNode) Key() string { return a.Node.Key() }

// ID is the identity a person's choices about the node are kept under: the
// subscription and the name the provider gave it.
func (a ActiveNode) ID() routing.NodeID {
	return routing.NodeID{Sub: a.SubID, Name: a.Node.Name, Hash: a.Node.Key(), Region: subscription.Region(a.Node.Name)}
}

// ActiveNodes returns the nodes of the enabled subscriptions, in subscription
// order, with each subscription's filters applied. Where two subscriptions
// offer the same server only the first is kept. Names are the providers': two
// nodes of different subscriptions may share one, which is why a node's
// identity includes its subscription.
func (l *Library) ActiveNodes() ([]ActiveNode, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []ActiveNode
	seen := map[string]bool{}
	for _, s := range l.data.Subscriptions {
		if !s.Enabled {
			continue
		}
		nodes, err := l.nodesLocked(s.ID)
		if err != nil {
			return nil, err
		}
		for _, n := range FilterNodes(nodes, s.Include, s.Exclude) {
			if seen[n.Key()] {
				continue
			}
			seen[n.Key()] = true
			out = append(out, ActiveNode{Node: n, SubID: s.ID, SubName: s.Name, SubAlias: s.Alias, Auto: !s.NoAuto, Imported: s.Kind == KindLocal})
		}
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

// ------------------------------------------------------ preferences, routing

// Prefs returns the network preferences.
func (l *Library) Prefs() configgen.Prefs {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.data.Prefs
}

// SetPrefs stores network preferences, normalized. It returns what was adjusted.
func (l *Library) SetPrefs(p configgen.Prefs) (notes []string, err error) {
	p, notes = p.Normalize()
	return notes, l.mutate(func() error {
		l.data.Prefs = p
		return nil
	})
}

// Routing returns a copy of the routing state: plans, exceptions, the main
// exit and the choices about nodes.
func (l *Library) Routing() routing.State {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.data.Routing.Clone()
}

// UpdateRouting changes the routing state. fn works on a copy; when it returns
// an error nothing is kept.
func (l *Library) UpdateRouting(fn func(*routing.State) error) error {
	return l.mutate(func() error {
		st := l.data.Routing.Clone()
		if err := fn(&st); err != nil {
			return err
		}
		l.data.Routing = st
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
		s.RuleMirrorPrefix = strings.TrimSpace(s.RuleMirrorPrefix)
		if s.RuleMirrorPrefix != "" {
			if err := ruleurl.ValidPrefix(s.RuleMirrorPrefix); err != nil {
				return fmt.Errorf("the mirror prefix is not usable: %w", err)
			}
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
