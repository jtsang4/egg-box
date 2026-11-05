package macplat

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"testing"
	"time"

	"eggbox/internal/agent/ledger"
	"eggbox/internal/agent/platform"
	"eggbox/internal/agent/routecheck"
)

func addr(s string) netip.Addr     { return netip.MustParseAddr(s) }
func prefix(s string) netip.Prefix { return netip.MustParsePrefix(s) }

// ------------------------------------------------------------ route decoding

func TestRawRouteDecoding(t *testing.T) {
	const up = rtfUp
	cases := []struct {
		name string
		raw  RawRoute
		want string // "" when the route must be dropped
		gw   string
	}{
		{"default through a gateway", RawRoute{Dest: addr("0.0.0.0"), Mask: addr("0.0.0.0"), Gateway: addr("192.0.2.1"), Flags: up | rtfGateway, IfIndex: 4}, "0.0.0.0/0", "192.0.2.1"},
		{"a network", RawRoute{Dest: addr("198.51.100.0"), Mask: addr("255.255.255.0"), Flags: up, IfIndex: 4}, "198.51.100.0/24", ""},
		{"a host", RawRoute{Dest: addr("198.51.100.7"), Flags: up | rtfHost, IfIndex: 4}, "198.51.100.7/32", ""},
		{"an odd mask length", RawRoute{Dest: addr("203.0.113.0"), Mask: addr("255.255.255.240"), Flags: up}, "203.0.113.0/28", ""},
		{"host bits are cleared", RawRoute{Dest: addr("203.0.113.9"), Mask: addr("255.255.255.0"), Flags: up}, "203.0.113.0/24", ""},
		{"ipv6 default", RawRoute{Dest: addr("::"), Mask: addr("::"), Gateway: addr("fe80::1"), Flags: up | rtfGateway}, "::/0", "fe80::1"},
		{"the scope hidden in a link-local gateway is removed", RawRoute{Dest: addr("::"), Mask: addr("::"), Gateway: addr("fe80:6::1"), Flags: up | rtfGateway}, "::/0", "fe80::1"},
		{"the scope hidden in a link-local network is removed", RawRoute{Dest: addr("fe80:6::"), Mask: addr("ffff:ffff:ffff:ffff::"), Flags: up}, "fe80::/64", ""},
		{"a gateway flag without a gateway is on-link", RawRoute{Dest: addr("0.0.0.0"), Mask: addr("0.0.0.0"), Flags: up | rtfGateway}, "0.0.0.0/0", ""},
		{"a link gateway is on-link", RawRoute{Dest: addr("0.0.0.0"), Mask: addr("0.0.0.0"), Flags: up}, "0.0.0.0/0", ""},

		{"down", RawRoute{Dest: addr("0.0.0.0"), Mask: addr("0.0.0.0"), Flags: rtfGateway}, "", ""},
		{"reject", RawRoute{Dest: addr("198.51.100.0"), Mask: addr("255.255.255.0"), Flags: up | rtfReject}, "", ""},
		{"blackhole", RawRoute{Dest: addr("198.51.100.0"), Mask: addr("255.255.255.0"), Flags: up | rtfBlackhole}, "", ""},
		{"neighbour cache", RawRoute{Dest: addr("198.51.100.7"), Flags: up | rtfHost | rtfLLInfo}, "", ""},
		{"cloned host", RawRoute{Dest: addr("198.51.100.8"), Flags: up | rtfHost | rtfWasCloned}, "", ""},
		{"multicast", RawRoute{Dest: addr("224.0.0.0"), Mask: addr("240.0.0.0"), Flags: up | rtfMulticast}, "", ""},
		{"broadcast", RawRoute{Dest: addr("255.255.255.255"), Flags: up | rtfHost | rtfBroadcast}, "", ""},
		{"a mask that is not a prefix", RawRoute{Dest: addr("198.51.100.0"), Mask: addr("255.0.255.0"), Flags: up}, "", ""},
		{"a mask of the wrong family", RawRoute{Dest: addr("198.51.100.0"), Mask: addr("ffff::"), Flags: up}, "", ""},
		{"no mask for a network", RawRoute{Dest: addr("198.51.100.0"), Flags: up}, "", ""},
		{"no destination", RawRoute{Mask: addr("0.0.0.0"), Flags: up}, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := c.raw.Route()
			if c.want == "" {
				if ok {
					t.Fatalf("kept as %v", got.Dest)
				}
				return
			}
			if !ok {
				t.Fatal("dropped")
			}
			if got.Dest != prefix(c.want) {
				t.Errorf("dest = %v, want %s", got.Dest, c.want)
			}
			var wantGW netip.Addr
			if c.gw != "" {
				wantGW = addr(c.gw)
			}
			if got.Gateway != wantGW {
				t.Errorf("gateway = %v, want %v", got.Gateway, wantGW)
			}
		})
	}
}

