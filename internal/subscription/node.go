// Package subscription turns what a proxy provider hands out into nodes the
// config generator can use.
//
// Providers publish the same servers in many shapes: a full sing-box
// configuration, a Clash/Mihomo profile, a (usually base64 encoded) list of
// share links. Parse recognises the shape and returns one Node per usable
// server, each carrying the sing-box outbound that reaches it. Everything else
// in the provider's file (its inbounds, DNS, rules, listeners) is ignored on
// purpose: the application writes those itself, under the privileged agent's
// policy.
//
// Nothing in this package logs or returns subscription links or node
// credentials in error messages; callers can show errors to the user and
// write them to log files.
package subscription

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

// Node is one proxy server from a subscription, expressed as the sing-box
// outbound (or endpoint, for WireGuard) that reaches it.
type Node struct {
	// Name is what the user sees. Parse leaves it as the provider wrote it;
	// Dedupe makes names unique.
	Name string `json:"name"`
	// Type is the sing-box type: vless, vmess, trojan, shadowsocks, hysteria,
	// hysteria2, tuic, anytls, snell, ssh, socks, http, wireguard.
	Type   string `json:"type"`
	Server string `json:"server"`
	Port   int    `json:"port"`
	// Endpoint is true for types sing-box models as endpoints (WireGuard).
	Endpoint bool `json:"endpoint,omitempty"`
	// Config is the complete sing-box object without its "tag".
	Config map[string]any `json:"config"`
	// Detour is the sing-box outbound, without its "tag", that the node's
	// connections go through, for protocols that ride on another: Shadowsocks
	// over ShadowTLS. It is nil for every other node.
	Detour map[string]any `json:"detour,omitempty"`
	// Link is the share link the node was read from, as written, when it came
	// from one. Only an import keeps it, to show and edit the node as its
	// link; a subscription's nodes are the provider's and drop it (see
	// WithoutLinks). It is no part of the node's identity (Key).
	Link string `json:"link,omitempty"`
}

// Key identifies the server behind a node independently of its display name,
// so a selection survives a provider renaming its nodes.
func (n Node) Key() string {
	cfg := cloneMap(n.Config)
	delete(cfg, "tag")
	id := map[string]any{"type": n.Type, "endpoint": n.Endpoint, "config": cfg}
	if n.Detour != nil {
		// Only when present, so the keys of all other nodes stay what they were.
		id["detour"] = n.Detour
	}
	raw, _ := json.Marshal(id)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8])
}

// Clone returns a deep copy.
func (n Node) Clone() Node {
	n.Config = cloneMap(n.Config)
	n.Detour = cloneMap(n.Detour)
	return n
}

// Transport names the transport in plain words ("TCP", "WebSocket", "gRPC"...).
func (n Node) Transport() string {
	if n.Type == "hysteria" || n.Type == "hysteria2" || n.Type == "tuic" {
		return "QUIC"
	}
	if t, ok := n.Config["transport"].(map[string]any); ok {
		switch s, _ := t["type"].(string); s {
		case "ws":
			return "WebSocket"
		case "grpc":
			return "gRPC"
		case "http":
			return "HTTP/2"
		case "httpupgrade":
			return "HTTPUpgrade"
		}
	}
	return "TCP"
}

// Protocol is the label shown on a node: "VLESS Reality", "Trojan", "Hysteria2".
func (n Node) Protocol() string {
	label := map[string]string{
		"vless": "VLESS", "vmess": "VMess", "trojan": "Trojan", "shadowsocks": "Shadowsocks",
		"hysteria": "Hysteria", "hysteria2": "Hysteria2", "tuic": "TUIC", "anytls": "AnyTLS",
		"socks": "SOCKS", "http": "HTTP", "wireguard": "WireGuard", "snell": "Snell", "ssh": "SSH",
	}[n.Type]
	if label == "" {
		label = strings.ToUpper(n.Type)
	}
	if tls, ok := n.Config["tls"].(map[string]any); ok {
		if r, ok := tls["reality"].(map[string]any); ok && truthy(r["enabled"]) {
			return label + " Reality"
		}
	}
	if t, _ := n.Detour["type"].(string); t == "shadowtls" {
		return label + " + ShadowTLS"
	}
	return label
}

