package routing

import (
	"errors"
	"fmt"
	"strings"
)

// RelayKind says how connections to a proxy server are dialled.
type RelayKind string

const (
	// RelayInherit defers to the subscription's default relay, then the node's
	// provider-declared relay, then a direct connection.
	RelayInherit RelayKind = ""
	// RelayDirect dials the node server directly, overriding any subscription
	// default or provider-declared relay.
	RelayDirect RelayKind = "direct"
	// RelayAuto dials through the fastest plain direct node.
	RelayAuto RelayKind = "auto"
	// RelayRegion dials through the fastest plain direct node of a region.
	RelayRegion RelayKind = "region"
	// RelayNode dials through one plain direct node (with optional same-region
	// stand-in while it is unhealthy).
	RelayNode RelayKind = "node"
)

// Relay describes how a node (or a subscription by default) reaches its server.
type Relay struct {
	Kind   RelayKind `json:"kind,omitempty"`
	Region string    `json:"region,omitempty"`
	Node   *NodeID   `json:"node,omitempty"`
	// NoFallback keeps a pinned relay node from being replaced by the fastest
	// plain direct node of its region while it is unhealthy.
	NoFallback bool `json:"noFallback,omitempty"`
}

// Relay constructors.
func RelayDirectVal() Relay { return Relay{Kind: RelayDirect} }
func RelayAutoVal() Relay   { return Relay{Kind: RelayAuto} }
func RelayRegionVal(code string) Relay {
	return Relay{Kind: RelayRegion, Region: strings.ToUpper(strings.TrimSpace(code))}
}
func RelayNodeVal(id NodeID) Relay { return Relay{Kind: RelayNode, Node: &id} }

// IsZero reports whether the relay means inherit (unset).
func (r Relay) IsZero() bool { return r.Kind == RelayInherit }

// Active reports whether the relay routes through another proxy rather than
// dialling directly.
func (r Relay) Active() bool {
	return r.Kind == RelayAuto || r.Kind == RelayRegion || r.Kind == RelayNode
}

// Valid reports why a relay setting cannot be used.
func (r Relay) Valid(allowInherit bool) error {
	switch r.Kind {
	case RelayInherit:
		if !allowInherit {
			return errors.New("relay kind is required")
		}
	case RelayDirect, RelayAuto:
	case RelayRegion:
		code := strings.ToUpper(strings.TrimSpace(r.Region))
		if len(code) != 2 || code[0] < 'A' || code[0] > 'Z' || code[1] < 'A' || code[1] > 'Z' {
			return fmt.Errorf("%q is not a region code", r.Region)
		}
	case RelayNode:
		if r.Node == nil || r.Node.IsZero() {
			return errors.New("no relay node was chosen")
		}
	default:
		return fmt.Errorf("unknown relay kind %q", r.Kind)
	}
	return nil
}

// Equal compares relays by meaning.
func (r Relay) Equal(o Relay) bool {
	if r.Kind != o.Kind || !strings.EqualFold(r.Region, o.Region) || r.NoFallback != o.NoFallback {
		return false
	}
	if r.Kind != RelayNode {
		return true
	}
	if r.Node == nil || o.Node == nil {
		return r.Node == o.Node
	}
	return r.Node.Same(*o.Node)
}

// Clone deep-copies a Relay.
func (r Relay) Clone() Relay {
	if r.Node != nil {
		n := *r.Node
		r.Node = &n
	}
	return r
}

// CloneRelayPtr deep-copies a *Relay.
func CloneRelayPtr(r *Relay) *Relay {
	if r == nil {
		return nil
	}
	c := r.Clone()
	return &c
}

// NormalizeRelayPtr normalizes a *Relay: nil or zero stays nil, regions are
// uppercased, and fields unrelated to Kind are cleared.
func NormalizeRelayPtr(r *Relay) *Relay {
	if r == nil || r.IsZero() {
		return nil
	}
	out := Relay{Kind: r.Kind}
	switch r.Kind {
	case RelayRegion:
		out.Region = strings.ToUpper(strings.TrimSpace(r.Region))
	case RelayNode:
		if r.Node != nil {
			n := *r.Node
			out.Node = &n
		}
		out.NoFallback = r.NoFallback
	}
	return &out
}

// RelaySource says where a node's effective relay came from.
type RelaySource int

