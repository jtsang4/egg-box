package subscription

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

const (
	testUUID = "00000000-0000-4000-8000-000000000001"
	testKey  = "Zm9yLXRlc3RzLW9ubHktbm90LWEtcmVhbC1rZXktMDAwMDA"
)

// get walks a dotted path through a node's config.
func get(n Node, path string) any {
	var cur any = n.Config
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[part]
	}
	return cur
}

func mustLink(t *testing.T, link string) Node {
	t.Helper()
	n, err := ParseLink(link)
	if err != nil {
		t.Fatalf("ParseLink(%q): %v", link, err)
	}
	return n
}

func wantCode(t *testing.T, link, code string) {
	t.Helper()
	_, err := ParseLink(link)
	var se *skipError
	if !errors.As(err, &se) || se.code != code {
		t.Fatalf("ParseLink(%q) error = %v, want code %q", link, err, code)
	}
}

func eq(t *testing.T, n Node, path string, want any) {
	t.Helper()
	got := get(n, path)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %#v, want %#v (config %v)", path, got, want, n.Config)
	}
}

func TestVLESSReality(t *testing.T) {
	n := mustLink(t, "vless://"+testUUID+"@node1.example.com:443?encryption=none&flow=xtls-rprx-vision&security=reality&sni=www.example.org&fp=chrome&pbk="+testKey+"&sid=ab12&type=tcp#Node%201%20%F0%9F%87%AD%F0%9F%87%B0")
	if n.Name != "Node 1 🇭🇰" || n.Server != "node1.example.com" || n.Port != 443 || n.Type != "vless" {
		t.Fatalf("node = %+v", n)
	}
	eq(t, n, "uuid", testUUID)
	eq(t, n, "flow", "xtls-rprx-vision")
	eq(t, n, "tls.enabled", true)
	eq(t, n, "tls.server_name", "www.example.org")
	eq(t, n, "tls.reality.public_key", testKey)
	eq(t, n, "tls.reality.short_id", "ab12")
	eq(t, n, "tls.utls.fingerprint", "chrome")
	if n.Protocol() != "VLESS Reality" || n.Transport() != "TCP" || Region(n.Name) != "HK" {
		t.Fatalf("labels: %s %s %s", n.Protocol(), n.Transport(), Region(n.Name))
	}
}

func TestVLESSRealityDefaultsToUTLS(t *testing.T) {
	n := mustLink(t, "vless://"+testUUID+"@h.example.com:443?security=reality&sni=a.example&pbk="+testKey+"&sid=1#x")
	eq(t, n, "tls.utls.fingerprint", "chrome")
}

func TestVLESSTransports(t *testing.T) {
	ws := mustLink(t, "vless://"+testUUID+"@h.example.com:443?security=tls&type=ws&host=cdn.example.net&path=%2Fws%3Fed%3D2048&alpn=h2,http/1.1&allowInsecure=1#ws")
	eq(t, ws, "transport.type", "ws")
	eq(t, ws, "transport.path", "/ws")
	eq(t, ws, "transport.max_early_data", 2048)
	eq(t, ws, "transport.headers.Host", "cdn.example.net")
	eq(t, ws, "tls.server_name", "cdn.example.net") // falls back to the Host header
	eq(t, ws, "tls.insecure", true)
	eq(t, ws, "tls.alpn", []any{"h2", "http/1.1"})

	grpc := mustLink(t, "vless://"+testUUID+"@h.example.com:443?security=tls&type=grpc&serviceName=svc&sni=s.example#g")
	eq(t, grpc, "transport.type", "grpc")
	eq(t, grpc, "transport.service_name", "svc")
	if grpc.Transport() != "gRPC" {
		t.Fatal(grpc.Transport())
	}

	h2 := mustLink(t, "vless://"+testUUID+"@h.example.com:443?security=tls&type=h2&host=a.example,b.example&path=/p#h")
	eq(t, h2, "transport.type", "http")
	eq(t, h2, "transport.host", []any{"a.example", "b.example"})

	up := mustLink(t, "vless://"+testUUID+"@h.example.com:80?type=httpupgrade&host=a.example&path=/u#u")
	eq(t, up, "transport.type", "httpupgrade")
	if get(up, "tls") != nil {
		t.Fatal("plain link must not enable TLS")
	}
}