// Skipped explains why a server in the provider's file did not become a node.
type Skipped struct {
	Name string `json:"name"`
	Type string `json:"type"`
	// Code is stable and machine readable so the GUI can translate it.
	Code   string `json:"code"`
	Detail string `json:"detail,omitempty"`
}

// Skip codes.
const (
	SkipUnsupportedProtocol  = "unsupported_protocol"
	SkipUnsupportedTransport = "unsupported_transport"
	SkipUnsupportedFeature   = "unsupported_feature"
	SkipInvalid              = "invalid"
	SkipInfoEntry            = "info_entry"
	SkipDuplicate            = "duplicate"
)

// supportedTypes are the sing-box types a node may have. The agent's policy
// decides what actually runs; this list keeps unknown things out of the library.
var supportedTypes = map[string]bool{
	"vless": true, "vmess": true, "trojan": true, "shadowsocks": true, "hysteria": true,
	"hysteria2": true, "tuic": true, "anytls": true, "snell": true, "ssh": true, "socks": true, "http": true, "wireguard": true,
}

// Validate checks that a node carries what its protocol needs. It is the last
// line of defence before a node reaches a configuration.
func (n Node) Validate() error {
	if !supportedTypes[n.Type] {
		return fmt.Errorf("unsupported protocol %q", n.Type)
	}
	if strings.TrimSpace(n.Server) == "" {
		return errors.New("the server address is empty")
	}
	if n.Port < 1 || n.Port > 65535 {
		return fmt.Errorf("port %d is out of range", n.Port)
	}
	if n.Detour != nil {
		if err := validateDetour(n); err != nil {
			return err
		}
	}
	need := func(keys ...string) error {
		for _, k := range keys {
			if s, _ := n.Config[k].(string); strings.TrimSpace(s) == "" {
				return fmt.Errorf("%s is missing", k)
			}
		}
		return nil
	}
	switch n.Type {
	case "vless", "vmess":
		return need("uuid")
	case "trojan", "anytls":
		return need("password")
	case "shadowsocks":
		if err := need("method"); err != nil {
			return err
		}
		method, _ := n.Config["method"].(string)
		if !ssMethods[method] {
			return fmt.Errorf("unsupported cipher %q", method)
		}
		if method == "none" {
			return nil
		}
		return need("password")
	case "tuic":
		return need("uuid")
	case "hysteria2":
		return need("password")
	case "snell":
		if err := need("psk"); err != nil {
			return err
		}
		return validateSnell(n.Config)
	case "ssh":
		return validateSSH(n.Config)
	case "hysteria":
		if s, _ := n.Config["auth_str"].(string); s == "" {
			if s, _ := n.Config["auth"].(string); s == "" {
				return errors.New("auth is missing")
			}
		}
	case "wireguard":
		if err := need("private_key"); err != nil {
			return err
		}
		peers, _ := n.Config["peers"].([]any)
		if len(peers) == 0 {
			return errors.New("peers are missing")
		}
		for _, p := range peers {
			peer, _ := p.(map[string]any)
			ips, ok := stringList(peer["allowed_ips"])
			if !ok {
				return errors.New("the allowed IPs of a WireGuard peer cannot be read")
			}
			for _, ip := range ips {
				if _, err := netip.ParsePrefix(ip); err != nil {
					return errors.New("the allowed IPs of a WireGuard peer cannot be read")
				}
			}
		}
	}
	return nil
}

