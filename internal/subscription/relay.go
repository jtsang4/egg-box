package subscription

import (
	"errors"
	"fmt"
	"strings"
)

// RelayKind says what kind of provider-declared proxy chain a node carries.
type RelayKind string

const (
	// RelayNode dials through another plain node of the same subscription.
	RelayNode RelayKind = "node"
	// RelayAuto dials through the fastest plain direct node.
	RelayAuto RelayKind = "auto"
	// RelayRegion dials through the fastest plain direct node of a region.
	RelayRegion RelayKind = "region"
)

// RelaySpec is the two-hop chain a provider declared for a node (Clash
// dialer-proxy or sing-box detour). It is nil for plain direct nodes.
type RelaySpec struct {
	Kind   RelayKind `json:"kind"`
	Node   string    `json:"node,omitempty"`
	Region string    `json:"region,omitempty"`
}

// Clone copies a RelaySpec.
func (r *RelaySpec) Clone() *RelaySpec {
	if r == nil {
		return nil
	}
	c := *r
	return &c
}

// Validate checks a provider-declared relay.
func (r *RelaySpec) Validate() error {
	if r == nil {
		return nil
	}
	switch r.Kind {
	case RelayNode:
		if strings.TrimSpace(r.Node) == "" {
			return errors.New("the relay node name is empty")
		}
	case RelayAuto:
	case RelayRegion:
		code := strings.ToUpper(strings.TrimSpace(r.Region))
		if len(code) != 2 || code[0] < 'A' || code[0] > 'Z' || code[1] < 'A' || code[1] > 'Z' {
			return fmt.Errorf("relay region %q is not a two-letter code", r.Region)
		}
	default:
		return fmt.Errorf("unknown relay kind %q", r.Kind)
	}
	return nil
}

// NeedsUDP reports whether the node's transport relies on UDP (QUIC or
// WireGuard), so the interface can remind the user that its relay must carry
// UDP when chaining through another proxy.
func (n Node) NeedsUDP() bool {
	switch n.Type {
	case "hysteria", "hysteria2", "tuic", "wireguard":
		return true
	}
	return false
}

// parsedGroup is a minimal provider group view used while resolving a
// provider-declared relay target (dialer-proxy / detour) to a plain node,
// a region or automatic selection.
type parsedGroup struct {
	name    string
	auto    bool
	def     string
	members []string
}

// resolveProviderRelays resolves raw provider relay references (Clash
// dialer-proxy or sing-box detour) for a set of parsed nodes. Only two-hop
// chains (pointing to a plain direct node, or a group that resolves to plain
// nodes) are accepted; deeper chains, cycles and unknown targets are skipped.
func resolveProviderRelays(nodes []Node, rawTarget map[int]string, groups map[string]parsedGroup, directTags map[string]bool) (kept []Node, skipped []Skipped) {
	isDirect := func(name string) bool {
		if name == "" || strings.EqualFold(name, "direct") {
			return true
		}
		return directTags[name]
	}

	// Map each parsed node's raw name to whether it is a plain (non-chained) node.
	plainByName := map[string]string{} // raw name -> cleaned/raw name
	chainedNames := map[string]bool{}
	for i, n := range nodes {
		t := strings.TrimSpace(rawTarget[i])
		if isDirect(t) {
			if _, exists := plainByName[n.Name]; !exists {
				plainByName[n.Name] = n.Name
			}
		} else {
			chainedNames[n.Name] = true
		}
	}

	var resolveGroup func(name string, seen map[string]bool) (*RelaySpec, error)
	resolveGroup = func(name string, seen map[string]bool) (*RelaySpec, error) {
		if seen[name] {
			return nil, skip(SkipUnsupportedFeature, "cyclic proxy chain is not supported")
		}
		seen[name] = true
		g, ok := groups[name]
		if !ok {
			return nil, skip(SkipUnsupportedFeature, "proxy chaining target is not in this subscription")
		}
		if g.auto {
			return resolveGroupMembers(g.members, plainByName, chainedNames, directTags)
		}
		// Select group: follow its default (or first non-direct/non-reject member).
		pick := g.def
		if pick == "" && len(g.members) > 0 {
			pick = g.members[0]
		}
		if pick == "" || isDirect(pick) {
			return nil, nil
		}
		if strings.EqualFold(pick, "reject") || strings.EqualFold(pick, "block") {
			return nil, skip(SkipUnsupportedFeature, "proxy chaining target cannot be a block outbound")
		}
		if plain, ok := plainByName[pick]; ok {
			return &RelaySpec{Kind: RelayNode, Node: plain}, nil
		}
		if chainedNames[pick] {
			return nil, skip(SkipUnsupportedFeature, "proxy chaining deeper than two hops is not supported")
		}
		if _, ok := groups[pick]; ok {
			return resolveGroup(pick, seen)
		}
		// If the default wasn't a direct match, fall back to inspecting plain members.
		return resolveGroupMembers(g.members, plainByName, chainedNames, directTags)
	}

	for i, n := range nodes {
		target := strings.TrimSpace(rawTarget[i])
		if isDirect(target) {
			kept = append(kept, n)
			continue
		}
		if target == n.Name {
			skipped = append(skipped, Skipped{Name: n.Name, Type: n.Type, Code: SkipUnsupportedFeature, Detail: "a node cannot relay through itself"})
			continue
		}
		var spec *RelaySpec
		var err error
		switch {
		case plainByName[target] != "":
			spec = &RelaySpec{Kind: RelayNode, Node: plainByName[target]}
		case chainedNames[target]:
			err = skip(SkipUnsupportedFeature, "proxy chaining deeper than two hops is not supported")
		case groups != nil:
			if _, ok := groups[target]; ok {
				spec, err = resolveGroup(target, map[string]bool{})
			} else {
				err = skip(SkipUnsupportedFeature, "proxy chaining target is not in this subscription")
			}
		default:
			err = skip(SkipUnsupportedFeature, "proxy chaining target is not in this subscription")
		}
		if err != nil {
			s := Skipped{Name: n.Name, Type: n.Type, Code: SkipUnsupportedFeature, Detail: err.Error()}
			if se, ok := err.(*skipError); ok {
				s.Code, s.Detail = se.code, se.detail
			}
			skipped = append(skipped, s)
			continue
		}
		n.Relay = spec
		if err := n.Validate(); err != nil {
			skipped = append(skipped, Skipped{Name: n.Name, Type: n.Type, Code: SkipInvalid, Detail: err.Error()})
			continue
		}
		kept = append(kept, n)
	}
	return kept, skipped
}

