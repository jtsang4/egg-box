package subscription

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// skipError says a link or entry cannot become a node and why.
type skipError struct{ code, detail string }

func (e *skipError) Error() string { return e.detail }

func skip(code, format string, args ...any) error {
	return &skipError{code: code, detail: fmt.Sprintf(format, args...)}
}

// parseLinks parses a list of share links, one per line.
func parseLinks(text string) (nodes []Node, skipped []Skipped) {
	for _, line := range strings.FieldsFunc(text, func(r rune) bool { return r == '\n' || r == '\r' }) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		if !strings.Contains(line, "://") {
			continue
		}
		n, err := ParseLink(line)
		if err != nil {
			skipped = append(skipped, skippedFrom(line, err))
			continue
		}
		n.Link = line
		nodes = append(nodes, n)
	}
	return nodes, skipped
}

func skippedFrom(line string, err error) Skipped {
	s := Skipped{Code: SkipInvalid, Detail: err.Error()}
	var se *skipError
	if errors.As(err, &se) {
		s.Code, s.Detail = se.code, se.detail
	}
	if i := strings.Index(line, "://"); i > 0 {
		s.Type = strings.ToLower(line[:i])
	}
	if sl, ok := splitShare(line); ok {
		s.Name = sl.Fragment
	}
	return s
}

// ErrNotOneLink means an edited link is empty or is more than one line.
var ErrNotOneLink = errors.New("give exactly one share link")

// EditedLink reads the link a person wrote for a node that was was, the way
// the node will be stored: the link kept with it, its name cleaned as an
// import cleans names. When the link still names the node the way was's own
// link did, the node keeps was's name: an import may have renamed it to tell
// it from another of the same name, and editing the port must not undo that.
// A link that names nothing gets a name made from what it now points at.
func EditedLink(was Node, link string) (Node, error) {
	link = strings.TrimSpace(link)
	if link == "" || strings.ContainsAny(link, "\r\n") {
		return Node{}, ErrNotOneLink
	}
	n, err := ParseLink(link)
	if err != nil {
		return Node{}, err
	}
	n.Link = link
	if was.Link != "" {
		if old, err := ParseLink(was.Link); err == nil && n.Name != "" && old.Name == n.Name && was.Name != "" {
			n.Name = was.Name
		}
	}
	kept, _ := Dedupe([]Node{n})
	return kept[0], nil
}

// ParseLink parses one share link (vless://, vmess://, trojan://, ss://,
// hysteria2://, snell:// ...).
func ParseLink(link string) (Node, error) {
	link = strings.TrimSpace(link)
	i := strings.Index(link, "://")
	if i <= 0 {
		return Node{}, skip(SkipInvalid, "not a share link")
	}
	var (
		n   Node
		err error
	)
	switch scheme := strings.ToLower(link[:i]); scheme {
	case "vless":
		n, err = parseVLESS(link)
	case "vmess":
		n, err = parseVMess(link)
	case "trojan":
		n, err = parseTrojan(link)
	case "ss":
		n, err = parseShadowsocks(link)
	case "hysteria2", "hy2":
		n, err = parseHysteria2(link)
	case "hysteria", "hy":
		n, err = parseHysteria(link)
	case "tuic":
		n, err = parseTUIC(link)
	case "anytls":
		n, err = parseAnyTLS(link)
	case "snell":
		n, err = parseSnell(link)
	case "ssh":
		n, err = parseSSH(link)
	case "http", "https":
		n, err = parseHTTP(link)
	case "socks", "socks5", "socks5h":
		n, err = parseSOCKS(link)
	case "wireguard", "wg":
		n, err = parseWireGuard(link)
	default:
		return Node{}, skip(SkipUnsupportedProtocol, "%s links are not supported", scheme)
	}
	if err != nil {
		return Node{}, err
	}
	if err := n.Validate(); err != nil {
		return Node{}, skip(SkipInvalid, "%v", err)
	}
	return n, nil
}

