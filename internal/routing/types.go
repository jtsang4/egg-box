// Package routing is the model behind the routing page: plans made of items,
// where each item sends one class of traffic somewhere, and the user's own
// exceptions that come before all of them.
//
// Nothing here touches the network, the disk or the core. The package decides
// what the effective plan is (a plan that follows a subscription or ships with
// the application, with the user's choices laid over it), how a choice survives
// a plan switch or a refresh, and how a node a person pinned is found again.
// Turning a plan into a configuration is configgen's job; keeping it is the
// library's.
package routing

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// FormatVersion is the version of the persisted State. Migrations only ever
// move forward and are safe to run twice.
const FormatVersion = 1

// NewID returns a short random identifier with a prefix ("x", "i", "p"...).
func NewID(prefix string) string {
	var b [5]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}

// ----------------------------------------------------------------- node id

// NodeID names a node in a way that survives its subscription being
// refreshed: the subscription and the name the provider gives it. The hash
// of its settings and the last recognised region are fallbacks.
type NodeID struct {
	Sub    string `json:"sub"`
	Name   string `json:"name"`
	Hash   string `json:"hash,omitempty"`
	Region string `json:"region,omitempty"`
}

// IsZero reports whether the identity names nothing.
func (n NodeID) IsZero() bool { return n.Sub == "" && n.Name == "" && n.Hash == "" }

// Key is what two identities of the same node have in common.
func (n NodeID) Key() string { return n.Sub + "\x00" + n.Name }

// Same reports whether a and b name the same node, by subscription and name,
// or by settings when both know them.
func (n NodeID) Same(o NodeID) bool {
	if n.Key() == o.Key() {
		return true
	}
	return n.Hash != "" && n.Hash == o.Hash && n.Sub == o.Sub
}

// --------------------------------------------------------------------- dest

// DestKind is where a class of traffic goes.
type DestKind string

const (
	// DestFollow sends traffic where the main exit does.
	DestFollow DestKind = "follow"
	DestDirect DestKind = "direct"
	DestReject DestKind = "reject"
	// DestAuto is the fastest node of all: only the main exit uses it.
	DestAuto DestKind = "auto"
	// DestRegion is the fastest node of a region.
	DestRegion DestKind = "region"
	// DestNode is one node, with a stand-in when it cannot be used.
	DestNode DestKind = "node"
)

// Dest is a destination.
type Dest struct {
	Kind   DestKind `json:"kind"`
	Region string   `json:"region,omitempty"`
	Node   *NodeID  `json:"node,omitempty"`
	// NoFallback keeps a pinned node from being replaced by the fastest node
	// of its region while it is unhealthy.
	NoFallback bool `json:"noFallback,omitempty"`
}

// Constructors for the common ones.
func Follow() Dest { return Dest{Kind: DestFollow} }
func Direct() Dest { return Dest{Kind: DestDirect} }
func Reject() Dest { return Dest{Kind: DestReject} }
func Auto() Dest   { return Dest{Kind: DestAuto} }

// ToRegion is the fastest node of a region ("US").
func ToRegion(code string) Dest { return Dest{Kind: DestRegion, Region: strings.ToUpper(code)} }

// ToNode pins a node.
func ToNode(id NodeID) Dest { return Dest{Kind: DestNode, Node: &id} }

// Valid reports why a destination cannot be used. The main exit may be
// automatic but cannot follow itself, go direct or be blocked.
func (d Dest) Valid(main bool) error {
	switch d.Kind {
	case DestFollow:
		if main {
			return errors.New("the main exit cannot follow itself")
		}
	case DestDirect, DestReject:
		if main {
			return errors.New("the main exit must be a proxy")
		}
	case DestAuto:
		if !main {
			return errors.New("only the main exit can be automatic")
		}
	case DestRegion:
		if len(d.Region) != 2 {
			return fmt.Errorf("%q is not a region code", d.Region)
		}
	case DestNode:
		if d.Node == nil || d.Node.IsZero() {
			return errors.New("no node was chosen")
		}
	default:
		return fmt.Errorf("unknown destination %q", d.Kind)
	}
	return nil
}

// Equal compares destinations by meaning: a node by subscription and name.
func (d Dest) Equal(o Dest) bool {
	if d.Kind != o.Kind || d.Region != o.Region || d.NoFallback != o.NoFallback {
		return false
	}
	if d.Kind != DestNode {
		return true
	}
	if d.Node == nil || o.Node == nil {
		return d.Node == o.Node
	}
	return d.Node.Same(*o.Node)
}

