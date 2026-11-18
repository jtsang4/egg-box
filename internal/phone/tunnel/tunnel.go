// Package tunnel lets the embedded sing-box core run on a phone, where the
// system owns the VPN interface: Android's VpnService and iOS's packet tunnel
// provider make the TUN and hand its file descriptor to the app, keep the
// app's own sockets out of it, and know the networks the device is on (which
// an app cannot read itself: Android refuses netlink to apps).
//
// Platform is the core's adapter.PlatformInterface over a Host, which the
// phone implements (Kotlin on Android, Swift in the iOS extension). Nothing
// here is specific to one phone, so it is tested on any machine with a fake
// Host.
package tunnel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"sync"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	tun "github.com/sagernet/sing-tun"
	"github.com/sagernet/sing/common/control"
	"github.com/sagernet/sing/common/logger"
	"github.com/sagernet/sing/common/x/list"
)

// Settings are what the system's VPN interface is made with: the core's TUN
// options, flattened to what VpnService.Builder and NEPacketTunnelNetworkSettings
// take.
type Settings struct {
	// Addresses are the interface's own addresses.
	Addresses []netip.Prefix
	// Routes go into the tunnel; ExcludedRoutes stay outside it.
	Routes         []netip.Prefix
	ExcludedRoutes []netip.Prefix
	// DNS are the resolvers the system uses while the tunnel is up, inside it.
	DNS []netip.Addr
	MTU int
}

// Interface is one network of the device.
type Interface struct {
	Name      string
	Index     int
	MTU       int
	Addresses []netip.Prefix
	// Kind is "wifi", "cellular", "ethernet" or "other".
	Kind     string
	DNS      []netip.Addr
	Gateways []netip.Addr
	Metered  bool
}

// Default is the network the system sends traffic through. Index -1 means
// there is none (offline).
type Default struct {
	Name        string
	Index       int
	Expensive   bool
	Constrained bool
}

// Owner is the app a connection belongs to.
type Owner struct {
	UID      int
	Packages []string
}

// ErrUnsupported is what a Host returns for what its system cannot do.
var ErrUnsupported = errors.ErrUnsupported

// Host is what the phone's system does for the core. Its methods may be called
// from any goroutine.
type Host interface {
	// OpenTun makes the VPN interface with the settings and returns its file
	// descriptor, which stays the Host's: the core works on a duplicate.
	OpenTun(s Settings) (int, error)
	// CloseTun ends the VPN once the core has let go of the interface. The
	// core closes only its duplicate: without this the system would keep the
	// VPN up, carrying traffic nowhere.
	CloseTun()
	// Protect keeps the socket out of the tunnel (Android). The iOS
	// extension's sockets never enter its own tunnel, so it does nothing there.
	Protect(fd int) error
	// Interfaces lists the device's networks.
	Interfaces() ([]Interface, error)
	// WatchDefault calls f with the default network now and after every
	// change, until stop is called.
	WatchDefault(f func(Default)) (stop func(), err error)
	// Owner finds the app that opened a connection; ErrUnsupported where the
	// system does not tell (iOS).
	Owner(network int32, source, destination netip.AddrPort) (Owner, error)
	// UnderNetworkExtension is true in the iOS extension.
	UnderNetworkExtension() bool
}

// Platform is the core's view of the phone. One Platform serves one engine;
// it may open the TUN several times over its life (once per connection).
type Platform struct {
	host Host
	// tunName reads the name of the interface behind a descriptor
	// (ioctl on Android, getsockopt on iOS); tests replace it.
	tunName func(fd int) (string, error)
	// newTun makes the core's TUN from options carrying the descriptor;
	// tests replace it.
	newTun func(tun.Options) (tun.Tun, error)

	mu        sync.Mutex
	network   adapter.NetworkManager
	name      string
	addresses []netip.Addr
	def       *control.Interface
	expensive bool
	limited   bool
	lost      chan error
}

