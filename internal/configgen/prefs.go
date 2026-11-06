// Package configgen writes the sing-box configuration the privileged agent
// runs, from the user's nodes, routing plan and preferences.
//
// The agent accepts only a narrow dialect (see internal/agent/policy): at most one TUN
// inbound whose addressing the agent owns, loopback listeners, no management
// APIs, rule sets only as submitted resources. This package produces exactly
// that dialect, so a generated configuration is accepted without surprises,
// and it keeps provider-supplied content (inbounds, DNS, rules) out of it.
//
// The input is already decided: which nodes exist, which items are in force
// and where each of them goes. Turning a routing plan into that (resolving the
// nodes a person pinned, compiling rule data) is internal/plancompile's job.
package configgen

import (
	"fmt"
	"net/url"
	"strings"
)

// Modes are the Clash-style modes the core switches between at runtime.
const (
	ModeRule   = "Rule"
	ModeGlobal = "Global"
	ModeDirect = "Direct"
)

// Fixed tags in the generated configuration.
const (
	TagProxy  = "Proxy"  // the main exit: a group the person (or a plan) picks a node in
	TagAuto   = "Auto"   // fastest node, tested periodically
	TagDirect = "direct" // straight out
	TagTun    = "tun-in"
	// RegionPrefix starts the tag of a per-region fastest-node group.
	RegionPrefix = "region-"
	// SelectorPrefix starts the tag of the group an item (or the fallback)
	// sends its traffic through.
	SelectorPrefix = "item-"
	// PinPrefix starts the tag of the group that gives a node pinned by an
	// exception its stand-in.
	PinPrefix = "pin-"
	// FallbackID is the item identifier of the fallback.
	FallbackID = "fallback"
)

// Prefs are the user's choices that shape the generated configuration but are
// not part of a routing plan.
type Prefs struct {
	// LANDirect keeps traffic to the local network out of the proxy.
	LANDirect bool `json:"lanDirect"`
	IPv6      bool `json:"ipv6"`
	// DomesticDNS answers names that are reached directly; RemoteDNS answers
	// the rest, through the proxy. Each is "1.1.1.1", "tls://host", "https://host/path"...
	DomesticDNS string `json:"domesticDns"`
	RemoteDNS   string `json:"remoteDns"`
	// TunDisabled runs the core without a TUN adapter: nothing on the machine
	// changes and only the local HTTP/SOCKS proxy (MixedPort) carries traffic,
	// for the programs pointed at it. Stored as "disabled" so preferences saved
	// before the switch existed keep TUN on.
	TunDisabled bool   `json:"tunDisabled"`
	TunStack    string `json:"tunStack"`
	MTU         int    `json:"mtu"`
	// MixedPort opens a local HTTP/SOCKS proxy on 127.0.0.1 when non-zero. It
	// is always open when TunDisabled is set (DefaultMixedPort if zero).
	MixedPort int `json:"mixedPort"`
	// TestURL, TestIntervalMin and ToleranceMs configure the automatic fastest-node groups.
	TestURL         string `json:"testUrl"`
	TestIntervalMin int    `json:"testIntervalMin"`
	ToleranceMs     int    `json:"toleranceMs"`
	// Sniff recovers host names from TLS and HTTP so domain rules apply to
	// connections that arrive as bare addresses (always on in TUN mode).
	Sniff bool `json:"sniff"`
}

// DefaultMixedPort is the local proxy port used when TUN is off and no port
// was chosen.
const DefaultMixedPort = 7890

// DefaultPrefs are the settings a new installation starts with.
func DefaultPrefs() Prefs {
	return Prefs{
		LANDirect:       true,
		IPv6:            false,
		DomesticDNS:     "223.5.5.5",
		RemoteDNS:       "https://1.1.1.1/dns-query",
		TunStack:        "system",
		MTU:             1500,
		TestURL:         "https://www.gstatic.com/generate_204",
		TestIntervalMin: 10,
		ToleranceMs:     50,
		Sniff:           true,
	}
}

// Normalize fills gaps and clamps values; it reports what was invalid.
func (p Prefs) Normalize() (Prefs, []string) {
	d := DefaultPrefs()
	var notes []string
	if strings.TrimSpace(p.DomesticDNS) == "" {
		p.DomesticDNS = d.DomesticDNS
	}
	if strings.TrimSpace(p.RemoteDNS) == "" {
		p.RemoteDNS = d.RemoteDNS
	}
	if _, err := dnsServerFor(p.DomesticDNS); err != nil {
		notes = append(notes, "domestic DNS: "+err.Error())
		p.DomesticDNS = d.DomesticDNS
	}
	if _, err := dnsServerFor(p.RemoteDNS); err != nil {
		notes = append(notes, "remote DNS: "+err.Error())
		p.RemoteDNS = d.RemoteDNS
	}
	switch p.TunStack {
	case "system", "gvisor", "mixed":
	case "":
		p.TunStack = d.TunStack
	default:
		notes = append(notes, fmt.Sprintf("unknown TUN stack %q", p.TunStack))
		p.TunStack = d.TunStack
	}
	if p.MTU == 0 {
		p.MTU = d.MTU
	}
	if p.MTU < 1280 || p.MTU > 9000 {
		notes = append(notes, "the MTU must be between 1280 and 9000")
		p.MTU = d.MTU
	}
	if p.MixedPort < 0 || p.MixedPort > 65535 {
		notes = append(notes, "the local proxy port is out of range")
		p.MixedPort = 0
	}
	if p.TunDisabled && p.MixedPort == 0 {
		// Without a TUN the local proxy is the only way in.
		p.MixedPort = DefaultMixedPort
	}
	if u, err := url.Parse(p.TestURL); p.TestURL == "" || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		if p.TestURL != "" {
			notes = append(notes, "the latency test URL must be an http(s) URL")
		}
		p.TestURL = d.TestURL
	}
	if p.TestIntervalMin <= 0 {
		p.TestIntervalMin = d.TestIntervalMin
	}
	if p.TestIntervalMin > 24*60 {
		p.TestIntervalMin = 24 * 60
	}
	if p.ToleranceMs <= 0 {
		p.ToleranceMs = d.ToleranceMs
	}
	if p.ToleranceMs > 5000 {
		p.ToleranceMs = 5000
	}
	return p, notes
}