func TestScopedFlagIsKept(t *testing.T) {
	r, ok := RawRoute{Dest: addr("0.0.0.0"), Mask: addr("0.0.0.0"), Gateway: addr("192.0.2.1"), Flags: rtfUp | rtfGateway | rtfIfScope}.Route()
	if !ok || !r.Scoped {
		t.Errorf("scoped default lost its scope: %+v %v", r, ok)
	}
}

// ------------------------------------------------------------------ the uplink

func iface(index int, name string, up bool, addrs ...string) Interface {
	i := Interface{Index: index, Name: name, Up: up, Kind: routecheck.ClassifyByName(name, false, name == "lo0")}
	for _, a := range addrs {
		i.Addrs = append(i.Addrs, prefix(a))
	}
	return i
}

func def(index int, gateway string, scoped bool) Route {
	r := Route{IfIndex: index, Scoped: scoped}
	if gateway == "" || addr(gateway).Is4() { // no gateway: an on-link IPv4 default
		r.Dest = prefix("0.0.0.0/0")
	} else {
		r.Dest = prefix("::/0")
	}
	if gateway != "" {
		r.Gateway = addr(gateway)
	}
	return r
}

// homeNetwork is a laptop on Wi-Fi.
func homeNetwork() ([]Route, []Interface) {
	ifaces := []Interface{
		iface(1, "lo0", true, "127.0.0.1/8"),
		iface(6, "en0", true, "192.0.2.20/24"),
		iface(7, "awdl0", true),
	}
	routes := []Route{
		def(6, "192.0.2.1", false),
		def(6, "fe80::1", false),
		{Dest: prefix("192.0.2.0/24"), IfIndex: 6},
	}
	return routes, ifaces
}

func TestUplinkOnAHomeNetwork(t *testing.T) {
	routes, ifaces := homeNetwork()
	r, i, ok := Uplink(routes, ifaces, nil)
	if !ok || i.Name != "en0" || r.Gateway != addr("192.0.2.1") {
		t.Fatalf("got %+v %+v %v", r, i, ok)
	}
	if got := Fingerprint(r, i); got != "en0|192.0.2.1" {
		t.Errorf("fingerprint = %s", got)
	}
}

func TestUplinkChangesWithTheNetwork(t *testing.T) {
	routes, ifaces := homeNetwork()
	r, i, _ := Uplink(routes, ifaces, nil)
	before := Fingerprint(r, i)

	// The same interface, another router: another network.
	routes[0] = def(6, "198.51.100.1", false)
	r, i, _ = Uplink(routes, ifaces, nil)
	if Fingerprint(r, i) == before {
		t.Error("a new gateway did not change the fingerprint")
	}
}

func TestUplinkIgnoresIPv6Alone(t *testing.T) {
	routes, ifaces := homeNetwork()
	a, ai, _ := Uplink(routes, ifaces, nil)
	// The IPv6 router changes; IPv4 stays.
	routes[1] = def(6, "fe80::2", false)
	b, bi, _ := Uplink(routes, ifaces, nil)
	if Fingerprint(a, ai) != Fingerprint(b, bi) {
		t.Error("a change of IPv6 alone looked like a new network")
	}
	// With no IPv4 default, IPv6 is what there is.
	routes = routes[1:]
	if r, _, ok := Uplink(routes, ifaces, nil); !ok || !r.Dest.Addr().Is6() {
		t.Errorf("IPv6-only uplink not found: %v %v", r, ok)
	}
}

func TestUplinkIsOfflineWithoutADefaultRouteOrALink(t *testing.T) {
	routes, ifaces := homeNetwork()
	if _, _, ok := Uplink(nil, ifaces, nil); ok {
		t.Error("an uplink without routes")
	}
	ifaces[1].Up = false
	if _, _, ok := Uplink(routes, ifaces, nil); ok {
		t.Error("an uplink through an interface with no link")
	}
}