var _ adapter.PlatformInterface = (*Platform)(nil)

// New returns the Platform of a Host.
func New(host Host) *Platform {
	return &Platform{host: host, tunName: interfaceName, newTun: newTun, lost: make(chan error, 1)}
}

// TunName is the VPN interface the last OpenInterface made ("" before).
func (p *Platform) TunName() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.name
}

// Lost delivers an error when the system took the tunnel away: the user
// revoked the VPN, or another app's VPN replaced it.
func (p *Platform) Lost() <-chan error { return p.lost }

// TunnelLost is called by the Host when the system ended the tunnel.
func (p *Platform) TunnelLost(reason string) {
	select {
	case p.lost <- fmt.Errorf("the system ended the VPN: %s", reason):
	default:
	}
}

// CloseTun ends the system's VPN after the core stopped.
func (p *Platform) CloseTun() {
	p.mu.Lock()
	p.name, p.addresses = "", nil
	p.mu.Unlock()
	p.host.CloseTun()
}

// ResetLost drops a loss reported for an earlier tunnel, before a new one.
func (p *Platform) ResetLost() {
	select {
	case <-p.lost:
	default:
	}
}

// SettingsOf flattens the core's TUN options for the system.
func SettingsOf(o *tun.Options) (Settings, error) {
	s := Settings{MTU: int(o.MTU)}
	s.Addresses = append(append(s.Addresses, o.Inet4Address...), o.Inet6Address...)
	if o.AutoRoute {
		routes, err := o.BuildAutoRouteRanges(true)
		if err != nil {
			return Settings{}, err
		}
		s.Routes = routes
	}
	s.ExcludedRoutes = append(append(s.ExcludedRoutes, o.Inet4RouteExcludeAddress...), o.Inet6RouteExcludeAddress...)
	if o.DNSModeOrDefault() != tun.DNSModeDisabled {
		dns, err := o.DNSServerAddress()
		if err != nil {
			return Settings{}, err
		}
		s.DNS = dns
	}
	return s, nil
}

// ---------------------------------------------------------------- adapter.PlatformInterface

func (p *Platform) Initialize(networkManager adapter.NetworkManager) error {
	p.mu.Lock()
	p.network = networkManager
	p.mu.Unlock()
	return nil
}

func (p *Platform) UsePlatformAutoDetectInterfaceControl() bool { return true }

func (p *Platform) AutoDetectInterfaceControl(fd int) error { return p.host.Protect(fd) }

func (p *Platform) UsePlatformInterface() bool { return true }

func (p *Platform) OpenInterface(options *tun.Options, _ option.TunPlatformOptions) (tun.Tun, error) {
	if len(options.IncludeUID) > 0 || len(options.ExcludeUID) > 0 || len(options.IncludeAndroidUser) > 0 {
		return nil, errors.New("tunnel: per-user TUN options are not supported on phones")
	}
	settings, err := SettingsOf(options)
	if err != nil {
		return nil, err
	}
	fd, err := p.host.OpenTun(settings)
	if err != nil {
		return nil, fmt.Errorf("open the VPN interface: %w", err)
	}
	name, err := p.tunName(fd)
	if err != nil {
		return nil, fmt.Errorf("read the VPN interface's name: %w", err)
	}
	options.Name = name
	if options.InterfaceMonitor != nil {
		options.InterfaceMonitor.RegisterMyInterface(name)
	}
	own, err := dupFD(fd)
	if err != nil {
		return nil, fmt.Errorf("duplicate the VPN interface: %w", err)
	}
	options.FileDescriptor = own
	addrs := make([]netip.Addr, 0, len(settings.Addresses))
	for _, a := range settings.Addresses {
		addrs = append(addrs, a.Addr())
	}
	p.mu.Lock()
	p.name, p.addresses = name, addrs
	p.mu.Unlock()
	return p.newTun(*options)
}

func (p *Platform) ProcessPlatformOptions(option.TunPlatformOptions) error { return nil }