func hostPort(l *shareLink) error {
	if l.Host == "" {
		return skip(SkipInvalid, "the server address is empty")
	}
	if l.Port < 1 || l.Port > 65535 {
		return skip(SkipInvalid, "the port is missing or out of range")
	}
	return nil
}

func nodeName(l *shareLink) string {
	if l.Fragment != "" {
		return l.Fragment
	}
	return l.param("remarks", "remark", "name", "ps")
}

// streamFrom builds the TLS and transport settings common to VLESS, VMess and Trojan links.
func streamFrom(l *shareLink, tlsDefault bool) (tlsSpec, map[string]any, error) {
	t := tlsSpec{
		ServerName:  l.param("sni", "peer", "servername"),
		Insecure:    l.flag("allowInsecure", "allowinsecure", "insecure"),
		ALPN:        splitList(l.param("alpn")),
		Fingerprint: l.param("fp", "fingerprint"),
	}
	switch sec := strings.ToLower(l.param("security")); sec {
	case "":
		t.Enabled = tlsDefault
	case "tls", "xtls":
		t.Enabled = true
	case "reality":
		t.Enabled, t.Reality = true, true
		t.PublicKey = l.param("pbk", "publickey", "public-key")
		t.ShortID = l.param("sid", "shortid", "short-id")
		if t.PublicKey == "" {
			return t, nil, skip(SkipInvalid, "REALITY needs a public key")
		}
	case "none":
	default:
		return t, nil, skip(SkipUnsupportedFeature, "security %q is not supported", sec)
	}
	kind := strings.ToLower(l.param("type", "network", "net"))
	if kind == "tcp" && strings.EqualFold(l.param("headerType", "headertype"), "http") {
		return t, nil, skip(SkipUnsupportedTransport, "TCP with an HTTP header is not supported")
	}
	spec := transportSpec{
		Kind:        kind,
		Path:        l.param("path"),
		Host:        l.param("host"),
		ServiceName: l.param("serviceName", "servicename", "service_name"),
	}
	if kind == "grpc" && spec.ServiceName == "" {
		spec.ServiceName = spec.Path
	}
	if t.Enabled && t.ServerName == "" && spec.Host != "" && kind != "h2" && kind != "http" {
		t.ServerName = spec.Host
	}
	tr, err := spec.object()
	if err != nil {
		return t, nil, skip(SkipUnsupportedTransport, "%v", err)
	}
	return t, tr, nil
}

func setIf(m map[string]any, key string, v any) {
	switch t := v.(type) {
	case string:
		if t == "" {
			return
		}
	case map[string]any:
		if len(t) == 0 {
			return
		}
	case nil:
		return
	}
	m[key] = v
}

func parseVLESS(raw string) (Node, error) {
	l, ok := splitShare(raw)
	if !ok {
		return Node{}, skip(SkipInvalid, "not a valid link")
	}
	if err := hostPort(l); err != nil {
		return Node{}, err
	}
	uuid, _, _ := l.user()
	if enc := strings.ToLower(l.param("encryption")); enc != "" && enc != "none" {
		return Node{}, skip(SkipUnsupportedFeature, "VLESS encryption is not supported")
	}
	t, tr, err := streamFrom(l, false)
	if err != nil {
		return Node{}, err
	}
	cfg := map[string]any{"type": "vless", "server": l.Host, "server_port": l.Port, "uuid": uuid}
	if flow := l.param("flow"); flow != "" {
		flow = strings.TrimSuffix(flow, "-udp443")
		if flow != "xtls-rprx-vision" {
			return Node{}, skip(SkipUnsupportedFeature, "flow %q is not supported", flow)
		}
		if !t.Enabled || tr != nil {
			return Node{}, skip(SkipInvalid, "flow needs TLS over plain TCP")
		}
		cfg["flow"] = flow
	}
	if pe := l.param("packetEncoding", "packet_encoding"); pe == "xudp" || pe == "packetaddr" {
		cfg["packet_encoding"] = pe
	}
	setIf(cfg, "tls", t.object())
	setIf(cfg, "transport", tr)
	return Node{Name: nodeName(l), Type: "vless", Server: l.Host, Port: l.Port, Config: cfg}, nil
}

