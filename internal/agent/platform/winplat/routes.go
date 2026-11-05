package winplat

import (
	"net/netip"
	"sort"

	"eggbox/internal/agent/routecheck"
)

// Adapter is one network adapter as read from the system.
type Adapter struct {
	Index       uint32 // the IPv4 interface index
	Index6      uint32 // the IPv6 interface index; the same for most adapters
	Name        string // what the user sees: "Wi-Fi", "Ethernet"
	Description string // what the driver calls it: "Wintun Userspace Tunnel"
	GUID        string // the adapter's permanent identity, "{...}"; a new adapter of the same name has another
	Metric4     uint32 // the interface's own metric, added to each route's
	Metric6     uint32
	Up          bool
	Kind        routecheck.Kind
	Addrs       []netip.Prefix
}

// Route is one entry of the routing table.
type Route struct {
	Dest    netip.Prefix
	Gateway netip.Addr // invalid for an on-link route
	// IfIndex is the interface index of the route's family.
	IfIndex uint32
	Metric  uint32
}

func (a Adapter) hasIndex(i uint32) bool { return i != 0 && (a.Index == i || a.Index6 == i) }

// adapterFor finds the adapter a route leaves through.
func adapterFor(adapters []Adapter, ifIndex uint32) (Adapter, bool) {
	for _, a := range adapters {
		if a.hasIndex(ifIndex) {
			return a, true
		}
	}
	return Adapter{}, false
}

// IsDefault reports whether the route is the one for everything.
func (r Route) IsDefault() bool { return r.Dest.Bits() == 0 && r.Dest.IsValid() }

// DefaultRoute picks the way out the system uses: of the default routes, those
// on an adapter that is up and is not ours, the one with the lowest total of
// route and adapter metric. IPv4 wins over IPv6, as it does in practice, so a
// machine whose IPv6 changes alone does not look like a new network.
func DefaultRoute(routes []Route, adapters []Adapter, ours func(Adapter) bool) (Route, Adapter, bool) {
	type cand struct {
		r      Route
		a      Adapter
		metric uint64
	}
	var v4, v6 []cand
	for _, r := range routes {
		if !r.IsDefault() {
			continue
		}
		a, ok := adapterFor(adapters, r.IfIndex)
		if !ok || !a.Up || (ours != nil && ours(a)) || a.Kind == routecheck.KindLoopback {
			continue
		}
		c := cand{r: r, a: a}
		if r.Dest.Addr().Is4() {
			c.metric = uint64(r.Metric) + uint64(a.Metric4)
			v4 = append(v4, c)
		} else {
			c.metric = uint64(r.Metric) + uint64(a.Metric6)
			v6 = append(v6, c)
		}
	}
	pool := v4
	if len(pool) == 0 {
		pool = v6
	}
	if len(pool) == 0 {
		return Route{}, Adapter{}, false
	}
	sort.SliceStable(pool, func(i, j int) bool {
		if pool[i].metric != pool[j].metric {
			return pool[i].metric < pool[j].metric
		}
		return pool[i].a.Index < pool[j].a.Index
	})
	return pool[0].r, pool[0].a, true
}

// Fingerprint names the current uplink: the adapter and its gateway. It
// changes when the machine moves to another network or another way out.
func Fingerprint(r Route, a Adapter) string {
	gw := "on-link"
	if r.Gateway.IsValid() && !r.Gateway.IsUnspecified() {
		gw = r.Gateway.String()
	}
	return a.Name + "|" + gw
}

// Snapshot converts what was read into what the route check judges. The
// adapter the agent made itself is marked, so it is never judged.
func Snapshot(routes []Route, adapters []Adapter, ours func(Adapter) bool) routecheck.Snapshot {
	var s routecheck.Snapshot
	if _, a, ok := DefaultRoute(routes, adapters, func(a Adapter) bool {
		return a.Kind != routecheck.KindPhysical || (ours != nil && ours(a))
	}); ok {
		s.PhysicalUplink = a.Name
	}
	for _, a := range adapters {
		s.Interfaces = append(s.Interfaces, routecheck.Interface{
			Name: a.Name, Index: int(a.Index), Up: a.Up, Kind: a.Kind, Addrs: a.Addrs,
			Ours: ours != nil && ours(a),
		})
	}
	for _, r := range routes {
		name := ""
		if a, ok := adapterFor(adapters, r.IfIndex); ok {
			name = a.Name
		}
		s.Routes = append(s.Routes, routecheck.Route{Dest: r.Dest, Gateway: r.Gateway, IfName: name})
	}
	return s
}