func (p *Platform) UsePlatformDefaultInterfaceMonitor() bool { return true }

func (p *Platform) CreateDefaultInterfaceMonitor(l logger.Logger) tun.DefaultInterfaceMonitor {
	return &monitor{p: p, log: l}
}

func (p *Platform) UsePlatformNetworkInterfaces() bool { return true }

func (p *Platform) NetworkInterfaces() ([]adapter.NetworkInterface, error) {
	list, err := p.host.Interfaces()
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	def, expensive, limited, own := p.def, p.expensive, p.limited, p.name
	p.mu.Unlock()
	out := make([]adapter.NetworkInterface, 0, len(list))
	seen := map[string]bool{}
	for _, it := range list {
		if it.Name == "" || seen[it.Name] {
			continue
		}
		seen[it.Name] = true
		isDefault := it.Name != own && def != nil && it.Index == def.Index
		out = append(out, adapter.NetworkInterface{
			Interface: control.Interface{
				Index: it.Index, MTU: it.MTU, Name: it.Name,
				Addresses: slices.Clone(it.Addresses), Flags: upFlags,
			},
			Type:        interfaceType(it.Kind),
			DNSServers:  addrStrings(it.DNS),
			Gateways:    slices.Clone(it.Gateways),
			Expensive:   it.Metered || isDefault && expensive,
			Constrained: isDefault && limited,
		})
	}
	return out, nil
}

func (p *Platform) UnderNetworkExtension() bool { return p.host.UnderNetworkExtension() }

func (p *Platform) NetworkExtensionIncludeAllNetworks() bool { return false }

func (p *Platform) ClearDNSCache() {}

func (p *Platform) RequestPermissionForWIFIState() error { return nil }

func (p *Platform) ReadWIFIState(context.Context) adapter.WIFIState { return adapter.WIFIState{} }

func (p *Platform) UsePlatformConnectionOwnerFinder() bool { return true }

func (p *Platform) FindConnectionOwner(r *adapter.FindConnectionOwnerRequest) (*adapter.ConnectionOwner, error) {
	src, err := netip.ParseAddr(r.SourceAddress)
	if err != nil {
		return nil, err
	}
	dst, err := netip.ParseAddr(r.DestinationAddress)
	if err != nil {
		return nil, err
	}
	o, err := p.host.Owner(r.IpProtocol, netip.AddrPortFrom(src, uint16(r.SourcePort)), netip.AddrPortFrom(dst, uint16(r.DestinationPort)))
	if err != nil {
		return nil, err
	}
	return &adapter.ConnectionOwner{UserId: int32(o.UID), AndroidPackageNames: o.Packages}, nil
}

func (p *Platform) UsePlatformWIFIMonitor() bool { return false }

func (p *Platform) UsePlatformNotification() bool { return false }

func (p *Platform) SendNotification(*adapter.Notification) error { return ErrUnsupported }

func (p *Platform) CancelNotification(string, int32) error { return ErrUnsupported }

func (p *Platform) MyInterfaceAddress() []netip.Addr {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.addresses)
}

func (p *Platform) UsePlatformNeighborResolver() bool { return false }

func (p *Platform) StartNeighborMonitor(adapter.NeighborUpdateListener) error { return ErrUnsupported }

func (p *Platform) CloseNeighborMonitor(adapter.NeighborUpdateListener) error { return nil }

func (p *Platform) UsePlatformShell() bool { return false }

func (p *Platform) CheckPlatformShell() error { return ErrUnsupported }

func (p *Platform) OpenShellSession(*adapter.PlatformUser, string, []string, string, int32, int32) (adapter.ShellSession, error) {
	return nil, ErrUnsupported
}

func (p *Platform) LookupUser(string) (*adapter.PlatformUser, error) { return nil, ErrUnsupported }

func (p *Platform) LookupSFTPServer() (string, error) { return "", ErrUnsupported }

