package subscription

import (
	"encoding/base64"
	"errors"
	"math/rand"
	"strings"
	"testing"
	"time"
)

const clashProfile = `
mixed-port: 7890
mode: rule
proxies:
  - {name: "HK ss", type: ss, server: ss.example.com, port: 8388, cipher: aes-256-gcm, password: pw1, udp: true}
  - name: "HK ss obfs"
    type: ss
    server: obfs.example.com
    port: "443"
    cipher: chacha20-poly1305
    password: pw2
    plugin: obfs
    plugin-opts: {mode: tls, host: www.example.org}
  - name: "JP vmess ws"
    type: vmess
    server: vm.example.com
    port: 443
    uuid: 00000000-0000-4000-8000-000000000001
    alterId: 0
    cipher: auto
    tls: true
    servername: cdn.example.net
    network: ws
    ws-opts:
      path: /vm
      headers: {Host: cdn.example.net}
      max-early-data: 2048
      early-data-header-name: Sec-WebSocket-Protocol
  - name: "US vless reality"
    type: vless
    server: vl.example.com
    port: 443
    uuid: 00000000-0000-4000-8000-000000000002
    flow: xtls-rprx-vision
    tls: true
    servername: www.example.org
    client-fingerprint: chrome
    reality-opts: {public-key: Zm9yLXRlc3RzLW9ubHktbm90LWEtcmVhbC1rZXktMDAwMDA, short-id: ab12}
    network: tcp
  - name: "SG trojan grpc"
    type: trojan
    server: tr.example.com
    port: 443
    password: pw3
    sni: tr.example.com
    network: grpc
    grpc-opts: {grpc-service-name: svc}
  - name: "KR hy2"
    type: hysteria2
    server: hy.example.com
    port: 443
    password: pw4
    sni: hy.example.com
    skip-cert-verify: true
    obfs: salamander
    obfs-password: ob
    up: "30 Mbps"
    down: "200 Mbps"
    ports: 20000-30000
  - name: "tuic"
    type: tuic
    server: tu.example.com
    port: 443
    uuid: 00000000-0000-4000-8000-000000000003
    password: pw5
    congestion-controller: bbr
    udp-relay-mode: native
    alpn: [h3]
    sni: tu.example.com
  - {name: "anytls", type: anytls, server: at.example.com, port: 443, password: pw6, sni: at.example.com}
  - {name: "socks", type: socks5, server: so.example.com, port: 1080, username: u, password: p}
  - {name: "pinned", type: trojan, server: pin.example.com, port: 443, password: x, fingerprint: "AA:BB"}
  - {name: "chain", type: trojan, server: ch.example.com, port: 443, password: x, dialer-proxy: other}
  - {name: "http obfs", type: vmess, server: ho.example.com, port: 80, uuid: 00000000-0000-4000-8000-000000000004, network: http}
  - {name: "ssr", type: ssr, server: ssr.example.com, port: 1, cipher: aes-128-cfb, password: x}
  - {name: "DIRECT", type: direct}
proxy-groups:
  - {name: Proxy, type: select, proxies: ["HK ss"]}
rules:
  - MATCH,Proxy
`