func TestVLESSUnsupported(t *testing.T) {
	wantCode(t, "vless://"+testUUID+"@h.example.com:443?security=tls&type=xhttp#x", SkipUnsupportedTransport)
	wantCode(t, "vless://"+testUUID+"@h.example.com:443?security=tls&type=kcp#x", SkipUnsupportedTransport)
	wantCode(t, "vless://"+testUUID+"@h.example.com:443?security=tls&type=tcp&headerType=http#x", SkipUnsupportedTransport)
	wantCode(t, "vless://"+testUUID+"@h.example.com:443?encryption=mlkem768x25519plus.native.0rtt.abc#x", SkipUnsupportedFeature)
	wantCode(t, "vless://"+testUUID+"@h.example.com:443?security=tls&flow=xtls-rprx-direct#x", SkipUnsupportedFeature)
	wantCode(t, "vless://"+testUUID+"@h.example.com:443?flow=xtls-rprx-vision#x", SkipInvalid) // flow without TLS
	wantCode(t, "vless://"+testUUID+"@h.example.com:443?security=reality#x", SkipInvalid)      // no public key
	wantCode(t, "vless://"+testUUID+"@:443#x", SkipInvalid)
	wantCode(t, "vless://"+testUUID+"@h.example.com#x", SkipInvalid)
	wantCode(t, "vless://@h.example.com:443#x", SkipInvalid) // no uuid
}

func vmessJSON(t *testing.T, m map[string]any) string {
	t.Helper()
	raw, _ := json.Marshal(m)
	return "vmess://" + base64.StdEncoding.EncodeToString(raw)
}

func TestVMess(t *testing.T) {
	n := mustLink(t, vmessJSON(t, map[string]any{
		"v": "2", "ps": "JP 01", "add": "v.example.com", "port": "443", "id": testUUID, "aid": "0",
		"scy": "auto", "net": "ws", "host": "cdn.example.net", "path": "/vm", "tls": "tls", "sni": "",
	}))
	if n.Name != "JP 01" || n.Server != "v.example.com" || n.Port != 443 {
		t.Fatalf("node = %+v", n)
	}
	eq(t, n, "uuid", testUUID)
	eq(t, n, "security", "auto")
	eq(t, n, "transport.type", "ws")
	eq(t, n, "tls.server_name", "cdn.example.net")
	if get(n, "alter_id") != nil {
		t.Fatal("alter_id 0 must be omitted")
	}
	if Region(n.Name) != "JP" {
		t.Fatal("region")
	}

	// Numbers instead of strings, alterId, a legacy cipher spelling.
	n = mustLink(t, vmessJSON(t, map[string]any{"ps": "x", "add": "v.example.com", "port": 8443, "id": testUUID, "aid": 4, "scy": "aes-128-gcm", "net": "tcp", "type": "none"}))
	eq(t, n, "alter_id", 4)
	eq(t, n, "security", "aes-128-gcm")
	if n.Port != 8443 {
		t.Fatal(n.Port)
	}

	// grpc keeps the service name in "path".
	n = mustLink(t, vmessJSON(t, map[string]any{"ps": "g", "add": "v.example.com", "port": "443", "id": testUUID, "net": "grpc", "path": "svc", "tls": "tls"}))
	eq(t, n, "transport.service_name", "svc")

	wantCode(t, vmessJSON(t, map[string]any{"ps": "k", "add": "v.example.com", "port": "443", "id": testUUID, "net": "kcp"}), SkipUnsupportedTransport)
	wantCode(t, vmessJSON(t, map[string]any{"ps": "k", "add": "v.example.com", "port": "443", "id": testUUID, "scy": "aes-128-cfb"}), SkipUnsupportedFeature)
	wantCode(t, vmessJSON(t, map[string]any{"ps": "k", "add": "", "port": "443", "id": testUUID}), SkipInvalid)
	wantCode(t, "vmess://not-base64-and-not-a-url", SkipInvalid)
}

