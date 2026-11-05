package macplat

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"sync"
	"time"

	"github.com/jtsang4/sing-box-ui/internal/agent/ledger"
	"github.com/jtsang4/sing-box-ui/internal/agent/platform"
	"github.com/jtsang4/sing-box-ui/internal/agent/routecheck"
	"github.com/jtsang4/sing-box-ui/internal/brand"
	"github.com/jtsang4/sing-box-ui/internal/macsys"
)

// DefaultResolverKey is where the agent keeps its temporary resolver in the
// system configuration store: the place a VPN's DNS settings go, which the
// system merges into its own and drops again when the key is removed.
const DefaultResolverKey = "State:/Network/Service/" + brand.AgentLabel + "/DNS"

// System is what the Swift bridge gives the driver (and what a fake gives the
// tests): the system resolver, and word of sleep and of who is at the screen.
// *bridge.Bridge implements it.
type System interface {
	// SetResolver points the system resolver at servers for every domain,
	// under key, marked with token.
	SetResolver(key string, servers []netip.Addr, token string) error
	// ResolverToken reads the mark of the entry under key. An entry with an
	// empty mark is somebody else's.
	ResolverToken(key string) (token string, present bool)
	RemoveResolver(key string) error

	ConsoleUser() (uid uint32, ok bool)
	WatchConsole(fn func(uid uint32, ok bool)) (stop func(), err error)
	WatchPower(fn func(awake bool)) (stop func(), err error)
}

// Host is what the driver reads of the machine's network, and hears of its
// changes. host_darwin.go reads the real one.
type Host interface {
	Interfaces() ([]Interface, error)
	Routes() ([]Route, error)
	// Watch calls notify whenever an interface, an address or a route changes,
	// until the result is called.
	Watch(notify func()) (stop func(), err error)
}

// Options configure the driver.
type Options struct {
	System System
	Host   Host
	// ResolverKey overrides DefaultResolverKey. Tests that touch nothing real
	// use a key of their own.
	ResolverKey string
	// AppearWait is how long the tunnel interface is given to show up with its
	// address once the core says it is up; zero means five seconds.
	AppearWait time.Duration
	// GoneWait is how long a tunnel interface that should be gone is given to
	// go; zero means two seconds.
	GoneWait time.Duration
	Logger   *slog.Logger
}

// Driver is the macOS platform driver.
type Driver struct {
	sys        System
	host       Host
	key        string
	appearWait time.Duration
	goneWait   time.Duration
	log        *slog.Logger
	events     chan platform.Event

	mu       sync.Mutex
	tunName  string // the interface the agent's core created, once Activate has seen it
	resolver string // the token of the resolver this run installed, empty when none
	stops    []func()
	closed   bool

	// who is at the screen, as last reported
	console struct {
		uid     uint32
		ok      bool
		tracker *macsys.ConsoleTracker
	}
}

// New returns the driver and starts listening for changes of the network.
func New(opts Options) (*Driver, error) {
	if opts.System == nil || opts.Host == nil {
		return nil, errors.New("macplat: the driver needs a system and a host")
	}
	d := &Driver{
		sys:        opts.System,
		host:       opts.Host,
		key:        opts.ResolverKey,
		appearWait: opts.AppearWait,
		goneWait:   opts.GoneWait,
		log:        opts.Logger,
		events:     make(chan platform.Event, 64),
	}
	if d.key == "" {
		d.key = DefaultResolverKey
	}
	if d.appearWait <= 0 {
		d.appearWait = 5 * time.Second
	}
	if d.goneWait <= 0 {
		d.goneWait = 2 * time.Second
	}
	if d.log == nil {
		d.log = slog.Default()
	}
	stop, err := d.host.Watch(func() { d.Inject(platform.NetworkChanged, "") })
	if err != nil {
		return nil, fmt.Errorf("listen for network changes: %w", err)
	}
	d.stops = append(d.stops, stop)
	return d, nil
}

// Inject delivers an event the system does not announce through the routing
// socket.
func (d *Driver) Inject(kind platform.EventKind, detail string) {
	select {
	case d.events <- platform.Event{Kind: kind, At: time.Now(), Detail: detail}:
	default: // a change is already waiting; the burst is coalesced downstream
	}
}

