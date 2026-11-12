package subscription

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func b64(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func TestShadowTLS(t *testing.T) {
	n := mustLink(t, "ss://"+b64("2022-blake3-aes-128-gcm:cHc=")+"@st.example.com:443/?plugin=shadow-tls%3Bhost%3Dwww.example.org%3Bpassword%3Dstpw%3Bversion%3D3%3Bfp%3Dchrome#st")
	if n.Type != "shadowsocks" || n.Server != "st.example.com" || n.Port != 443 || n.Protocol() != "Shadowsocks + ShadowTLS" {
		t.Fatalf("node = %+v", n)
	}
	d := Node{Config: n.Detour}
	eq(t, d, "type", "shadowtls")
	eq(t, d, "server", "st.example.com")
	eq(t, d, "server_port", 443)
	eq(t, d, "version", 3)
	eq(t, d, "password", "stpw")
	eq(t, d, "tls.server_name", "www.example.org")
	eq(t, d, "tls.utls.fingerprint", "chrome")
	eq(t, n, "plugin", nil)

	wantCode(t, "ss://"+b64("aes-128-gcm:pw")+"@s.example.com:443/?plugin=shadow-tls%3Bhost%3Dx", SkipInvalid)                              // v2 by default, needs a password
	wantCode(t, "ss://"+b64("aes-128-gcm:pw")+"@s.example.com:443/?plugin=shadow-tls%3Bpassword%3Dx%3Bversion%3D4", SkipUnsupportedFeature) // no such version
	n = mustLink(t, "ss://"+b64("aes-128-gcm:pw")+"@s.example.com:443/?plugin=shadow-tls%3Bversion%3D1")
	eq(t, Node{Config: n.Detour}, "password", nil)
	eq(t, Node{Config: n.Detour}, "tls.server_name", "bing.com")

	res, err := Parse([]byte(`proxies:
  - name: clash-st
    type: ss
    server: c.example.com
    port: 8443
    cipher: aes-128-gcm
    password: pw
    client-fingerprint: firefox
    plugin: shadow-tls
    plugin-opts: {host: cloud.example.org, password: stpw, version: 3}
  - name: pinned
    type: ss
    server: p.example.com
    port: 8443
    cipher: aes-128-gcm
    password: pw
    plugin: shadow-tls
    plugin-opts: {host: x, password: stpw, fingerprint: "ab:cd"}
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Nodes) != 1 || len(res.Skipped) != 1 || res.Skipped[0].Code != SkipUnsupportedFeature {
		t.Fatalf("nodes %v, skipped %+v", names(res.Nodes), res.Skipped)
	}
	d = Node{Config: res.Nodes[0].Detour}
	eq(t, d, "server", "c.example.com")
	eq(t, d, "server_port", 8443)
	eq(t, d, "tls.server_name", "cloud.example.org")
	eq(t, d, "tls.utls.fingerprint", "firefox")
	eq(t, d, "version", 3)

	res, err = Parse([]byte(`{"outbounds":[
	  {"type":"shadowsocks","tag":"sb-st","method":"aes-128-gcm","password":"pw","detour":"st-out"},
	  {"type":"shadowtls","tag":"st-out","server":"s.example.com","server_port":443,"version":3,"password":"stpw","tls":{"enabled":true,"server_name":"www.example.org"}},
	  {"type":"shadowtls","tag":"lonely","server":"l.example.com","server_port":443,"version":3,"password":"x","tls":{"enabled":true}},
	  {"type":"shadowsocks","tag":"chained","server":"c.example.com","server_port":1,"method":"aes-128-gcm","password":"pw","detour":"sb-st"}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Nodes) != 1 || res.Nodes[0].Name != "sb-st" || res.Nodes[0].Server != "s.example.com" || res.Nodes[0].Port != 443 {
		t.Fatalf("nodes %+v", res.Nodes)
	}
	eq(t, res.Nodes[0], "server", "s.example.com")
	if _, ok := res.Nodes[0].Config["detour"]; ok {
		t.Fatal("the detour tag must not stay in the node")
	}
	if _, ok := res.Nodes[0].Detour["tag"]; ok {
		t.Fatal("the detour keeps no tag")
	}
	skipped := map[string]string{}
	for _, s := range res.Skipped {
		skipped[s.Name] = s.Code
	}
	if len(skipped) != 2 || skipped["lonely"] != SkipUnsupportedProtocol || skipped["chained"] != SkipUnsupportedFeature {
		t.Fatalf("skipped %+v", res.Skipped)
	}

	// The detour is part of what the node is, and survives a round trip.
	a, b := res.Nodes[0], res.Nodes[0].Clone()
	b.Detour["password"] = "other"
	if a.Key() == b.Key() {
		t.Fatal("the key must cover the detour")
	}
	raw, _ := json.Marshal(a)
	var back Node
	if err := json.Unmarshal(raw, &back); err != nil || back.Validate() != nil || back.Protocol() != a.Protocol() {
		t.Fatalf("round trip: %v %+v", err, back)
	}
}

// TestKeysOfPlainNodesDidNotChange guards the selections people already made:
// a node without a detour keeps the key it had before detours existed.
func TestKeysOfPlainNodesDidNotChange(t *testing.T) {
	n := mustLink(t, "trojan://pw@t.example.com:443#x")
	raw, _ := json.Marshal(map[string]any{"type": n.Type, "endpoint": n.Endpoint, "config": n.Config})
	sum := sha256.Sum256(raw)
	if n.Key() != hex.EncodeToString(sum[:8]) {
		t.Fatal("the key of a plain node changed")
	}
}

func TestValidateDetour(t *testing.T) {
	good := mustLink(t, "ss://"+b64("aes-128-gcm:pw")+"@s.example.com:443/?plugin=shadow-tls%3Bpassword%3Dx%3Bversion%3D3")
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*Node){
		"not shadowtls":   func(n *Node) { n.Detour["type"] = "socks" },
		"not ss":          func(n *Node) { n.Type, n.Config["type"], n.Config["password"] = "trojan", "trojan", "pw" },
		"chained further": func(n *Node) { n.Detour["detour"] = "x" },
		"no server":       func(n *Node) { n.Detour["server"] = "" },
		"bad port":        func(n *Node) { n.Detour["server_port"] = 0 },
		"no password":     func(n *Node) { delete(n.Detour, "password") },
		"bad version":     func(n *Node) { n.Detour["version"] = 9 },
		"no tls":          func(n *Node) { delete(n.Detour, "tls") },
	} {
		n := good.Clone()
		mut(&n)
		if err := n.Validate(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func testKeys(t *testing.T) (private, host string) {
	t.Helper()
	block, err := ssh.MarshalPrivateKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{4}, 32)), "")
	if err != nil {
		t.Fatal(err)
	}
	pub, err := ssh.NewPublicKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{5}, 32)).Public())
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(block)), strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
}