func TestVMessStandardAndQuantumultForms(t *testing.T) {
	n := mustLink(t, "vmess://"+testUUID+"@v.example.com:443?encryption=auto&security=tls&type=ws&host=cdn.example.net&path=/p#std")
	eq(t, n, "transport.type", "ws")
	eq(t, n, "tls.enabled", true)

	body := base64.StdEncoding.EncodeToString([]byte("auto:" + testUUID + "@q.example.com:443"))
	n = mustLink(t, "vmess://"+body+"?remarks=Quan%20Tum&obfs=websocket&obfsParam=cdn.example.net&path=/q&tls=1&alterId=0")
	if n.Name != "Quan Tum" || n.Server != "q.example.com" {
		t.Fatalf("node = %+v", n)
	}
	eq(t, n, "transport.type", "ws")
	eq(t, n, "tls.enabled", true)
}

func TestTrojan(t *testing.T) {
	n := mustLink(t, "trojan://p%40ss:word@t.example.com:443?sni=t.example.com&alpn=h2#T%201")
	eq(t, n, "password", "p@ss:word")
	eq(t, n, "tls.enabled", true)
	eq(t, n, "tls.server_name", "t.example.com")

	ws := mustLink(t, "trojan://pw@t.example.com:443?security=tls&type=ws&host=cdn.example.net&path=/t&sni=cdn.example.net#ws")
	eq(t, ws, "transport.type", "ws")

	plain := mustLink(t, "trojan://pw@t.example.com:80?security=none&type=ws&host=cdn.example.net&path=/t#plain")
	if get(plain, "tls") != nil {
		t.Fatal("security=none must not enable TLS")
	}

	// A raw '%' in the password must not make the link unreadable.
	n = mustLink(t, "trojan://50%off@t.example.com:443#raw")
	eq(t, n, "password", "50%off")
}

func TestShadowsocksForms(t *testing.T) {
	b64 := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

	// SIP002, base64url userinfo.
	n := mustLink(t, "ss://"+b64("aes-256-gcm:pa55")+"@s.example.com:8388#S%201")
	eq(t, n, "method", "aes-256-gcm")
	eq(t, n, "password", "pa55")
	if n.Name != "S 1" || n.Port != 8388 {
		t.Fatalf("node = %+v", n)
	}

	// SIP002 with plain, percent-encoded userinfo (2022 ciphers).
	n = mustLink(t, "ss://2022-blake3-aes-256-gcm:YctPZ6U7xPPcU%2Bgp3u%2B0tx%2FtRizJN9K8y%2BuKlW2qjlI%3D@192.0.2.1:8888#plain")
	eq(t, n, "method", "2022-blake3-aes-256-gcm")
	eq(t, n, "password", "YctPZ6U7xPPcU+gp3u+0tx/tRizJN9K8y+uKlW2qjlI=")

	// Legacy: everything in one base64 blob.
	n = mustLink(t, "ss://"+base64.StdEncoding.EncodeToString([]byte("chacha20-ietf-poly1305:pw@old.example.com:443"))+"#legacy")
	eq(t, n, "method", "chacha20-ietf-poly1305")
	if n.Server != "old.example.com" || n.Name != "legacy" {
		t.Fatalf("node = %+v", n)
	}

	// IPv6 server and an alias cipher name.
	n = mustLink(t, "ss://"+b64("chacha20-poly1305:pw")+"@[2001:db8::1]:443#v6")
	if n.Server != "2001:db8::1" {
		t.Fatalf("server = %q", n.Server)
	}
	eq(t, n, "method", "chacha20-ietf-poly1305")

	// Plugins.
	n = mustLink(t, "ss://"+b64("aes-128-gcm:pw")+"@s.example.com:443/?plugin=obfs-local%3Bobfs%3Dhttp%3Bobfs-host%3Dwww.example.org#obfs")
	eq(t, n, "plugin", "obfs-local")
	eq(t, n, "plugin_opts", "obfs=http;obfs-host=www.example.org")
	n = mustLink(t, "ss://"+b64("aes-128-gcm:pw")+"@s.example.com:443/?plugin=simple-obfs;obfs=tls;obfs-host=x.example#unescaped")
	eq(t, n, "plugin", "obfs-local")
	eq(t, n, "plugin_opts", "obfs=tls;obfs-host=x.example")
	wantCode(t, "ss://"+b64("aes-128-gcm:pw")+"@s.example.com:443/?plugin=shadow-tls%3Bhost%3Dx#st", SkipUnsupportedFeature)

	// Unsupported cipher and broken links.
	_, err := ParseLink("ss://" + b64("rc5-cfb:pw") + "@s.example.com:443#bad")
	if err == nil {
		t.Fatal("unknown cipher must be rejected")
	}
	wantCode(t, "ss://%%%", SkipInvalid)
}