// anotherVPNRunning is the machine as the kernel really lays it out when some
// other VPN program has taken the way out: its tunnel holds the system's
// default, the real uplink's default is kept aside, scoped, and the system's
// own tunnels carry scoped IPv6 defaults of their own.
func anotherVPNRunning() ([]Route, []Interface) {
	ifaces := []Interface{
		iface(1, "lo0", true, "127.0.0.1/8"),
		iface(6, "en0", true, "192.0.2.20/24"),
		iface(21, "utun0", true, "fe80::1/64"), // the system's
		iface(22, "utun1", true, "fe80::2/64"), // the system's
		iface(25, "utun4", true, "198.18.0.1/16"),
	}
	routes := []Route{
		def(25, "", false),        // the other VPN: on-link, the system's default
		def(6, "192.0.2.1", true), // the physical uplink, scoped
		def(6, "fe80::1", true),   // ... in IPv6
		def(21, "fe80::1", true),  // system tunnels: scoped IPv6 defaults
		def(22, "fe80::1", true),  //
		{Dest: prefix("192.0.2.0/24"), IfIndex: 6},
	}
	return routes, ifaces
}

func TestUplinkIsThePhysicalNetworkWhenAnotherVPNHoldsTheDefault(t *testing.T) {
	routesForSnapshot, interfacesForSnapshot := anotherVPNRunning()
	if got := Snapshot(routesForSnapshot, interfacesForSnapshot, nil).PhysicalUplink; got != "en0" {
		t.Fatalf("snapshot lost the scoped physical uplink: %s", got)
	}
	routes, ifaces := anotherVPNRunning()
	r, i, ok := Uplink(routes, ifaces, nil)
	if !ok || i.Name != "en0" {
		t.Fatalf("the uplink is %s, want en0 (ok=%v)", i.Name, ok)
	}
	if !r.Scoped {
		t.Error("the answer should be the scoped default of the physical interface")
	}
}

func TestUplinkIsTheTunnelWhenNothingElseIsThere(t *testing.T) {
	routes, ifaces := anotherVPNRunning()
	routes = []Route{routes[0]}
	_, i, ok := Uplink(routes, ifaces, nil)
	if !ok || i.Name != "utun4" {
		t.Errorf("got %s %v", i.Name, ok)
	}
}

func TestUplinkNeverCountsOurOwnTunnel(t *testing.T) {
	routes, ifaces := homeNetwork()
	ifaces = append(ifaces, iface(30, "utun9", true, "172.19.0.1/30"))
	routes = append(routes, def(30, "", false)) // as if the core had taken the default
	ours := func(i Interface) bool { return i.Name == "utun9" }
	if _, i, _ := Uplink(routes, ifaces, ours); i.Name != "en0" {
		t.Errorf("the uplink is %s", i.Name)
	}
	if _, _, ok := Uplink(routes[3:], ifaces, ours); ok {
		t.Error("our own tunnel alone counted as a way out")
	}
}

// -------------------------------------------------------- what the route check sees

func TestSnapshotLeavesOutScopedRoutes(t *testing.T) {
	routes, ifaces := anotherVPNRunning()
	snap := Snapshot(routes, ifaces, nil)
	for _, r := range snap.Routes {
		if r.IfName == "utun0" || r.IfName == "utun1" {
			t.Errorf("the system's own tunnel %s appears with %v", r.IfName, r.Dest)
		}
	}
}

func TestAnotherVPNIsAConflictButTheSystemsOwnTunnelsAreNot(t *testing.T) {
	routes, ifaces := anotherVPNRunning()
	conflicts := routecheck.Analyze(Snapshot(routes, ifaces, nil))
	if len(conflicts) != 1 || conflicts[0].Interface != "utun4" || conflicts[0].Kind != routecheck.ConflictForeignDefaultRoute {
		t.Fatalf("conflicts = %v, want exactly the other VPN", conflicts)
	}

	// On a quiet machine there is none, even with the system's tunnels around.
	quiet := []Route{def(6, "192.0.2.1", false), def(21, "fe80::1", true), def(22, "fe80::1", true)}
	if c := routecheck.Analyze(Snapshot(quiet, ifaces, nil)); len(c) != 0 {
		t.Errorf("a quiet machine has conflicts: %v", c)
	}
}

