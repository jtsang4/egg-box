// Package rulefmt turns the rule lists the community publishes (sing-box
// .srs and JSON, Clash and Surge lists, domain and CIDR lists, AdGuard
// filters, hosts files) into one model, sing-box's own headless rules, and
// compiles that model back to the .srs files the core loads.
//
// The core only reads two formats, so every other format is the client's
// job. Everything here is pure: bytes in, rules out. Downloading lives in
// ruledata, and what the rules mean for a plan lives in routing.
package rulefmt

import (
	"errors"

	"github.com/sagernet/sing-box/option"
)

// Format is what Detect finds in a file's content. The extension is never
// consulted: mirrors and renamed files make it unreliable.
type Format string

const (
	FormatSRS        Format = "srs"          // sing-box binary rule set
	FormatSingBox    Format = "singbox-json" // sing-box source rule set
	FormatClashYAML  Format = "clash-yaml"   // Clash rule-provider with a payload
	FormatClassical  Format = "classical"    // Clash or Surge TYPE,value lines
	FormatDomainList Format = "domain-list"  // one domain per line, Surge DOMAIN-SET
	FormatCIDRList   Format = "cidr-list"    // one CIDR per line
	FormatAdGuard    Format = "adguard"      // Adblock/AdGuard filter list
	FormatHosts      Format = "hosts"        // 0.0.0.0 example.com
	FormatMRS        Format = "mrs"          // mihomo binary rule set (not readable here)
)

// Style says how a leading dot in a domain list reads. Clash's `.a.com` is
// the sub-domains only; Surge's DOMAIN-SET `.a.com` is a.com and all its
// sub-domains. Nothing in the file says which, so the source decides (see
// StyleFor).
type Style uint8

const (
	StyleClash Style = iota
	StyleSurge
)

// RefKind names the three datasets a Clash list can refer to by name.
type RefKind string

const (
	RefGeoSite RefKind = "geosite"
	RefGeoIP   RefKind = "geoip"
	RefASN     RefKind = "asn"
)

// Ref is a reference a list makes to a dataset it does not contain: a Clash
// `GEOSITE,google` line. The caller expands it with the matching MetaCubeX
// file and merges the result into the source.
type Ref struct {
	Kind RefKind `json:"kind"`
	Name string  `json:"name"` // "google", "cn", "45102" (ASN without the AS prefix)
}

// Stats counts what a source holds, in the terms the user sees: domain
// conditions, IP ranges, and everything else (ports, processes, ...). Types
// the core cannot evaluate are counted by name instead of being lost.
type Stats struct {
	Domain      int            `json:"domain"`
	IP          int            `json:"ip"`
	Other       int            `json:"other"`
	Unsupported map[string]int `json:"unsupported,omitempty"`
	Ignored     int            `json:"ignored,omitempty"` // lines nothing could be made of
}

// UnsupportedTotal is the number of skipped entries of all types.
func (s Stats) UnsupportedTotal() int {
	n := 0
	for _, c := range s.Unsupported {
		n += c
	}
	return n
}

// Parsed is the result of reading one file.
type Parsed struct {
	Format   Format
	Behavior string // clash-yaml only: domain, ipcidr or classical
	Rules    []option.HeadlessRule
	Refs     []Ref
	Stats    Stats
}

// Hint carries what the caller knows about a file besides its bytes.
type Hint struct {
	// Source is the URL or file name; only used to choose the Style.
	Source string
	// Style overrides the guess made from Source.
	Style *Style
}

var (
	// ErrMRS means the file is a mihomo .mrs, which only the project's
	// mapping table can replace with a sing-box file.
	ErrMRS = errors.New("mihomo .mrs rule set")
	// ErrEmpty means the file was understood but holds nothing the core can use.
	ErrEmpty = errors.New("no usable rules")
	// ErrUnrecognized means no known format fits.
	ErrUnrecognized = errors.New("unrecognized rule format")
)
