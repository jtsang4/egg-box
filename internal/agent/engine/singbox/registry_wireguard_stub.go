//go:build !with_wireguard

package singbox

import "github.com/sagernet/sing-box/adapter/endpoint"

// WireGuardCompiled reports whether the WireGuard endpoint is part of this build.
const WireGuardCompiled = false

func registerWireGuardEndpoint(*endpoint.Registry) {}
