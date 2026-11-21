package configgen

import (
	"encoding/json"
	"testing"

	"eggbox/internal/agent/policy"
	"eggbox/internal/subscription"
)

func testNode(name, typ, host string, port int) subscription.Node {
	cfg := map[string]any{"type": typ, "server": host, "server_port": port}
	switch typ {
	case "shadowsocks":
		cfg["method"], cfg["password"] = "aes-128-gcm", "pw"
	case "trojan":
		cfg["password"] = "pw"
		cfg["tls"] = map[string]any{"enabled": true, "server_name": host}
	case "socks":
		cfg["version"] = "5"
	}
	return subscription.Node{Name: name, Type: typ, Server: host, Port: port, Config: cfg}
}

func TestGenerateRelays(t *testing.T) {
	hk1 := testNode("HK 01", "shadowsocks", "hk1.example.com", 8388)
	hk2 := testNode("HK 02", "shadowsocks", "hk2.example.com", 8388)
	jp1 := testNode("JP 01", "shadowsocks", "jp1.example.com", 8388)
	usRegion := testNode("US Via HK", "socks", "us1.example.com", 1080)
	usPin := testNode("US Via HK01", "socks", "us2.example.com", 1080)
	usPinStrict := testNode("US Via HK01 Strict", "socks", "us3.example.com", 1080)
	usAuto := testNode("US Via Auto", "socks", "us4.example.com", 1080)
	usBlocked := testNode("US Blocked", "socks", "us5.example.com", 1080)
	usST := subscription.Node{
		Name: "US ShadowTLS", Type: "shadowsocks", Server: "st.example.com", Port: 443,
		Config: map[string]any{"type": "shadowsocks", "server": "st.example.com", "server_port": 443, "method": "aes-128-gcm", "password": "pw"},
		Detour: map[string]any{"type": "shadowtls", "server": "st.example.com", "server_port": 443, "version": 3, "password": "spw", "tls": map[string]any{"enabled": true}},
	}
	usWG := subscription.Node{
		Name: "US WG", Type: "wireguard", Server: "wg.example.com", Port: 51820, Endpoint: true,
		Config: map[string]any{
			"type": "wireguard", "address": []any{"10.0.0.2/32"}, "private_key": "k", "listen_port": 51820,
			"peers": []any{map[string]any{"address": "wg.example.com", "port": 51820, "public_key": "p", "allowed_ips": []any{"0.0.0.0/0"}}},
		},
	}

	in := Input{
		Prefs:    DefaultPrefs(),
		Main:     Exit{Kind: ExitAuto},
		Fallback: Exit{Kind: ExitMain},
		Nodes: []Node{
			{Node: hk1, Region: "HK"},
			{Node: hk2, Region: "HK"},
			{Node: jp1, Region: "JP"},
			{Node: usRegion, Region: "US", NoAuto: true, Relay: Relay{Kind: RelayRegion, Region: "HK"}},
			{Node: usPin, Region: "US", NoAuto: true, Relay: Relay{Kind: RelayNode, Node: hk1.Key()}},
			{Node: usPinStrict, Region: "US", NoAuto: true, Relay: Relay{Kind: RelayNode, Node: hk1.Key(), NoStandIn: true}},
			{Node: usAuto, Region: "US", NoAuto: true, Relay: Relay{Kind: RelayAuto}},
			{Node: usBlocked, Region: "US", NoAuto: true, Relay: Relay{Kind: RelayRegion, Region: "SG"}}, // no SG plain node -> blocked!
			{Node: usST, Region: "US", NoAuto: true, Relay: Relay{Kind: RelayNode, Node: jp1.Key()}},
			{Node: usWG, Region: "US", NoAuto: true, Relay: Relay{Kind: RelayRegion, Region: "HK"}},
		},
	}
	out, err := Generate(in)
	if err != nil {
		t.Fatal(err)
	}
	// Must pass the privileged policy gate!
	pol := policy.New(policy.Options{WireGuard: true, QUIC: true, DHCP: true, UTLS: true})
	res := pol.Normalize(out.JSON, policy.Env{ResourceDir: t.TempDir(), Resources: map[string]bool{}, CachePath: t.TempDir() + "/cache.db", LogLevel: "info"})
	if res.HasErrors() {
		t.Fatalf("policy rejected relayed config: %v\n%s", res.Errors(), out.JSON)
	}

	var doc map[string]any
	if err := json.Unmarshal(out.JSON, &doc); err != nil {
		t.Fatal(err)
	}
	byTag := map[string]map[string]any{}
	for _, raw := range doc["outbounds"].([]any) {
		o := raw.(map[string]any)
		byTag[o["tag"].(string)] = o
	}
	epByTag := map[string]map[string]any{}
	for _, raw := range doc["endpoints"].([]any) {
		o := raw.(map[string]any)
		epByTag[o["tag"].(string)] = o
	}

	// Relay groups must contain ONLY plain direct nodes.
	ra := byTag[TagRelayAuto]
	if ra == nil || len(ra["outbounds"].([]any)) != 3 {
		t.Fatalf("relay-auto = %v", ra)
	}
	rhk := byTag[RelayRegionPrefix+"HK"]
	if rhk == nil || len(rhk["outbounds"].([]any)) != 2 {
		t.Fatalf("relay-region-HK = %v", rhk)
	}
	if byTag[TagRelayBlocked] == nil || byTag[TagRelayBlocked]["type"] != "block" {
		t.Fatalf("relay-blocked = %v", byTag[TagRelayBlocked])
	}

	// Check each node's detour wiring.
	if got := byTag[out.Tags[usRegion.Key()]]["detour"]; got != RelayRegionPrefix+"HK" {
		t.Errorf("usRegion detour = %v", got)
	}
	pinTag := out.Relays[usPin.Key()]
	if pinTag != RelayPinPrefix+"1" {
		t.Fatalf("usPin relay tag = %q, want relay-pin-1", pinTag)
	}
	if got := byTag[out.Tags[usPin.Key()]]["detour"]; got != pinTag {
		t.Errorf("usPin detour = %v, want %s", got, pinTag)
	}
	if got := byTag[out.Tags[usPinStrict.Key()]]["detour"]; got != out.Tags[hk1.Key()] {
		t.Errorf("usPinStrict detour = %v, want %s", got, out.Tags[hk1.Key()])
	}
	if got := byTag[out.Tags[usAuto.Key()]]["detour"]; got != TagRelayAuto {
		t.Errorf("usAuto detour = %v", got)
	}
	if got := byTag[out.Tags[usBlocked.Key()]]["detour"]; got != TagRelayBlocked {
		t.Errorf("usBlocked detour = %v, want %s", got, TagRelayBlocked)
	}
	// ShadowTLS node: SS points to via-<tag>, and via-<tag> points to JP 01!
	stTag := out.Tags[usST.Key()]
	if got := byTag[stTag]["detour"]; got != DetourPrefix+stTag {
		t.Errorf("SS+ST detour = %v", got)
	}
	if got := byTag[DetourPrefix+stTag]["detour"]; got != out.Tags[jp1.Key()] {
		t.Errorf("ShadowTLS outer detour = %v, want %s", got, out.Tags[jp1.Key()])
	}
	// WireGuard endpoint gets detour and drops listen_port.
	wgObj := epByTag[out.Tags[usWG.Key()]]
	if got := wgObj["detour"]; got != RelayRegionPrefix+"HK" {
		t.Errorf("WG detour = %v", got)
	}
	if _, hasLP := wgObj["listen_port"]; hasLP {
		t.Error("WG endpoint with detour must drop listen_port")
	}

	// Plain config (no relays) must not emit any relay-* groups.
	plainOut, err := Generate(Input{Prefs: DefaultPrefs(), Main: Exit{Kind: ExitAuto}, Fallback: Exit{Kind: ExitMain}, Nodes: []Node{{Node: hk1, Region: "HK"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plainOut.Relays) != 0 {
		t.Errorf("plain config should have no Relays: %v", plainOut.Relays)
	}
	var pdoc map[string]any
	_ = json.Unmarshal(plainOut.JSON, &pdoc)
	for _, raw := range pdoc["outbounds"].([]any) {
		tag := raw.(map[string]any)["tag"].(string)
		if tag == TagRelayAuto || tag == TagRelayBlocked {
			t.Errorf("plain config emitted unnecessary %s", tag)
		}
	}
}

func TestGenerateAllNodesRelayedNeverFallsBackDirect(t *testing.T) {
	us := testNode("US Home", "socks", "us.example.com", 1080)
	out, err := Generate(Input{
		Prefs:    DefaultPrefs(),
		Main:     Exit{Kind: ExitAuto},
		Fallback: Exit{Kind: ExitMain},
		Nodes:    []Node{{Node: us, Region: "US", Relay: Relay{Kind: RelayAuto}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Relays[us.Key()] != TagRelayBlocked {
		t.Fatalf("expected relay-blocked when no plain nodes exist, got %q", out.Relays[us.Key()])
	}
	pol := policy.New(policy.Options{WireGuard: true, QUIC: true, DHCP: true, UTLS: true})
	res := pol.Normalize(out.JSON, policy.Env{ResourceDir: t.TempDir(), Resources: map[string]bool{}, CachePath: t.TempDir() + "/cache.db", LogLevel: "info"})
	if res.HasErrors() {
		t.Fatalf("policy rejected all-relayed config: %v", res.Errors())
	}
}

func TestGenerateIdenticalServersAcrossTwoSubscriptions(t *testing.T) {
	jp := Node{Sub: "sub-a", Node: testNode("Tokyo 01", "socks", "jp.example.com", 1080), Region: "JP"}
	shared := testNode("US Reality 01", "socks", "us.example.com", 1080)
	copyA := Node{Sub: "sub-a", Node: shared, Region: "US"}
	copyB := Node{Sub: "sub-b", Node: shared, Region: "US", NoAuto: true, Relay: Relay{Kind: RelayNode, Node: jp.Key(), NoStandIn: true}}

	out, err := Generate(Input{
		Prefs:    DefaultPrefs(),
		Main:     Exit{Kind: ExitNode, Node: copyB.Key(), NoStandIn: true},
		Fallback: Exit{Kind: ExitMain},
		Nodes:    []Node{jp, copyA, copyB},
	})
	if err != nil {
		t.Fatal(err)
	}
	tagA, tagB := out.Tags[copyA.Key()], out.Tags[copyB.Key()]
	if tagA == "" || tagB == "" || tagA == tagB {
		t.Fatalf("distinct subscriptions must get distinct outbound tags: tagA=%q tagB=%q", tagA, tagB)
	}
	if got := out.Selections[TagProxy]; got != tagB {
		t.Errorf("Proxy selected %q, want sub-b copy %q", got, tagB)
	}
	if _, relayed := out.Relays[copyA.Key()]; relayed {
		t.Errorf("sub-a copy should be plain direct, got relay %q", out.Relays[copyA.Key()])
	}
	if got := out.Relays[copyB.Key()]; got != out.Tags[jp.Key()] {
		t.Errorf("sub-b copy relay = %q, want %q", got, out.Tags[jp.Key()])
	}
	pol := policy.New(policy.Options{WireGuard: true, QUIC: true, DHCP: true, UTLS: true})
	res := pol.Normalize(out.JSON, policy.Env{ResourceDir: t.TempDir(), Resources: map[string]bool{}, CachePath: t.TempDir() + "/cache.db", LogLevel: "info"})
	if res.HasErrors() {
		t.Fatalf("policy rejected config with duplicate servers across subscriptions: %v", res.Errors())
	}
}
