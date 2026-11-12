package policy

import (
	"encoding/json"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
)

var testEnv = Env{
	ResourceDir: filepath.FromSlash("/data/config/rev-000003/resources"),
	Resources:   map[string]bool{"ca.pem": true, "geosite-cn.srs": true},
	CachePath:   filepath.FromSlash("/data/core/cache.db"),
	Tun: TunSettings{
		Addresses:   []string{"172.19.0.1/30", "fdfe:dcba:9876::1/126"},
		StrictRoute: true,
		DNSMode:     "hijack",
	},
}

func prod() *Policy { return New(Options{WireGuard: true, QUIC: true, DHCP: true, UTLS: true}) }
func dev() *Policy {
	return New(Options{WireGuard: true, QUIC: true, DHCP: true, UTLS: true, AllowServerInbounds: true})
}

// normalize runs the policy and returns the normalized document as a generic tree.
func normalize(t *testing.T, p *Policy, cfg string, env Env) (*Result, map[string]any) {
	t.Helper()
	res := p.Normalize([]byte(cfg), env)
	if res.HasErrors() {
		return res, nil
	}
	var out map[string]any
	if err := json.Unmarshal(res.JSON, &out); err != nil {
		t.Fatalf("normalized output is not valid JSON: %v\n%s", err, res.JSON)
	}
	return res, out
}

func mustPass(t *testing.T, p *Policy, cfg string) (*Result, map[string]any) {
	t.Helper()
	res, out := normalize(t, p, cfg, testEnv)
	if res.HasErrors() {
		t.Fatalf("expected the configuration to pass, got:\n%s", dump(res))
	}
	return res, out
}

func mustFail(t *testing.T, p *Policy, cfg, wantCode string) *Result {
	t.Helper()
	res, _ := normalize(t, p, cfg, testEnv)
	if !res.HasErrors() {
		t.Fatalf("expected an error with code %q, but the configuration passed:\n%s", wantCode, res.JSON)
	}
	for _, i := range res.Errors() {
		if i.Code == wantCode {
			return res
		}
	}
	t.Fatalf("expected code %q, got:\n%s", wantCode, dump(res))
	return nil
}

func dump(r *Result) string {
	var b strings.Builder
	for _, i := range r.Issues {
		b.WriteString("  " + i.String() + "\n")
	}
	return b.String()
}

func get(m map[string]any, path ...string) any {
	var cur any = m
	for _, p := range path {
		switch t := cur.(type) {
		case map[string]any:
			cur = t[p]
		default:
			return nil
		}
	}
	return cur
}

const minimal = `{
  "inbounds": [{"type": "mixed", "tag": "mixed-in", "listen_port": 7890}],
  "outbounds": [{"type": "direct", "tag": "direct"}]
}`

func TestMinimalConfigPasses(t *testing.T) {
	res, out := mustPass(t, prod(), minimal)
	if res.HasTun {
		t.Fatal("no TUN expected")
	}
	if got := get(out, "inbounds"); got == nil {
		t.Fatal("inbounds lost")
	}
	in := out["inbounds"].([]any)[0].(map[string]any)
	if in["listen"] != "127.0.0.1" {
		t.Fatalf("listen should default to loopback, got %v", in["listen"])
	}
	cache := get(out, "experimental", "cache_file").(map[string]any)
	if cache["enabled"] != true || cache["path"] != testEnv.CachePath {
		t.Fatalf("cache_file must be agent-controlled: %v", cache)
	}
	if _, ok := out["log"]; !ok {
		t.Fatal("a log section should always exist")
	}
}

func TestOutputIsDeterministic(t *testing.T) {
	a := prod().Normalize([]byte(minimal), testEnv)
	b := prod().Normalize([]byte(minimal), testEnv)
	if string(a.JSON) != string(b.JSON) || len(a.JSON) == 0 {
		t.Fatal("normalization must be deterministic")
	}
}

func TestNumbersKeepTheirPrecision(t *testing.T) {
	cfg := `{"outbounds":[{"type":"direct","tag":"d","udp_fragment":true}],
	 "inbounds":[{"type":"mixed","tag":"m","listen_port":7890}],
	 "route":{"default_fallback_delay":"300ms","default_mark":4294967295}}`
	res, _ := mustPass(t, prod(), cfg)
	if !strings.Contains(string(res.JSON), "4294967295") {
		t.Fatalf("large integers must survive: %s", res.JSON)
	}
}