// Clone copies a destination.
func (d Dest) Clone() Dest {
	if d.Node != nil {
		n := *d.Node
		d.Node = &n
	}
	return d
}

// Passes reports whether traffic sent there leaves through a proxy node
// chosen at runtime (so the choice can change without a new configuration).
func (d Dest) Passes() bool { return d.Kind != DestReject }

// ------------------------------------------------------------------ entries

// EntryKind is what a hand-written entry matches.
type EntryKind string

const (
	EntryDomain        EntryKind = "domain"
	EntryDomainSuffix  EntryKind = "domain_suffix"
	EntryDomainKeyword EntryKind = "domain_keyword"
	EntryDomainRegex   EntryKind = "domain_regex"
	EntryIPCIDR        EntryKind = "ip_cidr"
	EntryPort          EntryKind = "port"
	EntryProcessName   EntryKind = "process_name"
	EntryProcessPath   EntryKind = "process_path"
)

// EntryKinds lists the kinds in the order the interface offers them.
var EntryKinds = []EntryKind{
	EntryDomainSuffix, EntryDomain, EntryDomainKeyword, EntryDomainRegex,
	EntryIPCIDR, EntryPort, EntryProcessName, EntryProcessPath,
}

// IsHost reports whether the kind matches a host name (so it takes part in
// DNS routing and in the domain half of a plan).
func (k EntryKind) IsHost() bool {
	switch k {
	case EntryDomain, EntryDomainSuffix, EntryDomainKeyword, EntryDomainRegex:
		return true
	}
	return false
}

// Entry is one hand-written rule.
type Entry struct {
	Kind  EntryKind `json:"kind"`
	Value string    `json:"value"`
}

// ErrBadEntry is what Valid reports for an entry that cannot be used. The
// text of the error says why, in English; the interface words it itself.
var ErrBadEntry = errors.New("not a usable entry")

// badEntry is a reason an entry cannot be used; it is an ErrBadEntry.
type badEntry string

func (e badEntry) Error() string        { return string(e) }
func (e badEntry) Is(target error) bool { return target == ErrBadEntry }

func badEntryf(format string, args ...any) error { return badEntry(fmt.Sprintf(format, args...)) }

// Valid reports why an entry cannot be used.
func (e Entry) Valid() error {
	v := strings.TrimSpace(e.Value)
	if v == "" {
		return badEntry("the entry has no value")
	}
	switch e.Kind {
	case EntryDomain, EntryDomainSuffix:
		n := e.Normalize().Value
		if n == "" {
			return badEntryf("%q is not a domain", v)
		}
		if !validHostName(n) {
			return badEntryf("%q is not a domain name", v)
		}
	case EntryDomainKeyword:
		if !validKeyword(v) {
			return badEntryf("%q is not text that can be part of a domain name", v)
		}
	case EntryProcessName:
		if strings.ContainsAny(v, "/\\") || strings.ContainsFunc(v, unicode.IsControl) {
			return badEntryf("%q is not the name of a program (a path is a program path)", v)
		}
	case EntryProcessPath:
		if strings.ContainsFunc(v, unicode.IsControl) {
			return badEntry("the path must be on one line")
		}
	case EntryDomainRegex:
		if _, err := regexp.Compile(v); err != nil {
			return badEntryf("%q is not a valid regular expression", v)
		}
	case EntryIPCIDR:
		if _, ok := NormalizeCIDR(v); !ok {
			return badEntryf("%q is not an IP address or CIDR range", v)
		}
	case EntryPort:
		if _, ok := NormalizePort(v); !ok {
			return badEntryf("%q is not a port or port range", v)
		}
	default:
		return badEntryf("unknown entry type %q", e.Kind)
	}
	return nil
}