func TestSnapshotMarksOurTunnel(t *testing.T) {
	routes, ifaces := homeNetwork()
	ifaces = append(ifaces, iface(30, "utun9", true, "172.19.0.1/30"))
	snap := Snapshot(routes, ifaces, func(i Interface) bool { return i.Name == "utun9" })
	var found bool
	for _, i := range snap.Interfaces {
		if i.Name == "utun9" {
			found = i.Ours
		}
	}
	if !found {
		t.Error("our tunnel was not marked")
	}
}

// --------------------------------------------------------------- fake machine

type fakeHost struct {
	mu       sync.Mutex
	ifaces   []Interface
	routes   []Route
	notify   func()
	watching bool
}

func (h *fakeHost) Interfaces() ([]Interface, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Interface(nil), h.ifaces...), nil
}
func (h *fakeHost) Routes() ([]Route, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Route(nil), h.routes...), nil
}
func (h *fakeHost) Watch(notify func()) (func(), error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.notify, h.watching = notify, true
	return func() { h.mu.Lock(); h.watching = false; h.mu.Unlock() }, nil
}
func (h *fakeHost) set(ifaces []Interface) { h.mu.Lock(); h.ifaces = ifaces; h.mu.Unlock() }

type fakeSystem struct {
	mu        sync.Mutex
	resolvers map[string]string
	servers   map[string][]netip.Addr
	setErr    error
	removeErr error
	atSet     func() // called inside SetResolver, to see what was recorded by then

	uid       uint32
	uidOK     bool
	consoleFn func(uint32, bool)
	powerFn   func(bool)
	stopped   int
}

func newFakeSystem() *fakeSystem {
	return &fakeSystem{resolvers: map[string]string{}, servers: map[string][]netip.Addr{}}
}

func (s *fakeSystem) SetResolver(key string, servers []netip.Addr, token string) error {
	if s.atSet != nil {
		s.atSet()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.setErr != nil {
		return s.setErr
	}
	s.resolvers[key], s.servers[key] = token, servers
	return nil
}
func (s *fakeSystem) ResolverToken(key string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.resolvers[key]
	return t, ok
}
func (s *fakeSystem) RemoveResolver(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.removeErr != nil {
		return s.removeErr
	}
	delete(s.resolvers, key)
	return nil
}
func (s *fakeSystem) ConsoleUser() (uint32, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.uid, s.uidOK
}
func (s *fakeSystem) WatchConsole(fn func(uint32, bool)) (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.consoleFn = fn
	return func() { s.mu.Lock(); s.stopped++; s.mu.Unlock() }, nil
}
func (s *fakeSystem) WatchPower(fn func(bool)) (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.powerFn = fn
	return func() { s.mu.Lock(); s.stopped++; s.mu.Unlock() }, nil
}

type fakeRecorder struct {
	mu      sync.Mutex
	entries map[string]ledger.Entry
	addErr  map[string]error
}

func newRecorder() *fakeRecorder {
	return &fakeRecorder{entries: map[string]ledger.Entry{}, addErr: map[string]error{}}
}
func (r *fakeRecorder) Add(e ledger.Entry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.addErr[e.ID]; err != nil {
		return err
	}
	r.entries[e.ID] = e
	return nil
}
func (r *fakeRecorder) Remove(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.entries, id)
	return nil
}
func (r *fakeRecorder) count() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.entries) }

const testKey = "State:/Network/Service/test.eggbox/DNS"

