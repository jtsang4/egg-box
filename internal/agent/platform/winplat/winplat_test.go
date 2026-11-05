package winplat

import (
	"net/netip"
	"testing"

	"eggbox/internal/agent/platform"
	"eggbox/internal/agent/routecheck"
)

func TestClassifyAdapter(t *testing.T) {
	for _, c := range []struct {
		ifType            uint32
		name, description string
		want              routecheck.Kind
	}{
		{ifTypeLoopback, "Loopback Pseudo-Interface 1", "Software Loopback Interface 1", routecheck.KindLoopback},
		{ifTypeEthernet, "Ethernet", "Intel(R) Ethernet Connection I219-V", routecheck.KindPhysical},
		{ifTypeWiFi, "Wi-Fi", "Intel(R) Wi-Fi 6 AX201 160MHz", routecheck.KindPhysical},
		{ifTypeWWAN3GPP, "Cellular", "Mobile Broadband", routecheck.KindPhysical},
		{ifTypeEthernet, "Ethernet 2", "Wintun Userspace Tunnel", routecheck.KindTunnel},
		{ifTypeEthernet, "EggBox", "Wintun Userspace Tunnel", routecheck.KindTunnel},
		{ifTypeEthernet, "WireGuard", "WireGuard Tunnel", routecheck.KindTunnel},
		{ifTypeEthernet, "Local Area Connection", "TAP-Windows Adapter V9", routecheck.KindTunnel},
		{ifTypePPP, "Company VPN", "WAN Miniport (IKEv2)", routecheck.KindTunnel},
		{ifTypeTunnel, "Local Area Connection* 3", "Some Tunnel", routecheck.KindTunnel},
		{ifTypeTunnel, "Teredo Tunneling Pseudo-Interface", "Teredo Tunneling Pseudo-Interface", routecheck.KindVirtual},
		{ifTypeTunnel, "isatap.example", "Microsoft ISATAP Adapter", routecheck.KindVirtual},
		{ifTypeEthernet, "vEthernet (WSL)", "Hyper-V Virtual Ethernet Adapter", routecheck.KindVirtual},
		{ifTypeEthernet, "VMware Network Adapter VMnet8", "VMware Virtual Ethernet Adapter for VMnet8", routecheck.KindVirtual},
		{ifTypeEthernet, "Bluetooth Network Connection", "Bluetooth Device (Personal Area Network)", routecheck.KindVirtual},
		{1, "Mystery", "Something", routecheck.KindUnknown},
	} {
		if got := ClassifyAdapter(c.ifType, c.name, c.description); got != c.want {
			t.Errorf("%s / %s (type %d) = %s, want %s", c.name, c.description, c.ifType, got, c.want)
		}
	}
}

var (
	wifi = Adapter{Index: 12, Index6: 12, Name: "Wi-Fi", Metric4: 50, Metric6: 50, Up: true, Kind: routecheck.KindPhysical,
		Addrs: []netip.Prefix{netip.MustParsePrefix("192.168.1.20/24")}}
	eth = Adapter{Index: 7, Index6: 7, Name: "Ethernet", Metric4: 25, Metric6: 25, Up: true, Kind: routecheck.KindPhysical,
		Addrs: []netip.Prefix{netip.MustParsePrefix("10.0.0.5/24")}}
	tun = Adapter{Index: 31, Index6: 31, Name: "EggBox", Metric4: 1, Metric6: 1, Up: true, Kind: routecheck.KindTunnel}
)

func def(gw string, idx, metric uint32) Route {
	return Route{Dest: netip.MustParsePrefix("0.0.0.0/0"), Gateway: netip.MustParseAddr(gw), IfIndex: idx, Metric: metric}
}

func isOurs(a Adapter) bool { return a.Name == "EggBox" }

func TestDefaultRoutePrefersLowestTotalMetric(t *testing.T) {
	routes := []Route{def("192.168.1.1", 12, 0), def("10.0.0.1", 7, 0)}
	r, a, ok := DefaultRoute(routes, []Adapter{wifi, eth}, isOurs)
	if !ok || a.Name != "Ethernet" || r.Gateway.String() != "10.0.0.1" {
		t.Fatalf("got %+v %s %v, want Ethernet", r, a.Name, ok)
	}
	// A route metric tips the balance the other way.
	routes = []Route{def("192.168.1.1", 12, 0), def("10.0.0.1", 7, 100)}
	if _, a, _ = DefaultRoute(routes, []Adapter{wifi, eth}, isOurs); a.Name != "Wi-Fi" {
		t.Errorf("got %s, want Wi-Fi", a.Name)
	}
}

