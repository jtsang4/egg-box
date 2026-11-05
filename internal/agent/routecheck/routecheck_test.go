package routecheck

import (
	"net/netip"
	"testing"
)

func pfx(s string) netip.Prefix { return netip.MustParsePrefix(s) }

func physical() Snapshot {
	return Snapshot{
		Interfaces: []Interface{
			{Name: "lo0", Up: true, Kind: KindLoopback, Addrs: []netip.Prefix{pfx("127.0.0.1/8")}},
			{Name: "en0", Up: true, Kind: KindPhysical, Addrs: []netip.Prefix{pfx("192.168.1.20/24")}},
		},
		Routes: []Route{
			{Dest: pfx("0.0.0.0/0"), Gateway: netip.MustParseAddr("192.168.1.1"), IfName: "en0"},
			{Dest: pfx("192.168.1.0/24"), IfName: "en0"},
		},
	}
}

func TestChoosePlanPicksFirstFreeCandidate(t *testing.T) {
	plan, ok := ChoosePlan(physical(), DefaultCandidates())
	if !ok || plan.V4 != pfx("172.19.0.1/30") {
		t.Fatalf("plan = %+v ok=%v", plan, ok)
	}
	if plan.DNS() != netip.MustParseAddr("172.19.0.2") {
		t.Fatalf("dns = %s", plan.DNS())
	}
}

func TestChoosePlanSkipsOverlappingAddress(t *testing.T) {
	snap := physical()
	snap.Interfaces = append(snap.Interfaces, Interface{
		Name: "br0", Up: true, Kind: KindVirtual, Addrs: []netip.Prefix{pfx("172.19.0.77/24")},
	})
	plan, ok := ChoosePlan(snap, DefaultCandidates())
	if !ok || plan.V4 != pfx("172.29.0.1/30") {
		t.Fatalf("plan = %+v ok=%v", plan, ok)
	}

	// A neighbouring /24 that does not contain the candidate must not matter.
	snap.Interfaces[len(snap.Interfaces)-1].Addrs = []netip.Prefix{pfx("172.19.5.1/24")}
	plan, ok = ChoosePlan(snap, DefaultCandidates())
	if !ok || plan.V4 != pfx("172.19.0.1/30") {
		t.Fatalf("plan = %+v ok=%v", plan, ok)
	}
}

func TestChoosePlanSkipsOverlappingRoute(t *testing.T) {
	snap := physical()
	snap.Routes = append(snap.Routes, Route{Dest: pfx("172.16.0.0/12"), IfName: "en0"})
	// 172.19/172.29 both sit inside 172.16/12, so the first usable candidate is outside it.
	plan, ok := ChoosePlan(snap, DefaultCandidates())
	if !ok || plan.V4 != pfx("10.211.77.1/30") {
		t.Fatalf("plan = %+v ok=%v", plan, ok)
	}
}

func TestChoosePlanChecksIPv6Too(t *testing.T) {
	snap := physical()
	snap.Interfaces = append(snap.Interfaces, Interface{
		Name: "en1", Up: true, Kind: KindPhysical, Addrs: []netip.Prefix{pfx("fdfe:dcba:9876::55/64")},
	})
	plan, ok := ChoosePlan(snap, DefaultCandidates())
	if !ok || plan.V4 != pfx("172.29.0.1/30") {
		t.Fatalf("plan = %+v ok=%v", plan, ok)
	}
}

func TestChoosePlanFailsWhenEverythingCollides(t *testing.T) {
	snap := physical()
	snap.Routes = append(snap.Routes,
		Route{Dest: pfx("172.16.0.0/12"), IfName: "en0"},
		Route{Dest: pfx("10.0.0.0/8"), IfName: "en0"},
		Route{Dest: pfx("100.64.0.0/10"), IfName: "en0"},
	)
	if _, ok := ChoosePlan(snap, DefaultCandidates()); ok {
		t.Fatal("expected no free subnet")
	}
}

func TestOurOwnInterfaceNeverCounts(t *testing.T) {
	// A leftover of our own TUN holds the first candidate: it must not push us to a new subnet
	// (the leftover is cleaned up separately, and reusing the subnet is the stable choice).
	snap := physical()
	snap.Interfaces = append(snap.Interfaces, Interface{
		Name: "utun9", Up: true, Kind: KindTunnel, Ours: true, Addrs: []netip.Prefix{pfx("172.19.0.1/30")},
	})
	snap.Routes = append(snap.Routes, Route{Dest: pfx("172.19.0.0/30"), IfName: "utun9"})
	plan, ok := ChoosePlan(snap, DefaultCandidates())
	if !ok || plan.V4 != pfx("172.19.0.1/30") {
		t.Fatalf("plan = %+v ok=%v", plan, ok)
	}
}

