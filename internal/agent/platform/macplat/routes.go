// Package macplat is the macOS driver of the agent: how it reads the
// machine's interfaces and routes, notices changes, points the system resolver
// at the tunnel, and takes all of it back.
//
// What the numbers mean (which route is the way out, which interface is
// another VPN) and what the driver does with them is plain logic in this
// directory, tested anywhere against a fake machine. The two places that touch
// macOS itself are the Swift bridge (resolver, sleep, who is at the screen,
// reached through the System interface) and host_darwin.go (the routing table
// and its change notifications).
package macplat

import (
	"net/netip"
	"sort"

	"eggbox/internal/agent/routecheck"
)

// The flags macOS puts on a route (net/route.h). Only the ones that decide
// whether a route carries traffic are named.
const (
	rtfUp        = 0x1
	rtfGateway   = 0x2
	rtfHost      = 0x4
	rtfReject    = 0x8
	rtfLLInfo    = 0x400
	rtfBlackhole = 0x1000
	rtfWasCloned = 0x20000
	rtfBroadcast = 0x400000
	rtfMulticast = 0x800000
	// rtfIfScope marks a route that only applies to traffic bound to its
	// interface. When a VPN takes the default route, the physical uplink's
	// default stays on, scoped; the system's own tunnels (private relay and
	// such) carry scoped defaults of their own all the time.
	rtfIfScope = 0x1000000
)

// Interface is one network interface as read from the system.
type Interface struct {
	Index int
	Name  string
	// Up is true for an interface that is both enabled and has a link.
	Up    bool
	Kind  routecheck.Kind
	Addrs []netip.Prefix
}

// RawRoute is one route message of the kernel, decoded but not yet judged.
type RawRoute struct {
	Dest netip.Addr
	// Mask is the network mask as an address; invalid when the kernel sent none.
	Mask netip.Addr
	// Gateway is invalid for a route whose gateway is a link (on-link).
	Gateway netip.Addr
	Flags   int
	IfIndex int
}

// Route is a route that carries traffic.
type Route struct {
	Dest    netip.Prefix
	Gateway netip.Addr // invalid for an on-link route
	IfIndex int
	// Scoped routes apply only to traffic bound to their interface.
	Scoped bool
}

// IsDefault reports whether the route is the one for everything.
func (r Route) IsDefault() bool { return r.Dest.IsValid() && r.Dest.Bits() == 0 }

// Route judges a kernel route: ok is false for entries that are not a way to
// anywhere: routes that are down, reject or blackhole routes, the cache of
// neighbours and of cloned hosts, broadcast and multicast.
func (r RawRoute) Route() (Route, bool) {
	const unusable = rtfReject | rtfBlackhole | rtfLLInfo | rtfWasCloned | rtfBroadcast | rtfMulticast
	if r.Flags&rtfUp == 0 || r.Flags&unusable != 0 || !r.Dest.IsValid() {
		return Route{}, false
	}
	dest := clearScope(r.Dest.Unmap())
	bits := dest.BitLen()
	if r.Flags&rtfHost == 0 {
		if !r.Mask.IsValid() {
			return Route{}, false
		}
		b, ok := maskBits(r.Mask.Unmap(), dest.BitLen())
		if !ok {
			return Route{}, false
		}
		bits = b
	}
	prefix, err := dest.Prefix(bits)
	if err != nil {
		return Route{}, false
	}
	out := Route{Dest: prefix, IfIndex: r.IfIndex, Scoped: r.Flags&rtfIfScope != 0}
	if r.Flags&rtfGateway != 0 && r.Gateway.IsValid() {
		out.Gateway = clearScope(r.Gateway.Unmap())
	}
	return out, true
}

// clearScope removes the interface index the kernel hides inside the address
// of a link-local IPv6 address (fe80:4::1 is fe80::1 on interface 4).
func clearScope(a netip.Addr) netip.Addr {
	if a.Is6() && (a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsInterfaceLocalMulticast()) {
		b := a.As16()
		b[2], b[3] = 0, 0
		return netip.AddrFrom16(b)
	}
	return a
}

