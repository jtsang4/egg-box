// Package iostunnel is the core's side of EggBox on iOS. The VPN runs in the
// application's packet tunnel extension, a process the system starts with the
// tunnel and ends with it; the agent that chooses what runs stays in the
// application (internal/phone/iosengine). The two share an App Group
// container: the application writes the configuration the agent accepted
// there, the extension runs it with the core and serves the core's calls on a
// Unix socket there (internal/agent/engine/remote).
//
// Nothing here is specific to the extension's process, so it is tested on any
// machine; cmd/eggbox-tunnel is the extension's library around it.
package iostunnel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"eggbox/internal/agent/engine"
	"eggbox/internal/agent/engine/remote"
	"eggbox/internal/agent/engine/singbox"
	"eggbox/internal/fsutil"
	"eggbox/internal/phone/tunnel"
)

// The files of the shared container.
const (
	// PreparedFile is the configuration to run, which the application writes.
	PreparedFile = "tunnel/prepared.json"
	// SocketName is the core's socket. A container's path is too long for a
	// Unix socket's address on the simulator, so both processes use it from
	// the container as their working directory.
	SocketName = "engine.sock"
)

// prepared is engine.Prepared as the file holds it.
type prepared struct {
	Revision uint64          `json:"revision"`
	HasTun   bool            `json:"has_tun"`
	TunTag   string          `json:"tun_tag,omitempty"`
	LogLevel string          `json:"log_level,omitempty"`
	Config   json.RawMessage `json:"config"`
}

// WritePrepared hands a configuration to the extension.
func WritePrepared(dir string, p engine.Prepared) error {
	if !json.Valid(p.JSON) {
		return errors.New("iostunnel: the configuration is not JSON")
	}
	b, err := json.Marshal(prepared{Revision: p.Revision, HasTun: p.HasTun, TunTag: p.TunTag, LogLevel: p.LogLevel, Config: p.JSON})
	if err != nil {
		return err
	}
	path := filepath.Join(dir, PreparedFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, b, 0o600)
}

// ReadPrepared reads the configuration the application handed over.
func ReadPrepared(dir string) (engine.Prepared, error) {
	b, err := os.ReadFile(filepath.Join(dir, PreparedFile))
	if err != nil {
		return engine.Prepared{}, fmt.Errorf("read the configuration: %w", err)
	}
	var p prepared
	if err := json.Unmarshal(b, &p); err != nil {
		return engine.Prepared{}, fmt.Errorf("read the configuration: %w", err)
	}
	return engine.Prepared{Revision: p.Revision, JSON: p.Config, HasTun: p.HasTun, TunTag: p.TunTag, LogLevel: p.LogLevel}, nil
}

// Core is the running core of the extension.
type Core struct {
	eng *singbox.Engine
	srv *remote.Server
	lis net.Listener
}

// Start runs the configuration in dir with the system as host, and serves the
// core on socket (a path, or a name in the working directory).
func Start(ctx context.Context, dir, socket string, host tunnel.Host) (*Core, error) {
	p, err := ReadPrepared(dir)
	if err != nil {
		return nil, err
	}
	eng := singbox.NewWithPlatform(singbox.RegistryOptions{}, tunnel.New(host))
	srv := remote.NewServer(eng)
	_ = os.Remove(socket)
	lis, err := net.Listen("unix", socket)
	if err != nil {
		return nil, fmt.Errorf("serve the core: %w", err)
	}
	go func() { _ = srv.Serve(lis) }()
	if err := eng.Start(ctx, p); err != nil {
		lis.Close()
		_ = os.Remove(socket)
		return nil, err
	}
	return &Core{eng: eng, srv: srv, lis: lis}, nil
}

// Done is closed when the core stopped by itself; Exit says why.
func (c *Core) Done() <-chan struct{} { return c.srv.Done() }

// Exit is why the core stopped by itself.
func (c *Core) Exit() engine.ExitEvent { return c.srv.Exit() }

// Stop stops the core and the socket.
func (c *Core) Stop(ctx context.Context) error {
	err := c.eng.Stop(ctx)
	_ = c.lis.Close()
	return err
}

// ---------------------------------------------------------------- the host

// Host is the extension's side of the system: the packet tunnel provider
// applies the tunnel's settings (Apply, in Swift), and the core finds the
// interface they made among the process's descriptors.
type Host struct {
	// Apply sets the tunnel's network settings, as JSON (Settings), and
	// returns once the system applied them.
	Apply func(settings []byte) error
	// FindTun returns the descriptor of the tunnel's interface; nil scans the
	// process's descriptors for the utun one.
	FindTun func() (int, error)

	mu       sync.Mutex
	def      *tunnel.Default
	watchers map[int]func(tunnel.Default)
	next     int
}