func TestSSH(t *testing.T) {
	key, host := testKeys(t)
	n := mustLink(t, "ssh://me:p%40ss@ssh.example.com:2222#box")
	if n.Type != "ssh" || n.Name != "box" || n.Port != 2222 || n.Protocol() != "SSH" {
		t.Fatalf("node = %+v", n)
	}
	eq(t, n, "user", "me")
	eq(t, n, "password", "p@ss")

	n = mustLink(t, "ssh://me@ssh.example.com?private-key="+url.QueryEscape(key)+"&host-key="+url.QueryEscape(host))
	if n.Port != 22 {
		t.Fatalf("default port = %d", n.Port)
	}
	eq(t, n, "private_key", strings.TrimSpace(key))
	eq(t, n, "host_key", []any{host})

	wantCode(t, "ssh://me@ssh.example.com:22", SkipInvalid)                                            // no credentials
	wantCode(t, "ssh://me@ssh.example.com:22?private-key=%2Froot%2F.ssh%2Fid", SkipUnsupportedFeature) // a file path
	wantCode(t, "ssh://me@ssh.example.com:22?private-key="+url.QueryEscape("-----BEGIN OPENSSH PRIVATE KEY-----\nxx\n-----END OPENSSH PRIVATE KEY-----"), SkipInvalid)
	wantCode(t, "ssh://me:pw@ssh.example.com:22?host-key=garbage", SkipInvalid)
	wantCode(t, "ssh://me:pw@ssh.example.com:99999", SkipInvalid)

	res, err := Parse([]byte(`proxies:
  - {name: clash-ssh, type: ssh, server: s.example.com, port: 22, username: root, password: pw, host-key: ["` + host + `"], host-key-algorithms: [ssh-ed25519]}
  - {name: key-file, type: ssh, server: s.example.com, port: 22, username: root, private-key: /home/me/.ssh/id_ed25519}
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Nodes) != 1 || len(res.Skipped) != 1 || res.Skipped[0].Code != SkipUnsupportedFeature {
		t.Fatalf("nodes %v, skipped %+v", names(res.Nodes), res.Skipped)
	}
	eq(t, res.Nodes[0], "user", "root")
	eq(t, res.Nodes[0], "host_key_algorithms", []any{"ssh-ed25519"})

	res, err = Parse([]byte(`{"outbounds":[
	  {"type":"ssh","tag":"sb-ssh","server":"s.example.com","server_port":22,"user":"root","password":"pw"},
	  {"type":"ssh","tag":"sb-keyfile","server":"s.example.com","server_port":22,"private_key_path":"/root/.ssh/id"}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Nodes) != 1 || res.Nodes[0].Name != "sb-ssh" || len(res.Skipped) != 1 || res.Skipped[0].Name != "sb-keyfile" {
		t.Fatalf("nodes %v, skipped %+v", names(res.Nodes), res.Skipped)
	}
}

func TestHTTPLinks(t *testing.T) {
	n := mustLink(t, "http://u:p%40ss@proxy.example.com:8080#office")
	if n.Type != "http" || n.Name != "office" || n.Port != 8080 {
		t.Fatalf("node = %+v", n)
	}
	eq(t, n, "username", "u")
	eq(t, n, "password", "p@ss")
	eq(t, n, "tls", nil)

	n = mustLink(t, "https://"+b64("u:pw")+"@secure.example.com:443/?sni=front.example.com")
	eq(t, n, "username", "u")
	eq(t, n, "password", "pw")
	eq(t, n, "tls.server_name", "front.example.com")

	n = mustLink(t, "HTTP://proxy.example.com:3128")
	eq(t, n, "username", nil)

	// Web addresses in a plain-text subscription are not servers.
	for _, l := range []string{
		"https://t.me/some_channel",
		"https://example.com",
		"https://sub.example.com:8443/api/v1/client/subscribe?token=x",
		"https://sub.example.com:8443?token=x",
	} {
		wantCode(t, l, SkipUnsupportedProtocol)
	}
	res, err := Parse([]byte("trojan://pw@t.example.com:443#a\nhttps://t.me/channel\n"))
	if err != nil || len(res.Nodes) != 1 {
		t.Fatalf("nodes %v, err %v", names(res.Nodes), err)
	}
}

func TestClashWireGuardPeers(t *testing.T) {
	res, err := Parse([]byte(`proxies:
  - name: wg-multi
    type: wireguard
    ip: 172.16.0.2
    private-key: cHJpdmF0ZQ==
    peers:
      - {server: a.example.com, port: 2480, public-key: cHViYQ==, allowed-ips: ["0.0.0.0/0"], reserved: [1, 2, 3]}
      - {server: b.example.com, port: 2481, public-key: cHViYg==, pre-shared-key: cHNr}
  - name: wg-broken
    type: wireguard
    ip: 172.16.0.2
    private-key: cHJpdmF0ZQ==
    peers:
      - {server: a.example.com, public-key: cHViYQ==}
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Nodes) != 1 || len(res.Skipped) != 1 || res.Skipped[0].Code != SkipInvalid {
		t.Fatalf("nodes %v, skipped %+v", names(res.Nodes), res.Skipped)
	}
	n := res.Nodes[0]
	if n.Server != "a.example.com" || n.Port != 2480 || !n.Endpoint {
		t.Fatalf("node = %+v", n)
	}
	peers := n.Config["peers"].([]any)
	if len(peers) != 2 {
		t.Fatalf("peers = %v", peers)
	}
	eq(t, Node{Config: peers[0].(map[string]any)}, "allowed_ips", []any{"0.0.0.0/0"})
	eq(t, Node{Config: peers[0].(map[string]any)}, "reserved", []any{1, 2, 3})
	eq(t, Node{Config: peers[1].(map[string]any)}, "address", "b.example.com")
	eq(t, Node{Config: peers[1].(map[string]any)}, "pre_shared_key", "cHNr")
	eq(t, Node{Config: peers[1].(map[string]any)}, "allowed_ips", []any{"0.0.0.0/0", "::/0"})
}

func TestReviewRegressions(t *testing.T) {
	// base64 credentials holding a '/' are credentials, not a path.
	n := mustLink(t, "http://"+base64.StdEncoding.EncodeToString([]byte("user:pa?/ss"))+"@proxy.example.com:8080")
	eq(t, n, "username", "user")
	eq(t, n, "password", "pa?/ss")
	n = mustLink(t, "socks://"+base64.StdEncoding.EncodeToString([]byte("user:pa?/ss"))+"@proxy.example.com:1080")
	eq(t, n, "password", "pa?/ss")

	// WireGuard allowed IPs: a bare address is a host route; garbage is refused.
	res, err := Parse([]byte(`proxies:
  - {name: a, type: wireguard, ip: 172.16.0.2, private-key: cHJpdmF0ZQ==, peers: [{server: a.example.com, port: 1, public-key: cHVi, allowed-ips: ["10.0.0.2", "fd00::1", "0.0.0.0/0"]}]}
  - {name: b, type: wireguard, ip: 172.16.0.2, private-key: cHJpdmF0ZQ==, peers: [{server: b.example.com, port: 1, public-key: cHVi, allowed-ips: ["nope"]}]}
`))
	if err != nil || len(res.Nodes) != 1 || len(res.Skipped) != 1 {
		t.Fatalf("nodes %v skipped %+v err %v", names(res.Nodes), res.Skipped, err)
	}
	eq(t, Node{Config: res.Nodes[0].Config["peers"].([]any)[0].(map[string]any)}, "allowed_ips", []any{"10.0.0.2/32", "fd00::1/128", "0.0.0.0/0"})

	// What the core would refuse on start is refused at import, whatever the format.
	key, host := testKeys(t)
	keyJSON, _ := json.Marshal(key)
	for name, ob := range map[string]string{
		"snell obfs":         `{"type":"snell","tag":"x","server":"a","server_port":1,"version":4,"psk":"pw","obfs_mode":"foo"}`,
		"snell v6 mode":      `{"type":"snell","tag":"x","server":"a","server_port":1,"version":6,"psk":"test-psk-0001","mode":"foo"}`,
		"snell v6 obfs":      `{"type":"snell","tag":"x","server":"a","server_port":1,"version":6,"psk":"test-psk-0001","obfs_mode":"http"}`,
		"snell v4 mode":      `{"type":"snell","tag":"x","server":"a","server_port":1,"version":4,"psk":"pw","mode":"default"}`,
		"snell string ver":   `{"type":"snell","tag":"x","server":"a","server_port":1,"version":"4","psk":"pw"}`,
		"ssh bad key":        `{"type":"ssh","tag":"x","server":"a","server_port":22,"private_key":"k"}`,
		"ssh empty key":      `{"type":"ssh","tag":"x","server":"a","server_port":22,"password":"pw","private_key":""}`,
		"ssh bad host key":   `{"type":"ssh","tag":"x","server":"a","server_port":22,"password":"pw","host_key":["nope"]}`,
		"wg bad allowed ips": `{"type":"wireguard","tag":"x","private_key":"k","peers":[{"address":"a","port":1,"public_key":"p","allowed_ips":["10.0.0.2"]}]}`,
		"shadowtls fraction": `{"type":"shadowsocks","tag":"x","method":"aes-128-gcm","password":"pw","detour":"st"},{"type":"shadowtls","tag":"st","server":"a","server_port":1,"version":2.5,"password":"p","tls":{"enabled":true}}`,
		"shadowtls str port": `{"type":"shadowsocks","tag":"x","method":"aes-128-gcm","password":"pw","detour":"st"},{"type":"shadowtls","tag":"st","server":"a","server_port":"1","version":3,"password":"p","tls":{"enabled":true}}`,
	} {
		section := "outbounds"
		if strings.Contains(name, "wg") {
			section = "endpoints"
		}
		res, err := Parse([]byte(`{"` + section + `":[` + ob + `]}`))
		if err == nil || len(res.Nodes) != 0 {
			t.Errorf("%s: imported %+v", name, res.Nodes)
		}
	}
	for name, ob := range map[string]string{
		"ssh key":      `{"type":"ssh","tag":"x","server":"a","server_port":22,"private_key":` + string(keyJSON) + `,"host_key":["` + host + `"]}`,
		"ssh key list": `{"type":"ssh","tag":"x","server":"a","server_port":22,"private_key":` + string(mustJSON(strings.Split(strings.TrimSpace(key), "\n"))) + `}`,
		"snell v4":     `{"type":"snell","tag":"x","server":"a","server_port":1,"version":4,"psk":"pw","obfs_mode":"tls","obfs_host":"h"}`,
	} {
		if res, err := Parse([]byte(`{"outbounds":[` + ob + `]}`)); err != nil || len(res.Nodes) != 1 {
			t.Errorf("%s: %v %+v", name, err, res)
		}
	}
}

func mustJSON(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}
