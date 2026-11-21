package configgen

import (
	"errors"
	"net/netip"
	"strings"

	"github.com/sagernet/sing-box/option"

	"eggbox/internal/subscription"
)

// Limits the agent puts on the resources of one configuration. They mirror
// internal/agent/store, and a test keeps the two equal.
const (
	MaxResources     = 256
	MaxResourceBytes = 16 << 20
	MaxResourceTotal = 64 << 20
)

// RelayKind says how a node reaches its server.
type RelayKind string

const (
	// RelayNone dials the server directly.
	RelayNone RelayKind = ""
	// RelayAuto dials through the fastest plain direct node.
	RelayAuto RelayKind = "auto"
	// RelayRegion dials through the fastest plain direct node of a region.
	RelayRegion RelayKind = "region"
	// RelayNode dials through one plain direct node.
	RelayNode RelayKind = "node"
	// RelayBlocked refuses dials because the node's relay is unavailable
	// (never falling back to a direct connection).
	RelayBlocked RelayKind = "blocked"
)

// Relay is the first-hop proxy a node dials through.
type Relay struct {
	Kind RelayKind
	// Region is the two-letter code of a RelayRegion.
	Region string
	// Node is the Key of the plain direct node of a RelayNode.
	Node string
	// NoStandIn keeps a RelayNode from falling back to the fastest plain
	// direct node of its region while unhealthy.
	NoStandIn bool
}

// Node is a subscription node together with what the generator needs to know
// about it besides its settings.
type Node struct {
	subscription.Node
	// Region is the two-letter code the node counts as; "" when unknown.
	Region string
	// NoAuto keeps the node out of the automatic groups. It can still be chosen.
	NoAuto bool
	// Relay is the first-hop proxy this node dials through; zero value dials directly.
	Relay Relay
}

// ExitKind is where a class of traffic leaves.
type ExitKind string

const (
	// ExitMain is wherever the main exit sends traffic. Not for the main exit itself.
	ExitMain   ExitKind = "main"
	ExitDirect ExitKind = "direct"
	ExitReject ExitKind = "reject"
	// ExitAuto is the fastest node of all.
	ExitAuto ExitKind = "auto"
	// ExitRegion is the fastest node of one region.
	ExitRegion ExitKind = "region"
	// ExitNode is one node.
	ExitNode ExitKind = "node"
)

// Exit is a destination.
type Exit struct {
	Kind ExitKind
	// Region is the code of an ExitRegion.
	Region string
	// Node is the Key of the node of an ExitNode.
	Node string
	// NoStandIn keeps an ExitNode from being replaced by the fastest node of
	// its region while it does not answer.
	NoStandIn bool
}

// Item is one class of traffic of the plan in force: rules compiled to binary
// rule sets and the exit they lead to.
type Item struct {
	// ID is stable; the tags of the item's group and rule sets derive from it.
	ID   string
	Name string
	Exit Exit
	// Domain holds the rules that are decided without the destination's
	// address, IP those that need it. Either may be empty.
	Domain, IP []byte
	// DNS holds the host-name rules among Domain, and steers DNS. When it is
	// empty the item leaves DNS alone.
	DNS []byte
	// Process is whether the rules look at the program that made the connection.
	Process bool
	// Domestic sends the item's names to the domestic DNS server instead of
	// the remote one, whatever Exit is. The choice must not follow the exit:
	// a running core can be told to send traffic elsewhere, but not to ask a
	// different DNS server, and the exit is meant to be switchable at runtime.
	Domestic bool
	// Resolver overrides how the item's names are looked up (dnsspec): ""
	// leaves it to Domestic, dnsspec.Exit leaves it to the exit (the names are
	// answered with fake addresses and the exit is handed the name), anything
	// else is a DNS server asked through the item's group, so it follows the
	// item's exit at runtime.
	Resolver string
	// LANClaims are the private ranges the person wrote into the item: while
	// the item is in the configuration, the local network's direct rule leaves
	// them to the rules.
	LANClaims []string
}

// Exception is a rule of the person's own, ahead of every item.
type Exception struct {
	ID   string
	Rule option.HeadlessRule
	Exit Exit
	// Resolver is like Item.Resolver; a DNS server is asked through the
	// exception's exit.
	Resolver string
	// LANClaim is the private range the exception names, like an item's
	// LANClaims: "" when it names none.
	LANClaim string
}

// Input is everything Generate needs.
type Input struct {
	Nodes []Node
	Prefs Prefs
	// Main is the main exit: ExitAuto, ExitRegion or ExitNode.
	Main Exit
	// Exceptions are matched first, in order, after the local network.
	Exceptions []Exception
	// Items are matched in order: all the host-name parts, then all the
	// address parts.
	Items []Item
	// Fallback is where traffic nothing claimed goes.
	Fallback Exit
}

