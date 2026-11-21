package subscription

import (
	"encoding/json"
	"testing"
)

func TestClashProviderRelays(t *testing.T) {
	profile := `
proxies:
  - {name: "Hong Kong 01", type: ss, server: hk1.example.com, port: 8388, cipher: aes-128-gcm, password: pw}
  - {name: "Hong Kong 02", type: ss, server: hk2.example.com, port: 8388, cipher: aes-128-gcm, password: pw}
  - {name: "Japan 01", type: trojan, server: jp1.example.com, port: 443, password: pw}
  - {name: "US Home", type: socks5, server: us1.example.com, port: 1080, username: u, password: p, dialer-proxy: "Hong Kong 01"}
  - {name: "US Home HK", type: vless, server: us2.example.com, port: 443, uuid: 00000000-0000-4000-8000-000000000001, dialer-proxy: "HK Auto"}
  - {name: "US Home Any", type: trojan, server: us3.example.com, port: 443, password: pw, dialer-proxy: "All Auto"}
  - {name: "US Direct", type: trojan, server: us4.example.com, port: 443, password: pw, dialer-proxy: DIRECT}
  - {name: "Three Hop", type: trojan, server: h3.example.com, port: 443, password: pw, dialer-proxy: "US Home"}
  - {name: "Self Loop", type: trojan, server: loop.example.com, port: 443, password: pw, dialer-proxy: "Self Loop"}
  - {name: "Missing Target", type: ss, server: miss.example.com, port: 8388, cipher: aes-128-gcm, password: pw, dialer-proxy: "No Such Node"}
proxy-groups:
  - {name: "HK Auto", type: url-test, proxies: ["Hong Kong 01", "Hong Kong 02"]}
  - {name: "All Auto", type: url-test, proxies: ["Hong Kong 01", "Japan 01"]}
`
	res, err := Parse([]byte(profile))
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Node{}
	for _, n := range res.Nodes {
		byName[n.Name] = n
	}
	if len(res.Nodes) != 7 {
		t.Fatalf("got %d nodes: %v (skipped: %+v)", len(res.Nodes), names(res.Nodes), res.Skipped)
	}
	if r := byName["US Home"].Relay; r == nil || r.Kind != RelayNode || r.Node != "Hong Kong 01" {
		t.Errorf("US Home relay = %+v", r)
	}
	if r := byName["US Home HK"].Relay; r == nil || r.Kind != RelayRegion || r.Region != "HK" {
		t.Errorf("US Home HK relay = %+v", r)
	}
	if r := byName["US Home Any"].Relay; r == nil || r.Kind != RelayAuto {
		t.Errorf("US Home Any relay = %+v", r)
	}
	if r := byName["US Direct"].Relay; r != nil {
		t.Errorf("US Direct relay = %+v, want nil", r)
	}
	skipped := map[string]string{}
	for _, s := range res.Skipped {
		skipped[s.Name] = s.Code
	}
	for _, name := range []string{"Three Hop", "Self Loop", "Missing Target"} {
		if skipped[name] != SkipUnsupportedFeature {
			t.Errorf("skipped[%q] = %q, want %q (all: %+v)", name, skipped[name], SkipUnsupportedFeature, res.Skipped)
		}
	}
}

