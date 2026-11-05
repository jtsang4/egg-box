package configgen_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"eggbox/internal/agent/engine"
	"eggbox/internal/agent/engine/singbox"
	"eggbox/internal/agent/policy"
	"eggbox/internal/configgen"
	"eggbox/internal/subscription"
	"eggbox/internal/testsupport/srs"
)

const uuid = "00000000-0000-4000-8000-000000000001"

// links are synthetic servers of every shape the parsers produce.
func links() []string {
	l := []string{
		"vless://" + uuid + "@hk2.example.com:443?security=tls&type=ws&host=cdn.example.net&path=%2Fws&sni=cdn.example.net#HK%2002",
		"trojan://pw@hk3.example.com:443?sni=hk3.example.com#HK%2003",
		"vmess://" + b64(`{"ps":"JP 01","add":"jp1.example.com","port":"443","id":"`+uuid+`","aid":"0","net":"grpc","path":"svc","tls":"tls"}`),
		"trojan://pw@jp2.example.com:443?sni=jp2.example.com#JP%2002",
		"ss://" + b64("aes-256-gcm:pw") + "@us1.example.com:8388#US%2001",
		"ss://" + b64("aes-128-gcm:pw") + "@us2.example.com:443/?plugin=obfs-local%3Bobfs%3Dtls%3Bobfs-host%3Dwww.example.org#US%2002",
		"anytls://pw@sg1.example.com:443?sni=sg1.example.com#SG%2001",
		"socks5://u:p@proxy.example.com:1080#Plain%20SOCKS",
		"trojan://pw@dup.example.com:443#Proxy", // collides with a fixed tag
		"trojan://pw@dup2.example.com:443#direct",
	}
	if singbox.UTLSCompiled {
		pbk := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32))
		l = append(l, "vless://"+uuid+"@hk1.example.com:443?encryption=none&flow=xtls-rprx-vision&security=reality&sni=www.example.org&fp=chrome&pbk="+pbk+"&sid=ab12#HK%2001")
	}
	if singbox.QUICCompiled {
		l = append(l,
			"hysteria2://pw@kr1.example.com:443?sni=kr1.example.com&obfs=salamander&obfs-password=x&up=20&down=100#KR%2001",
			"tuic://"+uuid+":pw@kr2.example.com:443?congestion_control=bbr&sni=kr2.example.com#KR%2002",
		)
	}
	if singbox.WireGuardCompiled {
		// Valid 32-byte keys, as base64 and percent-encoded the way links carry them.
		priv := url.QueryEscape(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)))
		pub := url.QueryEscape(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32)))
		l = append(l, "wireguard://"+priv+"@wg.example.com:51820?publickey="+pub+"&address=10.9.0.2/32#WG")
	}
	return l
}

func b64(s string) string {
	return strings.TrimRight(base64.StdEncoding.EncodeToString([]byte(s)), "=")
}

func nodes(t *testing.T) []subscription.Node {
	t.Helper()
	res, err := subscription.Parse([]byte(strings.Join(links(), "\n")))
	if err != nil {
		t.Fatalf("parse fixtures: %v (skipped %+v)", err, res.Skipped)
	}
	if len(res.Skipped) != 0 {
		t.Fatalf("fixtures were skipped: %+v", res.Skipped)
	}
	return res.Nodes
}

func ruleSets(t *testing.T) map[string][]byte {
	t.Helper()
	return map[string][]byte{
		configgen.RuleSetCN:      srs.DomainSuffixes(t, "example.cn", "baidu.com"),
		configgen.RuleSetCNIP:    srs.CIDRs(t, "198.51.100.0/24"),
		configgen.RuleSetAds:     srs.DomainSuffixes(t, "ads.example"),
		configgen.RuleSetForeign: srs.DomainSuffixes(t, "example.org"),
	}
}