func TestClashProfile(t *testing.T) {
	res, err := Parse([]byte(clashProfile))
	if err != nil {
		t.Fatal(err)
	}
	if res.Format != FormatClash {
		t.Fatalf("format = %s", res.Format)
	}
	byName := map[string]Node{}
	for _, n := range res.Nodes {
		byName[n.Name] = n
	}
	if len(res.Nodes) != 9 {
		t.Fatalf("got %d nodes: %v", len(res.Nodes), names(res.Nodes))
	}
	eq(t, byName["HK ss"], "method", "aes-256-gcm")
	eq(t, byName["HK ss obfs"], "plugin", "obfs-local")
	eq(t, byName["HK ss obfs"], "plugin_opts", "obfs=tls;obfs-host=www.example.org")
	eq(t, byName["HK ss obfs"], "method", "chacha20-ietf-poly1305")
	if byName["HK ss obfs"].Port != 443 {
		t.Fatal("quoted port")
	}

	vm := byName["JP vmess ws"]
	eq(t, vm, "transport.type", "ws")
	eq(t, vm, "transport.max_early_data", 2048)
	eq(t, vm, "transport.headers.Host", "cdn.example.net")
	eq(t, vm, "tls.server_name", "cdn.example.net")

	vl := byName["US vless reality"]
	eq(t, vl, "flow", "xtls-rprx-vision")
	eq(t, vl, "tls.reality.short_id", "ab12")
	eq(t, vl, "tls.utls.fingerprint", "chrome")
	if vl.Protocol() != "VLESS Reality" {
		t.Fatal(vl.Protocol())
	}

	eq(t, byName["SG trojan grpc"], "transport.service_name", "svc")
	eq(t, byName["SG trojan grpc"], "tls.enabled", true)

	hy := byName["KR hy2"]
	eq(t, hy, "up_mbps", 30)
	eq(t, hy, "down_mbps", 200)
	eq(t, hy, "server_ports", []any{"20000:30000"})
	eq(t, hy, "obfs.password", "ob")
	eq(t, hy, "tls.insecure", true)

	eq(t, byName["tuic"], "congestion_control", "bbr")
	eq(t, byName["tuic"], "tls.alpn", []any{"h3"})
	eq(t, byName["socks"], "username", "u")

	reasons := map[string]string{}
	for _, s := range res.Skipped {
		reasons[s.Name] = s.Code
	}
	for name, code := range map[string]string{
		"pinned": SkipUnsupportedFeature, "chain": SkipUnsupportedFeature,
		"http obfs": SkipUnsupportedTransport, "ssr": SkipUnsupportedProtocol, "DIRECT": SkipInfoEntry,
	} {
		if reasons[name] != code {
			t.Errorf("skip %q = %q, want %q (all: %v)", name, reasons[name], code, reasons)
		}
	}
}

func names(nodes []Node) []string {
	var out []string
	for _, n := range nodes {
		out = append(out, n.Name)
	}
	return out
}

const singboxProfile = `{
  "log": {"level": "info"},
  "dns": {"servers": [{"type": "udp", "tag": "d", "server": "8.8.8.8"}]},
  "inbounds": [{"type": "mixed", "listen": "0.0.0.0", "listen_port": 2080}, {"type": "tun"}],
  "outbounds": [
    {"type": "selector", "tag": "Proxy", "outbounds": ["a", "b"]},
    {"type": "urltest", "tag": "Auto", "outbounds": ["a", "b"]},
    {"type": "direct", "tag": "direct"},
    {"type": "vless", "tag": "a", "server": "a.example.com", "server_port": 443, "uuid": "00000000-0000-4000-8000-000000000001",
     "flow": "xtls-rprx-vision", "tcp_fast_open": true, "bind_interface": "en0", "domain_resolver": "d",
     "tls": {"enabled": true, "server_name": "www.example.org", "reality": {"enabled": true, "public_key": "k", "short_id": "ab"}, "utls": {"enabled": true, "fingerprint": "chrome"}}},
    {"type": "trojan", "tag": "b", "server": "b.example.com", "server_port": 443, "password": "pw", "detour": "direct", "tls": {"enabled": true}},
    {"type": "trojan", "tag": "chained", "server": "c.example.com", "server_port": 443, "password": "pw", "detour": "a"},
    {"type": "shadowtls", "tag": "st", "server": "s.example.com", "server_port": 443, "version": 3},
    {"type": "hysteria2", "tag": "h", "server": "h.example.com", "server_ports": ["20000:30000"], "password": "pw"}
  ],
  "endpoints": [
    {"type": "wireguard", "tag": "wg", "address": ["10.0.0.2/32"], "private_key": "k", "peers": [{"address": "w.example.com", "port": 51820, "public_key": "p", "allowed_ips": ["0.0.0.0/0"]}]},
    {"type": "wireguard", "tag": "wgsys", "system": true, "address": ["10.0.0.3/32"], "private_key": "k", "peers": [{"address": "w2.example.com", "port": 1, "public_key": "p"}]}
  ],
  "route": {"rules": [{"outbound": "Proxy"}], "final": "Proxy"}
}`