// vmessSecurities are the cipher names sing-box accepts for VMess.
var vmessSecurities = map[string]bool{"auto": true, "none": true, "zero": true, "aes-128-gcm": true, "chacha20-poly1305": true}

func parseVMess(raw string) (Node, error) {
	body := raw[len("vmess://"):]
	frag := ""
	if j := strings.Index(body, "#"); j >= 0 {
		frag, body = unescape(body[j+1:]), body[:j]
	}
	main, rest, hasQuery := strings.Cut(body, "?")
	if dec, ok := decodeBase64(main); ok && len(dec) > 0 {
		s := strings.TrimSpace(string(dec))
		if strings.HasPrefix(s, "{") {
			return vmessFromJSON([]byte(s), frag)
		}
		if hasQuery {
			return vmessFromQuantumult(s, rest, frag)
		}
	}
	// vmess://uuid@host:port?... (the standard share link form)
	l, ok := splitShare(raw)
	if !ok || l.UserInfo == "" {
		return Node{}, skip(SkipInvalid, "not a valid VMess link")
	}
	if err := hostPort(l); err != nil {
		return Node{}, err
	}
	uuid, _, _ := l.user()
	t, tr, err := streamFrom(l, false)
	if err != nil {
		return Node{}, err
	}
	cfg := map[string]any{"type": "vmess", "server": l.Host, "server_port": l.Port, "uuid": uuid}
	sec := strings.ToLower(l.param("encryption", "scy"))
	if sec == "" {
		sec = "auto"
	}
	if !vmessSecurities[sec] {
		return Node{}, skip(SkipUnsupportedFeature, "cipher %q is not supported", sec)
	}
	cfg["security"] = sec
	if aid, _ := strconv.Atoi(l.param("aid", "alterId")); aid > 0 {
		cfg["alter_id"] = aid
	}
	if pe := l.param("packetEncoding", "packet_encoding"); pe == "xudp" || pe == "packetaddr" {
		cfg["packet_encoding"] = pe
	}
	setIf(cfg, "tls", t.object())
	setIf(cfg, "transport", tr)
	return Node{Name: nodeName(l), Type: "vmess", Server: l.Host, Port: l.Port, Config: cfg}, nil
}

// vmessFromJSON reads the v2rayN share format: vmess://base64({"ps":...,"add":...}).
func vmessFromJSON(data []byte, fragment string) (Node, error) {
	var m map[string]any
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return Node{}, skip(SkipInvalid, "the VMess link is not valid JSON")
	}
	port, _ := strconv.Atoi(asString(m["port"]))
	server := strings.TrimSpace(asString(m["add"]))
	if server == "" {
		return Node{}, skip(SkipInvalid, "the server address is empty")
	}
	sec := strings.ToLower(asString(m["scy"]))
	if sec == "" {
		sec = "auto"
	}
	if !vmessSecurities[sec] {
		return Node{}, skip(SkipUnsupportedFeature, "cipher %q is not supported", sec)
	}
	t := tlsSpec{
		Enabled:     strings.EqualFold(asString(m["tls"]), "tls"),
		ServerName:  asString(m["sni"]),
		ALPN:        splitList(asString(m["alpn"])),
		Fingerprint: asString(m["fp"]),
	}
	switch v := strings.ToLower(asString(m["allowInsecure"])); v {
	case "1", "true":
		t.Insecure = true
	}
	kind := strings.ToLower(asString(m["net"]))
	headerType := strings.ToLower(asString(m["type"]))
	if (kind == "tcp" || kind == "") && headerType == "http" {
		return Node{}, skip(SkipUnsupportedTransport, "TCP with an HTTP header is not supported")
	}
	spec := transportSpec{Kind: kind, Path: asString(m["path"]), Host: asString(m["host"])}
	if kind == "grpc" {
		spec.ServiceName = spec.Path
	}
	if t.Enabled && t.ServerName == "" && spec.Host != "" && kind != "h2" && kind != "http" {
		t.ServerName = spec.Host
	}
	tr, err := spec.object()
	if err != nil {
		return Node{}, skip(SkipUnsupportedTransport, "%v", err)
	}
	cfg := map[string]any{"type": "vmess", "server": server, "server_port": port, "uuid": asString(m["id"]), "security": sec}
	if aid, _ := strconv.Atoi(asString(m["aid"])); aid > 0 {
		cfg["alter_id"] = aid
	}
	setIf(cfg, "tls", t.object())
	setIf(cfg, "transport", tr)
	name := asString(m["ps"])
	if name == "" {
		name = fragment
	}
	return Node{Name: name, Type: "vmess", Server: server, Port: port, Config: cfg}, nil
}