// Claims are the private ranges the input's exceptions and items claim (see
// Exception.LANClaim, Item.LANClaims), as the configuration has them when
// every one of them makes it in; Output.LANClaims are those it has.
func (in Input) Claims() []string { return claimsOf(in.Exceptions, in.Items) }

// claimsOf gathers what exceptions and items claim, as valid prefixes without
// repeats, in order.
func claimsOf(xs []Exception, items []Item) []string {
	var out []string
	seen := map[string]bool{}
	add := func(c string) {
		p, err := netip.ParsePrefix(strings.TrimSpace(c))
		if err != nil {
			return
		}
		if s := p.Masked().String(); !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, x := range xs {
		add(x.LANClaim)
	}
	for _, it := range items {
		for _, c := range it.LANClaims {
			add(c)
		}
	}
	return out
}

// Resource is a file the configuration references as "res:<Name>".
type Resource struct {
	Name string
	Data []byte
}

// OwnerKind says what a rule set in the configuration came from.
type OwnerKind string

const (
	OwnerItem      OwnerKind = "item"
	OwnerException OwnerKind = "exception"
)

// Owner is what a rule-set tag belongs to, so a connection that matched it can
// be explained in the person's words.
type Owner struct {
	Kind OwnerKind
	ID   string
}

// Dropped is a node left out of the configuration.
type Dropped struct {
	Name   string
	Key    string
	Reason string
}

// Warning is something the user should know about a generated configuration.
type Warning struct {
	// Code is stable so the GUI can translate it.
	Code   string
	Detail string
}

// Warning codes.
const (
	WarnPrefsAdjusted = "prefs_adjusted"
	// WarnRegionMissing: an exit names a region that has no group (no node of
	// it takes part in the automatic choice); traffic follows the main exit.
	WarnRegionMissing = "region_missing"
	// WarnNodeMissing: an exit names a node that is not in the configuration.
	WarnNodeMissing = "node_missing"
	// WarnAutoEmpty: every node is kept out of the automatic groups, so they
	// use all of them instead.
	WarnAutoEmpty = "auto_empty"
	// WarnItemSkipped: an item was left out because the configuration could not
	// carry its rule data (too many or too large files).
	WarnItemSkipped = "item_skipped"
	// WarnExceptionInvalid: an exception's rule could not be written.
	WarnExceptionInvalid = "exception_invalid"
	// WarnResolverInvalid: an item or exception names a resolver that is not a
	// DNS server; its names are looked up as usual. Detail is its id.
	WarnResolverInvalid = "resolver_invalid"
	// WarnRelayUnavailable: a node's configured relay has no usable plain
	// direct node; its detour is blocked rather than falling back to direct.
	WarnRelayUnavailable = "relay_unavailable"
)

// Layout records where each node sits in the generated arrays, so an error
// reported at "$.outbounds[7]" can be traced back to a node.
type Layout struct {
	Outbounds []string // node key at each index of "outbounds", "" for groups
	Endpoints []string
}

// Output is a generated configuration.
type Output struct {
	JSON      []byte
	Resources []Resource
	// Tags maps a node key to its outbound tag.
	Tags map[string]string
	// Relays maps a relayed node's key to its detour outbound tag (a plain
	// node tag, TagRelayAuto, RelayRegionPrefix+code, RelayPinPrefix+n or
	// TagRelayBlocked). Plain direct nodes have no entry.
	Relays map[string]string
	// Groups are the group tags in display order: Proxy, Auto, region groups.
	Groups []string
	// Selectors maps an item ID (FallbackID for the fallback) to the tag of the
	// group its traffic goes through. Items that are blocked have none.
	Selectors map[string]string
	// Selections maps the tag of every group that can be switched at runtime
	// (TagProxy and the Selectors) to the member the input wants selected.
	Selections map[string]string
	// Owners maps the rule-set tags to the item or exception they came from.
	Owners map[string]Owner
	// Digest identifies the configuration apart from what Selections say, so
	// two outputs with the same digest differ only in switches that can be made
	// without restarting the core.
	Digest string
	// Skipped are the IDs of the items left out for want of room.
	Skipped []string
	// LANClaims are the private ranges the local network's direct rule leaves
	// to the rules: the exceptions' and the admitted items'.
	LANClaims []string
	Dropped   []Dropped
	Warnings  []Warning
	Layout    Layout
}

// ErrNoNodes means there is nothing to connect through.
var ErrNoNodes = errors.New("there are no nodes to generate a configuration from")

// Switches lists the selections to make so that a core running prev behaves
// like next, without restarting it. ok is false when the two differ in
// anything else, and the core has to be restarted with next.
func Switches(prev, next *Output) (switches map[string]string, ok bool) {
	if prev == nil || next == nil || prev.Digest == "" || prev.Digest != next.Digest {
		return nil, false
	}
	switches = map[string]string{}
	for tag, member := range next.Selections {
		if prev.Selections[tag] != member {
			switches[tag] = member
		}
	}
	return switches, true
}
