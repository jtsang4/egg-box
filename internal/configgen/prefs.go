// Package configgen writes the sing-box configuration the privileged agent
// runs, from the user's nodes and preferences.
//
// The agent accepts only a narrow dialect (see internal/agent/policy): one TUN
// inbound whose addressing the agent owns, loopback listeners, no management
// APIs, rule sets only as submitted resources. This package produces exactly
// that dialect, so a generated configuration is accepted without surprises,
// and it keeps provider-supplied content (inbounds, DNS, rules) out of it.
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
	TagProxy  = "Proxy"  // the selector the user picks a node in
	TagAuto   = "Auto"   // fastest node, tested periodically
	TagDirect = "direct" // straight out
	TagTun    = "tun-in"
	// RegionPrefix starts the tag of a per-region fastest-node group.
	RegionPrefix = "region-"
)

// Preset decides what Rule mode does with traffic that no custom rule claims.
type Preset string

const (
	// PresetChina sends mainland China sites and addresses straight out and
	// everything else through the proxy.
	PresetChina Preset = "china"
	// PresetProxyAll proxies everything except the local network.
	PresetProxyAll Preset = "proxy-all"
	// PresetForeignOnly proxies only sites outside China and sends the rest
	// straight out.
	PresetForeignOnly Preset = "foreign-only"
)

// Rule sets the presets rely on. They are binary sing-box rule sets published
// by the sing-box project and fetched by the application, then shipped to the
// agent as resources.
const (
	RuleSetCN      = "geosite-cn"
	RuleSetCNIP    = "geoip-cn"
	RuleSetAds     = "geosite-category-ads-all"
	RuleSetForeign = "geosite-geolocation-!cn"
)

// RequiredRuleSets lists the rule sets the preferences need, so the application
// can fetch them before generating a configuration.
func RequiredRuleSets(p Prefs) []string {
	p, _ = p.Normalize()
	var ids []string
	switch p.Preset {
	case PresetChina:
		ids = append(ids, RuleSetCN, RuleSetCNIP)
	case PresetForeignOnly:
		ids = append(ids, RuleSetForeign)
	}
	if p.BlockAds {
		ids = append(ids, RuleSetAds)
	}
	return ids
}

// RuleSetFile is the resource name a rule set is shipped under.
func RuleSetFile(id string) string {
	return strings.NewReplacer("!", "not-").Replace(id) + ".srs"
}

// Prefs are the user's choices that shape the generated configuration.
type Prefs struct {
	Preset    Preset `json:"preset"`
	LANDirect bool   `json:"lanDirect"`
	BlockAds  bool   `json:"blockAds"`
	IPv6      bool   `json:"ipv6"`
	// DomesticDNS answers names that are reachable directly; RemoteDNS answers
	// the rest, through the proxy. Each is "1.1.1.1", "tls://host", "https://host/path"...
	DomesticDNS string `json:"domesticDns"`
	RemoteDNS   string `json:"remoteDns"`
	TunStack    string `json:"tunStack"`
	MTU         int    `json:"mtu"`
	// MixedPort opens a local HTTP/SOCKS proxy on 127.0.0.1 when non-zero.
	MixedPort int `json:"mixedPort"`
	// TestURL, TestIntervalMin and ToleranceMs configure the automatic fastest-node groups.
	TestURL         string `json:"testUrl"`
	TestIntervalMin int    `json:"testIntervalMin"`
	ToleranceMs     int    `json:"toleranceMs"`
	// RegionGroups adds one fastest-node group per region with at least two nodes.
	RegionGroups bool `json:"regionGroups"`
	// Sniff recovers host names from TLS and HTTP so domain rules apply to
	// connections that arrive as bare addresses (always on in TUN mode).
	Sniff bool `json:"sniff"`
}

// DefaultPrefs are the settings a new installation starts with.
func DefaultPrefs() Prefs {
	return Prefs{
		Preset:          PresetChina,
		LANDirect:       true,
		BlockAds:        false,
		IPv6:            false,
		DomesticDNS:     "223.5.5.5",
		RemoteDNS:       "https://1.1.1.1/dns-query",
		TunStack:        "system",
		MTU:             1500,
		TestURL:         "https://www.gstatic.com/generate_204",
		TestIntervalMin: 10,
		ToleranceMs:     50,
		RegionGroups:    true,
		Sniff:           true,
	}
}

// Normalize fills gaps and clamps values; it reports what was invalid.
func (p Prefs) Normalize() (Prefs, []string) {
	d := DefaultPrefs()
	var notes []string
	switch p.Preset {
	case PresetChina, PresetProxyAll, PresetForeignOnly:
	case "":
		p.Preset = d.Preset
	default:
		notes = append(notes, fmt.Sprintf("unknown routing preset %q", p.Preset))
		p.Preset = d.Preset
	}
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

// RuleKind is what a custom rule matches.
type RuleKind string

const (
	KindDomain        RuleKind = "domain"
	KindDomainSuffix  RuleKind = "domain_suffix"
	KindDomainKeyword RuleKind = "domain_keyword"
	KindDomainRegex   RuleKind = "domain_regex"
	KindIPCIDR        RuleKind = "ip_cidr"
	KindProcessName   RuleKind = "process_name"
	KindPort          RuleKind = "port"
)

// Targets a custom rule may name besides a group tag.
const (
	TargetProxy  = TagProxy
	TargetDirect = TagDirect
	TargetReject = "reject"
)

// CustomRule is one user rule. Rules are matched in order, before the preset.
type CustomRule struct {
	Kind   RuleKind `json:"kind"`
	Value  string   `json:"value"`
	Target string   `json:"target"`
}

// Valid reports why a rule cannot be used.
func (r CustomRule) Valid() error {
	v := strings.TrimSpace(r.Value)
	if v == "" {
		return fmt.Errorf("the rule has no value")
	}
	if r.Target == "" {
		return fmt.Errorf("the rule has no target")
	}
	switch r.Kind {
	case KindDomain, KindDomainSuffix, KindDomainKeyword, KindProcessName:
		if strings.ContainsAny(v, " \t\r\n") {
			return fmt.Errorf("%q must not contain spaces", v)
		}
	case KindDomainRegex:
		if _, err := compileRegex(v); err != nil {
			return fmt.Errorf("%q is not a valid regular expression", v)
		}
	case KindIPCIDR:
		if !validCIDR(v) {
			return fmt.Errorf("%q is not an IP address or CIDR range", v)
		}
	case KindPort:
		if !validPort(v) {
			return fmt.Errorf("%q is not a port or port range", v)
		}
	default:
		return fmt.Errorf("unknown rule type %q", r.Kind)
	}
	return nil
}
