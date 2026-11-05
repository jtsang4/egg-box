//go:build windows

package winplat

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/jtsang4/sing-box-ui/internal/agent/ledger"
	"github.com/jtsang4/sing-box-ui/internal/agent/platform"
	"github.com/jtsang4/sing-box-ui/internal/agent/routecheck"
	"github.com/jtsang4/sing-box-ui/internal/brand"
)

const (
	adapterAppearWait = 10 * time.Second
	adapterGoneWait   = 2 * time.Second
	adapterPoll       = 200 * time.Millisecond
)

// Options configure the driver.
type Options struct {
	// InterfaceName is the TUN adapter's name; empty means brand.TunInterfaceName.
	InterfaceName string
	Logger        *slog.Logger
}

// Driver is the Windows platform driver. It reads the machine's network
// state, hears about changes, and removes the adapter if the core could not.
// There is one per process: the system's change notifications are not tied to
// an object.
type Driver struct {
	name   string
	log    *slog.Logger
	events chan platform.Event

	mu      sync.Mutex
	handles []windows.Handle
}

var (
	current atomic.Pointer[Driver]

	// notifyCallback is called by the system, on a thread of its own, for
	// every change of a route, an interface or an address. It must return
	// quickly and only tells the driver; the agent's netwatch coalesces the
	// bursts and decides what a change means.
	notifyCallback = windows.NewCallback(func(_, _ uintptr, _ uint32) uintptr {
		if d := current.Load(); d != nil {
			d.Inject(platform.NetworkChanged, "")
		}
		return 0
	})
)

// New returns the driver and starts listening for network changes.
func New(opts Options) (*Driver, error) {
	d := &Driver{
		name:   opts.InterfaceName,
		log:    opts.Logger,
		events: make(chan platform.Event, 64),
	}
	if d.name == "" {
		d.name = brand.TunInterfaceName
	}
	if d.log == nil {
		d.log = slog.Default()
	}
	if !current.CompareAndSwap(nil, d) {
		return nil, errors.New("winplat: a driver is already running in this process")
	}
	register := []struct {
		what string
		fn   func(family uint16, cb uintptr, ctx unsafe.Pointer, initial bool, h *windows.Handle) error
	}{
		{"route changes", windows.NotifyRouteChange2},
		{"interface changes", windows.NotifyIpInterfaceChange},
		{"address changes", windows.NotifyUnicastIpAddressChange},
	}
	for _, r := range register {
		var h windows.Handle
		if err := r.fn(windows.AF_UNSPEC, notifyCallback, nil, false, &h); err != nil {
			_ = d.Close()
			return nil, fmt.Errorf("listen for %s: %w", r.what, err)
		}
		d.handles = append(d.handles, h)
	}
	return d, nil
}

// Inject delivers an event the system does not announce on its own. The
// service entry uses it for sleep, resume and sign-in changes, which the
// service manager reports.
func (d *Driver) Inject(kind platform.EventKind, detail string) {
	select {
	case d.events <- platform.Event{Kind: kind, At: time.Now(), Detail: detail}:
	default: // a change is already waiting; the burst is coalesced downstream
	}
}

func (d *Driver) Name() string  { return "windows" }
func (d *Driver) Sandbox() bool { return false }

func (d *Driver) TunDefaults() platform.TunDefaults {
	return platform.TunDefaults{StrictRoute: true, DNSMode: "hijack", InterfaceName: d.name}
}

func (d *Driver) ours(a Adapter) bool { return strings.EqualFold(a.Name, d.name) }

// read reads adapters and routes together.
func (d *Driver) read() ([]Route, []Adapter, error) {
	adapters, err := readAdapters()
	if err != nil {
		return nil, nil, err
	}
	routes, err := readRoutes()
	if err != nil {
		return nil, nil, err
	}
	return routes, adapters, nil
}

func (d *Driver) Snapshot(context.Context) (routecheck.Snapshot, error) {
	routes, adapters, err := d.read()
	if err != nil {
		return routecheck.Snapshot{}, err
	}
	return Snapshot(routes, adapters, d.ours), nil
}

func (d *Driver) Fingerprint(context.Context) (string, error) {
	routes, adapters, err := d.read()
	if err != nil {
		return "", err
	}
	r, a, ok := DefaultRoute(routes, adapters, d.ours)
	if !ok {
		return "", errors.New("there is no way out to the network")
	}
	return Fingerprint(r, a), nil
}

func (d *Driver) Online(context.Context) bool {
	routes, adapters, err := d.read()
	if err != nil {
		return false
	}
	_, _, ok := DefaultRoute(routes, adapters, d.ours)
	return ok
}