func TestDefaultRoutesDoNotCountAsOverlap(t *testing.T) {
	// Every prefix overlaps 0.0.0.0/0, which would otherwise rule out all candidates.
	snap := physical()
	if _, ok := ChoosePlan(snap, DefaultCandidates()); !ok {
		t.Fatal("a plain default route must not block planning")
	}
}

func TestAnalyzeFlagsForeignTunnelWithDefaultRoute(t *testing.T) {
	snap := physical()
	snap.Interfaces = append(snap.Interfaces, Interface{Name: "utun4", Up: true, Kind: KindTunnel})
	snap.Routes = append(snap.Routes,
		Route{Dest: pfx("0.0.0.0/1"), IfName: "utun4"},
		Route{Dest: pfx("128.0.0.0/1"), IfName: "utun4"},
	)
	got := Analyze(snap)
	if len(got) != 1 || got[0].Kind != ConflictForeignDefaultRoute || got[0].Interface != "utun4" {
		t.Fatalf("conflicts = %+v", got)
	}
}

func TestAnalyzeHandlesIPv6SplitDefault(t *testing.T) {
	snap := physical()
	snap.Interfaces = append(snap.Interfaces, Interface{Name: "wg0", Up: true, Kind: KindTunnel})
	snap.Routes = append(snap.Routes, Route{Dest: pfx("8000::/1"), IfName: "wg0"})
	if got := Analyze(snap); len(got) != 1 || got[0].Interface != "wg0" {
		t.Fatalf("conflicts = %+v", got)
	}
}

func TestAnalyzeIgnoresHarmlessCases(t *testing.T) {
	snap := physical()
	snap.Interfaces = append(snap.Interfaces,
		Interface{Name: "utun1", Up: true, Kind: KindTunnel},             // tailscale-like: specific routes only
		Interface{Name: "utun2", Up: false, Kind: KindTunnel},            // down
		Interface{Name: "utun3", Up: true, Kind: KindTunnel, Ours: true}, // ours
		Interface{Name: "bridge0", Up: true, Kind: KindVirtual},          // not a tunnel
	)
	snap.Routes = append(snap.Routes,
		Route{Dest: pfx("100.64.0.0/10"), IfName: "utun1"},
		Route{Dest: pfx("0.0.0.0/1"), IfName: "utun2"},
		Route{Dest: pfx("0.0.0.0/1"), IfName: "utun3"},
		Route{Dest: pfx("0.0.0.0/0"), IfName: "bridge0"},
		Route{Dest: pfx("0.0.0.0/0"), IfName: "ghost"}, // unknown interface
	)
	if got := Analyze(snap); len(got) != 0 {
		t.Fatalf("expected no conflicts, got %+v", got)
	}
}

func TestAnalyzeReportsEachTunnelOnce(t *testing.T) {
	snap := physical()
	snap.Interfaces = append(snap.Interfaces,
		Interface{Name: "utun7", Up: true, Kind: KindTunnel},
		Interface{Name: "utun5", Up: true, Kind: KindTunnel},
	)
	snap.Routes = append(snap.Routes,
		Route{Dest: pfx("0.0.0.0/1"), IfName: "utun7"},
		Route{Dest: pfx("128.0.0.0/1"), IfName: "utun7"},
		Route{Dest: pfx("::/1"), IfName: "utun7"},
		Route{Dest: pfx("0.0.0.0/0"), IfName: "utun5"},
	)
	got := Analyze(snap)
	if len(got) != 2 || got[0].Interface != "utun5" || got[1].Interface != "utun7" {
		t.Fatalf("conflicts = %+v", got)
	}
}

func TestClassifyByName(t *testing.T) {
	cases := map[string]Kind{
		"lo0": KindLoopback, "lo": KindLoopback,
		"utun3": KindTunnel, "ppp0": KindTunnel, "wg0": KindTunnel, "tun0": KindTunnel, "Tailscale": KindTunnel,
		"WireGuard Tunnel": KindTunnel, "OpenVPN TAP-Windows6 VPN": KindTunnel,
		"bridge0": KindVirtual, "awdl0": KindVirtual, "vmnet8": KindVirtual, "docker0": KindVirtual,
		"en0": KindPhysical, "eth0": KindPhysical, "Wi-Fi": KindPhysical, "Ethernet": KindPhysical,
		"mystery": KindUnknown,
	}
	for name, want := range cases {
		if got := ClassifyByName(name, false, false); got != want {
			t.Errorf("ClassifyByName(%q) = %s, want %s", name, got, want)
		}
	}
	if ClassifyByName("mystery", true, false) != KindTunnel {
		t.Error("an unknown point-to-point interface should be a tunnel")
	}
}

