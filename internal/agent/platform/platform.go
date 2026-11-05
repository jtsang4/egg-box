// Package platform is the seam between the controller and the operating system.
//
// The controller decides *what* must happen (check routes, bring the TUN up,
// point DNS at it, clean up); a Driver knows *how* on one OS. Real drivers
// (darwin, windows) are only constructed by the installed service; tests and
// the development sandbox use platform/sim, which cannot affect the host.
package platform

import (
	"context"
	"net/netip"
	"time"

	"github.com/jtsang4/sing-box-ui/internal/agent/ledger"
	"github.com/jtsang4/sing-box-ui/internal/agent/routecheck"
)

// EventKind classifies what the OS reported.
type EventKind int

const (
	// NetworkChanged: interfaces, addresses or routes changed.
	NetworkChanged EventKind = iota + 1
	// Woke: the machine resumed from sleep.
	Woke
	// Slept: the machine is going to sleep.
	Slept
	// UserLoggedIn: the authorized user started a session.
	UserLoggedIn
	// UserLoggedOut: the authorized user ended the session (or another user took over).
	UserLoggedOut
)

func (k EventKind) String() string {
	switch k {
	case NetworkChanged:
		return "network-changed"
	case Woke:
		return "woke"
	case Slept:
		return "slept"
	case UserLoggedIn:
		return "user-logged-in"
	case UserLoggedOut:
		return "user-logged-out"
	default:
		return "unknown"
	}
}

// Event is one raw OS notification.
type Event struct {
	Kind   EventKind
	At     time.Time
	Detail string
}

// TunDefaults are the platform-owned TUN settings (see the design table).
type TunDefaults struct {
	// StrictRoute is true on Windows to stop DNS leaking around the TUN.
	StrictRoute bool
	// DNSMode is the sing-box tun.dns_mode: "hijack" on Windows, "disabled" on
	// macOS where the Swift bridge points the system resolver at the TUN.
	DNSMode string
	// InterfaceName fixes the adapter name on Windows; empty on macOS (utunN).
	InterfaceName string
}

// ActiveTun describes the running TUN for Activate.
type ActiveTun struct {
	Interface string
	Addresses []netip.Prefix
	// DNS is the in-tunnel resolver address the system should use.
	DNS netip.Addr
}

// Well-known ledger entry IDs. The controller pre-declares the TUN adapter
// under EntryTunInterface when the platform fixes its name (Windows), and
// drivers re-use the same IDs so a resource is never recorded twice.
const (
	EntryTunInterface = "iface"
	EntryResolver     = "resolver"
)

// Recorder lets a driver register the resources it creates in the ledger.
type Recorder interface {
	Add(ledger.Entry) error
	Remove(id string) error
}

// Driver implements the OS-specific parts.
type Driver interface {
	// Name identifies the driver ("darwin", "windows", "sim").
	Name() string
	// Sandbox is true when the driver must keep the host network untouched. The
	// controller then runs TUN configurations as a loopback proxy instead.
	Sandbox() bool

	// Snapshot reads interfaces and routes. It never changes anything.
	Snapshot(ctx context.Context) (routecheck.Snapshot, error)
	// Fingerprint identifies the current uplink (default interface and gateway).
	// It changes when the machine moves to another network.
	Fingerprint(ctx context.Context) (string, error)
	// Online reports whether a usable uplink exists.
	Online(ctx context.Context) bool
	// TunDefaults returns the platform's TUN settings.
	TunDefaults() TunDefaults

	// Activate applies OS-level pieces after the core created the TUN, e.g.
	// point the system resolver at the tunnel. Everything it creates must be
	// recorded through rec before the call returns.
	Activate(ctx context.Context, tun ActiveTun, rec Recorder) error
	// Deactivate undoes Activate. It must be idempotent.
	Deactivate(ctx context.Context, rec Recorder) error
	// Release removes one recorded resource after verifying it is still ours.
	Release(ctx context.Context, e ledger.Entry) (ledger.Outcome, error)
	// Sweep removes leftovers identified only by well-known names. It is used
	// when the ledger was unreadable and nothing is known about ownership.
	Sweep(ctx context.Context) error

	// Events streams raw OS notifications until ctx ends.
	Events(ctx context.Context) <-chan Event
	// Close releases the driver's own resources (watchers, handles).
	Close() error
}