// maskBits counts the length of a network mask. ok is false for a mask that is
// not a run of ones followed by zeros, or whose family is not width bits.
func maskBits(m netip.Addr, width int) (bits int, ok bool) {
	if m.BitLen() != width {
		return 0, false
	}
	seenZero := false
	for _, b := range m.AsSlice() {
		for n := 7; n >= 0; n-- {
			if b&(1<<n) == 0 {
				seenZero = true
				continue
			}
			if seenZero {
				return 0, false
			}
			bits++
		}
	}
	return bits, true
}

func interfaceByIndex(ifaces []Interface, index int) (Interface, bool) {
	for _, i := range ifaces {
		if i.Index == index {
			return i, true
		}
	}
	return Interface{}, false
}

// Snapshot converts what was read into what the route check judges. Scoped
// routes are left out: they are not competing ways out, and counting them
// would make the system's own tunnels look like another VPN. The interface
// the agent made itself is marked, so it is never judged.
func Snapshot(routes []Route, ifaces []Interface, ours func(Interface) bool) routecheck.Snapshot {
	var s routecheck.Snapshot
	if _, i, ok := Uplink(routes, ifaces, ours); ok && i.Kind == routecheck.KindPhysical {
		s.PhysicalUplink = i.Name
	}
	for _, i := range ifaces {
		s.Interfaces = append(s.Interfaces, routecheck.Interface{
			Name: i.Name, Index: i.Index, Up: i.Up, Kind: i.Kind, Addrs: i.Addrs,
			Ours: ours != nil && ours(i),
		})
	}
	for _, r := range routes {
		if r.Scoped {
			continue
		}
		name := ""
		if i, ok := interfaceByIndex(ifaces, r.IfIndex); ok {
			name = i.Name
		}
		s.Routes = append(s.Routes, routecheck.Route{Dest: r.Dest, Gateway: r.Gateway, IfName: name})
	}
	return s
}

// Uplink picks the way out of the machine to the network: the default route a
// connection through the physical network would take.
//
// Normally that is the system's own default route. When another VPN holds it,
// the real uplink is the scoped default the system keeps on the physical
// interface, so that is what counts then. IPv4 wins over IPv6, as it does in
// practice, so a change of IPv6 alone does not look like a new network. The
// agent's own interface is never the answer.
func Uplink(routes []Route, ifaces []Interface, ours func(Interface) bool) (Route, Interface, bool) {
	type cand struct {
		r     Route
		i     Interface
		class int // 0: the system's default on a real interface, 1: scoped on one, 2: through a tunnel
	}
	var all []cand
	for _, r := range routes {
		if !r.IsDefault() {
			continue
		}
		i, ok := interfaceByIndex(ifaces, r.IfIndex)
		if !ok || !i.Up || i.Kind == routecheck.KindLoopback || (ours != nil && ours(i)) {
			continue
		}
		c := cand{r: r, i: i}
		switch {
		case i.Kind == routecheck.KindTunnel:
			c.class = 2
		case r.Scoped:
			c.class = 1
		}
		all = append(all, c)
	}
	if len(all) == 0 {
		return Route{}, Interface{}, false
	}
	sort.SliceStable(all, func(a, b int) bool {
		x, y := all[a], all[b]
		if x.r.Dest.Addr().Is4() != y.r.Dest.Addr().Is4() {
			return x.r.Dest.Addr().Is4()
		}
		if x.class != y.class {
			return x.class < y.class
		}
		return x.i.Index < y.i.Index
	})
	return all[0].r, all[0].i, true
}

// Fingerprint names the current uplink: the interface and its gateway. It
// changes when the machine moves to another network or another way out.
func Fingerprint(r Route, i Interface) string {
	gw := "on-link"
	if r.Gateway.IsValid() && !r.Gateway.IsUnspecified() {
		gw = r.Gateway.String()
	}
	return i.Name + "|" + gw
}