func TestChoosePlanToleratesBroadBypassRoutesThroughAGateway(t *testing.T) {
	// A VPN app sends every private range around its tunnel through the router.
	snap := physical()
	gw := netip.MustParseAddr("192.0.2.1")
	snap.Routes = append(snap.Routes,
		Route{Dest: pfx("172.16.0.0/12"), Gateway: gw, IfName: "en0"},
		Route{Dest: pfx("10.0.0.0/8"), Gateway: gw, IfName: "en0"},
		Route{Dest: pfx("100.64.0.0/10"), Gateway: gw, IfName: "en0"},
	)
	plan, ok := ChoosePlan(snap, DefaultCandidates())
	if !ok || plan.V4 != pfx("172.19.0.1/30") {
		t.Fatalf("plan = %+v ok=%v", plan, ok)
	}
	// A completely free candidate still wins over shadowing a bypass route.
	snap.Routes = snap.Routes[:len(snap.Routes)-1]
	plan, ok = ChoosePlan(snap, DefaultCandidates())
	if !ok || plan.V4 != pfx("100.127.250.1/30") {
		t.Fatalf("plan = %+v ok=%v", plan, ok)
	}
}

func TestChoosePlanNeverShadowsSpecificGatewayRoutes(t *testing.T) {
	snap := physical()
	gw := netip.MustParseAddr("192.0.2.1")
	for _, c := range DefaultCandidates() {
		snap.Routes = append(snap.Routes, Route{Dest: c.V4.Masked(), Gateway: gw, IfName: "en0"})
	}
	if _, ok := ChoosePlan(snap, DefaultCandidates()); ok {
		t.Fatal("a route as specific as the candidate must rule it out")
	}
}

func TestTakeoverRoutesOutrankAForeignSplitDefault(t *testing.T) {
	snap := physical()
	snap.Interfaces = append(snap.Interfaces, Interface{Name: "utun4", Up: true, Kind: KindTunnel})
	for _, p := range []string{"0.0.0.0/0", "1.0.0.0/8", "2.0.0.0/7", "4.0.0.0/6", "8.0.0.0/5", "16.0.0.0/4",
		"32.0.0.0/3", "64.0.0.0/2", "128.0.0.0/1", "198.18.0.0/15", "::/1", "8000::/1"} {
		snap.Routes = append(snap.Routes, Route{Dest: pfx(p), IfName: "utun4"})
	}
	// A broad route through the physical uplink is not a competitor.
	snap.Routes = append(snap.Routes, Route{Dest: pfx("10.0.0.0/8"), Gateway: netip.MustParseAddr("192.0.2.1"), IfName: "en0"})
	routes := TakeoverRoutes(snap)

	for _, dest := range []string{"0.0.0.1", "1.1.1.1", "3.3.3.3", "40.0.0.1", "63.255.255.255", "90.0.0.1",
		"127.0.0.1", "150.1.1.1", "198.18.0.10", "220.0.0.1", "255.255.255.255", "10.1.1.1",
		"::1", "2001:db8::1", "7fff::1", "8001::1", "ffff::1"} {
		addr := netip.MustParseAddr(dest)
		var best netip.Prefix
		matches := 0
		for _, r := range routes {
			if r.Contains(addr) {
				matches++
				best = r
			}
		}
		if matches != 1 {
			t.Fatalf("%s is covered %d times by %v", dest, matches, routes)
		}
		for _, f := range snap.Routes {
			if f.IfName == "utun4" && f.Dest.Contains(addr) && f.Dest.Bits() <= 8 && f.Dest.Bits() >= best.Bits() {
				t.Fatalf("%s: %s does not outrank %s", dest, best, f.Dest)
			}
		}
		if best.Bits() < 2 {
			t.Fatalf("%s: %s is not more specific than a split default", dest, best)
		}
	}
	if len(routes) > 40 {
		t.Fatalf("too many routes: %d", len(routes))
	}
}

func TestTakeoverRoutesWithoutCompetitorsAreQuarters(t *testing.T) {
	got := TakeoverRoutes(physical())
	want := []string{"0.0.0.0/2", "64.0.0.0/2", "128.0.0.0/2", "192.0.0.0/2", "::/2", "4000::/2", "8000::/2", "c000::/2"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != pfx(want[i]) {
			t.Fatalf("got %v", got)
		}
	}
}
