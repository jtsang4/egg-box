package phoneplat

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"eggbox/internal/agent/ledger"
	"eggbox/internal/agent/routecheck"
)

func TestTheTunnelAvoidsTheDevicesNetworks(t *testing.T) {
	d := New(Options{Name: "android", Networks: func(context.Context) ([]Network, error) {
		return []Network{{Name: "wlan0", Addresses: []netip.Prefix{netip.MustParsePrefix("172.19.0.10/24")}}}, nil
	}})
	snap, err := d.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(routecheck.Analyze(snap)) != 0 {
		t.Fatal("a phone has no other tunnel to conflict with")
	}
	candidates := []routecheck.Plan{
		{V4: netip.MustParsePrefix("172.19.0.1/30")},
		{V4: netip.MustParsePrefix("172.23.0.1/30")},
	}
	plan, ok := routecheck.ChoosePlan(snap, candidates)
	if !ok || plan.V4 != candidates[1].V4 {
		t.Fatalf("plan %v (ok %v): the Wi-Fi's subnet must be avoided", plan, ok)
	}
}

func TestUnknownNetworksLeaveTheFirstSubnet(t *testing.T) {
	for _, d := range []*Driver{
		New(Options{Name: "ios"}),
		New(Options{Name: "android", Networks: func(context.Context) ([]Network, error) { return nil, errors.New("no") }}),
	} {
		snap, err := d.Snapshot(context.Background())
		if err != nil || len(snap.Interfaces) != 0 {
			t.Fatalf("%s: %+v, %v", d.Name(), snap, err)
		}
	}
}

func TestNothingIsLeftToRecordOrRelease(t *testing.T) {
	d := New(Options{Name: "android"})
	if d.Sandbox() {
		t.Fatal("a phone runs the real VPN")
	}
	if got := d.TunDefaults(); got.DNSMode != "hijack" || got.StrictRoute || got.InterfaceName != "" {
		t.Fatalf("defaults %+v", got)
	}
	if o, err := d.Release(context.Background(), ledger.Entry{ID: "iface"}); err != nil || o != ledger.AlreadyGone {
		t.Fatalf("release %v, %v", o, err)
	}
	a, _ := d.Fingerprint(context.Background())
	b, _ := d.Fingerprint(context.Background())
	if a != b || a == "" {
		t.Fatal("the fingerprint never changes: the core follows the network itself")
	}
}

func TestEventsEndWithTheContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := New(Options{Name: "android"}).Events(ctx)
	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("no events are sent")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the channel stays open after the context ended")
	}
}
