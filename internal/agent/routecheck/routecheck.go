// Package routecheck decides, from a snapshot of interfaces and routes, whether
// it is safe to bring up the TUN and which private subnet to give it.
//
// It is pure: platform drivers collect the snapshot, this package judges it.
// It never suggests deleting anybody else's routes; a conflict means "refuse or
// ask the user", exactly as the design requires.
package routecheck

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

// Kind is a coarse classification of a network interface.
type Kind int

const (
	KindUnknown Kind = iota
	KindPhysical
	KindLoopback
	// KindTunnel covers VPN-style point-to-point devices (utun, ppp, wireguard, tap...).
	KindTunnel
	KindVirtual
)

func (k Kind) String() string {
	switch k {
	case KindPhysical:
		return "physical"
	case KindLoopback:
		return "loopback"
	case KindTunnel:
		return "tunnel"
	case KindVirtual:
		return "virtual"
	default:
		return "unknown"
	}
}

// Interface is one network interface.
type Interface struct {
	Name  string
	Index int
	Up    bool
	Kind  Kind
	Addrs []netip.Prefix
	// Ours marks the interface the agent itself created (from the ledger); it is
	// excluded from every judgement.
	Ours bool
}

// Route is one routing table entry.
type Route struct {
	Dest    netip.Prefix
	Gateway netip.Addr
	IfName  string
}

// Snapshot is the input of the analysis.
type Snapshot struct {
	Interfaces []Interface
	Routes     []Route
}

// Plan is the private addressing used for the TUN. The first address of each
// prefix is the TUN's own; the next one is the in-tunnel DNS endpoint.
type Plan struct {
	V4 netip.Prefix
	V6 netip.Prefix
}

// DNS returns the in-tunnel DNS address for the family (the second host of the subnet).
func (p Plan) DNS() netip.Addr {
	return p.V4.Addr().Next()
}

// Addresses lists the prefixes in the order sing-box expects them.
func (p Plan) Addresses() []netip.Prefix {
	var out []netip.Prefix
	if p.V4.IsValid() {
		out = append(out, p.V4)
	}
	if p.V6.IsValid() {
		out = append(out, p.V6)
	}
	return out
}

// DefaultCandidates are conflict-free private ranges, tried in order. They sit
// in uncommon corners (172.19/16 and friends) so home and corporate LANs rarely collide.
func DefaultCandidates() []Plan {
	mk := func(v4, v6 string) Plan {
		return Plan{V4: netip.MustParsePrefix(v4), V6: netip.MustParsePrefix(v6)}
	}
	return []Plan{
		mk("172.19.0.1/30", "fdfe:dcba:9876::1/126"),
		mk("172.29.0.1/30", "fdfe:dcba:9877::1/126"),
		mk("172.31.250.1/30", "fdfe:dcba:9878::1/126"),
		mk("10.211.77.1/30", "fdfe:dcba:9879::1/126"),
		mk("100.127.250.1/30", "fdfe:dcba:987a::1/126"),
	}
}

// ConflictKind names the reason a connection should be refused or confirmed.
type ConflictKind string

const (
	// ConflictForeignDefaultRoute: another tunnel already carries default traffic.
	ConflictForeignDefaultRoute ConflictKind = "foreign_default_route"
	// ConflictNoSubnet: every candidate TUN subnet overlaps something that exists.
	ConflictNoSubnet ConflictKind = "no_free_subnet"
)

// Conflict explains one problem found in the snapshot.
type Conflict struct {
	Kind      ConflictKind
	Interface string
	Detail    string
}

func (c Conflict) String() string {
	if c.Interface != "" {
		return fmt.Sprintf("%s on %s: %s", c.Kind, c.Interface, c.Detail)
	}
	return fmt.Sprintf("%s: %s", c.Kind, c.Detail)
}

