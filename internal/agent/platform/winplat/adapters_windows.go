//go:build windows

package winplat

import (
	"errors"
	"fmt"
	"net/netip"
	"unsafe"

	"golang.org/x/sys/windows"
)

// readAdapters lists the network adapters, the way `ipconfig` does.
func readAdapters() ([]Adapter, error) {
	const flags = windows.GAA_FLAG_INCLUDE_PREFIX | windows.GAA_FLAG_SKIP_ANYCAST |
		windows.GAA_FLAG_SKIP_MULTICAST | windows.GAA_FLAG_SKIP_DNS_SERVER | windows.GAA_FLAG_INCLUDE_ALL_INTERFACES

	size := uint32(15 * 1024)
	for attempt := 0; attempt < 6; attempt++ {
		buf := make([]byte, size)
		first := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC, flags, 0, first, &size)
		if errors.Is(err, windows.ERROR_BUFFER_OVERFLOW) {
			continue // the list grew between asking for the size and reading it
		}
		if errors.Is(err, windows.ERROR_NO_DATA) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("list the network adapters: %w", err)
		}
		return parseAdapters(first, buf), nil
	}
	return nil, errors.New("list the network adapters: the list kept changing")
}

// parseAdapters walks the list the system wrote into buf. buf is passed so the
// memory is certain to stay alive while the list is read.
func parseAdapters(first *windows.IpAdapterAddresses, buf []byte) []Adapter {
	var out []Adapter
	for aa := first; aa != nil; aa = aa.Next {
		a := Adapter{
			Index:       aa.IfIndex,
			Index6:      aa.Ipv6IfIndex,
			Name:        windows.UTF16PtrToString(aa.FriendlyName),
			Description: windows.UTF16PtrToString(aa.Description),
			GUID:        windows.BytePtrToString(aa.AdapterName),
			Metric4:     aa.Ipv4Metric,
			Metric6:     aa.Ipv6Metric,
			Up:          aa.OperStatus == windows.IfOperStatusUp,
		}
		a.Kind = ClassifyAdapter(aa.IfType, a.Name, a.Description)
		for ua := aa.FirstUnicastAddress; ua != nil; ua = ua.Next {
			addr, ok := netip.AddrFromSlice(ua.Address.IP())
			if !ok {
				continue
			}
			addr = addr.Unmap()
			if addr.Is6() && addr.IsLinkLocalUnicast() {
				continue
			}
			a.Addrs = append(a.Addrs, netip.PrefixFrom(addr, int(ua.OnLinkPrefixLength)))
		}
		out = append(out, a)
	}
	_ = buf
	return out
}

// readRoutes reads the routing table (both address families).
func readRoutes() ([]Route, error) {
	var table *windows.MibIpForwardTable2
	if err := windows.GetIpForwardTable2(windows.AF_UNSPEC, &table); err != nil {
		return nil, fmt.Errorf("read the routing table: %w", err)
	}
	defer windows.FreeMibTable(unsafe.Pointer(table))

	rows := table.Rows()
	out := make([]Route, 0, len(rows))
	for i := range rows {
		r := &rows[i]
		dest, ok := sockaddrAddr(&r.DestinationPrefix.Prefix)
		if !ok {
			continue
		}
		route := Route{
			Dest:    netip.PrefixFrom(dest, int(r.DestinationPrefix.PrefixLength)),
			IfIndex: r.InterfaceIndex,
			Metric:  r.Metric,
		}
		if gw, ok := sockaddrAddr(&r.NextHop); ok && !gw.IsUnspecified() {
			route.Gateway = gw
		}
		out = append(out, route)
	}
	return out, nil
}

// sockaddrAddr reads the address out of a SOCKADDR_INET union.
func sockaddrAddr(sa *windows.RawSockaddrInet) (netip.Addr, bool) {
	switch sa.Family {
	case windows.AF_INET:
		return netip.AddrFrom4((*windows.RawSockaddrInet4)(unsafe.Pointer(sa)).Addr), true
	case windows.AF_INET6:
		return netip.AddrFrom16((*windows.RawSockaddrInet6)(unsafe.Pointer(sa)).Addr), true
	}
	return netip.Addr{}, false
}
