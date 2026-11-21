// Package droidvpn is Android's VPN service for the core: a native module whose
// Kotlin (kotlin/) runs a VpnService. It makes the VPN interface with the
// settings the core asks for, keeps the core's own sockets out of it, and tells
// the networks of the device, which Android does not let an app read itself.
//
// Callbacks return an error so that Kotlin calls them at once, on its own
// thread, whatever the application's window is doing: a VPN keeps running
// while the application is in the background.
package droidvpn

import (
	"context"
	"io"
)

// Service is Android's VPN service.
//
//mygo:native
type Service interface {
	// Prepare asks the person to let the application be the device's VPN, the
	// first time, and reports whether it may.
	Prepare(ctx context.Context) (bool, error)
	// Open starts the VPN with the settings and returns the file descriptor of
	// its interface, which stays the service's.
	Open(ctx context.Context, s TunSettings) (int, error)
	// Stop stops the VPN. It is safe when none runs.
	Stop() error
	// Protect keeps a socket out of the VPN; it reports false when the system
	// refused. Without a VPN running there is nothing to keep out of.
	Protect(fd int) bool
	// Networks lists the device's networks, the VPN's own left out.
	Networks() ([]Network, error)
	// WatchDefault calls f with the network the system sends traffic through,
	// now and after every change, until the subscription is closed.
	WatchDefault(f func(n DefaultNetwork) error) io.Closer
	// OnRevoked calls f when the system ends the VPN: the person turned it off
	// in the settings, or another application's VPN took its place.
	OnRevoked(f func(reason string) error) io.Closer
	// OnSystemStart calls f when the system starts the VPN by itself:
	// Android's always-on VPN, at boot or after the VPN went away. The
	// system may start the application's process for it, Go with it; a start
	// that came before f was given is told as f is given.
	OnSystemStart(f func() error) io.Closer
	// OnTileToggle calls f when the person taps EggBox's Quick Settings tile.
	// The system may start the application's process for it without a window;
	// a tap that arrived before f was given is delivered as f is given.
	OnTileToggle(f func() error) io.Closer
	// SetTileState updates EggBox's Quick Settings tile so its state and
	// subtitle follow the connection.
	SetTileState(state TileState) error
	// Owner finds the application that opened a connection.
	Owner(protocol int, source string, sourcePort int, destination string, destinationPort int) (ConnectionOwner, error)
}

// Tile state values carried by TileState.State.
const (
	TileInactive      = "inactive"
	TileConnecting    = "connecting"
	TileActive        = "active"
	TileDisconnecting = "disconnecting"
)

// TileState describes what Android's Quick Settings tile shows.
type TileState struct {
	// State is TileInactive, TileConnecting, TileActive or TileDisconnecting.
	State string
	// Subtitle is shown under the tile's label on Android 10+. Empty clears it.
	Subtitle string
}

// TunSettings are what the VPN interface is made with. Addresses and routes
// are CIDR prefixes ("172.19.0.1/30"), resolvers addresses.
type TunSettings struct {
	Session        string
	Addresses      []string
	Routes         []string
	ExcludedRoutes []string
	DNS            []string
	MTU            int
}

// Network is one network of the device.
type Network struct {
	Name      string
	Index     int
	MTU       int
	Addresses []string
	// Kind is "wifi", "cellular", "ethernet" or "other".
	Kind     string
	DNS      []string
	Gateways []string
	Metered  bool
}

// DefaultNetwork is the network the system sends traffic through; Index is -1
// when there is none.
type DefaultNetwork struct {
	Name        string
	Index       int
	Expensive   bool
	Constrained bool
}

// ConnectionOwner is the application of a connection: its user id and its
// packages (several applications can share a user id).
type ConnectionOwner struct {
	UID      int
	Packages []string
}