// ssMethods are the Shadowsocks ciphers sing-box implements.
var ssMethods = func() map[string]bool {
	m := map[string]bool{}
	for _, s := range []string{
		"none", "aes-128-gcm", "aes-192-gcm", "aes-256-gcm", "chacha20-ietf-poly1305",
		"xchacha20-ietf-poly1305", "2022-blake3-aes-128-gcm", "2022-blake3-aes-256-gcm",
		"2022-blake3-chacha20-poly1305", "aes-128-ctr", "aes-192-ctr", "aes-256-ctr",
		"aes-128-cfb", "aes-192-cfb", "aes-256-cfb", "rc4-md5", "chacha20-ietf", "xchacha20",
	} {
		m[s] = true
	}
	return m
}()

// Dedupe drops nodes that point at the same server with the same settings and
// gives the remaining ones unique, non-empty names. Order is preserved.
func Dedupe(nodes []Node) (kept []Node, dropped int) {
	seen := map[string]bool{}
	names := map[string]int{}
	for _, n := range nodes {
		k := n.Key()
		if seen[k] {
			dropped++
			continue
		}
		seen[k] = true
		n = n.Clone()
		n.Name = cleanName(n.Name)
		if n.Name == "" {
			n.Name = fmt.Sprintf("%s %s:%d", n.Protocol(), n.Server, n.Port)
		}
		base := n.Name
		for i := 2; names[strings.ToLower(n.Name)] > 0; i++ {
			n.Name = fmt.Sprintf("%s %d", base, i)
		}
		names[strings.ToLower(n.Name)]++
		kept = append(kept, n)
	}
	return kept, dropped
}

// cleanName trims a provider's name and removes characters that break lists
// and logs (control characters, line breaks).
func cleanName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 80 {
		s = string(r[:80])
	}
	return s
}

func cloneMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = cloneValue(v)
	}
	return out
}

func cloneValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return cloneMap(t)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = cloneValue(e)
		}
		return out
	case []string:
		return append([]string(nil), t...)
	default:
		return v
	}
}

func truthy(v any) bool {
	b, _ := v.(bool)
	return b
}

// WithoutLinks returns the nodes without the links they were read from: what
// a subscription keeps, since its nodes are never edited and the links would
// only be a second copy of their credentials.
func WithoutLinks(nodes []Node) []Node {
	out := make([]Node, len(nodes))
	for i, n := range nodes {
		n.Link = ""
		out[i] = n
	}
	return out
}

// validateDetour checks the outbound a node goes through. Only Shadowsocks
// over ShadowTLS is a chain a provider hands out as one server.
func validateDetour(n Node) error {
	if t, _ := n.Detour["type"].(string); t != "shadowtls" || n.Type != "shadowsocks" {
		return errors.New("proxy chaining is only supported for Shadowsocks over ShadowTLS")
	}
	if _, chained := n.Detour["detour"]; chained {
		return errors.New("ShadowTLS cannot go through another proxy")
	}
	if s, _ := n.Detour["server"].(string); strings.TrimSpace(s) == "" {
		return errors.New("the ShadowTLS server address is empty")
	}
	if p, ok := exactInt(n.Detour["server_port"]); !ok || p < 1 || p > 65535 {
		return fmt.Errorf("ShadowTLS port %d is out of range", p)
	}
	v, ok := exactInt(n.Detour["version"])
	if _, present := n.Detour["version"]; !present {
		v, ok = 1, true
	}
	if !ok {
		return errors.New("the ShadowTLS version is not a number")
	}
	switch v {
	case 0, 1:
	case 2, 3:
		if s, _ := n.Detour["password"].(string); s == "" {
			return errors.New("the ShadowTLS password is missing")
		}
	default:
		return fmt.Errorf("ShadowTLS version %d is not supported", v)
	}
	if tls, _ := n.Detour["tls"].(map[string]any); !truthy(tls["enabled"]) {
		return errors.New("ShadowTLS needs TLS")
	}
	return nil
}
