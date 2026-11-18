package tunnel

import (
	"errors"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	tun "github.com/sagernet/sing-tun"
	"github.com/sagernet/sing/common/control"
)

// fakeHost is a phone that records what the core asked of it.
type fakeHost struct {
	mu        sync.Mutex
	opened    []Settings
	closed    int
	protected []int
	fd        int
	ifaces    []Interface
	watch     func(Default)
	stopped   bool
}

func (h *fakeHost) OpenTun(s Settings) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.opened = append(h.opened, s)
	return h.fd, nil
}

func (h *fakeHost) CloseTun() {
	h.mu.Lock()
	h.closed++
	h.mu.Unlock()
}

func (h *fakeHost) Protect(fd int) error {
	h.mu.Lock()
	h.protected = append(h.protected, fd)
	h.mu.Unlock()
	return nil
}

func (h *fakeHost) Interfaces() ([]Interface, error) { return h.ifaces, nil }

func (h *fakeHost) WatchDefault(f func(Default)) (func(), error) {
	h.watch = f
	f(Default{Name: "wlan0", Index: 30})
	return func() { h.stopped = true }, nil
}

func (h *fakeHost) Owner(int32, netip.AddrPort, netip.AddrPort) (Owner, error) {
	return Owner{UID: 10123, Packages: []string{"org.example.browser"}}, nil
}

func (h *fakeHost) UnderNetworkExtension() bool { return false }

func prefixes(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(ss))
	for _, s := range ss {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}

func TestSettingsCarryTheCoresAddressesRoutesAndResolver(t *testing.T) {
	o := &tun.Options{
		Inet4Address: prefixes("172.19.0.1/30"), Inet6Address: prefixes("fdfe:dcba:9876::1/126"),
		MTU: 9000, AutoRoute: true,
		Inet4RouteExcludeAddress: prefixes("192.168.0.0/16"),
	}
	s, err := SettingsOf(o)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(s.Addresses, prefixes("172.19.0.1/30", "fdfe:dcba:9876::1/126")) || s.MTU != 9000 {
		t.Fatalf("addresses %v, mtu %d", s.Addresses, s.MTU)
	}
	// Everything goes in, but the excluded range, and its own subnet stays reachable.
	in := func(a string) bool {
		addr := netip.MustParseAddr(a)
		for _, r := range s.Routes {
			if r.Contains(addr) {
				return true
			}
		}
		return false
	}
	for _, a := range []string{"1.1.1.1", "8.8.8.8", "2606:4700::1111"} {
		if !in(a) {
			t.Errorf("%s is not routed into the tunnel: %v", a, s.Routes)
		}
	}
	if in("192.168.1.10") {
		t.Errorf("the excluded range is routed into the tunnel: %v", s.Routes)
	}
	if !slices.Equal(s.ExcludedRoutes, prefixes("192.168.0.0/16")) {
		t.Errorf("excluded %v", s.ExcludedRoutes)
	}
	// The resolver is the address after the tunnel's own, which the core answers.
	if len(s.DNS) == 0 || s.DNS[0] != netip.MustParseAddr("172.19.0.2") {
		t.Errorf("dns %v", s.DNS)
	}
}

func TestSettingsWithoutAutoRouteOrDNS(t *testing.T) {
	s, err := SettingsOf(&tun.Options{Inet4Address: prefixes("172.19.0.1/30"), DNSMode: tun.DNSModeDisabled})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Routes) != 0 || len(s.DNS) != 0 {
		t.Fatalf("routes %v, dns %v", s.Routes, s.DNS)
	}
}

func TestPerUserOptionsAreRefused(t *testing.T) {
	p := New(&fakeHost{})
	_, err := p.OpenInterface(&tun.Options{IncludeAndroidUser: []int{10}}, option.TunPlatformOptions{})
	if err == nil {
		t.Fatal("per-user TUN options must be refused")
	}
}

func TestProtectReachesTheSystem(t *testing.T) {
	h := &fakeHost{}
	p := New(h)
	if !p.UsePlatformAutoDetectInterfaceControl() {
		t.Fatal("the core must protect its sockets through the platform")
	}
	if err := p.AutoDetectInterfaceControl(42); err != nil || !slices.Equal(h.protected, []int{42}) {
		t.Fatalf("protected %v, err %v", h.protected, err)
	}
}