// vmessFromQuantumult reads vmess://base64("auto:uuid@host:port")?remarks=..&obfs=websocket&path=..
func vmessFromQuantumult(decoded, query, fragment string) (Node, error) {
	cred, hp, ok := strings.Cut(decoded, "@")
	if !ok {
		return Node{}, skip(SkipInvalid, "not a valid VMess link")
	}
	method, uuid, ok := strings.Cut(cred, ":")
	if !ok {
		return Node{}, skip(SkipInvalid, "not a valid VMess link")
	}
	l, ok := splitShare("vmess://" + uuid + "@" + hp + "?" + query)
	if !ok {
		return Node{}, skip(SkipInvalid, "not a valid VMess link")
	}
	if err := hostPort(l); err != nil {
		return Node{}, err
	}
	kind := ""
	switch strings.ToLower(l.param("obfs")) {
	case "websocket", "ws":
		kind = "ws"
	case "grpc":
		kind = "grpc"
	case "http", "none", "":
	default:
		return Node{}, skip(SkipUnsupportedTransport, "obfs %q is not supported", l.param("obfs"))
	}
	t := tlsSpec{
		Enabled:    l.flag("tls"),
		ServerName: l.param("peer", "sni"),
		Insecure:   l.flag("allowInsecure"),
	}
	spec := transportSpec{Kind: kind, Path: l.param("path"), Host: l.param("obfsParam", "host")}
	if kind == "grpc" {
		spec.ServiceName = spec.Path
	}
	tr, err := spec.object()
	if err != nil {
		return Node{}, skip(SkipUnsupportedTransport, "%v", err)
	}
	method = strings.ToLower(method)
	if method == "" {
		method = "auto"
	}
	if !vmessSecurities[method] {
		return Node{}, skip(SkipUnsupportedFeature, "cipher %q is not supported", method)
	}
	cfg := map[string]any{"type": "vmess", "server": l.Host, "server_port": l.Port, "uuid": uuid, "security": method}
	if aid, _ := strconv.Atoi(l.param("alterId", "aid")); aid > 0 {
		cfg["alter_id"] = aid
	}
	setIf(cfg, "tls", t.object())
	setIf(cfg, "transport", tr)
	name := l.param("remarks", "remark")
	if name == "" {
		name = fragment
	}
	return Node{Name: name, Type: "vmess", Server: l.Host, Port: l.Port, Config: cfg}, nil
}

func parseTrojan(raw string) (Node, error) {
	l, ok := splitShare(raw)
	if !ok {
		return Node{}, skip(SkipInvalid, "not a valid link")
	}
	if err := hostPort(l); err != nil {
		return Node{}, err
	}
	t, tr, err := streamFrom(l, true)
	if err != nil {
		return Node{}, err
	}
	cfg := map[string]any{"type": "trojan", "server": l.Host, "server_port": l.Port, "password": unescape(l.UserInfo)}
	setIf(cfg, "tls", t.object())
	setIf(cfg, "transport", tr)
	return Node{Name: nodeName(l), Type: "trojan", Server: l.Host, Port: l.Port, Config: cfg}, nil
}

