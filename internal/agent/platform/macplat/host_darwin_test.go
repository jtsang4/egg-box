//go:build darwin && !ios

package macplat

import (
	"syscall"
	"testing"
	"time"

	"eggbox/internal/agent/routecheck"
)

// These tests read the machine they run on and listen on a routing socket.
// They change nothing.

func TestSystemHostReadsThisMachine(t *testing.T) {
	h := SystemHost()
	ifaces, err := h.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	var loopback bool
	for _, i := range ifaces {
		if i.Name == "lo0" {
			loopback = i.Kind == routecheck.KindLoopback && i.Up
			var has bool
			for _, a := range i.Addrs {
				if a.Addr().IsLoopback() {
					has = true
				}
			}
			if !has {
				t.Error("lo0 has no loopback address")
			}
		}
	}
	if !loopback {
		t.Error("lo0 not found, or not a loopback interface that is up")
	}
}

func TestSystemHostRoutesAreSane(t *testing.T) {
	routes, err := SystemHost().Routes()
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) == 0 {
		t.Fatal("an empty routing table")
	}
	for _, r := range routes {
		if !r.Dest.IsValid() {
			t.Fatalf("invalid destination in %+v", r)
		}
		if r.Gateway.IsValid() && r.Gateway.Is4() != r.Dest.Addr().Is4() {
			t.Errorf("gateway %v of another family than %v", r.Gateway, r.Dest)
		}
		if r.Dest.Addr().Is6() && r.Dest.Addr().IsLinkLocalUnicast() {
			b := r.Dest.Addr().As16()
			if b[2] != 0 || b[3] != 0 {
				t.Errorf("the kernel's hidden scope was left in %v", r.Dest)
			}
		}
		if r.Dest.Addr() != r.Dest.Masked().Addr() {
			t.Errorf("destination %v has host bits set", r.Dest)
		}
	}
}

func TestSystemHostSnapshotAndUplinkFitTogether(t *testing.T) {
	h := SystemHost()
	routes, err := h.Routes()
	if err != nil {
		t.Fatal(err)
	}
	ifaces, err := h.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	snap := Snapshot(routes, ifaces, nil)
	if len(snap.Interfaces) != len(ifaces) {
		t.Errorf("%d interfaces became %d", len(ifaces), len(snap.Interfaces))
	}
	// Whether this machine is online is up to the machine; what has to hold is
	// that an answer never names an interface that is not there.
	if _, i, ok := Uplink(routes, ifaces, nil); ok && i.Name == "" {
		t.Error("an uplink without a name")
	}
	// The analysis must not take the system's own tunnels for another VPN: no
	// conflict may name an interface that does not carry default traffic. A
	// VPN carries it with the default route or with the halves it splits it
	// into (0/1 + 128/1, or 1/8 ... 128/1), as EggBox's own tunnel does: with
	// no ours given here, a connected EggBox is rightly another VPN.
	for _, c := range routecheck.Analyze(snap) {
		var held bool
		for _, r := range routes {
			if i, ok := interfaceByIndex(ifaces, r.IfIndex); ok && i.Name == c.Interface && r.Dest.IsValid() && r.Dest.Bits() <= 1 && !r.Scoped {
				held = true
			}
		}
		if !held {
			t.Errorf("conflict %v names an interface that carries no default traffic", c)
		}
	}
}

func TestSystemHostWatchStartsAndStops(t *testing.T) {
	stop, err := SystemHost().Watch(func() {})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { stop(); stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stopping the watcher blocked")
	}
}

func TestChangesNetwork(t *testing.T) {
	msg := func(typ int) []byte { return []byte{0x20, 0x00, 5, byte(typ), 0, 0, 0, 0} }
	for typ, want := range map[int]bool{
		syscall.RTM_ADD: true, syscall.RTM_DELETE: true, syscall.RTM_CHANGE: true,
		syscall.RTM_NEWADDR: true, syscall.RTM_DELADDR: true, syscall.RTM_IFINFO: true,
		syscall.RTM_GET: false, syscall.RTM_MISS: false, syscall.RTM_LOSING: false,
		syscall.RTM_RESOLVE: false, syscall.RTM_REDIRECT: false,
	} {
		if got := changesNetwork(msg(typ)); got != want {
			t.Errorf("message type %#x: changes network = %v, want %v", typ, got, want)
		}
	}
	if changesNetwork([]byte{1, 2}) {
		t.Error("a truncated message counted as a change")
	}
}