func (p *Platform) ReadSystemSSHHostKey() ([]byte, error) { return nil, ErrUnsupported }

func (p *Platform) TailscaleHostname() string { return "" }

func (p *Platform) UsePlatformBridge() bool { return false }

func (p *Platform) CreateBridge(adapter.BridgeOptions) (adapter.BridgeSession, error) {
	return nil, ErrUnsupported
}

// ---------------------------------------------------------------- default interface monitor

// monitor follows the default network as the Host reports it. The core binds
// its outbound sockets to that network and re-binds them when it changes, so
// moving between Wi-Fi and cellular needs no reconnect.
type monitor struct {
	p   *Platform
	log logger.Logger

	mu        sync.Mutex
	callbacks list.List[tun.DefaultInterfaceUpdateCallback]
	mine      []string
	started   bool
	stop      func()
}

var _ tun.DefaultInterfaceMonitor = (*monitor)(nil)

func (m *monitor) Start() error {
	stop, err := m.p.host.WatchDefault(m.update)
	if err != nil {
		return fmt.Errorf("watch the default network: %w", err)
	}
	m.mu.Lock()
	m.stop, m.started = stop, true
	m.mu.Unlock()
	return nil
}

func (m *monitor) Close() error {
	m.mu.Lock()
	stop := m.stop
	m.stop, m.started = nil, false
	m.mu.Unlock()
	if stop != nil {
		stop()
	}
	return nil
}

func (m *monitor) update(d Default) {
	p := m.p
	p.mu.Lock()
	network := p.network
	p.mu.Unlock()
	if network != nil {
		if err := network.UpdateInterfaces(); err != nil && m.log != nil {
			m.log.Error("update interfaces: ", err)
		}
	}
	var next *control.Interface
	if d.Index >= 0 && d.Name != "" {
		next = &control.Interface{Name: d.Name, Index: d.Index}
		if network != nil {
			if found, err := network.InterfaceFinder().ByIndex(d.Index); err == nil {
				next = found
			} else if m.log != nil {
				m.log.Error("find the default interface ", d.Name, ": ", err)
			}
		}
	}
	p.mu.Lock()
	prev := p.def
	p.def, p.expensive, p.limited = next, d.Expensive, d.Constrained
	p.mu.Unlock()
	if prev != nil && next != nil && prev.Name == next.Name && prev.Index == next.Index {
		return
	}
	m.mu.Lock()
	callbacks := m.callbacks.Array()
	m.mu.Unlock()
	for _, cb := range callbacks {
		cb(next, 0)
	}
}

func (m *monitor) DefaultInterface() *control.Interface {
	m.p.mu.Lock()
	defer m.p.mu.Unlock()
	return m.p.def
}

func (m *monitor) OverrideAndroidVPN() bool { return false }

func (m *monitor) AndroidVPNEnabled() bool { return false }

func (m *monitor) RegisterCallback(cb tun.DefaultInterfaceUpdateCallback) *list.Element[tun.DefaultInterfaceUpdateCallback] {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callbacks.PushBack(cb)
}

func (m *monitor) UnregisterCallback(e *list.Element[tun.DefaultInterfaceUpdateCallback]) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callbacks.Remove(e)
}

func (m *monitor) RegisterMyInterface(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mine = append(m.mine, name)
}

func (m *monitor) MyInterfaces() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.mine)
}

// ---------------------------------------------------------------- helpers

// upFlags marks an interface the system reports as usable.
const upFlags = net.FlagUp | net.FlagRunning

func interfaceType(kind string) C.InterfaceType {
	switch kind {
	case "wifi":
		return C.InterfaceTypeWIFI
	case "cellular":
		return C.InterfaceTypeCellular
	case "ethernet":
		return C.InterfaceTypeEthernet
	}
	return C.InterfaceTypeOther
}

func addrStrings(as []netip.Addr) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, a.String())
	}
	return out
}

func newTun(o tun.Options) (tun.Tun, error) { return tun.New(o) }