func newTestDriver(t *testing.T) (*Driver, *fakeHost, *fakeSystem) {
	t.Helper()
	routes, ifaces := homeNetwork()
	h := &fakeHost{ifaces: ifaces, routes: routes}
	s := newFakeSystem()
	d, err := New(Options{
		System: s, Host: h, ResolverKey: testKey,
		AppearWait: 150 * time.Millisecond, GoneWait: 150 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d, h, s
}

func withTun(h *fakeHost, name string, addrs ...string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ifaces = append(h.ifaces, iface(40, name, true, addrs...))
}

func activeTun(name string) platform.ActiveTun {
	return platform.ActiveTun{
		Interface: name,
		Addresses: []netip.Prefix{prefix("172.19.0.1/30"), prefix("fdfe:dcba:9876::1/126")},
		DNS:       addr("172.19.0.2"),
	}
}

// ---------------------------------------------------------------- the driver

func TestDriverDefaults(t *testing.T) {
	d, _, _ := newTestDriver(t)
	if d.Name() != "darwin" || d.Sandbox() {
		t.Errorf("name %q sandbox %v", d.Name(), d.Sandbox())
	}
	if td := d.TunDefaults(); td.DNSMode != "disabled" || td.InterfaceName != "" || td.StrictRoute {
		t.Errorf("tun defaults %+v", td)
	}
	if _, err := New(Options{}); err == nil {
		t.Error("a driver with no system was accepted")
	}
}

func TestDriverReadsTheNetwork(t *testing.T) {
	d, _, _ := newTestDriver(t)
	ctx := context.Background()
	if !d.Online(ctx) {
		t.Error("offline on a home network")
	}
	fp, err := d.Fingerprint(ctx)
	if err != nil || fp != "en0|192.0.2.1" {
		t.Errorf("fingerprint %q, %v", fp, err)
	}
	snap, err := d.Snapshot(ctx)
	if err != nil || len(snap.Interfaces) == 0 || len(snap.Routes) == 0 {
		t.Errorf("snapshot %+v, %v", snap, err)
	}
}

func TestDriverIsOfflineWithoutRoutes(t *testing.T) {
	d, h, _ := newTestDriver(t)
	h.mu.Lock()
	h.routes = nil
	h.mu.Unlock()
	if d.Online(context.Background()) {
		t.Error("online with no routes")
	}
	if _, err := d.Fingerprint(context.Background()); err == nil {
		t.Error("a fingerprint with no uplink")
	}
}

func TestActivateSetsTheResolverAndRecordsBothBeforeActing(t *testing.T) {
	d, h, s := newTestDriver(t)
	withTun(h, "utun9", "172.19.0.1/30", "fdfe:dcba:9876::1/126")
	rec := newRecorder()
	var recordedAtSet int
	s.atSet = func() { recordedAtSet = rec.count() }

	if err := d.Activate(context.Background(), activeTun("utun9"), rec); err != nil {
		t.Fatal(err)
	}
	if recordedAtSet != 2 {
		t.Errorf("%d resources were recorded when the resolver was set, want both", recordedAtSet)
	}
	token, present := s.ResolverToken(testKey)
	if !present || token == "" {
		t.Fatal("the resolver was not set")
	}
	if got := s.servers[testKey]; len(got) != 1 || got[0] != addr("172.19.0.2") {
		t.Errorf("servers = %v", got)
	}
	res := rec.entries[platform.EntryResolver]
	if res.Kind != ledger.KindDNS || res.Key != testKey || res.Token != token {
		t.Errorf("resolver entry %+v does not match the live one (%s)", res, token)
	}
	tun := rec.entries[platform.EntryTunInterface]
	if tun.Kind != ledger.KindTunInterface || tun.Key != "utun9" || tun.Token != "172.19.0.1" {
		t.Errorf("tunnel entry %+v", tun)
	}
}

func TestActivateRollsBackItsRecordsWhenTheResolverCannotBeSet(t *testing.T) {
	d, h, s := newTestDriver(t)
	withTun(h, "utun9", "172.19.0.1/30")
	s.setErr = errors.New("the system refused")
	rec := newRecorder()
	if err := d.Activate(context.Background(), activeTun("utun9"), rec); err == nil {
		t.Fatal("no error")
	}
	if rec.count() != 0 {
		t.Errorf("%d records left behind for nothing", rec.count())
	}
	if _, present := s.ResolverToken(testKey); present {
		t.Error("a resolver was set")
	}
}

func TestActivateRollsBackWhenTheLedgerRefuses(t *testing.T) {
	d, h, s := newTestDriver(t)
	withTun(h, "utun9", "172.19.0.1/30")
	rec := newRecorder()
	rec.addErr[platform.EntryResolver] = errors.New("disk full")
	if err := d.Activate(context.Background(), activeTun("utun9"), rec); err == nil {
		t.Fatal("no error")
	}
	if rec.count() != 0 {
		t.Errorf("%d records left", rec.count())
	}
	if _, present := s.ResolverToken(testKey); present {
		t.Error("the resolver was set although it could not be recorded")
	}
}

func TestActivateNeedsTheTunnelWithItsAddress(t *testing.T) {
	d, h, s := newTestDriver(t)
	rec := newRecorder()
	ctx := context.Background()

	if err := d.Activate(ctx, activeTun("utun9"), rec); err == nil {
		t.Error("activated without a tunnel")
	}
	withTun(h, "utun9", "10.0.0.5/24") // somebody else's address
	if err := d.Activate(ctx, activeTun("utun9"), rec); err == nil {
		t.Error("activated a tunnel that does not carry the agent's address")
	}
	if err := d.Activate(ctx, platform.ActiveTun{Addresses: activeTun("x").Addresses, DNS: addr("172.19.0.2")}, rec); err == nil {
		t.Error("activated without an interface name")
	}
	if err := d.Activate(ctx, platform.ActiveTun{Interface: "utun9", Addresses: activeTun("x").Addresses}, rec); err == nil {
		t.Error("activated without a resolver address")
	}
	if rec.count() != 0 {
		t.Error("records were written for refused activations")
	}
	if _, present := s.ResolverToken(testKey); present {
		t.Error("a resolver was set")
	}
}

func TestActivateWaitsForTheTunnelToComeUp(t *testing.T) {
	d, h, _ := newTestDriver(t)
	go func() {
		time.Sleep(60 * time.Millisecond)
		withTun(h, "utun9", "172.19.0.1/30")
	}()
	if err := d.Activate(context.Background(), activeTun("utun9"), newRecorder()); err != nil {
		t.Fatalf("a tunnel that came up a moment late was refused: %v", err)
	}
}

func TestDeactivateGivesTheResolverBackAndIsIdempotent(t *testing.T) {
	d, h, s := newTestDriver(t)
	withTun(h, "utun9", "172.19.0.1/30")
	rec := newRecorder()
	ctx := context.Background()
	if err := d.Activate(ctx, activeTun("utun9"), rec); err != nil {
		t.Fatal(err)
	}
	if err := d.Deactivate(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if _, present := s.ResolverToken(testKey); present {
		t.Error("the resolver is still set")
	}
	if _, ok := rec.entries[platform.EntryResolver]; ok {
		t.Error("the resolver is still recorded")
	}
	if _, ok := rec.entries[platform.EntryTunInterface]; !ok {
		t.Error("the tunnel record must stay until its release verifies the interface is gone")
	}
	if err := d.Deactivate(ctx, rec); err != nil {
		t.Errorf("second Deactivate: %v", err)
	}
}

func TestDeactivateKeepsTheClaimWhenTheSystemFails(t *testing.T) {
	d, h, s := newTestDriver(t)
	withTun(h, "utun9", "172.19.0.1/30")
	rec := newRecorder()
	ctx := context.Background()
	_ = d.Activate(ctx, activeTun("utun9"), rec)

	s.removeErr = errors.New("busy")
	if err := d.Deactivate(ctx, rec); err == nil {
		t.Fatal("a failure was hidden")
	}
	if _, ok := rec.entries[platform.EntryResolver]; !ok {
		t.Error("the record was dropped although the resolver is still there")
	}
	s.removeErr = nil
	if err := d.Deactivate(ctx, rec); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if _, present := s.ResolverToken(testKey); present {
		t.Error("the resolver is still set after the retry")
	}
}

func TestDeactivateLeavesAResolverSomebodyElseReplaced(t *testing.T) {
	d, h, s := newTestDriver(t)
	withTun(h, "utun9", "172.19.0.1/30")
	rec := newRecorder()
	ctx := context.Background()
	_ = d.Activate(ctx, activeTun("utun9"), rec)

	s.mu.Lock()
	s.resolvers[testKey] = "another-programs-token"
	s.mu.Unlock()
	if err := d.Deactivate(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ResolverToken(testKey); got != "another-programs-token" {
		t.Error("a resolver that is not ours was removed")
	}
}

func TestReleaseResolver(t *testing.T) {
	d, _, s := newTestDriver(t)
	ctx := context.Background()
	entry := ledger.Entry{ID: platform.EntryResolver, Kind: ledger.KindDNS, Key: testKey, Token: "mine"}

	if out, err := d.Release(ctx, entry); err != nil || out != ledger.AlreadyGone {
		t.Errorf("absent: %v %v", out, err)
	}
	s.resolvers[testKey] = "mine"
	if out, err := d.Release(ctx, entry); err != nil || out != ledger.Released {
		t.Errorf("ours: %v %v", out, err)
	}
	if _, present := s.ResolverToken(testKey); present {
		t.Error("not removed")
	}
	s.resolvers[testKey] = "theirs"
	if out, err := d.Release(ctx, entry); err != nil || out != ledger.NotOurs {
		t.Errorf("replaced: %v %v", out, err)
	}
	s.resolvers[testKey] = ""
	if out, err := d.Release(ctx, entry); err != nil || out != ledger.NotOurs {
		t.Errorf("an entry without any mark is not ours: %v %v", out, err)
	}
	if _, present := s.ResolverToken(testKey); !present {
		t.Error("a resolver that is not ours was removed")
	}
	s.resolvers[testKey] = "mine"
	s.removeErr = errors.New("busy")
	if _, err := d.Release(ctx, entry); err == nil {
		t.Error("a failed removal was reported as success")
	}
}

func TestReleaseTunnel(t *testing.T) {
	d, h, _ := newTestDriver(t)
	ctx := context.Background()
	entry := ledger.Entry{ID: platform.EntryTunInterface, Kind: ledger.KindTunInterface, Key: "utun9", Token: "172.19.0.1"}

	if out, err := d.Release(ctx, entry); err != nil || out != ledger.AlreadyGone {
		t.Errorf("gone: %v %v", out, err)
	}

	// The name was given to somebody else's tunnel.
	withTun(h, "utun9", "10.8.0.2/24")
	if out, err := d.Release(ctx, entry); err != nil || out != ledger.NotOurs {
		t.Errorf("recycled name: %v %v", out, err)
	}

	// Still ours: nothing can remove it but its owner, so say so.
	h.set(nil)
	withTun(h, "utun9", "172.19.0.1/30")
	start := time.Now()
	if _, err := d.Release(ctx, entry); err == nil {
		t.Error("a tunnel that is still there was reported gone")
	}
	if time.Since(start) < 100*time.Millisecond {
		t.Error("did not wait for the tunnel to go")
	}

	// It goes while we wait.
	go func() { time.Sleep(50 * time.Millisecond); h.set(nil) }()
	if out, err := d.Release(ctx, entry); err != nil || out != ledger.AlreadyGone {
		t.Errorf("went while waiting: %v %v", out, err)
	}

	if _, err := d.Release(ctx, ledger.Entry{Kind: ledger.KindRoute}); err == nil {
		t.Error("an unsupported kind was accepted")
	}
}

func TestReleaseStopsWaitingWhenCancelled(t *testing.T) {
	d, h, _ := newTestDriver(t)
	withTun(h, "utun9", "172.19.0.1/30")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.Release(ctx, ledger.Entry{Kind: ledger.KindTunInterface, Key: "utun9", Token: "172.19.0.1"}); err == nil {
		t.Error("no error although the context ended")
	}
}

func TestSweepRemovesOnlyAResolverThatCarriesTheMark(t *testing.T) {
	d, _, s := newTestDriver(t)
	ctx := context.Background()
	if err := d.Sweep(ctx); err != nil {
		t.Fatalf("nothing to sweep: %v", err)
	}
	s.resolvers[testKey] = ""
	_ = d.Sweep(ctx)
	if _, present := s.ResolverToken(testKey); !present {
		t.Error("an entry without our mark was swept")
	}
	s.resolvers[testKey] = "some-mark"
	if err := d.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if _, present := s.ResolverToken(testKey); present {
		t.Error("a marked leftover was not swept")
	}
	s.resolvers[testKey] = "some-mark"
	s.removeErr = errors.New("busy")
	if err := d.Sweep(ctx); err == nil {
		t.Error("a failed sweep was reported as done")
	}
}

func TestOurTunnelDoesNotChangeTheFingerprint(t *testing.T) {
	d, h, _ := newTestDriver(t)
	ctx := context.Background()
	before, _ := d.Fingerprint(ctx)

	withTun(h, "utun9", "172.19.0.1/30")
	if err := d.Activate(ctx, activeTun("utun9"), newRecorder()); err != nil {
		t.Fatal(err)
	}
	// The core now holds the default through its tunnel.
	h.mu.Lock()
	h.routes = append(h.routes, def(40, "", false))
	h.mu.Unlock()
	if after, _ := d.Fingerprint(ctx); after != before {
		t.Errorf("the agent's own tunnel changed the fingerprint: %s → %s", before, after)
	}
	snap, _ := d.Snapshot(ctx)
	for _, c := range routecheck.Analyze(snap) {
		t.Errorf("the agent's own tunnel is a conflict: %v", c)
	}

	// Once the tunnel is verified gone, the name is no longer ours.
	h.set(nil)
	_, _ = d.Release(ctx, ledger.Entry{Kind: ledger.KindTunInterface, Key: "utun9", Token: "172.19.0.1"})
	if d.ours(Interface{Name: "utun9"}) {
		t.Error("a name that is free again is still counted as ours")
	}
}

// ---------------------------------------------------------------- the events

func nextEvent(t *testing.T, ch <-chan platform.Event) platform.Event {
	t.Helper()
	select {
	case e := <-ch:
		return e
	case <-time.After(2 * time.Second):
		t.Fatal("no event")
		return platform.Event{}
	}
}

func noEvent(t *testing.T, ch <-chan platform.Event) {
	t.Helper()
	select {
	case e := <-ch:
		t.Fatalf("unexpected event %v", e.Kind)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestNetworkChangesBecomeEvents(t *testing.T) {
	d, h, _ := newTestDriver(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := d.Events(ctx)
	h.notify()
	if e := nextEvent(t, ch); e.Kind != platform.NetworkChanged {
		t.Errorf("kind %v", e.Kind)
	}
	cancel()
	if _, ok := <-ch; ok {
		t.Error("the stream did not end with its context")
	}
}

func TestSleepAndWake(t *testing.T) {
	d, _, s := newTestDriver(t)
	if err := d.FollowSystem(func() (uint32, bool) { return 501, true }); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := d.Events(ctx)
	s.powerFn(false)
	if e := nextEvent(t, ch); e.Kind != platform.Slept {
		t.Errorf("kind %v", e.Kind)
	}
	s.powerFn(true)
	if e := nextEvent(t, ch); e.Kind != platform.Woke {
		t.Errorf("kind %v", e.Kind)
	}
}

func TestTheUserComesAndGoes(t *testing.T) {
	d, _, s := newTestDriver(t)
	s.uid, s.uidOK = 502, true // somebody else is at the screen to begin with
	if err := d.FollowSystem(func() (uint32, bool) { return 501, true }); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := d.Events(ctx)
	noEvent(t, ch)

	s.consoleFn(501, true)
	if e := nextEvent(t, ch); e.Kind != platform.UserLoggedIn {
		t.Errorf("kind %v", e.Kind)
	}
	s.consoleFn(501, true)
	noEvent(t, ch)
	s.consoleFn(502, true) // fast user switching
	if e := nextEvent(t, ch); e.Kind != platform.UserLoggedOut {
		t.Errorf("kind %v", e.Kind)
	}
	s.consoleFn(0, false) // the login window
	noEvent(t, ch)
}

func TestTheUserWasAlreadyThere(t *testing.T) {
	d, _, s := newTestDriver(t)
	s.uid, s.uidOK = 501, true
	if err := d.FollowSystem(func() (uint32, bool) { return 501, true }); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if e := nextEvent(t, d.Events(ctx)); e.Kind != platform.UserLoggedIn {
		t.Errorf("kind %v", e.Kind)
	}
}

func TestTheFirstUserToClaimTheServiceIsAtTheScreen(t *testing.T) {
	d, _, s := newTestDriver(t)
	s.uid, s.uidOK = 501, true
	var owner uint32
	var known bool
	if err := d.FollowSystem(func() (uint32, bool) { return owner, known }); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := d.Events(ctx)
	noEvent(t, ch) // nobody is authorized: nobody is present

	owner, known = 501, true
	d.OwnerChanged()
	if e := nextEvent(t, ch); e.Kind != platform.UserLoggedIn {
		t.Errorf("kind %v", e.Kind)
	}
	d.OwnerChanged()
	noEvent(t, ch)
}

func TestOwnerChangedBeforeFollowingIsHarmless(t *testing.T) {
	d, _, _ := newTestDriver(t)
	d.OwnerChanged()
}

func TestCloseStopsEveryWatcher(t *testing.T) {
	d, h, s := newTestDriver(t)
	if err := d.FollowSystem(func() (uint32, bool) { return 501, true }); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if h.watching {
		t.Error("the network watcher is still running")
	}
	if s.stopped != 2 {
		t.Errorf("%d of 2 system watchers stopped", s.stopped)
	}
	if err := d.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}