func parseShadowsocks(raw string) (Node, error) {
	body := raw[len("ss://"):]
	name := ""
	if j := strings.Index(body, "#"); j >= 0 {
		name, body = unescape(body[j+1:]), body[:j]
	}
	var query string
	if j := strings.Index(body, "?"); j >= 0 {
		query, body = body[j+1:], body[:j]
	}
	body = strings.TrimSuffix(body, "/")

	var method, password, server string
	var port int
	if at := strings.LastIndex(body, "@"); at >= 0 {
		// SIP002: userinfo@host:port, userinfo is base64 or "method:password".
		ui := unescape(body[:at])
		var ok bool
		if m, p, found := strings.Cut(ui, ":"); found {
			method, password, ok = m, p, true
		} else if dec, good := decodeBase64(ui); good {
			m, p, found := strings.Cut(string(dec), ":")
			method, password, ok = m, p, found
		}
		if !ok {
			return Node{}, skip(SkipInvalid, "the Shadowsocks credentials cannot be read")
		}
		server, port = splitHostPort(body[at+1:])
	} else {
		// Legacy: base64(method:password@host:port).
		dec, ok := decodeBase64(body)
		if !ok {
			return Node{}, skip(SkipInvalid, "the Shadowsocks link cannot be decoded")
		}
		s := string(dec)
		at := strings.LastIndex(s, "@")
		if at < 0 {
			return Node{}, skip(SkipInvalid, "the Shadowsocks link cannot be decoded")
		}
		var found bool
		method, password, found = strings.Cut(s[:at], ":")
		if !found {
			return Node{}, skip(SkipInvalid, "the Shadowsocks credentials cannot be read")
		}
		server, port = splitHostPort(s[at+1:])
	}
	if server == "" || port < 1 || port > 65535 {
		return Node{}, skip(SkipInvalid, "the server address or port is invalid")
	}
	method = ssMethodAlias(strings.ToLower(strings.TrimSpace(method)))
	cfg := map[string]any{"type": "shadowsocks", "server": server, "server_port": port, "method": method, "password": password}

	n := Node{Name: name, Type: "shadowsocks", Server: server, Port: port, Config: cfg}
	if q := parseQuery(query); q.Get("plugin") != "" {
		pname, popts, _ := strings.Cut(q.Get("plugin"), ";")
		switch pname {
		case "simple-obfs", "obfs-local", "obfs":
			pname = "obfs-local"
		case "v2ray-plugin":
		case "shadow-tls", "shadowtls":
			o := pluginOpts(popts)
			return shadowTLSSpec{
				Host: o["host"], Password: o["password"], Version: o["version"],
				Fingerprint: o["fp"], ALPN: splitList(strings.ReplaceAll(o["alpn"], "|", ",")),
			}.over(n)
		default:
			return Node{}, skip(SkipUnsupportedFeature, "Shadowsocks plugin %q is not supported", pname)
		}
		cfg["plugin"] = pname
		setIf(cfg, "plugin_opts", popts)
	}
	return n, nil
}

// ssMethodAlias maps the names other clients use to sing-box's.
func ssMethodAlias(m string) string {
	switch m {
	case "chacha20-poly1305":
		return "chacha20-ietf-poly1305"
	case "xchacha20-poly1305":
		return "xchacha20-ietf-poly1305"
	case "plain", "dummy":
		return "none"
	}
	return m
}

// splitHostPort splits "host:port" and "[v6]:port" leniently.
func splitHostPort(s string) (string, int) {
	s = strings.TrimSpace(strings.TrimSuffix(s, "/"))
	if strings.HasPrefix(s, "[") {
		if j := strings.Index(s, "]"); j > 0 {
			p, _ := strconv.Atoi(strings.TrimPrefix(s[j+1:], ":"))
			return s[1:j], p
		}
		return "", 0
	}
	j := strings.LastIndex(s, ":")
	if j < 0 {
		return s, 0
	}
	p, _ := strconv.Atoi(s[j+1:])
	return s[:j], p
}