// Normalize returns the entry in the form the generator expects: trimmed,
// lower-cased where case does not matter, domains in the ASCII form the
// engine sees names in and without a final dot, suffixes without wildcards,
// addresses as prefixes, port ranges as "lo:hi".
func (e Entry) Normalize() Entry {
	v := strings.TrimSpace(e.Value)
	switch e.Kind {
	case EntryDomain, EntryDomainKeyword:
		v = strings.ToLower(v)
		if e.Kind == EntryDomain {
			v = asciiHost(v)
		}
	case EntryDomainSuffix:
		v = strings.ToLower(v)
		v = strings.TrimPrefix(v, "+.")
		v = strings.TrimPrefix(v, "*.")
		v = strings.TrimPrefix(v, ".")
		v = asciiHost(v)
	case EntryIPCIDR:
		if p, ok := NormalizeCIDR(v); ok {
			v = p
		}
	case EntryPort:
		if p, ok := NormalizePort(v); ok {
			v = p
		}
	}
	return Entry{Kind: e.Kind, Value: v}
}

// NormalizeCIDR turns "1.2.3.4" or "1.2.3.0/24" into a prefix string.
func NormalizeCIDR(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return "", false
		}
		return p.Masked().String(), true
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return "", false
	}
	return netip.PrefixFrom(a, a.BitLen()).String(), true
}

// NormalizePort turns "443", "8000-9000" or "8000:9000" into "443" or "8000:9000".
func NormalizePort(s string) (string, bool) {
	s = strings.TrimSpace(s)
	lo, hi, ranged := strings.Cut(s, ":")
	if !ranged {
		lo, hi, ranged = strings.Cut(s, "-")
	}
	a, err := strconv.Atoi(strings.TrimSpace(lo))
	if err != nil || a < 1 || a > 65535 {
		return "", false
	}
	if !ranged {
		return strconv.Itoa(a), true
	}
	b, err := strconv.Atoi(strings.TrimSpace(hi))
	if err != nil || b < a || b > 65535 {
		return "", false
	}
	if a == b {
		return strconv.Itoa(a), true
	}
	return fmt.Sprintf("%d:%d", a, b), true
}

// Exception is a rule of the user's own: global, ahead of every plan, with a
// destination of its own.
type Exception struct {
	ID      string    `json:"id"`
	Kind    EntryKind `json:"kind"`
	Value   string    `json:"value"`
	Dest    Dest      `json:"dest"`
	Enabled bool      `json:"enabled"`
}

// Entry is the exception's match.
func (x Exception) Entry() Entry { return Entry{Kind: x.Kind, Value: x.Value} }

// Valid reports why an exception cannot be used.
func (x Exception) Valid() error {
	if err := x.Entry().Valid(); err != nil {
		return err
	}
	return x.Dest.Valid(false)
}

// -------------------------------------------------------- sources and items

// SourceKind says where a source's rules come from.
type SourceKind string

const (
	// SourceURL is a rule-set file somewhere on the internet, in any format.
	SourceURL SourceKind = "url"
	// SourceInline is a handful of entries written by hand.
	SourceInline SourceKind = "inline"
)

// Source is the rule data behind (part of) an item.
type Source struct {
	ID   string     `json:"id"`
	Kind SourceKind `json:"kind"`
	// URL is the canonical address of a SourceURL, never a mirror's.
	URL string `json:"url,omitempty"`
	// Name and Project are for display ("geosite: netflix", "MetaCubeX").
	Name    string  `json:"name,omitempty"`
	Project string  `json:"project,omitempty"`
	Entries []Entry `json:"entries,omitempty"`
}

// URLSource makes a source for a file.
func URLSource(url, name, project string) Source {
	return Source{ID: sourceID(url), Kind: SourceURL, URL: url, Name: name, Project: project}
}

// InlineSource makes a source of hand-written entries.
func InlineSource(entries []Entry) Source {
	return Source{ID: NewID("n"), Kind: SourceInline, Name: "", Entries: entries}
}

func sourceID(url string) string {
	h := uint64(14695981039346656037)
	for i := 0; i < len(url); i++ {
		h ^= uint64(url[i])
		h *= 1099511628211
	}
	return "u" + strconv.FormatUint(h, 36)
}

// The notes an import leaves on an item (Item.Notes).
const (
	// NoteReordered: the rules the provider's plan sent to this item were not
	// all together, other items' rules were between them. The item is made
	// once, in the place of its first rules, so a few of its rules are now
	// asked earlier or later than the provider had them.
	NoteReordered = "reordered"
	// NoteMultiRegionAuto: the provider sent this item through a group that
	// picks the fastest of nodes in several regions, which no single
	// destination says. The item follows the main exit instead.
	NoteMultiRegionAuto = "multi_region_auto"
)

