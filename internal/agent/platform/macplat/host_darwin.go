//go:build darwin

package macplat

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"sync"
	"syscall"

	"golang.org/x/net/route"

	"github.com/jtsang4/sing-box-ui/internal/agent/routecheck"
)

// SystemHost reads the real machine: its interfaces, its routing table, and
// the notifications the kernel sends over a routing socket. It only reads and
// listens; nothing it does changes the network.
func SystemHost() Host { return systemHost{} }

type systemHost struct{}

func (systemHost) Interfaces() ([]Interface, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("list interfaces: %w", err)
	}
	out := make([]Interface, 0, len(ifs))
	for _, ifc := range ifs {
		i := Interface{
			Index: ifc.Index,
			Name:  ifc.Name,
			Up:    ifc.Flags&net.FlagUp != 0 && ifc.Flags&net.FlagRunning != 0,
			Kind: routecheck.ClassifyByName(ifc.Name,
				ifc.Flags&net.FlagPointToPoint != 0, ifc.Flags&net.FlagLoopback != 0),
		}
		// An interface whose addresses cannot be read is listed without them:
		// it may have just gone.
		if addrs, err := ifc.Addrs(); err == nil {
			for _, a := range addrs {
				if p, ok := prefixOf(a); ok {
					i.Addrs = append(i.Addrs, p)
				}
			}
		}
		out = append(out, i)
	}
	return out, nil
}

func prefixOf(a net.Addr) (netip.Prefix, bool) {
	n, ok := a.(*net.IPNet)
	if !ok {
		return netip.Prefix{}, false
	}
	ip, ok := netip.AddrFromSlice(n.IP)
	if !ok {
		return netip.Prefix{}, false
	}
	ones, _ := n.Mask.Size()
	ip = ip.Unmap()
	if ip.Is4() && ones > 32 {
		return netip.Prefix{}, false
	}
	p := netip.PrefixFrom(ip, ones)
	return p, p.IsValid()
}

func (systemHost) Routes() ([]Route, error) {
	rib, err := route.FetchRIB(syscall.AF_UNSPEC, route.RIBTypeRoute, 0)
	if err != nil {
		return nil, fmt.Errorf("read the routing table: %w", err)
	}
	msgs, err := route.ParseRIB(route.RIBTypeRoute, rib)
	if err != nil {
		return nil, fmt.Errorf("parse the routing table: %w", err)
	}
	var out []Route
	for _, m := range msgs {
		rm, ok := m.(*route.RouteMessage)
		if !ok {
			continue
		}
		if r, ok := rawRoute(rm).Route(); ok {
			out = append(out, r)
		}
	}
	return out, nil
}

func rawRoute(rm *route.RouteMessage) RawRoute {
	r := RawRoute{Flags: rm.Flags, IfIndex: rm.Index}
	at := func(i int) route.Addr {
		if i < len(rm.Addrs) {
			return rm.Addrs[i]
		}
		return nil
	}
	r.Dest = inetOf(at(syscall.RTAX_DST))
	r.Mask = inetOf(at(syscall.RTAX_NETMASK))
	r.Gateway = inetOf(at(syscall.RTAX_GATEWAY))
	return r
}

// inetOf reads an IP address out of a routing message's address; a link-layer
// address, which is how an on-link gateway is written, gives an invalid one.
func inetOf(a route.Addr) netip.Addr {
	switch a := a.(type) {
	case *route.Inet4Addr:
		return netip.AddrFrom4(a.IP)
	case *route.Inet6Addr:
		return netip.AddrFrom16(a.IP)
	}
	return netip.Addr{}
}

// Watch listens on a routing socket, over which the kernel announces every
// change of a route, an address or an interface, and calls notify for each.
func (systemHost) Watch(notify func()) (func(), error) {
	fd, err := syscall.Socket(syscall.AF_ROUTE, syscall.SOCK_RAW, 0)
	if err != nil {
		return nil, fmt.Errorf("open a routing socket: %w", err)
	}
	if err := syscall.SetNonblock(fd, true); err != nil {
		_ = syscall.Close(fd)
		return nil, err
	}
	// As an os.File the socket joins the runtime's poller, so closing it ends
	// the blocked read below.
	f := os.NewFile(uintptr(fd), "route")
	if f == nil {
		_ = syscall.Close(fd)
		return nil, errors.New("wrap the routing socket")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 1<<14)
		for {
			n, err := f.Read(buf)
			if err != nil {
				return // closed, or the socket is no longer usable
			}
			if changesNetwork(buf[:n]) {
				notify()
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = f.Close()
			<-done
		})
	}, nil
}

// changesNetwork tells the messages that mean the network changed from those
// that only answer somebody's question (a route lookup, a miss).
func changesNetwork(msg []byte) bool {
	// struct rt_msghdr starts with: u_short length, u_char version, u_char type.
	if len(msg) < 4 {
		return false
	}
	switch int(msg[3]) {
	case syscall.RTM_ADD, syscall.RTM_DELETE, syscall.RTM_CHANGE,
		syscall.RTM_NEWADDR, syscall.RTM_DELADDR, syscall.RTM_IFINFO, rtmIfInfo2:
		return true
	}
	return false
}

// rtmIfInfo2 is the interface message that carries 64-bit counters; the
// syscall package does not name it.
const rtmIfInfo2 = 0x12