const (
	// RelaySourceNone: no relay configured; node dials directly.
	RelaySourceNone RelaySource = iota
	// RelaySourceNode: explicitly set on this node by the user.
	RelaySourceNode
	// RelaySourceSub: inherited from the subscription's default relay.
	RelaySourceSub
	// RelaySourceProvider: declared by the subscription file (dialer-proxy / detour).
	RelaySourceProvider
)

// EffectiveRelay is the resolved relay for one node and where it came from.
type EffectiveRelay struct {
	Relay  Relay
	Source RelaySource
}

// Active reports whether the node connects through a relay.
func (e EffectiveRelay) Active() bool { return e.Relay.Active() }

// ResolveRelay computes the effective relay of a node in priority order:
// per-node user override > subscription default > provider-declared relay > direct.
func ResolveRelay(id NodeID, st State, subRelay, providerRelay *Relay) EffectiveRelay {
	if pref := st.NodePref(id); pref.Relay != nil && !pref.Relay.IsZero() {
		return EffectiveRelay{Relay: pref.Relay.Clone(), Source: RelaySourceNode}
	}
	if subRelay != nil && subRelay.Active() {
		return EffectiveRelay{Relay: subRelay.Clone(), Source: RelaySourceSub}
	}
	if providerRelay != nil && providerRelay.Active() {
		return EffectiveRelay{Relay: providerRelay.Clone(), Source: RelaySourceProvider}
	}
	return EffectiveRelay{Relay: RelayDirectVal(), Source: RelaySourceNone}
}

// RelayNodeInfo describes one active node for two-tier relay conflict checks.
type RelayNodeInfo struct {
	ID            NodeID
	Name          string
	SubID         string
	SubRelay      *Relay
	ProviderRelay *Relay
}

// RelayDependent names a node or subscription that currently pins a node as
// its relay, preventing that target from becoming a relayed node itself.
type RelayDependent struct {
	IsSub bool
	ID    string
	Name  string
}

// RelayDependentsOfNode lists the nodes and subscriptions that explicitly pin
// target as their relay node.
func RelayDependentsOfNode(target NodeID, nodes []RelayNodeInfo, subNames map[string]string, subRelays map[string]*Relay, st State) []RelayDependent {
	var out []RelayDependent
	seenSub := map[string]bool{}
	seenNode := map[string]bool{}
	// Subscriptions whose default relay pins target.
	for subID, r := range subRelays {
		if r != nil && r.Kind == RelayNode && r.Node != nil && r.Node.Same(target) {
			if !seenSub[subID] {
				seenSub[subID] = true
				name := subNames[subID]
				if name == "" {
					name = subID
				}
				out = append(out, RelayDependent{IsSub: true, ID: subID, Name: name})
			}
		}
	}
	// Active nodes whose effective relay pins target (either via node pref or provider relay).
	for _, n := range nodes {
		if n.ID.Same(target) {
			continue
		}
		eff := ResolveRelay(n.ID, st, n.SubRelay, n.ProviderRelay)
		if eff.Relay.Kind != RelayNode || eff.Relay.Node == nil || !eff.Relay.Node.Same(target) {
			continue
		}
		if eff.Source == RelaySourceSub {
			if !seenSub[n.SubID] {
				seenSub[n.SubID] = true
				name := subNames[n.SubID]
				if name == "" {
					name = n.SubID
				}
				out = append(out, RelayDependent{IsSub: true, ID: n.SubID, Name: name})
			}
			continue
		}
		k := n.ID.Key()
		if !seenNode[k] {
			seenNode[k] = true
			out = append(out, RelayDependent{ID: k, Name: n.Name})
		}
	}
	return out
}

// RelayDependentsOfSub lists the nodes and subscriptions that pin any node of
// targetSubID that would inherit a subscription-level default relay (i.e. does
// not have an explicit per-node Direct override).
func RelayDependentsOfSub(targetSubID string, nodes []RelayNodeInfo, subNames map[string]string, subRelays map[string]*Relay, st State) []RelayDependent {
	var out []RelayDependent
	seen := map[string]bool{}
	for _, n := range nodes {
		if n.SubID != targetSubID {
			continue
		}
		if pref := st.NodePref(n.ID); pref.Relay != nil && pref.Relay.Kind == RelayDirect {
			continue
		}
		for _, dep := range RelayDependentsOfNode(n.ID, nodes, subNames, subRelays, st) {
			if dep.IsSub && dep.ID == targetSubID {
				continue
			}
			key := fmt.Sprintf("%v|%s", dep.IsSub, dep.ID)
			if !seen[key] {
				seen[key] = true
				out = append(out, dep)
			}
		}
	}
	return out
}
