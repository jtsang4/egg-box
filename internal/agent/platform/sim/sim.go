// Package sim is a platform driver that lives entirely in memory. It simulates
// an operating system closely enough to exercise the controller (interfaces,
// routes, resolver state, network events) while being unable to change the
// host in any way. It is the driver for unit tests and for the development
// sandbox, which is how this project is built and verified without disturbing
// a developer's real network.
package sim

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"sync"
	"time"

	"github.com/jtsang4/sing-box-ui/internal/agent/ledger"
	"github.com/jtsang4/sing-box-ui/internal/agent/platform"
	"github.com/jtsang4/sing-box-ui/internal/agent/routecheck"
)

// Options configures a simulated platform.
type Options struct {
	// Sandbox makes the controller replace the TUN with a loopback proxy.
	// Controller tests that need the full TUN path set it to false.
	Sandbox bool
	// Defaults are returned from TunDefaults.
	Defaults platform.TunDefaults
}

// Driver implements platform.Driver.
type Driver struct {
	mu          sync.Mutex
	opts        Options
	snapshot    routecheck.Snapshot
	fingerprint string
	online      bool
	events      chan platform.Event

	// simulated OS state
	resolvers map[string]string // key -> token
	ifaces    map[string]bool

	activateErr   error
	deactivateErr error
	releaseErrs   map[string]error
	sweepErr      error
	sweeps        int
	calls         []string
}

// New returns a simulated platform on a quiet home network.
func New(opts Options) *Driver {
	if opts.Defaults.DNSMode == "" {
		opts.Defaults.DNSMode = "disabled"
	}
	return &Driver{
		opts: opts,
		snapshot: routecheck.Snapshot{
			Interfaces: []routecheck.Interface{
				{Name: "lo0", Up: true, Kind: routecheck.KindLoopback, Addrs: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/8")}},
				{Name: "en0", Index: 4, Up: true, Kind: routecheck.KindPhysical, Addrs: []netip.Prefix{netip.MustParsePrefix("192.168.1.20/24")}},
			},
			Routes: []routecheck.Route{
				{Dest: netip.MustParsePrefix("0.0.0.0/0"), Gateway: netip.MustParseAddr("192.168.1.1"), IfName: "en0"},
				{Dest: netip.MustParsePrefix("192.168.1.0/24"), IfName: "en0"},
			},
		},
		fingerprint: "en0|192.168.1.1",
		online:      true,
		events:      make(chan platform.Event, 64),
		resolvers:   map[string]string{},
		ifaces:      map[string]bool{},
		releaseErrs: map[string]error{},
	}
}

func (d *Driver) note(format string, args ...any) {
	d.calls = append(d.calls, fmt.Sprintf(format, args...))
}

// ----------------------------------------------------------------- scripting

// SetSnapshot replaces the simulated interfaces and routes.
func (d *Driver) SetSnapshot(s routecheck.Snapshot) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.snapshot = s
}

// SetFingerprint changes the simulated uplink identity (as if the Wi-Fi changed).
func (d *Driver) SetFingerprint(fp string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.fingerprint = fp
}

// SetOnline flips the simulated connectivity.
func (d *Driver) SetOnline(on bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.online = on
}

// Emit delivers a raw OS event.
func (d *Driver) Emit(kind platform.EventKind, detail string) {
	select {
	case d.events <- platform.Event{Kind: kind, At: time.Now(), Detail: detail}:
	default:
	}
}

// FailActivate makes Activate fail until cleared with nil.
func (d *Driver) FailActivate(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.activateErr = err
}

// FailDeactivate makes Deactivate fail until cleared with nil.
func (d *Driver) FailDeactivate(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.deactivateErr = err
}

// FailRelease makes Release of the ledger entry with this ID fail until cleared with nil.
func (d *Driver) FailRelease(id string, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err == nil {
		delete(d.releaseErrs, id)
		return
	}
	d.releaseErrs[id] = err
}