func TestJSONProblems(t *testing.T) {
	cases := map[string]string{
		"empty":            ``,
		"not an object":    `[]`,
		"trailing data":    `{} {}`,
		"duplicate key":    `{"log":{"level":"info"},"log":{"level":"debug"}}`,
		"nested duplicate": `{"outbounds":[{"type":"direct","tag":"a","tag":"b"}]}`,
		"malformed":        `{"outbounds":`,
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			res := prod().Normalize([]byte(cfg), testEnv)
			if !res.HasErrors() {
				t.Fatalf("expected an error for %q", cfg)
			}
		})
	}
	if res := prod().Normalize([]byte(strings.Repeat("[", 200)), testEnv); !res.HasErrors() {
		t.Fatal("absurd nesting must be rejected")
	}
	huge := `{"x":"` + strings.Repeat("a", 9<<20) + `"}`
	mustFailCode(t, prod().Normalize([]byte(huge), testEnv), "config_too_large")
}

func mustFailCode(t *testing.T, res *Result, code string) {
	t.Helper()
	for _, i := range res.Errors() {
		if i.Code == code {
			return
		}
	}
	t.Fatalf("expected code %q, got:\n%s", code, dump(res))
}

func TestForbiddenSections(t *testing.T) {
	cases := []struct{ name, cfg, code string }{
		{"clash api", `{"experimental":{"clash_api":{"external_controller":"127.0.0.1:9090"}}}`, "section_not_allowed"},
		{"v2ray api", `{"experimental":{"v2ray_api":{"listen":"127.0.0.1:8080"}}}`, "section_not_allowed"},
		{"debug pprof", `{"experimental":{"debug":{"listen":"127.0.0.1:6060"}}}`, "section_not_allowed"},
		{"services", `{"services":[{"type":"ssm-api","listen":"127.0.0.1"}]}`, "section_not_allowed"},
		{"certificate providers", `{"certificate_providers":[{"type":"acme"}]}`, "section_not_allowed"},
		{"network namespaces", `{"network_namespaces":[{"tag":"x"}]}`, "section_not_allowed"},
		{"unknown section", `{"nope":{}}`, "section_unknown"},
		{"unknown experimental", `{"experimental":{"nope":{}}}`, "section_unknown"},
		{"geoip", `{"route":{"geoip":{"path":"x.db"}}}`, "section_not_allowed"},
		{"cache file escape", `{"experimental":{"cache_file":{"enabled":true,"nope":1}}}`, "field_not_allowed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { mustFail(t, prod(), tc.cfg, tc.code) })
	}
}

func TestEmptyForbiddenSectionsAreTolerated(t *testing.T) {
	mustPass(t, prod(), `{"services":[],"certificate_providers":[],"network_namespaces":[]}`)
}

func TestCacheFileCannotBeRedirected(t *testing.T) {
	_, out := mustPass(t, prod(), `{"experimental":{"cache_file":{"enabled":false,"path":"/etc/cron.d/evil","store_fakeip":true}}}`)
	cache := get(out, "experimental", "cache_file").(map[string]any)
	if cache["path"] != testEnv.CachePath || cache["enabled"] != true || cache["store_fakeip"] != true {
		t.Fatalf("cache_file = %v", cache)
	}
}

func TestLogOutputIsNeverAFile(t *testing.T) {
	res, out := mustPass(t, prod(), `{"log":{"level":"debug","output":"/etc/passwd","timestamp":true}}`)
	if _, ok := get(out, "log").(map[string]any)["output"]; ok {
		t.Fatal("log.output must be removed")
	}
	found := false
	for _, i := range res.Issues {
		if i.Code == "log_output_ignored" && i.Severity == SeverityWarning {
			found = true
		}
	}
	if !found {
		t.Fatal("dropping log.output should warn")
	}
}

func TestLogLevelFollowsAgentSetting(t *testing.T) {
	env := testEnv
	env.LogLevel = "warn"
	_, out := normalize(t, prod(), `{"log":{"level":"trace"}}`, env)
	if get(out, "log", "level") != "warn" {
		t.Fatalf("log level = %v", get(out, "log", "level"))
	}
	mustFail(t, prod(), `{"log":{"level":"loud"}}`, "value_invalid")
	mustFail(t, prod(), `{"log":{"colour":true}}`, "field_not_allowed")
}

func TestTypeAllowLists(t *testing.T) {
	cases := []struct{ name, cfg, code string }{
		{"ssh key file", `{"outbounds":[{"type":"ssh","tag":"s","server":"x","private_key_path":"/root/.ssh/id"}]}`, "path_not_allowed"},
		{"ssh key file as a resource", `{"outbounds":[{"type":"ssh","tag":"s","server":"x","private_key_path":"res:id"}]}`, "path_not_allowed"},
		{"tor outbound", `{"outbounds":[{"type":"tor","tag":"t","executable_path":"/bin/sh"}]}`, "type_not_allowed"},
		{"tailscale endpoint", `{"endpoints":[{"type":"tailscale","tag":"ts"}]}`, "type_not_allowed"},
		{"socks inbound in production", `{"inbounds":[{"type":"socks","tag":"s"}]}`, "type_not_allowed"},
		{"redirect inbound", `{"inbounds":[{"type":"redirect","tag":"r"}]}`, "type_not_allowed"},
		{"resolved dns", `{"dns":{"servers":[{"type":"resolved","tag":"r"}]}}`, "type_not_allowed"},
		{"tailscale dns", `{"dns":{"servers":[{"type":"tailscale","tag":"r"}]}}`, "type_not_allowed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { mustFail(t, prod(), tc.cfg, tc.code) })
	}
}

func TestProtocolOutboundsAreAllowed(t *testing.T) {
	cfg := `{"outbounds":[
	  {"type":"direct","tag":"d"},{"type":"block","tag":"b"},
	  {"type":"shadowsocks","tag":"ss","server":"a","server_port":1,"method":"aes-128-gcm","password":"x"},
	  {"type":"vmess","tag":"vm","server":"a","server_port":1,"uuid":"u"},
	  {"type":"vless","tag":"vl","server":"a","server_port":1,"uuid":"u"},
	  {"type":"trojan","tag":"tj","server":"a","server_port":1,"password":"x"},
	  {"type":"hysteria2","tag":"h2","server":"a","server_port":1,"password":"x"},
	  {"type":"tuic","tag":"tu","server":"a","server_port":1,"uuid":"u","password":"x"},
	  {"type":"anytls","tag":"at","server":"a","server_port":1,"password":"x"},
	  {"type":"snell","tag":"sn","server":"a","server_port":1,"version":6,"psk":"0123456789ab","reuse":true},
	  {"type":"ssh","tag":"sh","server":"a","server_port":22,"user":"u","password":"x","private_key":"k","host_key":["ssh-ed25519 AAAA"]},
	  {"type":"shadowtls","tag":"st","server":"a","server_port":443,"version":3,"password":"x","tls":{"enabled":true,"server_name":"a"}},
	  {"type":"shadowsocks","tag":"ss-st","method":"aes-128-gcm","password":"x","detour":"st"},
	  {"type":"selector","tag":"sel","outbounds":["ss"]},
	  {"type":"urltest","tag":"ut","outbounds":["ss"]}
	],"endpoints":[{"type":"wireguard","tag":"wg","private_key":"k","peers":[]}]}`
	mustPass(t, prod(), cfg)
}

func TestQUICAndWireGuardFollowBuildCapabilities(t *testing.T) {
	p := New(Options{})
	mustFail(t, p, `{"outbounds":[{"type":"hysteria2","tag":"h"}]}`, "type_not_allowed")
	mustFail(t, p, `{"endpoints":[{"type":"wireguard","tag":"w"}]}`, "type_not_allowed")
	mustFail(t, p, `{"dns":{"servers":[{"type":"quic","tag":"q"}]}}`, "type_not_allowed")
	mustFail(t, p, `{"dns":{"servers":[{"type":"dhcp","tag":"d"}]}}`, "type_not_allowed")
}

func TestServerInboundsOnlyInDevAndAlwaysLoopback(t *testing.T) {
	cfg := `{"inbounds":[{"type":"socks","tag":"s","listen":"127.0.0.1","listen_port":1080}]}`
	mustFail(t, prod(), cfg, "type_not_allowed")
	mustPass(t, dev(), cfg)
	mustFail(t, dev(), `{"inbounds":[{"type":"socks","tag":"s","listen":"0.0.0.0","listen_port":1080}]}`, "listen_not_loopback")
}

func TestListenMustBeLoopback(t *testing.T) {
	ok := []string{"127.0.0.1", "127.0.0.2", "::1"}
	for _, l := range ok {
		cfg := `{"inbounds":[{"type":"mixed","tag":"m","listen":"` + l + `","listen_port":1}]}`
		if res, _ := normalize(t, prod(), cfg, testEnv); res.HasErrors() {
			t.Errorf("%s should be accepted:\n%s", l, dump(res))
		}
	}
	bad := []string{"0.0.0.0", "::", "192.168.1.5", "localhost", "example.com", "10.0.0.1", "[::1]", "fe80::1"}
	for _, l := range bad {
		cfg := `{"inbounds":[{"type":"mixed","tag":"m","listen":"` + l + `","listen_port":1}]}`
		res, _ := normalize(t, prod(), cfg, testEnv)
		if !res.HasErrors() {
			t.Errorf("%q must be rejected", l)
		}
	}
	mustFail(t, prod(), `{"inbounds":[{"type":"mixed","tag":"m","listen":42}]}`, "type_mismatch")
}

func TestSystemProxyIsNotAllowed(t *testing.T) {
	mustFail(t, prod(), `{"inbounds":[{"type":"mixed","tag":"m","listen_port":1,"set_system_proxy":true}]}`, "system_proxy_not_allowed")
	mustPass(t, prod(), `{"inbounds":[{"type":"mixed","tag":"m","listen_port":1,"set_system_proxy":false}]}`)
}

func TestWireGuardSystemInterfaceIsDenied(t *testing.T) {
	mustFail(t, prod(), `{"endpoints":[{"type":"wireguard","tag":"w","system":true,"private_key":"k"}]}`, "field_not_allowed")
}

func TestFilePathsAreDenied(t *testing.T) {
	cases := []struct{ name, cfg string }{
		{"tls certificate path", `{"outbounds":[{"type":"trojan","tag":"t","tls":{"enabled":true,"certificate_path":"/etc/shadow"}}]}`},
		{"tls key path", `{"outbounds":[{"type":"trojan","tag":"t","tls":{"enabled":true,"client_key_path":"/root/key"}}]}`},
		{"ech config path", `{"outbounds":[{"type":"trojan","tag":"t","tls":{"enabled":true,"ech":{"enabled":true,"config_path":"/etc/x"}}}]}`},
		{"top-level certificate dir", `{"certificate":{"certificate_directory_path":["/etc/ssl"]}}`},
		{"top-level certificate path", `{"certificate":{"certificate_path":["/etc/ssl/ca.pem"]}}`},
		{"protect path", `{"outbounds":[{"type":"direct","tag":"d","protect_path":"/var/run/docker.sock"}]}`},
		{"dhcp lease files", `{"route":{"dhcp_lease_files":["/var/db/dhcpd_leases"]}}`},
		{"hosts dns file", `{"dns":{"servers":[{"type":"hosts","tag":"h","path":["/etc/shadow"]}]}}`},
		{"local rule set path", `{"route":{"rule_set":[{"type":"local","tag":"r","format":"binary","path":"/etc/passwd"}]}}`},
		{"unknown file-ish key", `{"outbounds":[{"type":"direct","tag":"d","ca_file":"/etc/passwd"}]}`},
		{"unknown socket key", `{"outbounds":[{"type":"direct","tag":"d","unix_socket":"/var/run/x"}]}`},
		{"path on a generic object", `{"route":{"rules":[{"path":"/etc/passwd","outbound":"d"}]}}`},
		{"netns", `{"outbounds":[{"type":"direct","tag":"d","netns":"x"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { mustFail(t, prod(), tc.cfg, "path_not_allowed") })
	}
}

func TestBenignPathsAreAllowed(t *testing.T) {
	cfg := `{
	  "outbounds":[
	    {"type":"vmess","tag":"ws","server":"a","server_port":1,"uuid":"u","transport":{"type":"ws","path":"/ws"}},
	    {"type":"vless","tag":"hu","server":"a","server_port":1,"uuid":"u","transport":{"type":"httpupgrade","path":"/up"}},
	    {"type":"vless","tag":"ht","server":"a","server_port":1,"uuid":"u","transport":{"type":"http","path":"/h"}},
	    {"type":"http","tag":"hp","server":"a","server_port":1,"path":"/proxy"}
	  ],
	  "dns":{"servers":[{"type":"https","tag":"doh","server":"1.1.1.1","path":"/dns-query"}]},
	  "route":{"rules":[{"process_path":["/usr/bin/curl"],"process_path_regex":[".*"],"process_name":["curl"],"outbound":"ws"}]}
	}`
	mustPass(t, prod(), cfg)
}

func TestPathBenignOnlyInItsOwnPlace(t *testing.T) {
	// "path" is a URL path for a ws transport but a file for a hosts server.
	mustPass(t, prod(), `{"outbounds":[{"type":"vmess","tag":"ws","transport":{"type":"ws","path":"/x"}}]}`)
	mustFail(t, prod(), `{"outbounds":[{"type":"vmess","tag":"ws","detour":{"path":"/etc/passwd"}}]}`, "path_not_allowed")
	mustFail(t, prod(), `{"dns":{"servers":[{"type":"hosts","tag":"h","path":"/etc/shadow"}]}}`, "path_not_allowed")
}

func TestPathDefaultsAreFalseForNonStrings(t *testing.T) {
	// Booleans named like paths are not file references.
	mustPass(t, prod(), `{"outbounds":[{"type":"hysteria2","tag":"h","server":"a","server_port":1,"password":"x","disable_path_mtu_discovery":true,"tcp_multi_path":true}]}`)
	// An explicitly false/empty forbidden key is harmless.
	mustPass(t, prod(), `{"outbounds":[{"type":"direct","tag":"d","protect_path":""}]}`)
}

func TestResourceReferencesAreRewritten(t *testing.T) {
	cfg := `{
	  "outbounds":[{"type":"trojan","tag":"t","server":"a","server_port":1,"password":"x",
	     "tls":{"enabled":true,"certificate_path":"res:ca.pem"}}],
	  "route":{"rule_set":[{"type":"local","tag":"geosite-cn","format":"binary","path":"res:geosite-cn.srs"}]}
	}`
	res, out := mustPass(t, prod(), cfg)
	want := filepath.Join(testEnv.ResourceDir, "ca.pem")
	if got := get(out, "outbounds"); got == nil || !strings.Contains(string(res.JSON), strings.ReplaceAll(want, `\`, `\\`)) {
		t.Fatalf("certificate_path not rewritten:\n%s", res.JSON)
	}
	rs := get(out, "route", "rule_set").([]any)[0].(map[string]any)
	if rs["path"] != filepath.Join(testEnv.ResourceDir, "geosite-cn.srs") {
		t.Fatalf("rule set path = %v", rs["path"])
	}
}

func TestMissingResourceIsReported(t *testing.T) {
	mustFail(t, prod(), `{"route":{"rule_set":[{"type":"local","tag":"r","format":"binary","path":"res:absent.srs"}]}}`, "resource_missing")
	mustFail(t, prod(), `{"outbounds":[{"type":"trojan","tag":"t","tls":{"enabled":true,"certificate_path":"res:absent.pem"}}]}`, "resource_missing")
}

func TestResourceTraversalIsImpossible(t *testing.T) {
	// ".." and separators are rejected when resources are stored, so they can
	// never be members of env.Resources; the reference must therefore fail.
	mustFail(t, prod(), `{"route":{"rule_set":[{"type":"local","tag":"r","format":"binary","path":"res:../../etc/passwd"}]}}`, "resource_missing")
}

func TestRuleSetKinds(t *testing.T) {
	mustFail(t, prod(), `{"route":{"rule_set":[{"type":"remote","tag":"r","format":"binary","url":"https://example.com/x.srs"}]}}`, "rule_set_remote_not_allowed")
	mustFail(t, prod(), `{"route":{"rule_set":[{"type":"mystery","tag":"r"}]}}`, "type_not_allowed")
	mustPass(t, prod(), `{"route":{"rule_set":[{"tag":"inline","rules":[{"domain":["example.com"]}]}]}}`)
}

const tunConfig = `{
  "inbounds": [{"type":"tun","tag":"tun-in","mtu":1400,"stack":"mixed","route_exclude_address":["192.168.0.0/16"]}],
  "outbounds": [{"type":"direct","tag":"direct"}],
  "route": {"rules":[{"action":"sniff"}]}
}`

func TestTunIsOwnedByTheAgent(t *testing.T) {
	// Even if the GUI tries to set addressing, routing and DNS fields, the agent's plan wins.
	cfg := `{"inbounds":[{"type":"tun","tag":"tun-in","address":["10.0.0.1/8"],"auto_route":false,"strict_route":false,
	  "dns_mode":"disabled","interface_name":"evil0"}],"outbounds":[{"type":"direct","tag":"d"}]}`
	res, out := mustPass(t, prod(), cfg)
	if !res.HasTun || res.TunTag != "tun-in" {
		t.Fatalf("result = %+v", res)
	}
	tun := get(out, "inbounds").([]any)[0].(map[string]any)
	addrs := tun["address"].([]any)
	if len(addrs) != 2 || addrs[0] != "172.19.0.1/30" {
		t.Fatalf("address = %v", addrs)
	}
	if tun["auto_route"] != true || tun["strict_route"] != true || tun["dns_mode"] != "hijack" {
		t.Fatalf("tun = %v", tun)
	}
	if _, ok := tun["interface_name"]; ok {
		t.Fatal("interface_name must be removed when the platform does not set one")
	}
}

func TestTunInterfaceNameComesFromThePlan(t *testing.T) {
	env := testEnv
	env.Tun.InterfaceName = "EggBox"
	_, out := normalize(t, prod(), tunConfig, env)
	if get(out, "inbounds").([]any)[0].(map[string]any)["interface_name"] != "EggBox" {
		t.Fatal("interface_name should follow the plan")
	}
}

func TestTunDefaultsAndAllowedTuning(t *testing.T) {
	res, out := mustPass(t, prod(), tunConfig)
	tun := get(out, "inbounds").([]any)[0].(map[string]any)
	if tun["stack"] != "mixed" || tun["mtu"].(float64) != 1400 {
		t.Fatalf("tuning lost: %v", tun)
	}
	if !res.HasTun {
		t.Fatal("HasTun")
	}
	_, out = mustPass(t, prod(), `{"inbounds":[{"type":"tun"}]}`)
	tun = get(out, "inbounds").([]any)[0].(map[string]any)
	if tun["tag"] != "tun-in" || tun["stack"] != "system" || tun["mtu"].(float64) != 1500 {
		t.Fatalf("defaults = %v", tun)
	}
}

func TestTunRouteSettingsAreForced(t *testing.T) {
	_, out := mustPass(t, prod(), tunConfig)
	if get(out, "route", "auto_detect_interface") != true {
		t.Fatal("route.auto_detect_interface must be forced on with a TUN")
	}
	rules := get(out, "route", "rules").([]any)
	first := rules[0].(map[string]any)
	if first["action"] != "hijack-dns" || first["inbound"].([]any)[0] != "tun-in" {
		t.Fatalf("a hijack-dns rule must come first: %v", rules)
	}
	if len(rules) != 2 {
		t.Fatalf("user rules must be preserved: %v", rules)
	}
}

func TestConfirmedTakeoverOverridesRoutingAndOutranksVPNDefaults(t *testing.T) {
	env := testEnv
	env.Tun.TakeoverInterface = "en0"
	res, out := normalize(t, prod(), `{"inbounds":[{"type":"tun","route_address":["10.0.0.0/8"],"route_exclude_address":["0.0.0.0/0"]}],"route":{"auto_detect_interface":true}}`, env)
	if res.HasErrors() {
		t.Fatal(dump(res))
	}
	if get(out, "route", "default_interface") != "en0" || get(out, "route", "auto_detect_interface") != false {
		t.Fatal("takeover did not bind the physical uplink")
	}
	tun := out["inbounds"].([]any)[0].(map[string]any)
	if tun["route_exclude_address"] != nil {
		t.Fatal("provider exclusions must not undo confirmed takeover")
	}
	var routes []netip.Prefix
	for _, value := range tun["route_address"].([]any) {
		routes = append(routes, netip.MustParsePrefix(value.(string)))
	}
	for _, dest := range []string{"1.1.1.1", "65.1.1.1", "129.1.1.1", "203.0.113.1", "2001:db8::1", "4001::1", "8001::1", "c001::1"} {
		addr := netip.MustParseAddr(dest)
		wins := false
		for _, route := range routes {
			if route.Contains(addr) && route.Bits() > 1 {
				wins = true
			}
		}
		if !wins {
			t.Fatalf("takeover does not beat a competing default or half-default for %s", dest)
		}
	}
}

func TestTunWithoutRouteSectionStillGetsRouteSettings(t *testing.T) {
	_, out := mustPass(t, prod(), `{"inbounds":[{"type":"tun"}]}`)
	if get(out, "route", "auto_detect_interface") != true {
		t.Fatal("route section must be created for a TUN")
	}
}

func TestExistingDNSHijackRuleIsNotDuplicated(t *testing.T) {
	cfg := `{"inbounds":[{"type":"tun","tag":"t"}],"route":{"rules":[{"port":53,"action":"hijack-dns"}]}}`
	_, out := mustPass(t, prod(), cfg)
	if got := len(get(out, "route", "rules").([]any)); got != 1 {
		t.Fatalf("rules = %d, the equivalent rule already exists", got)
	}
	// A hijack rule limited by other conditions does not count.
	cfg = `{"inbounds":[{"type":"tun","tag":"t"}],"route":{"rules":[{"port":53,"domain":["a.com"],"action":"hijack-dns"}]}}`
	_, out = mustPass(t, prod(), cfg)
	if got := len(get(out, "route", "rules").([]any)); got != 2 {
		t.Fatalf("rules = %d, a conditional rule is not a substitute", got)
	}
}

func TestMixedOnlyConfigDoesNotTouchRouteSettings(t *testing.T) {
	_, out := mustPass(t, prod(), minimal)
	if get(out, "route", "auto_detect_interface") != nil {
		t.Fatal("no TUN: leave routing alone")
	}
}

func TestTunValidation(t *testing.T) {
	cases := []struct{ name, cfg, code string }{
		{"two tuns", `{"inbounds":[{"type":"tun","tag":"a"},{"type":"tun","tag":"b"}]}`, "tun_multiple"},
		{"auto redirect", `{"inbounds":[{"type":"tun","auto_redirect":true}]}`, "tun_field_not_allowed"},
		{"include package", `{"inbounds":[{"type":"tun","include_package":["x"]}]}`, "tun_field_not_allowed"},
		{"platform", `{"inbounds":[{"type":"tun","platform":{"http_proxy":{"enabled":true}}}]}`, "tun_field_not_allowed"},
		{"netns", `{"inbounds":[{"type":"tun","netns":"x"}]}`, "tun_field_not_allowed"},
		{"deprecated inet4", `{"inbounds":[{"type":"tun","inet4_address":["1.2.3.4/24"]}]}`, "tun_field_not_allowed"},
		{"bad stack", `{"inbounds":[{"type":"tun","stack":"magic"}]}`, "value_invalid"},
		{"mtu too small", `{"inbounds":[{"type":"tun","mtu":100}]}`, "value_invalid"},
		{"mtu too large", `{"inbounds":[{"type":"tun","mtu":65000}]}`, "value_invalid"},
		{"mtu not a number", `{"inbounds":[{"type":"tun","mtu":"big"}]}`, "value_invalid"},
		{"bad exclude prefix", `{"inbounds":[{"type":"tun","route_exclude_address":["not-a-cidr"]}]}`, "value_invalid"},
		{"exclude wrong type", `{"inbounds":[{"type":"tun","route_address":7}]}`, "type_mismatch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { mustFail(t, prod(), tc.cfg, tc.code) })
	}
}

func TestTunDNSAddressesAreOptional(t *testing.T) {
	env := testEnv
	env.Tun.DNSAddresses = []string{"172.19.0.2"}
	_, out := normalize(t, prod(), `{"inbounds":[{"type":"tun"}]}`, env)
	if got := get(out, "inbounds").([]any)[0].(map[string]any)["dns_address"]; got == nil {
		t.Fatal("dns_address should follow the plan when provided")
	}
	_, out = mustPass(t, prod(), `{"inbounds":[{"type":"tun","dns_address":["8.8.8.8"]}]}`)
	if _, ok := get(out, "inbounds").([]any)[0].(map[string]any)["dns_address"]; ok {
		t.Fatal("a GUI supplied dns_address must not survive")
	}
}

func TestSchemaKeyIsDropped(t *testing.T) {
	_, out := mustPass(t, prod(), `{"$schema":"https://sing-box.sagernet.org/schema.json","outbounds":[{"type":"direct","tag":"d"}]}`)
	if _, ok := out["$schema"]; ok {
		t.Fatal("$schema should be dropped")
	}
}

func TestIssuesCarryJSONPaths(t *testing.T) {
	res := mustFail(t, prod(), `{"outbounds":[{"type":"direct","tag":"a"},{"type":"tor","tag":"b"}]}`, "type_not_allowed")
	if got := res.Errors()[0].Path; got != "$.outbounds[1].type" {
		t.Fatalf("path = %q", got)
	}
}

func TestSandboxReplacesTunWithLoopbackProxy(t *testing.T) {
	env := testEnv
	env.SandboxPort = 17890
	cfg := `{"inbounds":[{"type":"tun","tag":"tun-in","address":["10.0.0.1/8"],"auto_route":true,"mtu":1400}],
	  "route":{"rules":[{"inbound":["tun-in"],"action":"hijack-dns","port":[53]}]},
	  "outbounds":[{"type":"direct","tag":"d"}]}`
	res := prod().Normalize([]byte(cfg), env)
	if res.HasErrors() {
		t.Fatalf("unexpected errors: %s", dump(res))
	}
	if res.HasTun || !res.SandboxTun {
		t.Fatalf("result = %+v", res)
	}
	var out map[string]any
	if err := json.Unmarshal(res.JSON, &out); err != nil {
		t.Fatal(err)
	}
	in := out["inbounds"].([]any)[0].(map[string]any)
	if in["type"] != "mixed" || in["tag"] != "tun-in" || in["listen"] != "127.0.0.1" || in["listen_port"] != float64(17890) {
		t.Fatalf("inbound = %v", in)
	}
	for _, k := range []string{"address", "auto_route", "mtu", "strict_route", "dns_mode"} {
		if _, ok := in[k]; ok {
			t.Fatalf("%s must not survive the replacement: %v", k, in)
		}
	}
	route := out["route"].(map[string]any)
	if _, ok := route["auto_detect_interface"]; ok {
		t.Fatalf("sandbox must not force TUN route settings: %v", route)
	}
	found := false
	for _, i := range res.Issues {
		found = found || i.Code == "sandbox_tun_replaced"
	}
	if !found {
		t.Fatalf("expected a sandbox warning, got %s", dump(res))
	}
}

func TestSandboxDoesNotAffectConfigsWithoutTun(t *testing.T) {
	env := testEnv
	env.SandboxPort = 17890
	res := prod().Normalize([]byte(minimal), env)
	if res.HasErrors() || res.SandboxTun || res.HasTun {
		t.Fatalf("result = %+v %s", res, dump(res))
	}
}

func TestUTLSAndRealityFollowTheBuild(t *testing.T) {
	cfg := `{
	  "outbounds": [
	    {"type": "direct", "tag": "direct"},
	    {"type": "vless", "tag": "plain", "server": "a.example.com", "server_port": 443, "uuid": "u", "tls": {"enabled": true}},
	    {"type": "vless", "tag": "fp", "server": "b.example.com", "server_port": 443, "uuid": "u",
	     "tls": {"enabled": true, "utls": {"enabled": true, "fingerprint": "chrome"}}},
	    {"type": "vless", "tag": "reality", "server": "c.example.com", "server_port": 443, "uuid": "u",
	     "tls": {"enabled": true, "utls": {"enabled": true}, "reality": {"enabled": true, "public_key": "k", "short_id": "ab"}}},
	    {"type": "vless", "tag": "off", "server": "d.example.com", "server_port": 443, "uuid": "u",
	     "tls": {"enabled": true, "utls": {"enabled": false}}}
	  ]
	}`
	if res, _ := normalize(t, prod(), cfg, testEnv); res.HasErrors() {
		t.Fatalf("a build with uTLS must accept fingerprints and REALITY:\n%s", dump(res))
	}
	res, _ := normalize(t, New(Options{}), cfg, testEnv)
	paths := map[string]bool{}
	for _, i := range res.Errors() {
		if i.Code != "feature_not_available" {
			t.Fatalf("unexpected issue: %s", i)
		}
		paths[i.Path] = true
	}
	for _, want := range []string{"$.outbounds[2].tls.utls", "$.outbounds[3].tls.utls", "$.outbounds[3].tls.reality"} {
		if !paths[want] {
			t.Errorf("missing finding at %s, got %v", want, paths)
		}
	}
	if len(paths) != 3 {
		t.Errorf("only the nodes that use the feature may be flagged, got %v", paths)
	}
}
