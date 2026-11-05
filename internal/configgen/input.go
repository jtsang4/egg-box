package configgen

import (
	"errors"

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

// Node is a subscription node together with what the generator needs to know
// about it besides its settings.
type Node struct {
	subscription.Node
	// Region is the two-letter code the node counts as; "" when unknown.
	Region string
	// NoAuto keeps the node out of the automatic groups. It can still be chosen.
	NoAuto bool
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
}

// Exception is a rule of the person's own, ahead of every item.
type Exception struct {
	ID   string
	Rule option.HeadlessRule
	Exit Exit
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
	Skipped  []string
	Dropped  []Dropped
	Warnings []Warning
	Layout   Layout
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