// FailSweep makes Sweep fail until cleared with nil.
func (d *Driver) FailSweep(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sweepErr = err
}

// PlantResolver simulates a resolver entry left behind by a previous run.
func (d *Driver) PlantResolver(key, token string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.resolvers[key] = token
}

// PlantInterface simulates a TUN device left behind by a previous run.
func (d *Driver) PlantInterface(name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ifaces[name] = true
}

// Resolvers returns the simulated resolver entries currently installed.
func (d *Driver) Resolvers() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, 0, len(d.resolvers))
	for k := range d.resolvers {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Interfaces returns the simulated leftover or active interfaces.
func (d *Driver) Interfaces() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, 0, len(d.ifaces))
	for k := range d.ifaces {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Calls returns the recorded call log.
func (d *Driver) Calls() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.calls...)
}

// Sweeps counts Sweep invocations.
func (d *Driver) Sweeps() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.sweeps
}

// -------------------------------------------------------------- platform API

func (d *Driver) Name() string  { return "sim" }
func (d *Driver) Sandbox() bool { return d.opts.Sandbox }

func (d *Driver) Snapshot(context.Context) (routecheck.Snapshot, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.snapshot, nil
}

func (d *Driver) Fingerprint(context.Context) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.fingerprint, nil
}

func (d *Driver) Online(context.Context) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.online
}

func (d *Driver) TunDefaults() platform.TunDefaults { return d.opts.Defaults }

func (d *Driver) Activate(_ context.Context, tun platform.ActiveTun, rec platform.Recorder) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.note("activate %s", tun.Interface)
	if d.activateErr != nil {
		return d.activateErr
	}
	d.ifaces[tun.Interface] = true
	key := "sim:resolver:" + tun.Interface
	token := randomToken()
	d.resolvers[key] = token
	if err := rec.Add(ledger.Entry{ID: platform.EntryResolver, Kind: ledger.KindDNS, Key: key, Token: token}); err != nil {
		delete(d.resolvers, key)
		return err
	}
	return rec.Add(ledger.Entry{ID: platform.EntryTunInterface, Kind: ledger.KindTunInterface, Key: tun.Interface})
}

func (d *Driver) Deactivate(_ context.Context, rec platform.Recorder) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.note("deactivate")
	if d.deactivateErr != nil {
		return d.deactivateErr
	}
	for k := range d.resolvers {
		delete(d.resolvers, k)
	}
	d.ifaces = map[string]bool{}
	_ = rec.Remove(platform.EntryResolver)
	_ = rec.Remove(platform.EntryTunInterface)
	return nil
}

func (d *Driver) Release(_ context.Context, e ledger.Entry) (ledger.Outcome, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.note("release %s", e.ID)
	if err := d.releaseErrs[e.ID]; err != nil {
		return 0, err
	}
	switch e.Kind {
	case ledger.KindDNS:
		token, ok := d.resolvers[e.Key]
		switch {
		case !ok:
			return ledger.AlreadyGone, nil
		case token != e.Token:
			return ledger.NotOurs, nil
		}
		delete(d.resolvers, e.Key)
		return ledger.Released, nil
	case ledger.KindTunInterface:
		if !d.ifaces[e.Key] {
			return ledger.AlreadyGone, nil
		}
		delete(d.ifaces, e.Key)
		return ledger.Released, nil
	}
	return 0, errors.New("sim: unsupported resource kind " + string(e.Kind))
}

func (d *Driver) Sweep(context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sweeps++
	d.note("sweep")
	if d.sweepErr != nil {
		return d.sweepErr
	}
	for k := range d.resolvers {
		delete(d.resolvers, k)
	}
	d.ifaces = map[string]bool{}
	return nil
}

func (d *Driver) Events(ctx context.Context) <-chan platform.Event {
	out := make(chan platform.Event)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case e := <-d.events:
				select {
				case out <- e:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out
}

func (d *Driver) Close() error { return nil }

func randomToken() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

var _ platform.Driver = (*Driver)(nil)