func TestHysteria2(t *testing.T) {
	n := mustLink(t, "hysteria2://secret@h.example.com:443?sni=h.example.com&insecure=1&obfs=salamander&obfs-password=ob&up=50%20mbps&down=100mbps&alpn=h3#H2")
	eq(t, n, "password", "secret")
	eq(t, n, "server_port", 443)
	eq(t, n, "obfs.type", "salamander")
	eq(t, n, "obfs.password", "ob")
	eq(t, n, "up_mbps", 50)
	eq(t, n, "down_mbps", 100)
	eq(t, n, "tls.insecure", true)

	// Port hopping, in the authority and as a parameter.
	n = mustLink(t, "hy2://pw@h.example.com:443,20000-30000#hop")
	eq(t, n, "server_ports", []any{"443:443", "20000:30000"})
	if n.Port != 443 {
		t.Fatalf("port = %d", n.Port)
	}
	n = mustLink(t, "hysteria2://pw@h.example.com:443?mport=40000-41000#hop2")
	eq(t, n, "server_ports", []any{"40000:41000"})

	// user:pass authentication is one password string.
	n = mustLink(t, "hysteria2://user:pass@h.example.com:443#x")
	eq(t, n, "password", "user:pass")

	wantCode(t, "hysteria2://pw@h.example.com:443?obfs=other&obfs-password=x#x", SkipUnsupportedFeature)
	wantCode(t, "hysteria2://pw@h.example.com:443?pinSHA256=AA:BB#x", SkipUnsupportedFeature)
	// With insecure set a pin adds nothing the core could verify, so the node still works.
	mustLink(t, "hysteria2://pw@h.example.com:443?pinSHA256=AA:BB&insecure=1#x")
}

func TestHysteriaTUICAnyTLS(t *testing.T) {
	n := mustLink(t, "hysteria://h.example.com:443?protocol=udp&auth=tok&peer=h.example.com&insecure=1&upmbps=20&downmbps=200&alpn=h3&obfs=xplus&obfsParam=op#H1")
	eq(t, n, "auth_str", "tok")
	eq(t, n, "up_mbps", 20)
	eq(t, n, "obfs", "op")
	wantCode(t, "hysteria://h.example.com:443?protocol=faketcp&auth=t#x", SkipUnsupportedFeature)

	n = mustLink(t, "tuic://"+testUUID+":pw@u.example.com:443?congestion_control=bbr&udp_relay_mode=native&alpn=h3&sni=u.example.com&allow_insecure=1&disable_sni=1#T")
	eq(t, n, "uuid", testUUID)
	eq(t, n, "password", "pw")
	eq(t, n, "congestion_control", "bbr")
	eq(t, n, "udp_relay_mode", "native")
	eq(t, n, "tls.disable_sni", true)
	wantCode(t, "tuic://"+testUUID+":pw@u.example.com:443?congestion_control=weird#x", SkipUnsupportedFeature)

	n = mustLink(t, "anytls://pw@a.example.com:443?sni=a.example.com&insecure=1&fp=firefox#A")
	eq(t, n, "password", "pw")
	eq(t, n, "tls.utls.fingerprint", "firefox")
}

func TestSOCKSAndWireGuard(t *testing.T) {
	n := mustLink(t, "socks5://user:pw@s.example.com:1080#S5")
	eq(t, n, "username", "user")
	eq(t, n, "password", "pw")
	eq(t, n, "version", "5")

	n = mustLink(t, "socks://"+base64.RawURLEncoding.EncodeToString([]byte("user:pw"))+"@s.example.com:1080#S")
	eq(t, n, "username", "user")
	eq(t, n, "password", "pw")

	n = mustLink(t, "wireguard://"+testKey+"@w.example.com:51820?publickey="+testKey+"&address=10.0.0.2/32,fd00::2/128&reserved=1,2,3&mtu=1280#WG")
	if !n.Endpoint || n.Type != "wireguard" || n.Server != "w.example.com" || n.Port != 51820 {
		t.Fatalf("node = %+v", n)
	}
	eq(t, n, "mtu", 1280)
	eq(t, n, "address", []any{"10.0.0.2/32", "fd00::2/128"})
	peer := get(n, "peers").([]any)[0].(map[string]any)
	if peer["address"] != "w.example.com" || !reflect.DeepEqual(peer["reserved"], []any{1, 2, 3}) {
		t.Fatalf("peer = %v", peer)
	}
}