func parseHysteria2(raw string) (Node, error) {
	l, ok := splitShare(raw)
	if !ok {
		return Node{}, skip(SkipInvalid, "not a valid link")
	}
	if l.Host == "" {
		return Node{}, skip(SkipInvalid, "the server address is empty")
	}
	cfg := map[string]any{"type": "hysteria2", "server": l.Host, "password": unescape(l.UserInfo)}
	port := l.Port
	ranges := portRanges(l.param("mport", "ports"))
	if strings.ContainsAny(l.PortSpec, ",-") {
		ranges = append(portRanges(l.PortSpec), ranges...)
	}
	switch {
	case len(ranges) > 0:
		cfg["server_ports"] = anySlice(ranges)
		if port == 0 {
			port, _ = strconv.Atoi(strings.SplitN(ranges[0], ":", 2)[0])
		}
	case port >= 1 && port <= 65535:
		cfg["server_port"] = port
	default:
		return Node{}, skip(SkipInvalid, "the port is missing or out of range")
	}
	if o := l.param("obfs"); o != "" {
		if o != "salamander" {
			return Node{}, skip(SkipUnsupportedFeature, "obfs %q is not supported", o)
		}
		cfg["obfs"] = map[string]any{"type": "salamander", "password": l.param("obfs-password", "obfs_password")}
	}
	if v := mbps(l.param("up", "upmbps")); v > 0 {
		cfg["up_mbps"] = v
	}
	if v := mbps(l.param("down", "downmbps")); v > 0 {
		cfg["down_mbps"] = v
	}
	t := tlsSpec{
		Enabled:    true,
		ServerName: l.param("sni", "peer"),
		Insecure:   l.flag("insecure", "allowInsecure"),
		ALPN:       splitList(l.param("alpn")),
	}
	if l.param("pinSHA256", "pin") != "" && !t.Insecure {
		return Node{}, skip(SkipUnsupportedFeature, "certificate pinning is not supported")
	}
	cfg["tls"] = t.object()
	return Node{Name: nodeName(l), Type: "hysteria2", Server: l.Host, Port: port, Config: cfg}, nil
}

func parseHysteria(raw string) (Node, error) {
	l, ok := splitShare(raw)
	if !ok {
		return Node{}, skip(SkipInvalid, "not a valid link")
	}
	if err := hostPort(l); err != nil {
		return Node{}, err
	}
	if p := strings.ToLower(l.param("protocol")); p != "" && p != "udp" {
		return Node{}, skip(SkipUnsupportedFeature, "Hysteria protocol %q is not supported", p)
	}
	cfg := map[string]any{"type": "hysteria", "server": l.Host, "server_port": l.Port}
	setIf(cfg, "auth_str", l.param("auth", "auth_str"))
	up, down := mbps(l.param("upmbps", "up")), mbps(l.param("downmbps", "down"))
	if up == 0 {
		up = 50
	}
	if down == 0 {
		down = 100
	}
	cfg["up_mbps"], cfg["down_mbps"] = up, down
	if o := l.param("obfs"); o != "" {
		cfg["obfs"] = l.param("obfsParam", "obfs-password")
	}
	t := tlsSpec{
		Enabled:    true,
		ServerName: l.param("peer", "sni"),
		Insecure:   l.flag("insecure", "allowInsecure"),
		ALPN:       splitList(l.param("alpn")),
	}
	cfg["tls"] = t.object()
	if cfg["auth_str"] == nil {
		cfg["auth_str"] = ""
	}
	return Node{Name: nodeName(l), Type: "hysteria", Server: l.Host, Port: l.Port, Config: cfg}, nil
}