func TestSingBoxProfile(t *testing.T) {
	res, err := Parse([]byte(singboxProfile))
	if err != nil {
		t.Fatal(err)
	}
	if res.Format != FormatSingBox {
		t.Fatalf("format = %s", res.Format)
	}
	byName := map[string]Node{}
	for _, n := range res.Nodes {
		byName[n.Name] = n
	}
	if len(res.Nodes) != 4 {
		t.Fatalf("nodes = %v", names(res.Nodes))
	}
	a := byName["a"]
	for _, gone := range []string{"tag", "bind_interface", "domain_resolver"} {
		if _, ok := a.Config[gone]; ok {
			t.Errorf("%s must be removed from the node", gone)
		}
	}
	eq(t, a, "tcp_fast_open", true)
	eq(t, a, "tls.reality.public_key", "k")
	if a.Port != 443 || a.Server != "a.example.com" {
		t.Fatalf("a = %+v", a)
	}
	if _, ok := byName["b"].Config["detour"]; ok {
		t.Error("a detour to direct is meaningless and must be removed")
	}
	if byName["h"].Port != 20000 {
		t.Fatalf("hysteria2 port = %d", byName["h"].Port)
	}
	wg := byName["wg"]
	if !wg.Endpoint || wg.Server != "w.example.com" || wg.Port != 51820 {
		t.Fatalf("wg = %+v", wg)
	}
	reasons := map[string]string{}
	for _, s := range res.Skipped {
		reasons[s.Name] = s.Code
	}
	for name, code := range map[string]string{
		"chained": SkipUnsupportedFeature, "st": SkipUnsupportedProtocol, "wgsys": SkipUnsupportedFeature,
	} {
		if reasons[name] != code {
			t.Errorf("skip %q = %q, want %q (all: %v)", name, reasons[name], code, reasons)
		}
	}
	// Structural entries and the file's own inbounds/DNS/rules never show up as skipped servers.
	for _, n := range []string{"Proxy", "Auto", "direct"} {
		if _, ok := reasons[n]; ok {
			t.Errorf("%s is structure, not a skipped server", n)
		}
	}
}