func TestUnsupportedAndBrokenLinks(t *testing.T) {
	for _, l := range []string{"ssr://abc", "juicity://x@y:1", "http://example.com/x", "mieru://x"} {
		wantCode(t, l, SkipUnsupportedProtocol)
	}
	wantCode(t, "no scheme here", SkipInvalid)
	wantCode(t, "trojan://", SkipInvalid)
	wantCode(t, "tuic://"+testUUID+"@u.example.com:99999#x", SkipInvalid)
}

func TestKeyIgnoresTheName(t *testing.T) {
	a := mustLink(t, "trojan://pw@t.example.com:443#One")
	b := mustLink(t, "trojan://pw@t.example.com:443#Two")
	c := mustLink(t, "trojan://other@t.example.com:443#One")
	if a.Key() != b.Key() {
		t.Fatal("same server must have the same key")
	}
	if a.Key() == c.Key() {
		t.Fatal("different credentials must change the key")
	}
	// A node loaded back from JSON (numbers become float64) keeps its key.
	raw, _ := json.Marshal(a)
	var back Node
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Key() != a.Key() {
		t.Fatalf("key changed through JSON: %s != %s", back.Key(), a.Key())
	}
}

func TestDedupeNamesAndDuplicates(t *testing.T) {
	var nodes []Node
	for _, l := range []string{
		"trojan://pw@a.example.com:443#Same",
		"trojan://pw@b.example.com:443#Same",
		"trojan://pw@a.example.com:443#Duplicate of the first",
		"trojan://pw@c.example.com:443#",
		"trojan://pw@d.example.com:443#same",
	} {
		nodes = append(nodes, mustLink(t, l))
	}
	kept, dropped := Dedupe(nodes)
	if dropped != 1 || len(kept) != 4 {
		t.Fatalf("kept %d dropped %d", len(kept), dropped)
	}
	seen := map[string]bool{}
	for _, n := range kept {
		if n.Name == "" || seen[strings.ToLower(n.Name)] {
			t.Fatalf("names not unique: %q in %v", n.Name, kept)
		}
		seen[strings.ToLower(n.Name)] = true
	}
	if kept[0].Name != "Same" || kept[1].Name != "Same 2" || !strings.Contains(kept[2].Name, "c.example.com") || kept[3].Name != "same 3" {
		var names []string
		for _, n := range kept {
			names = append(names, n.Name)
		}
		t.Fatalf("names = %q", names)
	}
}

func TestValidate(t *testing.T) {
	good := mustLink(t, "trojan://pw@t.example.com:443#x")
	for name, mut := range map[string]func(*Node){
		"empty server": func(n *Node) { n.Server = " " },
		"port zero":    func(n *Node) { n.Port = 0 },
		"no password":  func(n *Node) { n.Config["password"] = "" },
		"bad type":     func(n *Node) { n.Type = "ssh" },
	} {
		n := good.Clone()
		mut(&n)
		if err := n.Validate(); err == nil {
			t.Fatalf("%s: expected an error", name)
		}
	}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	// The cloned map must not alias the original.
	c := good.Clone()
	c.Config["password"] = "changed"
	if good.Config["password"] != "pw" {
		t.Fatal("Clone shares the config map")
	}
}

func TestLinksNeverLeakIntoErrors(t *testing.T) {
	secret := "SECRET-CREDENTIAL-VALUE"
	for _, l := range []string{
		"vless://" + secret + "@:443?x=1",
		"trojan://" + secret + "@h.example.com:70000",
		"ss://" + secret,
		"vmess://" + secret,
		"tuic://" + secret + "@h.example.com:0",
	} {
		_, err := ParseLink(l)
		if err == nil {
			t.Fatalf("%q should fail", l)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaks the credential: %v", err)
		}
	}
	_, skipped := parseLinks("vless://" + secret + "@:443")
	for _, s := range skipped {
		if strings.Contains(fmt.Sprint(s), secret) {
			t.Fatalf("skip record leaks the credential: %+v", s)
		}
	}
}