func parseTUIC(raw string) (Node, error) {
	l, ok := splitShare(raw)
	if !ok {
		return Node{}, skip(SkipInvalid, "not a valid link")
	}
	if err := hostPort(l); err != nil {
		return Node{}, err
	}
	uuid, password, _ := l.user()
	cfg := map[string]any{"type": "tuic", "server": l.Host, "server_port": l.Port, "uuid": uuid}
	setIf(cfg, "password", password)
	if cc := strings.ToLower(l.param("congestion_control", "congestion-controller")); cc != "" {
		switch cc {
		case "bbr", "cubic", "new_reno":
			cfg["congestion_control"] = cc
		default:
			return Node{}, skip(SkipUnsupportedFeature, "congestion control %q is not supported", cc)
		}
	}
	if m := strings.ToLower(l.param("udp_relay_mode", "udp-relay-mode")); m == "native" || m == "quic" {
		cfg["udp_relay_mode"] = m
	}
	if l.flag("udp_over_stream", "udp-over-stream") {
		cfg["udp_over_stream"] = true
	}
	if l.flag("reduce_rtt", "zero_rtt_handshake") {
		cfg["zero_rtt_handshake"] = true
	}
	t := tlsSpec{
		Enabled:    true,
		ServerName: l.param("sni", "peer"),
		Insecure:   l.flag("allow_insecure", "allowInsecure", "insecure"),
		ALPN:       splitList(l.param("alpn")),
		DisableSNI: l.flag("disable_sni", "disable-sni"),
	}
	cfg["tls"] = t.object()
	return Node{Name: nodeName(l), Type: "tuic", Server: l.Host, Port: l.Port, Config: cfg}, nil
}

func parseAnyTLS(raw string) (Node, error) {
	l, ok := splitShare(raw)
	if !ok {
		return Node{}, skip(SkipInvalid, "not a valid link")
	}
	if err := hostPort(l); err != nil {
		return Node{}, err
	}
	t := tlsSpec{
		Enabled:     true,
		ServerName:  l.param("sni", "peer"),
		Insecure:    l.flag("insecure", "allowInsecure"),
		ALPN:        splitList(l.param("alpn")),
		Fingerprint: l.param("fp", "fingerprint"),
	}
	cfg := map[string]any{"type": "anytls", "server": l.Host, "server_port": l.Port, "password": unescape(l.UserInfo), "tls": t.object()}
	return Node{Name: nodeName(l), Type: "anytls", Server: l.Host, Port: l.Port, Config: cfg}, nil
}

func parseSOCKS(raw string) (Node, error) {
	l, ok := splitShare(raw)
	if !ok {
		return Node{}, skip(SkipInvalid, "not a valid link")
	}
	if err := hostPort(l); err != nil {
		return Node{}, err
	}
	user, pass, hasPass := l.user()
	if !hasPass && user != "" {
		// socks://base64(user:pass)@host:port
		if dec, ok := decodeBase64(user); ok {
			if u, p, found := strings.Cut(string(dec), ":"); found {
				user, pass = u, p
			}
		}
	}
	cfg := map[string]any{"type": "socks", "server": l.Host, "server_port": l.Port, "version": "5"}
	setIf(cfg, "username", user)
	setIf(cfg, "password", pass)
	return Node{Name: nodeName(l), Type: "socks", Server: l.Host, Port: l.Port, Config: cfg}, nil
}

func parseWireGuard(raw string) (Node, error) {
	l, ok := splitShare(raw)
	if !ok {
		return Node{}, skip(SkipInvalid, "not a valid link")
	}
	if err := hostPort(l); err != nil {
		return Node{}, err
	}
	priv, _, _ := l.user()
	pub := l.param("publickey", "public-key", "peer_public_key")
	if priv == "" || pub == "" {
		return Node{}, skip(SkipInvalid, "WireGuard keys are missing")
	}
	addresses := splitList(l.param("address", "ip"))
	if len(addresses) == 0 {
		return Node{}, skip(SkipInvalid, "the WireGuard address is missing")
	}
	return wireguardNode(nodeName(l), l.Host, l.Port, priv, pub, l.param("presharedkey", "pre-shared-key"), addresses, l.param("reserved"), l.param("mtu")), nil
}