// resolveGroupMembers turns the member list of an automatic/region group into
// a RelayRegion (when every plain member shares a single region) or RelayAuto.
func resolveGroupMembers(members []string, plainByName map[string]string, chainedNames map[string]bool, directTags map[string]bool) (*RelaySpec, error) {
	var regions []string
	seenRegion := map[string]bool{}
	plainCount := 0
	unknownRegion := false
	for _, m := range members {
		if m == "" || strings.EqualFold(m, "direct") || directTags[m] || strings.EqualFold(m, "reject") || strings.EqualFold(m, "block") {
			continue
		}
		if plainByName[m] != "" {
			plainCount++
			r := Region(m)
			if r == "" {
				unknownRegion = true
			} else if !seenRegion[r] {
				seenRegion[r] = true
				regions = append(regions, r)
			}
			continue
		}
		if chainedNames[m] {
			continue
		}
	}
	if plainCount == 0 {
		return nil, skip(SkipUnsupportedFeature, "proxy chaining group has no plain direct nodes")
	}
	if !unknownRegion && len(regions) == 1 {
		return &RelaySpec{Kind: RelayRegion, Region: regions[0]}, nil
	}
	return &RelaySpec{Kind: RelayAuto}, nil
}

// reconcileRelays runs after info-entry filtering and Dedupe so that every
// provider-declared RelayNode points at the final deduped name of a plain node
// that survived. Chained nodes whose target was dropped (e.g. an info entry)
// are skipped cleanly rather than leaving a dangling target.
func reconcileRelays(nodes []Node) (kept []Node, skipped []Skipped) {
	plain := map[string]string{} // lowercase name -> final name
	for _, n := range nodes {
		if n.Relay == nil {
			k := strings.ToLower(n.Name)
			if _, exists := plain[k]; !exists {
				plain[k] = n.Name
			}
		}
	}
	for _, n := range nodes {
		if n.Relay == nil || n.Relay.Kind != RelayNode {
			kept = append(kept, n)
			continue
		}
		target := cleanName(n.Relay.Node)
		final, ok := plain[strings.ToLower(target)]
		if !ok {
			skipped = append(skipped, Skipped{
				Name: n.Name, Type: n.Type, Code: SkipUnsupportedFeature,
				Detail: "proxy chaining target is not available",
			})
			continue
		}
		n.Relay = &RelaySpec{Kind: RelayNode, Node: final}
		kept = append(kept, n)
	}
	return kept, skipped
}