// FollowSystem starts passing on sleep and wake, and the comings and goings of
// the authorized user at the screen. owner says who that is (ok = false while
// nobody is authorized yet). If the user is already there, the agent is told.
func (d *Driver) FollowSystem(owner func() (uint32, bool)) error {
	d.mu.Lock()
	d.console.tracker = macsys.NewConsoleTracker(owner)
	d.console.uid, d.console.ok = d.sys.ConsoleUser()
	changed, present := d.console.tracker.Apply(d.console.uid, d.console.ok)
	d.mu.Unlock()
	if changed && present {
		d.Inject(platform.UserLoggedIn, "the user was already at the screen")
	}

	stopConsole, err := d.sys.WatchConsole(func(uid uint32, ok bool) {
		d.mu.Lock()
		d.console.uid, d.console.ok = uid, ok
		changed, present := d.console.tracker.Apply(uid, ok)
		d.mu.Unlock()
		d.announce(changed, present)
	})
	if err != nil {
		return fmt.Errorf("watch who is at the screen: %w", err)
	}
	stopPower, err := d.sys.WatchPower(func(awake bool) {
		if awake {
			d.Inject(platform.Woke, "")
		} else {
			d.Inject(platform.Slept, "")
		}
	})
	if err != nil {
		stopConsole()
		return fmt.Errorf("watch sleep and wake: %w", err)
	}
	d.mu.Lock()
	d.stops = append(d.stops, stopConsole, stopPower)
	d.mu.Unlock()
	return nil
}

// OwnerChanged tells the driver the authorized user is now somebody else (the
// first user to connect became it): whether they are at the screen is judged
// again.
func (d *Driver) OwnerChanged() {
	d.mu.Lock()
	if d.console.tracker == nil {
		d.mu.Unlock()
		return
	}
	changed, present := d.console.tracker.Apply(d.console.uid, d.console.ok)
	d.mu.Unlock()
	d.announce(changed, present)
}

func (d *Driver) announce(changed, present bool) {
	switch {
	case !changed:
	case present:
		d.Inject(platform.UserLoggedIn, "")
	default:
		d.Inject(platform.UserLoggedOut, "")
	}
}

func (d *Driver) Name() string  { return "darwin" }
func (d *Driver) Sandbox() bool { return false }

// TunDefaults: macOS gives the tunnel's name itself (utunN), and the system
// resolver is pointed at the tunnel by Activate, so the core is told to
// leave DNS alone.
func (d *Driver) TunDefaults() platform.TunDefaults {
	return platform.TunDefaults{StrictRoute: false, DNSMode: "disabled"}
}

func (d *Driver) ours(i Interface) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.tunName != "" && i.Name == d.tunName
}

func (d *Driver) read() ([]Route, []Interface, error) {
	ifaces, err := d.host.Interfaces()
	if err != nil {
		return nil, nil, err
	}
	routes, err := d.host.Routes()
	if err != nil {
		return nil, nil, err
	}
	return routes, ifaces, nil
}

func (d *Driver) Snapshot(context.Context) (routecheck.Snapshot, error) {
	routes, ifaces, err := d.read()
	if err != nil {
		return routecheck.Snapshot{}, err
	}
	return Snapshot(routes, ifaces, d.ours), nil
}

func (d *Driver) Fingerprint(context.Context) (string, error) {
	routes, ifaces, err := d.read()
	if err != nil {
		return "", err
	}
	r, i, ok := Uplink(routes, ifaces, d.ours)
	if !ok {
		return "", errors.New("there is no way out to the network")
	}
	return Fingerprint(r, i), nil
}

func (d *Driver) Online(context.Context) bool {
	routes, ifaces, err := d.read()
	if err != nil {
		return false
	}
	_, _, ok := Uplink(routes, ifaces, d.ours)
	return ok
}

// Activate checks that the tunnel the core created is really there, with the
// address the agent chose, points the system resolver at it, and records both
// so a later run can find and undo them.
func (d *Driver) Activate(ctx context.Context, tun platform.ActiveTun, rec platform.Recorder) error {
	if tun.Interface == "" {
		return errors.New("the core created no tunnel interface")
	}
	if !tun.DNS.IsValid() {
		return errors.New("the tunnel has no resolver address")
	}
	fingerprint, err := d.waitTun(ctx, tun)
	if err != nil {
		return err
	}
	token, err := newToken()
	if err != nil {
		return err
	}
	// Written down before acting: a crash in between leaves a record of a
	// resolver that may or may not exist, which is harmless, and not an
	// unrecorded one, which is not.
	if err := rec.Add(ledger.Entry{ID: platform.EntryTunInterface, Kind: ledger.KindTunInterface, Key: tun.Interface, Token: fingerprint}); err != nil {
		return err
	}
	if err := rec.Add(ledger.Entry{ID: platform.EntryResolver, Kind: ledger.KindDNS, Key: d.key, Token: token}); err != nil {
		_ = rec.Remove(platform.EntryTunInterface)
		return err
	}
	if err := d.sys.SetResolver(d.key, []netip.Addr{tun.DNS}, token); err != nil {
		_ = rec.Remove(platform.EntryResolver)
		_ = rec.Remove(platform.EntryTunInterface)
		return fmt.Errorf("point the system resolver at the tunnel: %w", err)
	}
	d.mu.Lock()
	d.tunName = tun.Interface
	d.resolver = token
	d.mu.Unlock()
	return nil
}

