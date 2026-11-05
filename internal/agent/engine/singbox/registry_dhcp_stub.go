//go:build !with_dhcp

package singbox

import "github.com/sagernet/sing-box/dns"

// DHCPCompiled reports whether the DHCP DNS transport is part of this build.
const DHCPCompiled = false

func registerDHCPTransport(*dns.TransportRegistry) {}
