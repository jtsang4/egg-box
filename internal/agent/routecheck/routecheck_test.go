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
