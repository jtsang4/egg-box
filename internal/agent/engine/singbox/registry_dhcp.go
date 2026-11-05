//go:build with_dhcp

package singbox

import (
	"github.com/sagernet/sing-box/dns"
	"github.com/sagernet/sing-box/dns/transport/dhcp"
)

// DHCPCompiled reports whether the DHCP DNS transport is part of this build.
const DHCPCompiled = true

func registerDHCPTransport(registry *dns.TransportRegistry) {
	dhcp.RegisterTransport(registry)
}