// Item is one class of traffic: a name, where its rules come from and where
// it goes.
type Item struct {
	ID string `json:"id"`
	// Key marks the items the application ships ("ai", "cn"...): their names
	// are translated.
	Key     string   `json:"key,omitempty"`
	Name    string   `json:"name"`
	Icon    string   `json:"icon,omitempty"`
	Sources []Source `json:"sources,omitempty"`
	Dest    Dest     `json:"dest"`
	Enabled bool     `json:"enabled"`
	// Aliases are other names the item is known by, so a choice carries over
	// to a plan that names it differently.
	Aliases []string `json:"aliases,omitempty"`
	// Catalog is the catalog entry the item was made from.
	Catalog string `json:"catalog,omitempty"`
	// Notes are codes (the Note constants) for things an import wants the
	// person to know about this item; the item's page says them.
	Notes []string `json:"notes,omitempty"`
	// Anchor, on an item the user added to a followed plan, is the name of the
	// item it goes before ("" goes before the fallback).
	Anchor string `json:"anchor,omitempty"`
	// Domestic marks an item made of services in the country the person is
	// in: their names are looked up with the domestic DNS server, whatever the
	// item's destination is at the moment. The choice cannot follow the
	// destination, because a running core can switch where traffic goes but not
	// which DNS server answers; and so changing the destination between direct
	// and a node never needs a new configuration.
	Domestic bool `json:"domestic,omitempty"`
}

// Clone copies an item.
func (it Item) Clone() Item {
	it.Sources = cloneSources(it.Sources)
	it.Aliases = append([]string(nil), it.Aliases...)
	it.Notes = append([]string(nil), it.Notes...)
	it.Dest = it.Dest.Clone()
	return it
}

func cloneSources(in []Source) []Source {
	if in == nil {
		return nil
	}
	out := append([]Source(nil), in...)
	for i, s := range out {
		out[i].Entries = append([]Entry(nil), s.Entries...)
	}
	return out
}

// Keys are the normalized names the item answers to.
func (it Item) Keys() []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if k := NormName(s); k != "" && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	add(it.Name)
	for _, a := range it.Aliases {
		add(a)
	}
	if names, ok := builtinNames[it.Key]; ok {
		for _, n := range names {
			add(n)
		}
	}
	return out
}

