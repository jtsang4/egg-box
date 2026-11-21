package subscription

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// cm is a Clash option map with forgiving accessors: providers quote numbers,
// write booleans as strings and use either a string or a list.
type cm map[string]any

func toCM(v any) cm {
	switch t := v.(type) {
	case map[string]any:
		return cm(t)
	case map[any]any:
		out := cm{}
		for k, e := range t {
			out[fmt.Sprint(k)] = e
		}
		return out
	}
	return nil
}

func (m cm) str(keys ...string) string {
	for _, k := range keys {
		if s := asString(m[k]); s != "" {
			return s
		}
	}
	return ""
}

func (m cm) num(keys ...string) int {
	for _, k := range keys {
		switch t := m[k].(type) {
		case int:
			return t
		case int64:
			return int(t)
		case uint64:
			return int(t)
		case float64:
			return int(t)
		case string:
			if n, err := strconv.Atoi(strings.TrimSpace(t)); err == nil {
				return n
			}
		}
	}
	return 0
}

func (m cm) boolean(keys ...string) bool {
	for _, k := range keys {
		switch t := m[k].(type) {
		case bool:
			return t
		case string:
			if s := strings.ToLower(strings.TrimSpace(t)); s == "true" || s == "1" || s == "yes" {
				return true
			}
		}
	}
	return false
}

func (m cm) list(keys ...string) []string {
	for _, k := range keys {
		switch t := m[k].(type) {
		case string:
			if l := splitList(t); len(l) > 0 {
				return l
			}
		case []any:
			var out []string
			for _, e := range t {
				if s := asString(e); s != "" {
					out = append(out, s)
				}
			}
			if len(out) > 0 {
				return out
			}
		}
	}
	return nil
}

func (m cm) obj(keys ...string) cm {
	for _, k := range keys {
		if o := toCM(m[k]); o != nil {
			return o
		}
	}
	return cm{}
}

