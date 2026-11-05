// Package winplat is the Windows driver of the agent: how it reads the
// machine's interfaces and routes, notices changes, and removes what it made.
//
// Deciding what the numbers mean (which adapter is a VPN, which route is the
// way out) is plain logic in this file and routes.go, tested anywhere. Reading
// the numbers from the system is in the *_windows.go files.
package winplat

import (
	"strings"

	"eggbox/internal/agent/routecheck"
)

// The interface types Windows reports (IANAifType).
const (
	ifTypeEthernet  = 6
	ifTypePPP       = 23
	ifTypeLoopback  = 24
	ifTypeWiFi      = 71
	ifTypeTunnel    = 131
	ifTypeWWAN3GPP  = 243
	ifTypeWWAN3GPP2 = 244
)

// pseudoHints are interfaces Windows makes for itself and that carry nobody's
// VPN. They are told apart before the tunnel rule, because their type says
// "tunnel".
var pseudoHints = []string{"teredo", "isatap", "6to4", "kernel debug"}

// tunnelHints are the names VPN software gives its adapters.
var tunnelHints = []string{
	"wintun", "wireguard", "tap-windows", "tap-win", "openvpn", "tailscale", "zerotier", "nordlynx",
	"vpn", "tunnel", "clash", "sing-box", "mullvad", "proton", "ipsec", "anyconnect", "globalprotect",
}

// virtualHints are adapters of virtual machines and containers.
var virtualHints = []string{
	"hyper-v", "vethernet", "vmware", "virtualbox", "vmnet", "wsl", "docker", "npcap", "bluetooth",
	"loopback adapter", "host-only",
}

// ClassifyAdapter says what kind of interface an adapter is, from the type
// Windows gives it and the names it carries.
func ClassifyAdapter(ifType uint32, name, description string) routecheck.Kind {
	if ifType == ifTypeLoopback {
		return routecheck.KindLoopback
	}
	text := strings.ToLower(name + " " + description)
	if containsAny(text, pseudoHints) {
		return routecheck.KindVirtual
	}
	if ifType == ifTypeTunnel || ifType == ifTypePPP || containsAny(text, tunnelHints) {
		return routecheck.KindTunnel
	}
	if containsAny(text, virtualHints) {
		return routecheck.KindVirtual
	}
	switch ifType {
	case ifTypeEthernet, ifTypeWiFi, ifTypeWWAN3GPP, ifTypeWWAN3GPP2:
		return routecheck.KindPhysical
	}
	return routecheck.KindUnknown
}

func containsAny(text string, words []string) bool {
	for _, w := range words {
		if strings.Contains(text, w) {
			return true
		}
	}
	return false
}