func TestSingBoxBareOutboundArray(t *testing.T) {
	res, err := Parse([]byte(`[{"type":"trojan","tag":"x","server":"x.example.com","server_port":443,"password":"pw","tls":{"enabled":true}}]`))
	if err != nil || len(res.Nodes) != 1 || res.Nodes[0].Name != "x" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestLinkListsAndBase64(t *testing.T) {
	links := []string{
		"trojan://pw@a.example.com:443#Alpha",
		"vless://" + testUUID + "@b.example.com:443?security=tls&sni=b.example.com#Beta",
		"# a comment line",
		"unknown-line-without-scheme",
		"ssr://abc",
		"",
	}
	plain := strings.Join(links, "\r\n")

	res, err := Parse([]byte(plain))
	if err != nil || res.Format != FormatLinks || len(res.Nodes) != 2 {
		t.Fatalf("plain: res=%+v err=%v", res, err)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Code != SkipUnsupportedProtocol {
		t.Fatalf("skipped = %+v", res.Skipped)
	}

	for name, enc := range map[string]*base64.Encoding{
		"std": base64.StdEncoding, "raw-std": base64.RawStdEncoding, "url": base64.URLEncoding, "raw-url": base64.RawURLEncoding,
	} {
		body := enc.EncodeToString([]byte(plain))
		// Providers wrap base64 at 76 columns.
		var wrapped strings.Builder
		for i := 0; i < len(body); i += 76 {
			end := min(i+76, len(body))
			wrapped.WriteString(body[i:end] + "\n")
		}
		for _, in := range []string{body, wrapped.String(), "\ufeff" + body} {
			res, err := Parse([]byte(in))
			if err != nil || res.Format != FormatBase64Links || len(res.Nodes) != 2 {
				t.Fatalf("%s: res=%+v err=%v", name, res, err)
			}
		}
	}

	// base64 around a Clash profile or a sing-box file is also common.
	res, err = Parse([]byte(base64.StdEncoding.EncodeToString([]byte(clashProfile))))
	if err != nil || res.Format != FormatClash {
		t.Fatalf("base64 clash: res=%+v err=%v", res, err)
	}
}

func TestInfoEntriesAreNotNodes(t *testing.T) {
	links := strings.Join([]string{
		"trojan://pw@127.0.0.1:1#" + "剩余流量：123.4 GB",
		"trojan://pw@127.0.0.1:2#" + "套餐到期：2026-12-31",
		"trojan://pw@a.example.com:443#" + "Expire: 2026-12-31",
		"trojan://pw@loopback.example.com:443#" + "官网：example.org",
		"trojan://pw@real.example.com:443#" + "🇭🇰 香港 IEPL 0.5倍流量",
		"trojan://pw@real2.example.com:443#" + "Tokyo 02",
	}, "\n")
	res, err := Parse([]byte(links))
	if err != nil {
		t.Fatal(err)
	}
	got := names(res.Nodes)
	want := []string{"🇭🇰 香港 IEPL 0.5倍流量", "Tokyo 02"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("nodes = %q, want %q", got, want)
	}
	if len(res.Notices) != 4 {
		t.Fatalf("notices = %q", res.Notices)
	}
}

func TestParseErrors(t *testing.T) {
	if _, err := Parse(nil); !errors.Is(err, ErrEmpty) {
		t.Fatalf("empty: %v", err)
	}
	if _, err := Parse([]byte("  \n\t ")); !errors.Is(err, ErrEmpty) {
		t.Fatalf("blank: %v", err)
	}
	if _, err := Parse([]byte("<html><body>Please log in</body></html>")); !errors.Is(err, ErrUnknownFormat) {
		t.Fatalf("html: %v", err)
	}
	if _, err := Parse([]byte(`{"a": `)); !errors.Is(err, ErrUnknownFormat) {
		t.Fatalf("broken json: %v", err)
	}
	res, err := Parse([]byte(`{"outbounds":[{"type":"direct","tag":"d"},{"type":"shadowtls","tag":"s"}]}`))
	if !errors.Is(err, ErrNoNodes) || res == nil || len(res.Skipped) != 1 {
		t.Fatalf("no nodes: res=%+v err=%v", res, err)
	}
	if _, err := Parse([]byte(`{"log": {}}`)); !errors.Is(err, ErrUnknownFormat) {
		t.Fatalf("json without outbounds: %v", err)
	}
}

func TestParseNeverPanics(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	seeds := []string{
		clashProfile, singboxProfile, "vless://", "ss://", "vmess://", "hysteria2://@:", "trojan://%zz@h:1",
		"proxies:\n  - 1\n  - [a]\n  - {type: ss}\n", `[1,"a",null,{"type":1}]`, `{"outbounds":[null,1,{"type":"vless","server":5}]}`,
		"[::1]:443", "://", "a://b@[", "wireguard://@h:1?address=x",
	}
	for i := 0; i < 400; i++ {
		var b []byte
		if i%2 == 0 {
			b = []byte(seeds[rng.Intn(len(seeds))])
			for j := 0; j < rng.Intn(6); j++ {
				if len(b) > 0 {
					b[rng.Intn(len(b))] = byte(rng.Intn(256))
				}
			}
			if len(b) > 0 && rng.Intn(3) == 0 {
				b = b[:rng.Intn(len(b))]
			}
		} else {
			b = make([]byte, rng.Intn(300))
			rng.Read(b)
		}
		_, _ = Parse(b)
	}
}

func TestRegion(t *testing.T) {
	for name, want := range map[string]string{
		"🇭🇰 Hong Kong 01": "HK", "香港 IPLC": "HK", "HK-02": "HK", "hk02": "HK",
		"Tokyo A": "JP", "日本 东京": "JP", "JP_1": "JP", "🇺🇸 US Los Angeles": "US", "美国 洛杉矶 02": "US",
		"Singapore 3": "SG", "狮城 01": "SG", "TW 台北": "TW", "KR Seoul": "KR", "UK London": "GB",
		"Frankfurt": "DE", "unknown place": "", "": "", "busy server": "", "Plus 1": "",
	} {
		if got := Region(name); got != want {
			t.Errorf("Region(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestUserinfo(t *testing.T) {
	u, ok := ParseUserinfo("upload=455727941; download=6174315083; total=1073741824000; expire=2218532293")
	if !ok || u.Upload != 455727941 || u.Download != 6174315083 || u.Total != 1073741824000 {
		t.Fatalf("u = %+v", u)
	}
	if want := time.Unix(2218532293, 0).UTC(); !u.Expire.Equal(want) {
		t.Fatalf("expire = %v", u.Expire)
	}
	rem, ok := u.Remaining()
	if !ok || rem != u.Total-u.Used() {
		t.Fatalf("remaining = %d %v", rem, ok)
	}
	if _, ok := ParseUserinfo("nonsense"); ok {
		t.Fatal("garbage must not parse")
	}
	u, _ = ParseUserinfo("upload=1;download=1;total=0;expire=0")
	if _, ok := u.Remaining(); ok || !u.Expire.IsZero() {
		t.Fatalf("no quota: %+v", u)
	}
	over := Usage{Upload: 5, Download: 5, Total: 8}
	if r, ok := over.Remaining(); !ok || r != 0 {
		t.Fatalf("over quota remaining = %d", r)
	}
}