// parseClash reads the "proxies" of a Clash/Mihomo profile.
func parseClash(data []byte) ([]Node, []Skipped, error) {
	var doc struct {
		Proxies     []any `yaml:"proxies"`
		ProxyGroups []any `yaml:"proxy-groups"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, nil, fmt.Errorf("the profile is not valid YAML")
	}
	groups := map[string]parsedGroup{}
	for _, raw := range doc.ProxyGroups {
		gm := toCM(raw)
		if gm == nil {
			continue
		}
		gname := gm.str("name")
		if gname == "" {
			continue
		}
		typ := strings.ToLower(gm.str("type"))
		groups[gname] = parsedGroup{
			name:    gname,
			auto:    typ == "url-test" || typ == "fallback" || typ == "load-balance",
			members: gm.list("proxies"),
		}
	}
	var nodes []Node
	var skipped []Skipped
	rawRelays := map[int]string{}
	for _, p := range doc.Proxies {
		m := toCM(p)
		if m == nil {
			continue
		}
		n, err := clashNode(m)
		if err == nil {
			err = n.Validate()
			if err != nil {
				err = skip(SkipInvalid, "%v", err)
			}
		}
		if err != nil {
			s := Skipped{Name: m.str("name"), Type: strings.ToLower(m.str("type")), Code: SkipInvalid, Detail: err.Error()}
			if se, ok := err.(*skipError); ok {
				s.Code, s.Detail = se.code, se.detail
			}
			skipped = append(skipped, s)
			continue
		}
		if dp := strings.TrimSpace(m.str("dialer-proxy")); dp != "" {
			rawRelays[len(nodes)] = dp
		}
		nodes = append(nodes, n)
	}
	if len(rawRelays) > 0 {
		var rskip []Skipped
		nodes, rskip = resolveProviderRelays(nodes, rawRelays, groups, nil)
		skipped = append(skipped, rskip...)
	}
	return nodes, skipped, nil
}

func clashStream(m cm, tlsDefault bool) (tlsSpec, map[string]any, error) {
	t := tlsSpec{
		Enabled:     tlsDefault || m.boolean("tls"),
		ServerName:  m.str("servername", "sni", "server-name"),
		Insecure:    m.boolean("skip-cert-verify"),
		ALPN:        m.list("alpn"),
		Fingerprint: m.str("client-fingerprint"),
	}
	if ro := m.obj("reality-opts"); len(ro) > 0 {
		t.Enabled, t.Reality = true, true
		t.PublicKey, t.ShortID = ro.str("public-key"), ro.str("short-id")
		if t.PublicKey == "" {
			return t, nil, skip(SkipInvalid, "REALITY needs a public key")
		}
	}
	if m.str("fingerprint") != "" && !t.Insecure {
		return t, nil, skip(SkipUnsupportedFeature, "certificate pinning is not supported")
	}
	network := strings.ToLower(m.str("network"))
	spec := transportSpec{Kind: network}
	switch network {
	case "ws":
		o := m.obj("ws-opts", "ws-opt")
		spec.Path = o.str("path")
		headers := o.obj("headers")
		spec.Host = headers.str("Host", "host")
		spec.EarlyData = o.num("max-early-data")
		spec.EarlyHeader = o.str("early-data-header-name")
		for k := range headers {
			if !strings.EqualFold(k, "host") {
				if spec.Headers == nil {
					spec.Headers = map[string]string{}
				}
				spec.Headers[k] = asString(headers[k])
			}
		}
		if o.boolean("v2ray-http-upgrade") {
			spec.Kind = "httpupgrade"
		}
	case "grpc":
		spec.ServiceName = m.obj("grpc-opts", "grpc-opt").str("grpc-service-name")
	case "h2":
		o := m.obj("h2-opts", "h2-opt")
		spec.Host = strings.Join(o.list("host"), ",")
		spec.Path = o.str("path")
	case "http":
		return t, nil, skip(SkipUnsupportedTransport, "TCP with an HTTP header is not supported")
	}
	if t.Enabled && t.ServerName == "" && spec.Host != "" && network != "h2" {
		t.ServerName = spec.Host
	}
	tr, err := spec.object()
	if err != nil {
		return t, nil, skip(SkipUnsupportedTransport, "%v", err)
	}
	return t, tr, nil
}

func clashNode(m cm) (Node, error) {
	name := m.str("name")
	typ := strings.ToLower(m.str("type"))
	server, port := m.str("server"), m.num("port")
	base := func(t string) map[string]any {
		return map[string]any{"type": t, "server": server, "server_port": port}
	}
	node := func(t string, cfg map[string]any) Node {
		return Node{Name: name, Type: t, Server: server, Port: port, Config: cfg}
	}
	switch typ {
	case "ss":
		cfg := base("shadowsocks")
		cfg["method"] = ssMethodAlias(strings.ToLower(m.str("cipher", "method")))
		cfg["password"] = m.str("password")
		var stls *shadowTLSSpec
		switch plugin := strings.ToLower(m.str("plugin")); plugin {
		case "":
		case "shadow-tls", "shadowtls":
			o := m.obj("plugin-opts")
			stls = &shadowTLSSpec{
				Host: o.str("host"), Password: o.str("password"), Version: o.str("version"),
				Fingerprint: m.str("client-fingerprint"), Insecure: o.boolean("skip-cert-verify"), ALPN: o.list("alpn"),
			}
			if o.str("fingerprint") != "" && !stls.Insecure {
				return Node{}, skip(SkipUnsupportedFeature, "certificate pinning is not supported")
			}
		case "obfs", "simple-obfs", "obfs-local":
			o := m.obj("plugin-opts")
			var opts []string
			if v := o.str("mode"); v != "" {
				opts = append(opts, "obfs="+v)
			}
			if v := o.str("host"); v != "" {
				opts = append(opts, "obfs-host="+v)
			}
			cfg["plugin"] = "obfs-local"
			setIf(cfg, "plugin_opts", strings.Join(opts, ";"))
		case "v2ray-plugin":
			o := m.obj("plugin-opts")
			var opts []string
			if v := o.str("mode"); v != "" {
				opts = append(opts, "mode="+v)
			}
			if o.boolean("tls") {
				opts = append(opts, "tls")
			}
			if v := o.str("host"); v != "" {
				opts = append(opts, "host="+v)
			}
			if v := o.str("path"); v != "" {
				opts = append(opts, "path="+v)
			}
			if o.boolean("skip-cert-verify") {
				return Node{}, skip(SkipUnsupportedFeature, "v2ray-plugin without certificate verification is not supported")
			}
			cfg["plugin"] = "v2ray-plugin"
			setIf(cfg, "plugin_opts", strings.Join(opts, ";"))
		default:
			return Node{}, skip(SkipUnsupportedFeature, "Shadowsocks plugin %q is not supported", plugin)
		}
		if m.boolean("udp-over-tcp") {
			cfg["udp_over_tcp"] = true
		}
		if stls != nil {
			return stls.over(node("shadowsocks", cfg))
		}
		return node("shadowsocks", cfg), nil

	case "vmess":
		t, tr, err := clashStream(m, false)
		if err != nil {
			return Node{}, err
		}
		cfg := base("vmess")
		cfg["uuid"] = m.str("uuid")
		sec := strings.ToLower(m.str("cipher"))
		if sec == "" {
			sec = "auto"
		}
		if !vmessSecurities[sec] {
			return Node{}, skip(SkipUnsupportedFeature, "cipher %q is not supported", sec)
		}
		cfg["security"] = sec
		if aid := m.num("alterId", "alter-id"); aid > 0 {
			cfg["alter_id"] = aid
		}
		if m.boolean("xudp") || m.str("packet-encoding") == "xudp" {
			cfg["packet_encoding"] = "xudp"
		}
		setIf(cfg, "tls", t.object())
		setIf(cfg, "transport", tr)
		return node("vmess", cfg), nil

	case "vless":
		t, tr, err := clashStream(m, false)
		if err != nil {
			return Node{}, err
		}
		if enc := strings.ToLower(m.str("encryption")); enc != "" && enc != "none" {
			return Node{}, skip(SkipUnsupportedFeature, "VLESS encryption is not supported")
		}
		cfg := base("vless")
		cfg["uuid"] = m.str("uuid")
		if flow := strings.TrimSuffix(m.str("flow"), "-udp443"); flow != "" {
			if flow != "xtls-rprx-vision" {
				return Node{}, skip(SkipUnsupportedFeature, "flow %q is not supported", flow)
			}
			if !t.Enabled || tr != nil {
				return Node{}, skip(SkipInvalid, "flow needs TLS over plain TCP")
			}
			cfg["flow"] = flow
		}
		if m.boolean("xudp") || m.str("packet-encoding") == "xudp" {
			cfg["packet_encoding"] = "xudp"
		}
		setIf(cfg, "tls", t.object())
		setIf(cfg, "transport", tr)
		return node("vless", cfg), nil

	case "trojan":
		t, tr, err := clashStream(m, true)
		if err != nil {
			return Node{}, err
		}
		cfg := base("trojan")
		cfg["password"] = m.str("password")
		setIf(cfg, "tls", t.object())
		setIf(cfg, "transport", tr)
		return node("trojan", cfg), nil

	case "hysteria2", "hy2":
		cfg := base("hysteria2")
		cfg["password"] = m.str("password", "auth")
		if ports := portRanges(m.str("ports")); len(ports) > 0 {
			cfg["server_ports"] = anySlice(ports)
			if port == 0 {
				port, _ = strconv.Atoi(strings.SplitN(ports[0], ":", 2)[0])
				cfg["server_port"] = port
			}
		}
		if hop := m.num("hop-interval"); hop > 0 {
			cfg["hop_interval"] = fmt.Sprintf("%ds", hop)
		}
		if o := m.str("obfs"); o != "" {
			if o != "salamander" {
				return Node{}, skip(SkipUnsupportedFeature, "obfs %q is not supported", o)
			}
			cfg["obfs"] = map[string]any{"type": "salamander", "password": m.str("obfs-password")}
		}
		if v := mbps(m.str("up")); v > 0 {
			cfg["up_mbps"] = v
		}
		if v := mbps(m.str("down")); v > 0 {
			cfg["down_mbps"] = v
		}
		t := tlsSpec{Enabled: true, ServerName: m.str("sni", "servername"), Insecure: m.boolean("skip-cert-verify"), ALPN: m.list("alpn")}
		if m.str("fingerprint") != "" && !t.Insecure {
			return Node{}, skip(SkipUnsupportedFeature, "certificate pinning is not supported")
		}
		cfg["tls"] = t.object()
		n := node("hysteria2", cfg)
		n.Port = port
		return n, nil

	case "hysteria":
		cfg := base("hysteria")
		setIf(cfg, "auth_str", m.str("auth-str", "auth_str", "auth"))
		up, down := mbps(m.str("up")), mbps(m.str("down"))
		if up == 0 {
			up = 50
		}
		if down == 0 {
			down = 100
		}
		cfg["up_mbps"], cfg["down_mbps"] = up, down
		if p := strings.ToLower(m.str("protocol")); p != "" && p != "udp" {
			return Node{}, skip(SkipUnsupportedFeature, "Hysteria protocol %q is not supported", p)
		}
		setIf(cfg, "obfs", m.str("obfs"))
		t := tlsSpec{Enabled: true, ServerName: m.str("sni", "peer"), Insecure: m.boolean("skip-cert-verify"), ALPN: m.list("alpn")}
		cfg["tls"] = t.object()
		if cfg["auth_str"] == nil {
			cfg["auth_str"] = ""
		}
		return node("hysteria", cfg), nil

	case "tuic":
		cfg := base("tuic")
		cfg["uuid"] = m.str("uuid")
		setIf(cfg, "password", m.str("password"))
		if cc := strings.ToLower(m.str("congestion-controller", "congestion_control")); cc != "" {
			switch cc {
			case "bbr", "cubic", "new_reno":
				cfg["congestion_control"] = cc
			default:
				return Node{}, skip(SkipUnsupportedFeature, "congestion control %q is not supported", cc)
			}
		}
		if v := strings.ToLower(m.str("udp-relay-mode", "udp_relay_mode")); v == "native" || v == "quic" {
			cfg["udp_relay_mode"] = v
		}
		if m.boolean("udp-over-stream") {
			cfg["udp_over_stream"] = true
		}
		if m.boolean("reduce-rtt") {
			cfg["zero_rtt_handshake"] = true
		}
		if hb := m.num("heartbeat-interval"); hb > 0 {
			cfg["heartbeat"] = fmt.Sprintf("%dms", hb)
		}
		t := tlsSpec{Enabled: true, ServerName: m.str("sni", "servername"), Insecure: m.boolean("skip-cert-verify"), ALPN: m.list("alpn"), DisableSNI: m.boolean("disable-sni")}
		if m.str("fingerprint") != "" && !t.Insecure {
			return Node{}, skip(SkipUnsupportedFeature, "certificate pinning is not supported")
		}
		cfg["tls"] = t.object()
		return node("tuic", cfg), nil

	case "anytls":
		cfg := base("anytls")
		cfg["password"] = m.str("password")
		if v := m.num("idle-session-check-interval"); v > 0 {
			cfg["idle_session_check_interval"] = fmt.Sprintf("%ds", v)
		}
		if v := m.num("idle-session-timeout"); v > 0 {
			cfg["idle_session_timeout"] = fmt.Sprintf("%ds", v)
		}
		if v := m.num("min-idle-session"); v > 0 {
			cfg["min_idle_session"] = v
		}
		t := tlsSpec{Enabled: true, ServerName: m.str("sni", "servername"), Insecure: m.boolean("skip-cert-verify"), ALPN: m.list("alpn"), Fingerprint: m.str("client-fingerprint")}
		if m.str("fingerprint") != "" && !t.Insecure {
			return Node{}, skip(SkipUnsupportedFeature, "certificate pinning is not supported")
		}
		cfg["tls"] = t.object()
		return node("anytls", cfg), nil

	case "socks5", "socks":
		if m.boolean("tls") {
			return Node{}, skip(SkipUnsupportedFeature, "SOCKS over TLS is not supported")
		}
		cfg := base("socks")
		cfg["version"] = "5"
		setIf(cfg, "username", m.str("username"))
		setIf(cfg, "password", m.str("password"))
		return node("socks", cfg), nil

	case "http":
		cfg := base("http")
		setIf(cfg, "username", m.str("username"))
		setIf(cfg, "password", m.str("password"))
		if m.boolean("tls") {
			t := tlsSpec{Enabled: true, ServerName: m.str("sni", "servername"), Insecure: m.boolean("skip-cert-verify")}
			cfg["tls"] = t.object()
		}
		return node("http", cfg), nil

	case "wireguard":
		var addresses []string
		for _, k := range []string{"ip", "ipv6"} {
			if v := m.str(k); v != "" {
				if !strings.Contains(v, "/") {
					if k == "ip" {
						v += "/32"
					} else {
						v += "/128"
					}
				}
				addresses = append(addresses, v)
			}
		}
		if len(addresses) == 0 {
			return Node{}, skip(SkipInvalid, "the WireGuard address is missing")
		}
		raw, _ := m["peers"].([]any)
		if len(raw) == 0 {
			if toCM(m["peers"]) != nil {
				return Node{}, skip(SkipInvalid, "the WireGuard peers cannot be read")
			}
			return wireguardNode(name, server, port, m.str("private-key"), m.str("public-key"), m.str("pre-shared-key"), addresses, clashReserved(m), m.str("mtu")), nil
		}
		// Several peers: the first one is the server the node is shown as.
		var n Node
		var peers []any
		for i, e := range raw {
			p := toCM(e)
			if p == nil || p.str("server") == "" || p.num("port") < 1 || p.num("port") > 65535 || p.str("public-key") == "" {
				return Node{}, skip(SkipInvalid, "a WireGuard peer is incomplete")
			}
			one := wireguardNode(name, p.str("server"), p.num("port"), m.str("private-key"), p.str("public-key"), p.str("pre-shared-key"), addresses, clashReserved(p), m.str("mtu"))
			peer := one.Config["peers"].([]any)[0].(map[string]any)
			if ips := p.list("allowed-ips"); len(ips) > 0 {
				prefixes, ok := wireguardPrefixes(ips)
				if !ok {
					return Node{}, skip(SkipInvalid, "the allowed IPs of a WireGuard peer cannot be read")
				}
				peer["allowed_ips"] = anySlice(prefixes)
			}
			if i == 0 {
				n = one
			}
			peers = append(peers, peer)
		}
		n.Config["peers"] = peers
		return n, nil

	case "snell":
		o := m.obj("obfs-opts")
		return snellSpec{
			Server:   server,
			Port:     port,
			PSK:      m.str("psk"),
			Version:  m.str("version"),
			ObfsMode: o.str("mode"),
			ObfsHost: o.str("host"),
			Mode:     m.str("mode"),
			Reuse:    m.boolean("reuse"),
		}.node(name)

	case "ssh":
		return sshSpec{
			Server:     server,
			Port:       port,
			User:       m.str("username", "user"),
			Password:   m.str("password"),
			PrivateKey: m.str("private-key", "privateKey"),
			Passphrase: m.str("private-key-passphrase"),
			HostKeys:   m.list("host-key"),
			HostKeyAlg: m.list("host-key-algorithms"),
		}.node(name)

	case "ssr", "mieru", "masque", "sudoku", "trusttunnel":
		return Node{}, skip(SkipUnsupportedProtocol, "%s is not supported", typ)
	case "direct", "dns", "reject", "reject-drop", "pass", "compatible":
		return Node{}, skip(SkipInfoEntry, "%s is not a proxy server", typ)
	default:
		return Node{}, skip(SkipUnsupportedProtocol, "%s is not supported", typ)
	}
}

// clashReserved reads WireGuard's reserved bytes, written as "1,2,3" or [1, 2, 3].
func clashReserved(m cm) string {
	if r := m.list("reserved"); len(r) == 3 {
		return strings.Join(r, ",")
	}
	if rl, ok := m["reserved"].([]any); ok && len(rl) == 3 {
		parts := make([]string, 3)
		for i, e := range rl {
			parts[i] = asString(e)
		}
		return strings.Join(parts, ",")
	}
	return ""
}

// wireguardPrefixes reads allowed IPs, taking a bare address for itself alone
// the way Clash clients do.
func wireguardPrefixes(ips []string) ([]string, bool) {
	out := make([]string, 0, len(ips))
	for _, s := range ips {
		if !strings.Contains(s, "/") {
			a, err := netip.ParseAddr(s)
			if err != nil {
				return nil, false
			}
			s = netip.PrefixFrom(a, a.BitLen()).String()
		}
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return nil, false
		}
		out = append(out, p.String())
	}
	return out, true
}
