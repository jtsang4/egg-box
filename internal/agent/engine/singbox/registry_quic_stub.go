//go:build !with_quic

package singbox

import (
	"github.com/sagernet/sing-box/adapter/inbound"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/dns"
)

// QUICCompiled reports whether QUIC based protocols are part of this build.
const QUICCompiled = false

func registerQUICInbounds(*inbound.Registry)        {}
func registerQUICOutbounds(*outbound.Registry)      {}
func registerQUICTransports(*dns.TransportRegistry) {}
