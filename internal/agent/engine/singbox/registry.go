// Package singbox embeds the sing-box core (pinned in go.mod) for agentd.
//
// The core runs as root / SYSTEM, so this package registers an explicit
// allow-list of protocols instead of the upstream "include" registry, which
// also links SSH, Tor, Tailscale, management APIs and other features a
// network client never needs. Anything not registered here cannot be
// instantiated even if a configuration names it; internal/agent/policy
// rejects such configurations earlier with a readable message.
package singbox

import (
	"context"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter/certificate"
	"github.com/sagernet/sing-box/adapter/endpoint"
	"github.com/sagernet/sing-box/adapter/inbound"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/adapter/service"
	"github.com/sagernet/sing-box/dns"
	"github.com/sagernet/sing-box/dns/transport"
	"github.com/sagernet/sing-box/dns/transport/fakeip"
	"github.com/sagernet/sing-box/dns/transport/hosts"
	"github.com/sagernet/sing-box/dns/transport/local"
	"github.com/sagernet/sing-box/protocol/anytls"
	"github.com/sagernet/sing-box/protocol/block"
	"github.com/sagernet/sing-box/protocol/direct"
	"github.com/sagernet/sing-box/protocol/group"
	"github.com/sagernet/sing-box/protocol/http"
	"github.com/sagernet/sing-box/protocol/mixed"
	"github.com/sagernet/sing-box/protocol/shadowsocks"
	"github.com/sagernet/sing-box/protocol/shadowtls"
	"github.com/sagernet/sing-box/protocol/snell"
	"github.com/sagernet/sing-box/protocol/socks"
	"github.com/sagernet/sing-box/protocol/trojan"
	"github.com/sagernet/sing-box/protocol/tun"
	"github.com/sagernet/sing-box/protocol/vless"
	"github.com/sagernet/sing-box/protocol/vmess"
)

// RegistryOptions tunes which optional protocols are registered.
type RegistryOptions struct {
	// ServerInbounds additionally registers protocol server inbounds
	// (shadowsocks, socks, http, direct, ...). It exists for tests and the
	// demo network, whose fake upstream servers are sing-box instances; the
	// production agent never sets it.
	ServerInbounds bool
}

// Context returns ctx carrying the registries box.New needs.
func Context(ctx context.Context, opts RegistryOptions) context.Context {
	return box.Context(ctx,
		InboundRegistry(opts),
		OutboundRegistry(),
		EndpointRegistry(),
		DNSTransportRegistry(),
		service.NewRegistry(),
		certificate.NewRegistry(),
	)
}

// InboundRegistry lists the inbounds a client configuration may use: the TUN
// device and a local mixed proxy.
func InboundRegistry(opts RegistryOptions) *inbound.Registry {
	registry := inbound.NewRegistry()
	tun.RegisterInbound(registry)
	mixed.RegisterInbound(registry)
	if opts.ServerInbounds {
		direct.RegisterInbound(registry)
		socks.RegisterInbound(registry)
		http.RegisterInbound(registry)
		shadowsocks.RegisterInbound(registry)
		registerQUICInbounds(registry)
	}
	return registry
}

// OutboundRegistry lists the outbounds: groups, direct, and the proxy
// protocols subscriptions use.
func OutboundRegistry() *outbound.Registry {
	registry := outbound.NewRegistry()
	direct.RegisterOutbound(registry)
	block.RegisterOutbound(registry)
	group.RegisterSelector(registry)
	group.RegisterURLTest(registry)
	socks.RegisterOutbound(registry)
	http.RegisterOutbound(registry)
	shadowsocks.RegisterOutbound(registry)
	snell.RegisterOutbound(registry)
	vmess.RegisterOutbound(registry)
	vless.RegisterOutbound(registry)
	trojan.RegisterOutbound(registry)
	shadowtls.RegisterOutbound(registry)
	anytls.RegisterOutbound(registry)
	registerQUICOutbounds(registry)
	return registry
}

// EndpointRegistry lists the endpoints: WireGuard, when compiled in.
func EndpointRegistry() *endpoint.Registry {
	registry := endpoint.NewRegistry()
	registerWireGuardEndpoint(registry)
	return registry
}

// DNSTransportRegistry lists the DNS transports a configuration may use.
func DNSTransportRegistry() *dns.TransportRegistry {
	registry := dns.NewTransportRegistry()
	transport.RegisterTCP(registry)
	transport.RegisterUDP(registry)
	transport.RegisterTLS(registry)
	transport.RegisterHTTPS(registry)
	hosts.RegisterTransport(registry)
	local.RegisterTransport(registry)
	fakeip.RegisterTransport(registry)
	registerQUICTransports(registry)
	registerDHCPTransport(registry)
	return registry
}