// Activate waits for the core's adapter and records it. The core brings up
// DNS handling itself on Windows (see TunDefaults); nothing else is changed.
func (d *Driver) Activate(ctx context.Context, tun platform.ActiveTun, rec platform.Recorder) error {
	name := tun.Interface
	if name == "" {
		name = d.name
	}
	a, err := d.waitAdapter(ctx, name, true, adapterAppearWait)
	if err != nil {
		return fmt.Errorf("the tunnel adapter %q did not appear: %w", name, err)
	}
	return rec.Add(ledger.Entry{ID: platform.EntryTunInterface, Kind: ledger.KindTunInterface, Key: name, Token: a.GUID})
}

// Deactivate has nothing to give back: Windows keeps no system setting for
// the tunnel outside the adapter, which Release deals with.
func (d *Driver) Deactivate(context.Context, platform.Recorder) error { return nil }

// Release removes a recorded adapter once it is certain to be ours: it has the
// recorded GUID. The core normally removed it when it stopped.
func (d *Driver) Release(ctx context.Context, e ledger.Entry) (ledger.Outcome, error) {
	if e.Kind != ledger.KindTunInterface {
		return 0, fmt.Errorf("winplat: unsupported resource kind %q", e.Kind)
	}
	a, err := d.waitAdapter(ctx, e.Key, false, adapterGoneWait)
	if errors.Is(err, errAdapterGone) {
		return ledger.AlreadyGone, nil
	}
	if err != nil {
		return 0, err
	}
	if e.Token != "" && !SameGUID(a.GUID, e.Token) {
		return ledger.NotOurs, nil
	}
	if !IsWintun(a.Description) {
		return ledger.NotOurs, nil
	}
	if err := d.removeAndVerify(ctx, a); err != nil {
		return 0, err
	}
	return ledger.Released, nil
}

// Sweep removes a leftover adapter of our name when nothing is known about
// who made it. A device that is not a Wintun adapter is not touched.
func (d *Driver) Sweep(ctx context.Context) error {
	a, found, err := d.findAdapter(d.name)
	if err != nil || !found || !IsWintun(a.Description) {
		return err
	}
	return d.removeAndVerify(ctx, a)
}

func (d *Driver) removeAndVerify(ctx context.Context, a Adapter) error {
	d.log.Info("removing a leftover tunnel adapter", "name", a.Name)
	if err := removeAdapter(a.GUID); err != nil && !errors.Is(err, errAdapterNotFound) {
		return err
	}
	if _, err := d.waitAdapter(ctx, a.Name, false, adapterGoneWait*2); err == nil {
		return fmt.Errorf("the adapter %q is still there; remove it in Network Connections", a.Name)
	} else if !errors.Is(err, errAdapterGone) {
		return err
	}
	return nil
}

var errAdapterGone = errors.New("the adapter is gone")

// findAdapter looks an adapter up by its name.
func (d *Driver) findAdapter(name string) (Adapter, bool, error) {
	adapters, err := readAdapters()
	if err != nil {
		return Adapter{}, false, err
	}
	for _, a := range adapters {
		if strings.EqualFold(a.Name, name) {
			return a, true, nil
		}
	}
	return Adapter{}, false, nil
}

// waitAdapter waits until the adapter is there (present) or gone (not
// present). For "gone" it returns errAdapterGone on success; if the adapter is
// still there when the time is up it returns the adapter and no error.
func (d *Driver) waitAdapter(ctx context.Context, name string, present bool, within time.Duration) (Adapter, error) {
	deadline := time.Now().Add(within)
	for {
		a, found, err := d.findAdapter(name)
		if err != nil {
			return Adapter{}, err
		}
		if present && found {
			return a, nil
		}
		if !present && !found {
			return Adapter{}, errAdapterGone
		}
		if time.Now().After(deadline) {
			if present {
				return Adapter{}, errors.New("timed out")
			}
			return a, nil
		}
		select {
		case <-ctx.Done():
			return Adapter{}, ctx.Err()
		case <-time.After(adapterPoll):
		}
	}
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

// Close stops listening for changes.
func (d *Driver) Close() error {
	d.mu.Lock()
	handles := d.handles
	d.handles = nil
	d.mu.Unlock()
	var first error
	for _, h := range handles {
		if err := windows.CancelMibChangeNotify2(h); err != nil && first == nil {
			first = err
		}
	}
	current.CompareAndSwap(d, nil)
	return first
}

var _ platform.Driver = (*Driver)(nil)