func TestNetworkInterfacesDescribeTheDevicesNetworks(t *testing.T) {
	h := &fakeHost{ifaces: []Interface{
		{Name: "wlan0", Index: 30, MTU: 1500, Kind: "wifi", Addresses: prefixes("192.168.1.20/24"),
			DNS: []netip.Addr{netip.MustParseAddr("192.168.1.1")}, Gateways: []netip.Addr{netip.MustParseAddr("192.168.1.1")}},
		{Name: "rmnet0", Index: 12, Kind: "cellular", Metered: true},
		{Name: "wlan0", Index: 30}, // a network the system lists twice
		{Name: ""},
	}}
	p := New(h)
	list, err := p.NetworkInterfaces()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("got %d interfaces: %+v", len(list), list)
	}
	w, c := list[0], list[1]
	if w.Name != "wlan0" || w.Index != 30 || w.Type != C.InterfaceTypeWIFI || w.Expensive ||
		!slices.Equal(w.DNSServers, []string{"192.168.1.1"}) || len(w.Gateways) != 1 || len(w.Addresses) != 1 {
		t.Errorf("wifi %+v", w)
	}
	if c.Type != C.InterfaceTypeCellular || !c.Expensive {
		t.Errorf("cellular %+v", c)
	}
}

func TestDefaultNetworkChangesReachTheCore(t *testing.T) {
	h := &fakeHost{}
	p := New(h)
	m := p.CreateDefaultInterfaceMonitor(nil)
	var seen []string
	m.RegisterCallback(func(i *control.Interface, _ int) {
		if i == nil {
			seen = append(seen, "none")
			return
		}
		seen = append(seen, i.Name)
	})
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	if d := m.DefaultInterface(); d == nil || d.Name != "wlan0" || d.Index != 30 {
		t.Fatalf("default %+v", d)
	}
	h.watch(Default{Name: "wlan0", Index: 30}) // the same network again: nothing to tell
	h.watch(Default{Index: -1})                // offline
	h.watch(Default{Name: "rmnet0", Index: 12, Expensive: true})
	if want := []string{"wlan0", "none", "rmnet0"}; !slices.Equal(seen, want) {
		t.Fatalf("callbacks %v, want %v", seen, want)
	}
	if err := m.Close(); err != nil || !h.stopped {
		t.Fatalf("close: %v, stopped %v", err, h.stopped)
	}
}

func TestLossIsReportedOncePerTunnel(t *testing.T) {
	p := New(&fakeHost{})
	p.TunnelLost("turned off in the settings")
	p.TunnelLost("again") // a second report while the first is pending is dropped
	err := <-p.Lost()
	if err == nil || !strings.Contains(err.Error(), "turned off") {
		t.Fatalf("lost: %v", err)
	}
	p.TunnelLost("stale")
	p.ResetLost()
	select {
	case err := <-p.Lost():
		t.Fatalf("a loss of an earlier tunnel leaked into the next: %v", err)
	default:
	}
}

func TestOwnerNamesTheApplication(t *testing.T) {
	p := New(&fakeHost{})
	o, err := p.FindConnectionOwner(&adapterRequest)
	if err != nil {
		t.Fatal(err)
	}
	if o.UserId != 10123 || !slices.Equal(o.AndroidPackageNames, []string{"org.example.browser"}) {
		t.Fatalf("owner %+v", o)
	}
	bad := adapterRequest
	bad.SourceAddress = "not an address"
	if _, err := p.FindConnectionOwner(&bad); err == nil {
		t.Fatal("a malformed address must be an error")
	}
}

func TestUnsupportedFeaturesSayUnsupported(t *testing.T) {
	p := New(&fakeHost{})
	if p.UsePlatformShell() || p.UsePlatformBridge() || p.UsePlatformNotification() || p.UsePlatformNeighborResolver() {
		t.Fatal("phones offer no shell, bridge, notification or neighbour features to the core")
	}
	if _, err := p.LookupUser("root"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("lookup user: %v", err)
	}
}

var adapterRequest = adapter.FindConnectionOwnerRequest{
	IpProtocol: 6, SourceAddress: "172.19.0.1", SourcePort: 40000, DestinationAddress: "93.184.216.34", DestinationPort: 443,
}
