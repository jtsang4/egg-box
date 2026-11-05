//go:build with_wireguard

package singbox

import (
	"github.com/sagernet/sing-box/adapter/endpoint"
	"github.com/sagernet/sing-box/protocol/wireguard"
)

// WireGuardCompiled reports whether the WireGuard endpoint is part of this build.
const WireGuardCompiled = true

func registerWireGuardEndpoint(registry *endpoint.Registry) {
	wireguard.RegisterEndpoint(registry)
}