func TestSingBoxProviderRelays(t *testing.T) {
	profile := `{
  "outbounds": [
    {"type": "direct", "tag": "dir"},
    {"type": "shadowsocks", "tag": "Hong Kong 01", "server": "hk1.example.com", "server_port": 8388, "method": "aes-128-gcm", "password": "pw"},
    {"type": "shadowsocks", "tag": "Hong Kong 02", "server": "hk2.example.com", "server_port": 8388, "method": "aes-128-gcm", "password": "pw"},
    {"type": "urltest", "tag": "HK", "outbounds": ["Hong Kong 01", "Hong Kong 02"]},
    {"type": "selector", "tag": "Pick", "outbounds": ["Hong Kong 01", "Hong Kong 02"], "default": "Hong Kong 02"},
    {"type": "socks", "tag": "US Socks", "server": "us1.example.com", "server_port": 1080, "detour": "Hong Kong 01"},
    {"type": "trojan", "tag": "US Region", "server": "us2.example.com", "server_port": 443, "password": "pw", "detour": "HK"},
    {"type": "trojan", "tag": "US Select", "server": "us3.example.com", "server_port": 443, "password": "pw", "detour": "Pick"},
    {"type": "trojan", "tag": "Direct Tag", "server": "us4.example.com", "server_port": 443, "password": "pw", "detour": "dir"},
    {"type": "shadowsocks", "tag": "SS+ST Relayed", "method": "aes-128-gcm", "password": "pw", "detour": "st-relayed"},
    {"type": "shadowtls", "tag": "st-relayed", "server": "st.example.com", "server_port": 443, "version": 3, "password": "spw", "tls": {"enabled": true}, "detour": "Hong Kong 01"},
    {"type": "trojan", "tag": "Three Hop", "server": "h3.example.com", "server_port": 443, "password": "pw", "detour": "US Socks"},
    {"type": "trojan", "tag": "Ghost", "server": "g.example.com", "server_port": 443, "password": "pw", "detour": "missing"}
  ],
  "endpoints": [
    {"type": "wireguard", "tag": "WG Relayed", "address": ["10.0.0.2/32"], "private_key": "k", "detour": "Hong Kong 01", "peers": [{"address": "wg.example.com", "port": 51820, "public_key": "p", "allowed_ips": ["0.0.0.0/0"]}]}
  ]
}`
	res, err := Parse([]byte(profile))
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Node{}
	for _, n := range res.Nodes {
		byName[n.Name] = n
	}
	if len(res.Nodes) != 8 {
		t.Fatalf("got %d nodes: %v (skipped: %+v)", len(res.Nodes), names(res.Nodes), res.Skipped)
	}
	if r := byName["US Socks"].Relay; r == nil || r.Kind != RelayNode || r.Node != "Hong Kong 01" {
		t.Errorf("US Socks relay = %+v", r)
	}
	if r := byName["US Region"].Relay; r == nil || r.Kind != RelayRegion || r.Region != "HK" {
		t.Errorf("US Region relay = %+v", r)
	}
	if r := byName["US Select"].Relay; r == nil || r.Kind != RelayNode || r.Node != "Hong Kong 02" {
		t.Errorf("US Select relay = %+v", r)
	}
	if r := byName["Direct Tag"].Relay; r != nil {
		t.Errorf("Direct Tag relay = %+v, want nil", r)
	}
	st := byName["SS+ST Relayed"]
	if st.Detour == nil || st.Relay == nil || st.Relay.Kind != RelayNode || st.Relay.Node != "Hong Kong 01" {
		t.Errorf("SS+ST Relayed = %+v", st)
	}
	if _, hasRaw := st.Detour["detour"]; hasRaw {
		t.Errorf("ShadowTLS detour map must not keep raw detour string")
	}
	wg := byName["WG Relayed"]
	if !wg.Endpoint || wg.Relay == nil || wg.Relay.Kind != RelayNode || wg.Relay.Node != "Hong Kong 01" {
		t.Errorf("WG Relayed = %+v", wg)
	}
	if !wg.NeedsUDP() || byName["US Socks"].NeedsUDP() {
		t.Errorf("NeedsUDP mismatch: wg=%v socks=%v", wg.NeedsUDP(), byName["US Socks"].NeedsUDP())
	}
	skipped := map[string]string{}
	for _, s := range res.Skipped {
		skipped[s.Name] = s.Code
	}
	if skipped["Three Hop"] != SkipUnsupportedFeature || skipped["Ghost"] != SkipUnsupportedFeature {
		t.Errorf("skipped = %+v", res.Skipped)
	}

	// Relay is part of Key and survives JSON round trip.
	a, b := byName["US Socks"], byName["US Socks"].Clone()
	b.Relay = nil
	if a.Key() == b.Key() {
		t.Error("Key must cover Relay")
	}
	raw, _ := json.Marshal(a)
	var back Node
	if err := json.Unmarshal(raw, &back); err != nil || back.Relay == nil || back.Relay.Node != "Hong Kong 01" {
		t.Fatalf("round trip: %v %+v", err, back)
	}
}