// ChoosePlan returns the first candidate whose subnets overlap no existing
// interface address or route. ok is false when every candidate collides.
func ChoosePlan(snap Snapshot, candidates []Plan) (Plan, bool) {
	for _, c := range candidates {
		if !overlapsAnything(snap, c.V4) && !overlapsAnything(snap, c.V6) {
			return c, true
		}
	}
	return Plan{}, false
}

func overlapsAnything(snap Snapshot, p netip.Prefix) bool {
	if !p.IsValid() {
		return false
	}
	for _, ifc := range snap.Interfaces {
		if ifc.Ours {
			continue
		}
		for _, a := range ifc.Addrs {
			if a.Overlaps(p) {
				return true
			}
		}
	}
	for _, r := range snap.Routes {
		if ownInterface(snap, r.IfName) || isDefault(r.Dest) {
			continue
		}
		if r.Dest.Overlaps(p) {
			return true
		}
	}
	return false
}

func ownInterface(snap Snapshot, name string) bool {
	for _, ifc := range snap.Interfaces {
		if ifc.Name == name {
			return ifc.Ours
		}
	}
	return false
}

// isDefault reports whether the prefix covers (about) everything: 0/0, ::/0, or
// one half of a split default (0.0.0.0/1, 128.0.0.0/1, ::/1, 8000::/1).
func isDefault(p netip.Prefix) bool {
	return p.IsValid() && p.Bits() <= 1
}

// Analyze looks for foreign tunnels that already carry default traffic.
func Analyze(snap Snapshot) []Conflict {
	byName := map[string]Interface{}
	for _, ifc := range snap.Interfaces {
		byName[ifc.Name] = ifc
	}
	seen := map[string]bool{}
	var out []Conflict
	for _, r := range snap.Routes {
		if !isDefault(r.Dest) {
			continue
		}
		ifc, ok := byName[r.IfName]
		if !ok || ifc.Ours || !ifc.Up || ifc.Kind != KindTunnel || seen[ifc.Name] {
			continue
		}
		seen[ifc.Name] = true
		out = append(out, Conflict{
			Kind:      ConflictForeignDefaultRoute,
			Interface: ifc.Name,
			Detail:    fmt.Sprintf("%s routes %s", ifc.Name, r.Dest),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Interface < out[j].Interface })
	return out
}

// isAccessPointName matches macOS's "ap1"-style Wi-Fi hotspot interface without
// catching adapters whose human name merely starts with "ap".
func isAccessPointName(n string) bool {
	return len(n) > 2 && strings.HasPrefix(n, "ap") && n[2] >= '0' && n[2] <= '9'
}

// ClassifyByName is a heuristic used by drivers when the OS reports no interface type.
func ClassifyByName(name string, pointToPoint, loopback bool) Kind {
	n := strings.ToLower(name)
	switch {
	case loopback || n == "lo" || strings.HasPrefix(n, "lo0"):
		return KindLoopback
	case strings.HasPrefix(n, "utun"), strings.HasPrefix(n, "ppp"), strings.HasPrefix(n, "ipsec"),
		strings.HasPrefix(n, "gif"), strings.HasPrefix(n, "tun"), strings.HasPrefix(n, "tap"),
		strings.HasPrefix(n, "wg"), strings.HasPrefix(n, "wireguard"), strings.Contains(n, "vpn"),
		strings.Contains(n, "wintun"), strings.Contains(n, "tailscale"):
		return KindTunnel
	case strings.HasPrefix(n, "bridge"), strings.HasPrefix(n, "vmnet"), strings.HasPrefix(n, "veth"),
		strings.HasPrefix(n, "docker"), strings.HasPrefix(n, "awdl"), strings.HasPrefix(n, "llw"),
		strings.HasPrefix(n, "anpi"), isAccessPointName(n):
		return KindVirtual
	case pointToPoint:
		return KindTunnel
	case strings.HasPrefix(n, "en"), strings.HasPrefix(n, "eth"), strings.HasPrefix(n, "wlan"),
		strings.HasPrefix(n, "wi-fi"), strings.HasPrefix(n, "ethernet"):
		return KindPhysical
	default:
		return KindUnknown
	}
}