func TestDefaultRouteIgnoresOurTunnelAndDeadAdapters(t *testing.T) {
	// Once connected, the tunnel owns the best default route; the uplink is
	// still the physical one, so the fingerprint does not change on connect.
	routes := []Route{def("172.19.0.2", 31, 0), def("192.168.1.1", 12, 0)}
	_, a, ok := DefaultRoute(routes, []Adapter{wifi, tun}, isOurs)
	if !ok || a.Name != "Wi-Fi" {
		t.Fatalf("got %s, %v", a.Name, ok)
	}
	down := eth
	down.Up = false
	if _, a, _ = DefaultRoute([]Route{def("10.0.0.1", 7, 0), def("192.168.1.1", 12, 0)}, []Adapter{down, wifi}, isOurs); a.Name != "Wi-Fi" {
		t.Errorf("an adapter that is down was chosen: %s", a.Name)
	}
	if _, _, ok := DefaultRoute([]Route{def("172.19.0.2", 31, 0)}, []Adapter{tun}, isOurs); ok {
		t.Error("the tunnel alone is not a way out")
	}
	if _, _, ok := DefaultRoute(nil, []Adapter{wifi}, isOurs); ok {
		t.Error("no routes, no way out")
	}
}

func TestDefaultRouteIPv4BeforeIPv6(t *testing.T) {
	v6 := Route{Dest: netip.MustParsePrefix("::/0"), Gateway: netip.MustParseAddr("fe80::1"), IfIndex: 7, Metric: 0}
	r, a, ok := DefaultRoute([]Route{v6, def("192.168.1.1", 12, 0)}, []Adapter{wifi, eth}, isOurs)
	if !ok || a.Name != "Wi-Fi" || !r.Dest.Addr().Is4() {
		t.Errorf("got %+v %s", r, a.Name)
	}
	r, a, ok = DefaultRoute([]Route{v6}, []Adapter{wifi, eth}, isOurs)
	if !ok || a.Name != "Ethernet" || r.Dest.Addr().Is4() {
		t.Errorf("an IPv6-only network still has a way out: %+v %s %v", r, a.Name, ok)
	}
}

func TestFingerprintChangesWithTheNetwork(t *testing.T) {
	home := Fingerprint(def("192.168.1.1", 12, 0), wifi)
	cafe := Fingerprint(def("172.20.10.1", 12, 0), wifi)
	cable := Fingerprint(def("10.0.0.1", 7, 0), eth)
	if home == cafe || home == cable || cafe == cable {
		t.Errorf("fingerprints collide: %q %q %q", home, cafe, cable)
	}
	if home != Fingerprint(def("192.168.1.1", 12, 0), wifi) {
		t.Error("the fingerprint is not stable")
	}
	onlink := Route{Dest: netip.MustParsePrefix("0.0.0.0/0"), IfIndex: 12}
	if got := Fingerprint(onlink, wifi); got != "Wi-Fi|on-link" {
		t.Errorf("got %q", got)
	}
}

func TestSnapshotMarksOurAdapterAndNamesRoutes(t *testing.T) {
	routes := []Route{def("192.168.1.1", 12, 0), {Dest: netip.MustParsePrefix("172.19.0.0/30"), IfIndex: 31}}
	s := Snapshot(routes, []Adapter{wifi, tun}, isOurs)
	if len(s.Interfaces) != 2 || len(s.Routes) != 2 {
		t.Fatalf("%+v", s)
	}
	for _, i := range s.Interfaces {
		if i.Ours != (i.Name == "EggBox") {
			t.Errorf("%s: Ours = %v", i.Name, i.Ours)
		}
	}
	if s.Routes[0].IfName != "Wi-Fi" || s.Routes[1].IfName != "EggBox" {
		t.Errorf("route names: %+v", s.Routes)
	}
}

func TestSameGUIDIgnoresCaseAndBraces(t *testing.T) {
	if !SameGUID("{0AB1-22}", "0ab1-22") {
		t.Error("case and braces must not matter")
	}
	if SameGUID("{0AB1-22}", "{0AB1-23}") {
		t.Error("different GUIDs compared equal")
	}
}

func TestIsWintun(t *testing.T) {
	for desc, want := range map[string]bool{
		"Wintun Userspace Tunnel": true,
		"sing-tun Tunnel":         true,
		"WireGuard Tunnel":        false,
		"TAP-Windows Adapter V9":  false,
		"":                        false,
	} {
		if got := IsWintun(desc); got != want {
			t.Errorf("IsWintun(%q) = %v", desc, got)
		}
	}
}

func TestPowerEvent(t *testing.T) {
	for in, want := range map[uint32]platform.EventKind{
		0x4:  platform.Slept,
		0x12: platform.Woke,
		0x7:  platform.Woke,
		0x6:  platform.Woke,
	} {
		if got, ok := PowerEvent(in); !ok || got != want {
			t.Errorf("PowerEvent(%#x) = %v, %v; want %v", in, got, ok, want)
		}
	}
	for _, in := range []uint32{0xA /* power status */, 0x8013 /* setting */, 0} {
		if _, ok := PowerEvent(in); ok {
			t.Errorf("PowerEvent(%#x) should be ignored", in)
		}
	}
}
