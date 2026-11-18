// Package iosvpn is iOS's VPN for the application: a native module whose
// Swift (swift/) runs the system's NETunnelProviderManager. It installs
// EggBox's VPN configuration, which the system asks the person to allow the
// first time, and starts and stops the packet tunnel extension that runs the
// core (native/ios/PacketTunnel). The application and the extension share an
// App Group container, whose directory it also finds.
//
// The callback returns an error so that Swift calls it at once, whatever the
// application's window is doing.
package iosvpn

import (
	"context"
	"io"

	"github.com/egoist/mygo/native"
)

// ErrNotAllowed is the person declining to add EggBox's VPN configuration.
var ErrNotAllowed = native.NewError("not_allowed")

// Tunnel is the system's VPN of the application.
//
//mygo:native
type Tunnel interface {
	// GroupDirectory is the container the application shares with its
	// extension.
	GroupDirectory() (string, error)
	// Install adds EggBox's VPN configuration, which the system asks the
	// person to allow the first time, or turns it back on; ErrNotAllowed when
	// they decline.
	Install(ctx context.Context) error
	// Start installs EggBox's VPN configuration the first time, starts the
	// tunnel and returns once it is connected.
	Start(ctx context.Context) error
	// Stop stops the tunnel and returns once it is down.
	Stop(ctx context.Context) error
	// Status is the tunnel's state: "invalid" (not installed),
	// "disconnected", "connecting", "connected", "reasserting" or
	// "disconnecting".
	Status(ctx context.Context) (string, error)
	// WatchStatus calls f with the state after every change, until the
	// subscription is closed.
	WatchStatus(f func(status string) error) io.Closer
}