// wireguardNode builds a sing-box WireGuard endpoint with a single peer that
// routes everything (the tunnel is used as a proxy).
func wireguardNode(name, server string, port int, private, public, psk string, addresses []string, reserved, mtu string) Node {
	peer := map[string]any{
		"address": server, "port": port, "public_key": public,
		"allowed_ips": []any{"0.0.0.0/0", "::/0"},
	}
	setIf(peer, "pre_shared_key", psk)
	if r := strings.Split(reserved, ","); len(r) == 3 {
		var rs []any
		for _, p := range r {
			v, err := strconv.Atoi(strings.TrimSpace(p))
			if err != nil || v < 0 || v > 255 {
				rs = nil
				break
			}
			rs = append(rs, v)
		}
		if rs != nil {
			peer["reserved"] = rs
		}
	}
	cfg := map[string]any{
		"type": "wireguard", "address": anySlice(addresses), "private_key": private,
		"peers": []any{peer},
	}
	if v, err := strconv.Atoi(mtu); err == nil && v >= 576 && v <= 9000 {
		cfg["mtu"] = v
	}
	return Node{Name: name, Type: "wireguard", Server: server, Port: port, Endpoint: true, Config: cfg}
}

// asString renders a JSON scalar as text.
func asString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(t)
	case json.Number:
		return t.String()
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case bool:
		return strconv.FormatBool(t)
	}
	return fmt.Sprint(v)
}

// parseHTTP reads http://user:password@host:port#name and its TLS form
// https://. Only a bare proxy address counts: a line in a plain-text
// subscription that is a web address (a path, no port) is not a server.
func parseHTTP(raw string) (Node, error) {
	l, ok := splitShare(raw)
	if !ok {
		return Node{}, skip(SkipInvalid, "not a valid link")
	}
	rest := raw[len(l.Scheme)+3:]
	if j := strings.IndexAny(rest, "?#"); j >= 0 {
		rest = rest[:j]
	}
	if j := strings.LastIndex(rest, "@"); j >= 0 {
		rest = rest[j+1:] // base64 credentials may hold a '/'
	}
	if j := strings.Index(rest, "/"); j >= 0 && strings.TrimRight(rest[j:], "/") != "" {
		return Node{}, skip(SkipUnsupportedProtocol, "web addresses are not proxy servers")
	}
	if l.PortSpec == "" {
		return Node{}, skip(SkipUnsupportedProtocol, "web addresses are not proxy servers")
	}
	for k := range l.Query {
		switch k {
		case "sni", "peer", "insecure", "allowInsecure", "skip-cert-verify":
		default: // a query of its own (a token...) makes it a web address
			return Node{}, skip(SkipUnsupportedProtocol, "web addresses are not proxy servers")
		}
	}
	if err := hostPort(l); err != nil {
		return Node{}, err
	}
	user, password, hasPassword := l.user()
	if !hasPassword && user != "" {
		// http://base64(user:pass)@host:port, as some clients export it.
		if dec, ok := decodeBase64(user); ok {
			if u, p, found := strings.Cut(string(dec), ":"); found {
				user, password = u, p
			}
		}
	}
	cfg := map[string]any{"type": "http", "server": l.Host, "server_port": l.Port}
	setIf(cfg, "username", user)
	setIf(cfg, "password", password)
	if l.Scheme == "https" {
		t := tlsSpec{Enabled: true, ServerName: l.param("sni", "peer"), Insecure: l.flag("insecure", "allowInsecure", "skip-cert-verify")}
		cfg["tls"] = t.object()
	}
	return Node{Name: nodeName(l), Type: "http", Server: l.Host, Port: l.Port, Config: cfg}, nil
}