// NormName compares item names the way people do: the emoji and spaces at the
// front do not count, nor does case.
func NormName(s string) string {
	s = strings.TrimLeftFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// ------------------------------------------------------------------- plans

// PlanKind says who maintains a plan.
type PlanKind string

const (
	// PlanBuiltin ships with the application.
	PlanBuiltin PlanKind = "builtin"
	// PlanSubscription came with a subscription and follows it.
	PlanSubscription PlanKind = "subscription"
	// PlanMine is the user's own, free to edit.
	PlanMine PlanKind = "mine"
)

// BuiltinID is the identifier of the plan the application ships.
const BuiltinID = "builtin"

// SubPlanID is the identifier of the plan a subscription brought.
func SubPlanID(sub string) string { return "sub:" + sub }

// Plan is an ordered list of items and what happens to the rest.
type Plan struct {
	ID    string   `json:"id"`
	Kind  PlanKind `json:"kind"`
	Name  string   `json:"name"`
	SubID string   `json:"subId,omitempty"`
	Items []Item   `json:"items,omitempty"`
	// Fallback is where traffic no item claims goes.
	Fallback Dest `json:"fallback"`
	// Main is the main exit a subscription's plan starts with, applied when the
	// plan is adopted for the first time.
	Main *Dest `json:"main,omitempty"`
	// Origin is a note on where a plan the user keeps came from.
	Origin    string    `json:"origin,omitempty"`
	UpdatedAt time.Time `json:"updatedAt,omitempty"`
}

// Clone copies a plan.
func (p *Plan) Clone() *Plan {
	if p == nil {
		return nil
	}
	c := *p
	c.Items = make([]Item, len(p.Items))
	for i, it := range p.Items {
		c.Items[i] = it.Clone()
	}
	c.Fallback = p.Fallback.Clone()
	if p.Main != nil {
		m := p.Main.Clone()
		c.Main = &m
	}
	return &c
}

// Followed reports whether the plan is maintained elsewhere, so its own items
// cannot be removed or moved.
func (p *Plan) Followed() bool { return p.Kind != PlanMine }

// Overlay is what the user laid over a plan that is maintained elsewhere:
// choices for its items, items of their own, and the fallback.
type Overlay struct {
	// Dest and Enabled are keyed by the normalized name of the plan's item; an
	// item without an entry is as the plan has it.
	Dest     map[string]Dest `json:"dest,omitempty"`
	Enabled  map[string]bool `json:"enabled,omitempty"`
	Items    []Item          `json:"items,omitempty"`
	Fallback *Dest           `json:"fallback,omitempty"`
	// Extra are sources the user added to an item the plan itself owns (a
	// domain the plan's AI rule set misses), keyed like Dest. They stay when
	// the plan is refreshed and follow the item to a plan that has one of the
	// same name.
	Extra map[string][]Source `json:"extra,omitempty"`
	// Seen are the names the plan had when it was last looked at, to tell the
	// person what a refresh added or removed.
	Seen []string `json:"seen,omitempty"`
}

// Clone copies an overlay.
func (o *Overlay) Clone() *Overlay {
	if o == nil {
		return &Overlay{}
	}
	c := &Overlay{Seen: append([]string(nil), o.Seen...)}
	if len(o.Dest) > 0 {
		c.Dest = make(map[string]Dest, len(o.Dest))
		for k, v := range o.Dest {
			c.Dest[k] = v.Clone()
		}
	}
	if len(o.Enabled) > 0 {
		c.Enabled = make(map[string]bool, len(o.Enabled))
		for k, v := range o.Enabled {
			c.Enabled[k] = v
		}
	}
	if len(o.Extra) > 0 {
		c.Extra = make(map[string][]Source, len(o.Extra))
		for k, v := range o.Extra {
			c.Extra[k] = cloneSources(v)
		}
	}
	for _, it := range o.Items {
		c.Items = append(c.Items, it.Clone())
	}
	if o.Fallback != nil {
		f := o.Fallback.Clone()
		c.Fallback = &f
	}
	return c
}

// NodePref are the choices a person made about one node.
type NodePref struct {
	// Hash is the node's settings as last seen, to find it again when it is renamed.
	Hash string `json:"hash,omitempty"`
	// Region overrides the region recognised from the node's name.
	Region string `json:"region,omitempty"`
	// NoAuto keeps the node out of the automatic groups.
	NoAuto bool `json:"noAuto,omitempty"`
}

// State is everything the user chose about routing.
type State struct {
	Version int `json:"version"`
	// Active is the plan in force ("" or BuiltinID: the one the application ships).
	Active string `json:"active,omitempty"`
	// Plans are the plans the user keeps and those that came with subscriptions.
	Plans    []*Plan             `json:"plans,omitempty"`
	Overlays map[string]*Overlay `json:"overlays,omitempty"`
	// Exceptions are matched in order, before any plan.
	Exceptions []Exception `json:"exceptions,omitempty"`
	// Main is the main exit.
	Main Dest `json:"main"`
	// Nodes holds choices about single nodes, by NodeID.Key().
	Nodes map[string]NodePref `json:"nodes,omitempty"`
	// Recent are the nodes last chosen as the main exit, newest first, for
	// switching back to one quickly.
	Recent []NodeID `json:"recent,omitempty"`
}

// MaxRecent is how many nodes are remembered as recently chosen.
const MaxRecent = 6

// NewState is the state of a new installation.
func NewState() State {
	return State{Version: FormatVersion, Active: BuiltinID, Main: Auto()}
}

// Clone deep-copies the state, so a caller can edit it and hand it back.
func (s State) Clone() State {
	c := s
	c.Plans = make([]*Plan, len(s.Plans))
	for i, p := range s.Plans {
		c.Plans[i] = p.Clone()
	}
	c.Overlays = make(map[string]*Overlay, len(s.Overlays))
	for k, o := range s.Overlays {
		c.Overlays[k] = o.Clone()
	}
	c.Exceptions = make([]Exception, len(s.Exceptions))
	for i, x := range s.Exceptions {
		x.Dest = x.Dest.Clone()
		c.Exceptions[i] = x
	}
	c.Main = s.Main.Clone()
	c.Nodes = make(map[string]NodePref, len(s.Nodes))
	for k, v := range s.Nodes {
		c.Nodes[k] = v
	}
	c.Recent = append([]NodeID(nil), s.Recent...)
	return c
}

// ActiveID is the identifier of the plan in force.
func (s State) ActiveID() string {
	if s.Active == "" {
		return BuiltinID
	}
	return s.Active
}
