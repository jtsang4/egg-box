// Package androidhost is the core's tunnel.Host on Android: the VpnService of
// the droidvpn native module, which this package turns into what
// internal/phone/tunnel and the phone's platform driver ask for.
package androidhost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"sync"
	"time"

	"eggbox/internal/agent/platform/phoneplat"
	"eggbox/internal/phone/droidvpn"
	"eggbox/internal/phone/tunnel"
)

// consentTimeout bounds the wait for the person to answer the system's
// question whether the application may be the device's VPN.
const consentTimeout = 2 * time.Minute

// Host is Android's side of the core.
type Host struct {
	svc     droidvpn.Service
	session string

	mu      sync.Mutex
	revoked io.Closer
}

var _ tunnel.Host = (*Host)(nil)

// New returns the Host over the VPN service; session names the VPN in the
// system's settings.
func New(svc droidvpn.Service, session string) *Host {
	return &Host{svc: svc, session: session}
}

// Bind reports the system ending the VPN to the platform.
func (h *Host) Bind(p *tunnel.Platform) {
	closer := h.svc.OnRevoked(func(reason string) error {
		p.TunnelLost(reason)
		return nil
	})
	h.mu.Lock()
	h.revoked = closer
	h.mu.Unlock()
}

// OpenTun asks the person the first time, then makes the VPN interface.
func (h *Host) OpenTun(s tunnel.Settings) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), consentTimeout)
	defer cancel()
	ok, err := h.svc.Prepare(ctx)
	if err != nil {
		return -1, fmt.Errorf("ask to be the device's VPN: %w", err)
	}
	if !ok {
		return -1, ErrNotAllowed
	}
	return h.svc.Open(ctx, droidvpn.TunSettings{
		Session:        h.session,
		Addresses:      prefixStrings(s.Addresses),
		Routes:         prefixStrings(s.Routes),
		ExcludedRoutes: prefixStrings(s.ExcludedRoutes),
		DNS:            addrStrings(s.DNS),
		MTU:            s.MTU,
	})
}

// ErrNotAllowed is the person declining the VPN.
var ErrNotAllowed = errors.New("the VPN was not allowed: EggBox may connect once it is allowed to be the device's VPN")

func (h *Host) CloseTun() { _ = h.svc.Stop() }

func (h *Host) Protect(fd int) error {
	if !h.svc.Protect(fd) {
		return errors.New("the system did not keep the socket out of the VPN")
	}
	return nil
}

func (h *Host) Interfaces() ([]tunnel.Interface, error) {
	list, err := h.svc.Networks()
	if err != nil {
		return nil, err
	}
	out := make([]tunnel.Interface, 0, len(list))
	for _, n := range list {
		out = append(out, tunnel.Interface{
			Name: n.Name, Index: n.Index, MTU: n.MTU, Kind: n.Kind, Metered: n.Metered,
			Addresses: parsePrefixes(n.Addresses), DNS: parseAddrs(n.DNS), Gateways: parseAddrs(n.Gateways),
		})
	}
	return out, nil
}

// WatchDefault hands the changes to f on a goroutine of its own, in order: the
// core reads the networks again on each, which is a call back into Kotlin.
func (h *Host) WatchDefault(f func(tunnel.Default)) (func(), error) {
	changes := make(chan tunnel.Default, 16)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case d := <-changes:
				f(d)
			case <-done:
				return
			}
		}
	}()
	closer := h.svc.WatchDefault(func(n droidvpn.DefaultNetwork) error {
		d := tunnel.Default{Name: n.Name, Index: n.Index, Expensive: n.Expensive, Constrained: n.Constrained}
		select {
		case changes <- d:
		case <-done:
		}
		return nil
	})
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = closer.Close()
			close(done)
		})
	}, nil
}

func (h *Host) Owner(network int32, source, destination netip.AddrPort) (tunnel.Owner, error) {
	o, err := h.svc.Owner(int(network), source.Addr().String(), int(source.Port()), destination.Addr().String(), int(destination.Port()))
	if err != nil {
		return tunnel.Owner{}, err
	}
	return tunnel.Owner{UID: o.UID, Packages: o.Packages}, nil
}

func (h *Host) UnderNetworkExtension() bool { return false }

// Networks lists the device's networks for the platform driver, so that the
// tunnel's subnet avoids theirs.
func (h *Host) Networks(context.Context) ([]phoneplat.Network, error) {
	list, err := h.svc.Networks()
	if err != nil {
		return nil, err
	}
	out := make([]phoneplat.Network, 0, len(list))
	for _, n := range list {
		out = append(out, phoneplat.Network{Name: n.Name, Addresses: parsePrefixes(n.Addresses)})
	}
	return out, nil
}

// Close stops listening to the system.
func (h *Host) Close() {
	h.mu.Lock()
	c := h.revoked
	h.revoked = nil
	h.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
}

func prefixStrings(ps []netip.Prefix) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.String())
	}
	return out
}

func addrStrings(as []netip.Addr) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, a.String())
	}
	return out
}

func parsePrefixes(ss []string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(ss))
	for _, s := range ss {
		if p, err := netip.ParsePrefix(s); err == nil {
			out = append(out, p)
		}
	}
	return out
}

func parseAddrs(ss []string) []netip.Addr {
	out := make([]netip.Addr, 0, len(ss))
	for _, s := range ss {
		if a, err := netip.ParseAddr(s); err == nil {
			out = append(out, a)
		}
	}
	return out
}