// waitTun waits until the tunnel interface is up and carries the address the
// agent gave it, and returns that address as the interface's fingerprint.
func (d *Driver) waitTun(ctx context.Context, tun platform.ActiveTun) (string, error) {
	deadline := time.Now().Add(d.appearWait)
	for {
		ifaces, err := d.host.Interfaces()
		if err != nil {
			return "", err
		}
		for _, i := range ifaces {
			if i.Name != tun.Interface {
				continue
			}
			if fp, ok := tunFingerprint(tun.Addresses, i); ok {
				return fp, nil
			}
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("the tunnel interface %s did not come up with the address the agent gave it", tun.Interface)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// tunFingerprint is the address the agent gave the tunnel, which tells it
// from another program's tunnel that was handed the same name later.
func tunFingerprint(want []netip.Prefix, i Interface) (string, bool) {
	for _, w := range want {
		for _, have := range i.Addrs {
			if have.Addr() == w.Addr() {
				return w.Addr().String(), true
			}
		}
	}
	return "", false
}

// Deactivate gives the system resolver back. It is idempotent.
func (d *Driver) Deactivate(_ context.Context, rec platform.Recorder) error {
	d.mu.Lock()
	token := d.resolver
	d.resolver = ""
	d.mu.Unlock()
	if token != "" {
		if _, err := d.releaseResolver(d.key, token); err != nil {
			d.mu.Lock()
			d.resolver = token // still ours to give back
			d.mu.Unlock()
			return err
		}
	}
	_ = rec.Remove(platform.EntryResolver)
	return nil
}

// Release takes one recorded resource down, after making sure it is ours.
func (d *Driver) Release(ctx context.Context, e ledger.Entry) (ledger.Outcome, error) {
	switch e.Kind {
	case ledger.KindDNS:
		return d.releaseResolver(e.Key, e.Token)
	case ledger.KindTunInterface:
		return d.releaseTun(ctx, e)
	}
	return 0, fmt.Errorf("macplat: unsupported resource kind %q", e.Kind)
}

func (d *Driver) releaseResolver(key, token string) (ledger.Outcome, error) {
	have, present := d.sys.ResolverToken(key)
	switch {
	case !present:
		return ledger.AlreadyGone, nil
	case have == "" || have != token:
		return ledger.NotOurs, nil
	}
	if err := d.sys.RemoveResolver(key); err != nil {
		return 0, fmt.Errorf("remove the temporary resolver: %w", err)
	}
	return ledger.Released, nil
}

// releaseTun checks that the tunnel is gone. The system removes a tunnel when
// the program that made it lets go, which the core did when it stopped; a
// program cannot remove another's, so one that is still there is only
// reported. An interface of the same name that does not carry our address is
// somebody else's, given the name after ours was freed.
func (d *Driver) releaseTun(ctx context.Context, e ledger.Entry) (ledger.Outcome, error) {
	deadline := time.Now().Add(d.goneWait)
	for {
		ifaces, err := d.host.Interfaces()
		if err != nil {
			return 0, err
		}
		var found *Interface
		for i := range ifaces {
			if ifaces[i].Name == e.Key {
				found = &ifaces[i]
			}
		}
		if found == nil {
			d.forgetTun(e.Key)
			return ledger.AlreadyGone, nil
		}
		if e.Token != "" && !carries(*found, e.Token) {
			d.forgetTun(e.Key)
			return ledger.NotOurs, nil
		}
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("the tunnel interface %s is still there; it goes when the program that made it stops", e.Key)
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// forgetTun stops counting an interface as the agent's own once it is gone: the
// system may give the name to another program's tunnel.
func (d *Driver) forgetTun(name string) {
	d.mu.Lock()
	if d.tunName == name {
		d.tunName = ""
	}
	d.mu.Unlock()
}

func carries(i Interface, address string) bool {
	a, err := netip.ParseAddr(address)
	if err != nil {
		return false
	}
	for _, p := range i.Addrs {
		if p.Addr() == a {
			return true
		}
	}
	return false
}

// Sweep removes the resolver the agent leaves behind when nothing is known of
// who made it, if it carries the agent's mark. An entry under the key without
// the mark is somebody else's and stays. A leftover tunnel interface cannot be
// removed by anyone but its owner, and is gone with it.
func (d *Driver) Sweep(context.Context) error {
	token, present := d.sys.ResolverToken(d.key)
	if !present || token == "" {
		return nil
	}
	if err := d.sys.RemoveResolver(d.key); err != nil {
		return fmt.Errorf("remove the temporary resolver: %w", err)
	}
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

// Close stops listening. It does not undo Activate: that is Deactivate's job,
// and the ledger's if the program ends before it.
func (d *Driver) Close() error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	d.closed = true
	stops := d.stops
	d.stops = nil
	d.mu.Unlock()
	for _, stop := range stops {
		stop()
	}
	return nil
}

func newToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

var _ platform.Driver = (*Driver)(nil)