var _ tunnel.Host = (*Host)(nil)

// Settings is the tunnel's network settings as the provider reads them.
type Settings struct {
	Addresses      []Prefix `json:"addresses"`
	Routes         []Prefix `json:"routes"`
	ExcludedRoutes []Prefix `json:"excluded_routes"`
	DNS            []string `json:"dns"`
	MTU            int      `json:"mtu"`
}

// Prefix is an address and its prefix length, with the mask an IPv4 one
// takes in NEIPv4Settings.
type Prefix struct {
	Address string `json:"address"`
	Bits    int    `json:"bits"`
	IPv6    bool   `json:"ipv6"`
	Mask    string `json:"mask,omitempty"`
}

// SettingsJSON turns the core's settings into the provider's.
func SettingsJSON(s tunnel.Settings) ([]byte, error) {
	conv := func(ps []netip.Prefix) []Prefix {
		out := make([]Prefix, 0, len(ps))
		for _, p := range ps {
			x := Prefix{Address: p.Addr().String(), Bits: p.Bits(), IPv6: p.Addr().Is6() && !p.Addr().Is4In6()}
			if !x.IPv6 {
				m := net.CIDRMask(p.Bits(), 32)
				x.Mask = net.IP(m).String()
			}
			out = append(out, x)
		}
		return out
	}
	dns := make([]string, 0, len(s.DNS))
	for _, a := range s.DNS {
		dns = append(dns, a.String())
	}
	mtu := s.MTU
	if mtu == 0 {
		mtu = 1500
	}
	return json.Marshal(Settings{Addresses: conv(s.Addresses), Routes: conv(s.Routes), ExcludedRoutes: conv(s.ExcludedRoutes), DNS: dns, MTU: mtu})
}

func (h *Host) OpenTun(s tunnel.Settings) (int, error) {
	b, err := SettingsJSON(s)
	if err != nil {
		return -1, err
	}
	if err := h.Apply(b); err != nil {
		return -1, fmt.Errorf("apply the tunnel's settings: %w", err)
	}
	find := h.FindTun
	if find == nil {
		find = findUtun
	}
	return find()
}

// CloseTun has nothing to close: the system ends the interface with the
// provider.
func (h *Host) CloseTun() {}

// Protect does nothing: the provider's own sockets never enter its tunnel.
func (h *Host) Protect(int) error { return nil }

func (h *Host) Interfaces() ([]tunnel.Interface, error) {
	list, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []tunnel.Interface
	for _, ifc := range list {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || strings.HasPrefix(ifc.Name, "utun") {
			continue
		}
		it := tunnel.Interface{Name: ifc.Name, Index: ifc.Index, MTU: ifc.MTU, Kind: kindOf(ifc.Name)}
		if addrs, err := ifc.Addrs(); err == nil {
			for _, a := range addrs {
				if p, err := netip.ParsePrefix(a.String()); err == nil {
					it.Addresses = append(it.Addresses, p)
				}
			}
		}
		out = append(out, it)
	}
	return out, nil
}

// kindOf tells an interface's kind by the names iOS gives them.
func kindOf(name string) string {
	switch {
	case strings.HasPrefix(name, "en"):
		return "wifi"
	case strings.HasPrefix(name, "pdp_ip"):
		return "cellular"
	}
	return "other"
}

// WatchDefault reports the default network that SetDefault receives.
func (h *Host) WatchDefault(f func(tunnel.Default)) (func(), error) {
	h.mu.Lock()
	if h.watchers == nil {
		h.watchers = map[int]func(tunnel.Default){}
	}
	id := h.next
	h.next++
	h.watchers[id] = f
	cur := h.def
	h.mu.Unlock()
	if cur != nil {
		f(*cur)
	}
	return func() {
		h.mu.Lock()
		delete(h.watchers, id)
		h.mu.Unlock()
	}, nil
}

// SetDefault is the provider telling the default network (NWPathMonitor).
func (h *Host) SetDefault(d tunnel.Default) {
	h.mu.Lock()
	h.def = &d
	ws := make([]func(tunnel.Default), 0, len(h.watchers))
	for _, f := range h.watchers {
		ws = append(ws, f)
	}
	h.mu.Unlock()
	for _, f := range ws {
		f(d)
	}
}

// Owner is not told on iOS.
func (h *Host) Owner(int32, netip.AddrPort, netip.AddrPort) (tunnel.Owner, error) {
	return tunnel.Owner{}, tunnel.ErrUnsupported
}

func (h *Host) UnderNetworkExtension() bool { return true }
