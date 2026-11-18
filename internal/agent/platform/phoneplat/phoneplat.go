// Package phoneplat is the platform of a phone, where the agent runs inside
// the application and the system owns the VPN interface.
//
// On Android a VpnService, and on iOS a packet tunnel provider, makes the TUN
// with the addresses, routes and resolver the core asks for (through
// internal/phone/tunnel) and removes it when the tunnel stops or the process
// ends. Nothing is left on the device for the agent to record or release, and
// the system allows one VPN at a time, so there is no other tunnel's route to
// judge: the driver only reads the device's networks, so that the tunnel's
// subnet does not collide with one of them.
package phoneplat

import (
	"context"
	"net/netip"

	"eggbox/internal/agent/ledger"
	"eggbox/internal/agent/platform"
	"eggbox/internal/agent/routecheck"
)

// Network is one network the device is on.
type Network struct {
	Name      string
	Addresses []netip.Prefix
}

// Options configure a Driver.
type Options struct {
	// Name is the platform's name ("android", "ios").
	Name string
	// Networks lists the device's networks; nil, or an error, means none are
	// known and the first candidate subnet is used.
	Networks func(ctx context.Context) ([]Network, error)
}

// Driver implements platform.Driver for a phone.
type Driver struct {
	opts Options
}

var _ platform.Driver = (*Driver)(nil)

// New returns the driver of a phone.
func New(o Options) *Driver { return &Driver{opts: o} }

func (d *Driver) Name() string { return d.opts.Name }

// Sandbox is false: the tunnel is the system's VPN, which the person allowed.
func (d *Driver) Sandbox() bool { return false }

// Snapshot lists the device's networks as physical interfaces, without
// routes: an app cannot read the routing table, and the system's VPN replaces
// any other.
func (d *Driver) Snapshot(ctx context.Context) (routecheck.Snapshot, error) {
	var snap routecheck.Snapshot
	if d.opts.Networks == nil {
		return snap, nil
	}
	nets, err := d.opts.Networks(ctx)
	if err != nil {
		return snap, nil
	}
	for i, n := range nets {
		snap.Interfaces = append(snap.Interfaces, routecheck.Interface{
			Name: n.Name, Index: i + 1, Up: true, Kind: routecheck.KindPhysical, Addrs: n.Addresses,
		})
	}
	return snap, nil
}

// Fingerprint is constant: the core follows the default network itself and
// moves its connections when it changes, so a change of network is no reason
// to reconnect (which would make the system tear the VPN down and up again).
func (d *Driver) Fingerprint(context.Context) (string, error) { return d.opts.Name, nil }

func (d *Driver) Online(context.Context) bool { return true }

// TunDefaults let the system choose the interface's name, and the core answer
// the resolver the system is pointed at.
func (d *Driver) TunDefaults() platform.TunDefaults {
	return platform.TunDefaults{DNSMode: "hijack"}
}

// Activate has nothing to do: the system pointed its resolver at the tunnel
// when it made it.
func (d *Driver) Activate(context.Context, platform.ActiveTun, platform.Recorder) error { return nil }

func (d *Driver) Deactivate(context.Context, platform.Recorder) error { return nil }

// Release finds nothing: what the system made it removed.
func (d *Driver) Release(context.Context, ledger.Entry) (ledger.Outcome, error) {
	return ledger.AlreadyGone, nil
}

func (d *Driver) Sweep(context.Context) error { return nil }

// Events sends nothing: the phone's lifecycle reaches the agent through the
// application, and network changes through the core.
func (d *Driver) Events(ctx context.Context) <-chan platform.Event {
	out := make(chan platform.Event)
	go func() {
		<-ctx.Done()
		close(out)
	}()
	return out
}

func (d *Driver) Close() error { return nil }