// accept runs a generated configuration through the exact gate production uses:
// the agent's policy and the real core's dry run.
func accept(t *testing.T, out *configgen.Output) *policy.Result {
	t.Helper()
	dir := t.TempDir()
	res := map[string]bool{}
	for _, r := range out.Resources {
		res[r.Name] = true
		if err := os.WriteFile(filepath.Join(dir, r.Name), r.Data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reg := singbox.RegistryOptions{}
	pol := policy.New(singbox.PolicyOptions(reg))
	result := pol.Normalize(out.JSON, policy.Env{
		ResourceDir: dir, Resources: res, CachePath: filepath.Join(dir, "cache.db"), LogLevel: "info",
		Tun: policy.TunSettings{Addresses: []string{"172.19.0.1/30", "fdfe:dcba:9876::1/126"}, DNSMode: "disabled"},
	})
	if result.HasErrors() {
		t.Fatalf("the policy rejected the generated configuration:\n%v\n%s", result.Errors(), out.JSON)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	eng := singbox.New(reg)
	if err := eng.Check(ctx, engine.Prepared{Revision: 1, JSON: result.JSON, HasTun: result.HasTun, TunTag: result.TunTag}); err != nil {
		t.Fatalf("the core rejected the generated configuration: %v\n%s", err, out.JSON)
	}
	return result
}

func generate(t *testing.T, in configgen.Input) *configgen.Output {
	t.Helper()
	out, err := configgen.Generate(in)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestGeneratedConfigurationsAreAccepted(t *testing.T) {
	base := configgen.DefaultPrefs()
	var cases = map[string]configgen.Prefs{}
	for _, preset := range []configgen.Preset{configgen.PresetChina, configgen.PresetProxyAll, configgen.PresetForeignOnly} {
		for _, ads := range []bool{false, true} {
			p := base
			p.Preset, p.BlockAds = preset, ads
			cases[fmt.Sprintf("%s ads=%v", preset, ads)] = p
		}
	}
	v6 := base
	v6.IPv6, v6.MixedPort, v6.RegionGroups, v6.Sniff, v6.TunStack, v6.MTU = true, 7890, false, false, "mixed", 1400
	cases["ipv6, mixed port, no regions, no sniff"] = v6
	for name, dns := range map[string][2]string{
		"udp":      {"udp://119.29.29.29:5353", "tcp://8.8.8.8"},
		"dot":      {"tls://dot.pub", "tls://dns.google:853"},
		"doh":      {"https://doh.pub/dns-query", "https://dns.google/dns-query"},
		"ipv6 dns": {"[2400:3200::1]", "https://[2606:4700:4700::1111]/dns-query"},
		"local":    {"local", "https://1.1.1.1/dns-query"},
	} {
		p := base
		p.DomesticDNS, p.RemoteDNS = dns[0], dns[1]
		cases["dns "+name] = p
	}

	for name, prefs := range cases {
		t.Run(name, func(t *testing.T) {
			out := generate(t, configgen.Input{Nodes: nodes(t), Prefs: prefs, RuleSets: ruleSets(t)})
			if len(out.Warnings) != 0 {
				t.Fatalf("unexpected warnings: %+v", out.Warnings)
			}
			accept(t, out)
		})
	}
}

func TestCustomRulesOfEveryKindAreAccepted(t *testing.T) {
	in := configgen.Input{
		Nodes: nodes(t), Prefs: configgen.DefaultPrefs(), RuleSets: ruleSets(t),
		Rules: []configgen.CustomRule{
			{Kind: configgen.KindDomain, Value: "Exact.Example.com", Target: configgen.TargetDirect},
			{Kind: configgen.KindDomainSuffix, Value: "*.corp.example", Target: configgen.TargetDirect},
			{Kind: configgen.KindDomainSuffix, Value: ".stream.example", Target: "region-HK"},
			{Kind: configgen.KindDomainKeyword, Value: "tracker", Target: configgen.TargetReject},
			{Kind: configgen.KindDomainRegex, Value: `^ads?\d+\.example\.com$`, Target: configgen.TargetReject},
			{Kind: configgen.KindIPCIDR, Value: "203.0.113.0/24", Target: configgen.TargetProxy},
			{Kind: configgen.KindIPCIDR, Value: "2001:db8::/32", Target: configgen.TargetDirect},
			{Kind: configgen.KindProcessName, Value: "curl", Target: configgen.TargetProxy},
			{Kind: configgen.KindPort, Value: "22", Target: configgen.TargetDirect},
			{Kind: configgen.KindPort, Value: "8000-9000", Target: "US 01"},
		},
	}
	out := generate(t, in)
	if len(out.Warnings) != 0 {
		t.Fatalf("warnings: %+v", out.Warnings)
	}
	accept(t, out)

	var cfg map[string]any
	_ = json.Unmarshal(out.JSON, &cfg)
	route := cfg["route"].(map[string]any)
	if route["find_process"] != true {
		t.Fatal("a process rule needs find_process")
	}
	// Exactly one resolve action sits before the first address rule, ahead of the preset's own.
	resolves := 0
	sawIPRule := false
	for _, r := range route["rules"].([]any) {
		m := r.(map[string]any)
		if m["action"] == "resolve" {
			resolves++
			if sawIPRule {
				t.Fatal("resolve must come before the first address rule")
			}
		}
		if _, ok := m["ip_cidr"]; ok {
			sawIPRule = true
		}
	}
	if resolves != 1 {
		t.Fatalf("resolve actions = %d, want 1", resolves)
	}
	if !strings.Contains(string(out.JSON), `"exact.example.com"`) || !strings.Contains(string(out.JSON), `"corp.example"`) || !strings.Contains(string(out.JSON), `"stream.example"`) {
		t.Fatal("domain values were not normalised")
	}
}

func TestInvalidCustomRulesBecomeWarnings(t *testing.T) {
	out := generate(t, configgen.Input{
		Nodes: nodes(t), Prefs: configgen.DefaultPrefs(), RuleSets: ruleSets(t),
		Rules: []configgen.CustomRule{
			{Kind: configgen.KindDomain, Value: "", Target: configgen.TargetDirect},
			{Kind: configgen.KindIPCIDR, Value: "not-an-ip", Target: configgen.TargetDirect},
			{Kind: configgen.KindDomainRegex, Value: "([", Target: configgen.TargetDirect},
			{Kind: configgen.KindPort, Value: "70000", Target: configgen.TargetDirect},
			{Kind: "bogus", Value: "x", Target: configgen.TargetDirect},
			{Kind: configgen.KindDomain, Value: "ok.example", Target: "no-such-group"},
			{Kind: configgen.KindDomain, Value: "fine.example", Target: configgen.TargetDirect},
		},
	})
	codes := map[string]int{}
	for _, w := range out.Warnings {
		codes[w.Code]++
	}
	if codes[configgen.WarnRuleInvalid] != 5 || codes[configgen.WarnRuleTargetAbsent] != 1 {
		t.Fatalf("warnings = %+v", out.Warnings)
	}
	if strings.Contains(string(out.JSON), "ok.example") || !strings.Contains(string(out.JSON), "fine.example") {
		t.Fatal("only the valid rule may be rendered")
	}
	accept(t, out)
}

func TestMissingRuleSetsDegradeWithWarnings(t *testing.T) {
	prefs := configgen.DefaultPrefs()
	prefs.BlockAds = true
	out := generate(t, configgen.Input{Nodes: nodes(t), Prefs: prefs})
	missing := map[string]bool{}
	for _, w := range out.Warnings {
		if w.Code == configgen.WarnRuleSetMissing {
			missing[w.Detail] = true
		}
	}
	for _, id := range []string{configgen.RuleSetCN, configgen.RuleSetCNIP, configgen.RuleSetAds} {
		if !missing[id] {
			t.Fatalf("no warning for %s: %+v", id, out.Warnings)
		}
	}
	if len(out.Resources) != 0 || strings.Contains(string(out.JSON), "rule_set") {
		t.Fatal("nothing may reference a rule set that is not there")
	}
	accept(t, out)

	// A partial set still gets used.
	sets := ruleSets(t)
	delete(sets, configgen.RuleSetCNIP)
	out = generate(t, configgen.Input{Nodes: nodes(t), Prefs: configgen.DefaultPrefs(), RuleSets: sets})
	if len(out.Resources) != 1 || out.Resources[0].Name != "geosite-cn.srs" {
		t.Fatalf("resources = %+v", out.Resources)
	}
	accept(t, out)
}

func TestSelectedNodeAndTags(t *testing.T) {
	ns := nodes(t)
	out := generate(t, configgen.Input{Nodes: ns, Prefs: configgen.DefaultPrefs(), RuleSets: ruleSets(t), Selected: ns[2].Key()})

	var cfg struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(out.JSON, &cfg); err != nil {
		t.Fatal(err)
	}
	byTag := map[string]map[string]any{}
	for _, o := range cfg.Outbounds {
		tag := o["tag"].(string)
		if _, dup := byTag[tag]; dup {
			t.Fatalf("duplicate tag %q", tag)
		}
		byTag[tag] = o
	}
	proxy := byTag["Proxy"]
	if proxy["type"] != "selector" || proxy["default"] != out.Tags[ns[2].Key()] {
		t.Fatalf("selector = %v, want default %q", proxy, out.Tags[ns[2].Key()])
	}
	// Nodes named like fixed tags are renamed, never merged into them.
	for _, n := range ns {
		tag := out.Tags[n.Key()]
		if n.Name == "Proxy" || n.Name == "direct" {
			if tag == n.Name {
				t.Fatalf("node %q kept a reserved tag", n.Name)
			}
		}
		if _, ok := byTag[tag]; !ok && !n.Endpoint {
			t.Fatalf("node %q has no outbound %q", n.Name, tag)
		}
	}
	if byTag["direct"]["type"] != "direct" {
		t.Fatal("the direct outbound must stay")
	}

	// Without a selection (or with an unknown one) the fastest node is the default.
	for _, sel := range []string{"", "no-such-key"} {
		out = generate(t, configgen.Input{Nodes: ns, Prefs: configgen.DefaultPrefs(), RuleSets: ruleSets(t), Selected: sel})
		if !strings.Contains(string(out.JSON), `"default": "Auto"`) {
			t.Fatalf("selected %q must fall back to Auto", sel)
		}
	}
}

func TestRegionGroups(t *testing.T) {
	out := generate(t, configgen.Input{Nodes: nodes(t), Prefs: configgen.DefaultPrefs(), RuleSets: ruleSets(t)})
	want := []string{"Proxy", "Auto", "region-HK", "region-JP", "region-US"}
	if singbox.QUICCompiled {
		want = []string{"Proxy", "Auto", "region-HK", "region-JP", "region-KR", "region-US"}
	}
	if strings.Join(out.Groups, ",") != strings.Join(want, ",") {
		t.Fatalf("groups = %v, want %v", out.Groups, want)
	}
	prefs := configgen.DefaultPrefs()
	prefs.RegionGroups = false
	out = generate(t, configgen.Input{Nodes: nodes(t), Prefs: prefs, RuleSets: ruleSets(t)})
	if len(out.Groups) != 2 {
		t.Fatalf("groups = %v", out.Groups)
	}
}

func TestModesExistInEveryPreset(t *testing.T) {
	for _, preset := range []configgen.Preset{configgen.PresetChina, configgen.PresetProxyAll, configgen.PresetForeignOnly} {
		prefs := configgen.DefaultPrefs()
		prefs.Preset = preset
		out := generate(t, configgen.Input{Nodes: nodes(t), Prefs: prefs, RuleSets: ruleSets(t)})
		var cfg map[string]any
		_ = json.Unmarshal(out.JSON, &cfg)
		modes := map[string]bool{}
		collect := func(rules any) {
			for _, r := range rules.([]any) {
				if m, ok := r.(map[string]any)["clash_mode"].(string); ok {
					modes[m] = true
				}
			}
		}
		collect(cfg["route"].(map[string]any)["rules"])
		collect(cfg["dns"].(map[string]any)["rules"])
		if !modes["Direct"] || !modes["Global"] {
			t.Fatalf("%s: Direct and Global must be switchable at runtime, got %v", preset, modes)
		}
	}
}

func TestOutputIsDeterministicAndInputIsUntouched(t *testing.T) {
	ns := nodes(t)
	before, _ := json.Marshal(ns)
	in := configgen.Input{Nodes: ns, Prefs: configgen.DefaultPrefs(), RuleSets: ruleSets(t)}
	a := generate(t, in)
	b := generate(t, in)
	if !bytes.Equal(a.JSON, b.JSON) {
		t.Fatal("the same input must give the same bytes")
	}
	after, _ := json.Marshal(ns)
	if !bytes.Equal(before, after) {
		t.Fatal("Generate modified its input")
	}
}

func TestNoNodes(t *testing.T) {
	if _, err := configgen.Generate(configgen.Input{Prefs: configgen.DefaultPrefs()}); err != configgen.ErrNoNodes {
		t.Fatalf("err = %v", err)
	}
	// Nodes that fail validation count as none.
	bad := subscription.Node{Name: "x", Type: "trojan", Server: "", Port: 443, Config: map[string]any{"type": "trojan"}}
	if _, err := configgen.Generate(configgen.Input{Nodes: []subscription.Node{bad}, Prefs: configgen.DefaultPrefs()}); err != configgen.ErrNoNodes {
		t.Fatalf("err = %v", err)
	}
}

func TestPrefsNormalizeClampsAndReports(t *testing.T) {
	p, notes := configgen.Prefs{
		Preset: "weird", DomesticDNS: "ftp://x", RemoteDNS: "https://", TunStack: "nope", MTU: 99999,
		MixedPort: 70000, TestURL: "ftp://x", TestIntervalMin: 99999, ToleranceMs: 99999,
	}.Normalize()
	if len(notes) != 7 {
		t.Fatalf("notes = %q", notes)
	}
	d := configgen.DefaultPrefs()
	if p.Preset != d.Preset || p.DomesticDNS != d.DomesticDNS || p.RemoteDNS != d.RemoteDNS || p.TunStack != d.TunStack ||
		p.MTU != d.MTU || p.MixedPort != 0 || p.TestURL != d.TestURL || p.TestIntervalMin != 24*60 || p.ToleranceMs != 5000 {
		t.Fatalf("prefs = %+v", p)
	}
	zero, notes := configgen.Prefs{}.Normalize()
	if len(notes) != 0 || zero.Preset != d.Preset || zero.MTU != d.MTU || zero.TestIntervalMin != d.TestIntervalMin {
		t.Fatalf("zero prefs = %+v %q", zero, notes)
	}
}

func TestHealDropsOnlyTheNodeTheAgentRejects(t *testing.T) {
	ns := nodes(t)
	// A node whose settings the agent's policy refuses: a certificate read from a file path.
	var evil subscription.Node
	for _, n := range ns {
		if n.Name == "HK 02" {
			evil = n.Clone()
		}
	}
	evil.Name = "Evil"
	evil.Config["tls"].(map[string]any)["certificate_path"] = "/etc/passwd"
	evil.Config["server"] = "evil.example.com"
	evil.Server = "evil.example.com"
	in := configgen.Input{Nodes: append([]subscription.Node{evil}, ns...), Prefs: configgen.DefaultPrefs(), RuleSets: ruleSets(t)}

	out := generate(t, in)
	pol := policy.New(singbox.PolicyOptions(singbox.RegistryOptions{}))
	dir := t.TempDir()
	res := map[string]bool{}
	for _, r := range out.Resources {
		res[r.Name] = true
	}
	env := policy.Env{ResourceDir: dir, Resources: res, CachePath: filepath.Join(dir, "c.db"), Tun: policy.TunSettings{Addresses: []string{"172.19.0.1/30"}}}
	result := pol.Normalize(out.JSON, env)
	if !result.HasErrors() {
		t.Fatal("the policy should reject the node")
	}
	var issues []configgen.PathIssue
	for _, is := range result.Errors() {
		issues = append(issues, configgen.PathIssue{Path: is.Path, Message: is.Code})
	}
	healed, dropped, changed := configgen.Heal(in, out, issues)
	if !changed || len(dropped) != 1 || dropped[0].Name != "Evil" {
		t.Fatalf("dropped = %+v (issues %+v)", dropped, issues)
	}
	if len(healed.Nodes) != len(in.Nodes)-1 || healed.Nodes[0].Name == "Evil" {
		t.Fatalf("nodes: %d -> %d", len(in.Nodes), len(healed.Nodes))
	}
	accept(t, generate(t, healed))

	// Issues that do not point at a node heal nothing.
	if _, _, changed := configgen.Heal(in, out, []configgen.PathIssue{{Path: "$.dns.servers[0]"}, {Path: "$.outbounds[0]"}, {Path: "$.outbounds[999]"}}); changed {
		t.Fatal("only node-level issues are healable")
	}
}

func TestDNSServerSpecs(t *testing.T) {
	for spec, want := range map[string]string{
		"223.5.5.5":                      `{"server":"223.5.5.5","type":"udp"}`,
		"udp://119.29.29.29:5353":        `{"server":"119.29.29.29","server_port":5353,"type":"udp"}`,
		"tcp://8.8.8.8":                  `{"server":"8.8.8.8","type":"tcp"}`,
		"tls://dot.pub":                  `{"server":"dot.pub","type":"tls"}`,
		"https://dns.google/dns-query":   `{"server":"dns.google","type":"https"}`,
		"https://dns.example/custom":     `{"path":"/custom","server":"dns.example","type":"https"}`,
		"https://[2606:4700:4700::1111]": `{"server":"2606:4700:4700::1111","type":"https"}`,
		"local":                          `{"type":"local"}`,
	} {
		prefs := configgen.DefaultPrefs()
		prefs.DomesticDNS = spec
		out := generate(t, configgen.Input{Nodes: nodes(t), Prefs: prefs, RuleSets: ruleSets(t)})
		var cfg struct {
			DNS struct {
				Servers []map[string]any `json:"servers"`
			} `json:"dns"`
		}
		_ = json.Unmarshal(out.JSON, &cfg)
		var got map[string]any
		for _, s := range cfg.DNS.Servers {
			if s["tag"] == "dns-domestic" {
				got = s
			}
		}
		delete(got, "tag")
		delete(got, "domain_resolver")
		raw, _ := json.Marshal(got)
		if string(raw) != want {
			t.Errorf("%s -> %s, want %s", spec, raw, want)
		}
	}
	// Servers named by host get a bootstrap resolver; literal addresses do not.
	prefs := configgen.DefaultPrefs()
	prefs.DomesticDNS = "tls://dot.pub"
	out := generate(t, configgen.Input{Nodes: nodes(t), Prefs: prefs, RuleSets: ruleSets(t)})
	if !strings.Contains(string(out.JSON), `"domain_resolver": "dns-bootstrap"`) {
		t.Fatal("a DNS server named by host needs a bootstrap resolver")
	}
	out = generate(t, configgen.Input{Nodes: nodes(t), Prefs: configgen.DefaultPrefs(), RuleSets: ruleSets(t)})
	if strings.Contains(string(out.JSON), `"domain_resolver": "dns-bootstrap"`) {
		t.Fatal("literal addresses need no bootstrap resolver")
	}
}

func TestSelectedMayNameARegionGroup(t *testing.T) {
	ns := nodes(t)
	out := generate(t, configgen.Input{Nodes: ns, Prefs: configgen.DefaultPrefs(), RuleSets: ruleSets(t), Selected: "region-HK"})
	var cfg struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(out.JSON, &cfg); err != nil {
		t.Fatal(err)
	}
	for _, o := range cfg.Outbounds {
		if o["tag"] == "Proxy" && o["default"] != "region-HK" {
			t.Fatalf("selector default = %v, want the region group", o["default"])
		}
	}
	// A group that does not exist, and the selector itself, fall back to Auto.
	for _, sel := range []string{"region-ZZ", "Proxy"} {
		out = generate(t, configgen.Input{Nodes: ns, Prefs: configgen.DefaultPrefs(), RuleSets: ruleSets(t), Selected: sel})
		if err := json.Unmarshal(out.JSON, &cfg); err != nil {
			t.Fatal(err)
		}
		for _, o := range cfg.Outbounds {
			if o["tag"] == "Proxy" && o["default"] != "Auto" {
				t.Fatalf("selected %q: default = %v, want Auto", sel, o["default"])
			}
		}
	}
}

func TestRequiredRuleSetsFollowThePreferences(t *testing.T) {
	p := configgen.DefaultPrefs()
	got := configgen.RequiredRuleSets(p)
	if len(got) != 2 || got[0] != configgen.RuleSetCN || got[1] != configgen.RuleSetCNIP {
		t.Errorf("china preset: %v", got)
	}
	p.BlockAds = true
	if got := configgen.RequiredRuleSets(p); len(got) != 3 || got[2] != configgen.RuleSetAds {
		t.Errorf("with ad blocking: %v", got)
	}
	p.Preset = configgen.PresetProxyAll
	if got := configgen.RequiredRuleSets(p); len(got) != 1 || got[0] != configgen.RuleSetAds {
		t.Errorf("proxy-all with ad blocking: %v", got)
	}
	p.Preset, p.BlockAds = configgen.PresetForeignOnly, false
	if got := configgen.RequiredRuleSets(p); len(got) != 1 || got[0] != configgen.RuleSetForeign {
		t.Errorf("foreign-only: %v", got)
	}
}
