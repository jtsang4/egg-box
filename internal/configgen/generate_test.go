package configgen_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"golang.org/x/crypto/ssh"

	"eggbox/internal/agent/engine"
	"eggbox/internal/agent/engine/singbox"
	"eggbox/internal/agent/policy"
	"eggbox/internal/agent/store"
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
		"snell://test-psk-0001@sg2.example.com:1366?version=6&mode=default&reuse=true#SG%2002",
		"snell://pw@sg3.example.com:1367?version=4&obfs=http&obfs-host=www.example.org#SG%2003",
		"ss://" + b64("aes-128-gcm:pw") + "@st.example.com:443/?plugin=shadow-tls%3Bhost%3Dwww.example.org%3Bpassword%3Dstpw%3Bversion%3D3#SG%2004",
		"ssh://u:pw@ssh1.example.com:22#SG%2005",
		"https://u:pw@hp.example.com:8443?sni=hp.example.com#HTTPS%20proxy",
		"ssh://u@ssh2.example.com:2222?private-key=" + url.QueryEscape(testSSHKey()) + "&host-key=" + url.QueryEscape(testSSHHostKey()) + "#SG%2006",
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

// testSSHKey is a made-up ed25519 key in OpenSSH's PEM form. It is made once:
// the encoding carries random check bytes, and the fixtures must not change
// between two generations.
var testSSHKey = sync.OnceValue(func() string {
	block, err := ssh.MarshalPrivateKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{4}, 32)), "")
	if err != nil {
		panic(err)
	}
	return string(pem.EncodeToMemory(block))
})

func testSSHHostKey() string {
	pub, err := ssh.NewPublicKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{5}, 32)).Public())
	if err != nil {
		panic(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
}

func b64(s string) string {
	return strings.TrimRight(base64.StdEncoding.EncodeToString([]byte(s)), "=")
}

func subNodes(t *testing.T) []subscription.Node {
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

// nodes are the fixtures as the generator takes them, regions recognised from the names.
func nodes(t *testing.T) []configgen.Node {
	t.Helper()
	var out []configgen.Node
	for _, n := range subNodes(t) {
		out = append(out, configgen.Node{Node: n, Region: subscription.Region(n.Name)})
	}
	return out
}

func nodeNamed(t *testing.T, ns []configgen.Node, name string) configgen.Node {
	t.Helper()
	for _, n := range ns {
		if n.Name == name {
			return n
		}
	}
	t.Fatalf("no node named %q", name)
	return configgen.Node{}
}

func main(k configgen.ExitKind) configgen.Exit { return configgen.Exit{Kind: k} }
func region(code string) configgen.Exit {
	return configgen.Exit{Kind: configgen.ExitRegion, Region: code}
}
func pin(n configgen.Node) configgen.Exit {
	return configgen.Exit{Kind: configgen.ExitNode, Node: n.Key()}
}

// domainItem is an item with a host-name part that also steers DNS.
func domainItem(t *testing.T, id string, exit configgen.Exit, suffixes ...string) configgen.Item {
	t.Helper()
	data := srs.DomainSuffixes(t, suffixes...)
	return configgen.Item{ID: id, Name: "Item " + id, Exit: exit, Domain: data, DNS: data}
}

func ipItem(t *testing.T, id string, exit configgen.Exit, cidrs ...string) configgen.Item {
	t.Helper()
	return configgen.Item{ID: id, Name: "Item " + id, Exit: exit, IP: srs.CIDRs(t, cidrs...)}
}

func hostRule(kind string, values ...string) option.HeadlessRule {
	d := option.DefaultHeadlessRule{}
	switch kind {
	case "domain":
		d.Domain = values
	case "suffix":
		d.DomainSuffix = values
	case "keyword":
		d.DomainKeyword = values
	case "regex":
		d.DomainRegex = values
	case "cidr":
		d.IPCIDR = values
	case "process":
		d.ProcessName = values
	case "port":
		for _, v := range values {
			var n uint16
			fmt.Sscan(v, &n)
			d.Port = append(d.Port, n)
		}
	case "portrange":
		d.PortRange = values
	}
	return option.HeadlessRule{Type: C.RuleTypeDefault, DefaultOptions: d}
}

func exception(id string, rule option.HeadlessRule, exit configgen.Exit) configgen.Exception {
	return configgen.Exception{ID: id, Rule: rule, Exit: exit}
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

// baseInput is a plan of a few items over the fixture nodes.
func baseInput(t *testing.T) configgen.Input {
	t.Helper()
	return configgen.Input{
		Nodes: nodes(t), Prefs: configgen.DefaultPrefs(), Main: main(configgen.ExitAuto),
		Items: []configgen.Item{
			domainItem(t, "ai", main(configgen.ExitMain), "ai.example"),
			domainItem(t, "ads", main(configgen.ExitReject), "ads.example"),
			domainItem(t, "cn", main(configgen.ExitDirect), "example.cn", "baidu.com"),
			ipItem(t, "cnip", main(configgen.ExitDirect), "198.51.100.0/24"),
		},
		Fallback: main(configgen.ExitMain),
	}
}

type config struct {
	DNS struct {
		Servers []map[string]any `json:"servers"`
		Rules   []map[string]any `json:"rules"`
		Final   string           `json:"final"`
	} `json:"dns"`
	Outbounds []map[string]any `json:"outbounds"`
	Route     struct {
		Rules       []map[string]any `json:"rules"`
		RuleSet     []map[string]any `json:"rule_set"`
		Final       string           `json:"final"`
		FindProcess bool             `json:"find_process"`
	} `json:"route"`
}

func parse(t *testing.T, out *configgen.Output) config {
	t.Helper()
	var c config
	if err := json.Unmarshal(out.JSON, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func (c config) outbound(tag string) map[string]any {
	for _, o := range c.Outbounds {
		if o["tag"] == tag {
			return o
		}
	}
	return nil
}

// describe renders a route or DNS rule as "match=>target" for comparisons.
func describe(r map[string]any) string {
	var what string
	switch {
	case r["rule_set"] != nil:
		what = "set:" + r["rule_set"].([]any)[0].(string)
	case r["clash_mode"] != nil:
		what = "mode:" + r["clash_mode"].(string)
	case r["protocol"] != nil:
		what = "protocol:" + r["protocol"].(string)
	case r["ip_is_private"] != nil:
		what = "private"
	case r["type"] == "logical":
		what = "private-unclaimed"
	case r["domain_suffix"] != nil:
		what = "suffix"
	default:
		// A rule that matches everything: sniff, resolve, a closing reject.
		return "action:" + fmt.Sprint(r["action"])
	}
	switch {
	case r["action"] == "reject":
		return what + "=>reject"
	case r["action"] != nil && r["outbound"] == nil && r["server"] == nil:
		return what + "=>" + r["action"].(string)
	case r["outbound"] != nil:
		return what + "=>" + r["outbound"].(string)
	case r["server"] != nil:
		return what + "=>" + r["server"].(string)
	}
	return what
}

func describeAll(rules []map[string]any) []string {
	var out []string
	for _, r := range rules {
		out = append(out, describe(r))
	}
	return out
}

// ----------------------------------------------------------------- tests

func TestGeneratedConfigurationsAreAccepted(t *testing.T) {
	ns := nodes(t)
	hk := nodeNamed(t, ns, "HK 02")
	base := configgen.DefaultPrefs()
	cases := map[string]configgen.Prefs{"defaults": base}
	v6 := base
	v6.IPv6, v6.MixedPort, v6.Sniff, v6.TunStack, v6.MTU = true, 7890, false, "mixed", 1400
	cases["ipv6, mixed port, no sniff"] = v6
	lan := base
	lan.LANDirect = false
	cases["no LAN rules"] = lan
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
			in := baseInput(t)
			in.Prefs = prefs
			out := generate(t, in)
			if len(out.Warnings) != 0 {
				t.Fatalf("unexpected warnings: %+v", out.Warnings)
			}
			accept(t, out)
		})
	}

	exits := map[string]configgen.Exit{
		"main": main(configgen.ExitMain), "direct": main(configgen.ExitDirect), "reject": main(configgen.ExitReject),
		"auto": main(configgen.ExitAuto), "region": region("HK"), "node": pin(hk),
		"node, no stand-in": {Kind: configgen.ExitNode, Node: hk.Key(), NoStandIn: true},
	}
	for name, exit := range exits {
		t.Run("item exit "+name, func(t *testing.T) {
			in := baseInput(t)
			in.Items[0].Exit = exit
			out := generate(t, in)
			if len(out.Warnings) != 0 {
				t.Fatalf("unexpected warnings: %+v", out.Warnings)
			}
			accept(t, out)
		})
		t.Run("fallback "+name, func(t *testing.T) {
			in := baseInput(t)
			in.Fallback = exit
			out := generate(t, in)
			if len(out.Warnings) != 0 {
				t.Fatalf("unexpected warnings: %+v", out.Warnings)
			}
			accept(t, out)
		})
		t.Run("exception exit "+name, func(t *testing.T) {
			in := baseInput(t)
			in.Exceptions = []configgen.Exception{exception("e1", hostRule("suffix", "e.example"), exit)}
			out := generate(t, in)
			if len(out.Warnings) != 0 {
				t.Fatalf("unexpected warnings: %+v", out.Warnings)
			}
			accept(t, out)
		})
	}
	for name, exit := range map[string]configgen.Exit{"auto": main(configgen.ExitAuto), "region": region("JP"), "node": pin(hk), "node, no stand-in": {Kind: configgen.ExitNode, Node: hk.Key(), NoStandIn: true}} {
		t.Run("main exit "+name, func(t *testing.T) {
			in := baseInput(t)
			in.Main = exit
			out := generate(t, in)
			if len(out.Warnings) != 0 {
				t.Fatalf("unexpected warnings: %+v", out.Warnings)
			}
			accept(t, out)
		})
	}
}

func TestExceptionsOfEveryKindAreAccepted(t *testing.T) {
	in := baseInput(t)
	in.Exceptions = []configgen.Exception{
		exception("e1", hostRule("domain", "exact.example.com"), main(configgen.ExitDirect)),
		exception("e2", hostRule("suffix", "corp.example"), main(configgen.ExitDirect)),
		exception("e3", hostRule("suffix", "stream.example"), region("HK")),
		exception("e4", hostRule("keyword", "tracker"), main(configgen.ExitReject)),
		exception("e5", hostRule("regex", `^ads?\d+\.example\.com$`), main(configgen.ExitReject)),
		exception("e6", hostRule("cidr", "203.0.113.0/24"), main(configgen.ExitMain)),
		exception("e7", hostRule("cidr", "2001:db8::/32"), main(configgen.ExitDirect)),
		exception("e8", hostRule("process", "curl"), main(configgen.ExitMain)),
		exception("e9", hostRule("portrange", "8000:9000"), main(configgen.ExitDirect)),
		exception("e10", hostRule("port", "22"), main(configgen.ExitDirect)),
	}
	out := generate(t, in)
	if len(out.Warnings) != 0 {
		t.Fatalf("warnings: %+v", out.Warnings)
	}
	accept(t, out)
	c := parse(t, out)
	if !c.Route.FindProcess {
		t.Fatal("a process rule needs find_process")
	}
	resolves := 0
	for _, r := range c.Route.Rules {
		if r["action"] == "resolve" {
			resolves++
		}
	}
	if resolves != 1 {
		t.Fatalf("resolve actions = %d, want exactly one for exceptions and items together", resolves)
	}
	for _, id := range []string{"e1", "e9"} {
		if o := out.Owners["x-"+id]; o.Kind != configgen.OwnerException || o.ID != id {
			t.Errorf("owner of x-%s = %+v", id, o)
		}
	}
}

func TestRuleLayers(t *testing.T) {
	in := baseInput(t)
	in.Items[2].Domestic = true // the "cn" item is made of local services
	in.Items = append(in.Items, domainItem(t, "late", main(configgen.ExitMain), "late.example"), ipItem(t, "late-ip", region("JP"), "192.0.2.0/24"))
	in.Exceptions = []configgen.Exception{
		exception("e1", hostRule("suffix", "one.example"), main(configgen.ExitDirect)),
		exception("e2", hostRule("cidr", "203.0.113.0/24"), main(configgen.ExitMain)),
		exception("e3", hostRule("domain", "three.example"), main(configgen.ExitReject)),
		exception("e4", hostRule("process", "curl"), main(configgen.ExitMain)),
		exception("e5", hostRule("port", "22"), main(configgen.ExitDirect)),
	}
	out := generate(t, in)
	accept(t, out)
	c := parse(t, out)

	wantRoute := []string{
		"action:sniff",
		"protocol:dns=>hijack-dns",
		"mode:Direct=>direct",
		"mode:Global=>Proxy",
		"private=>direct",
		"suffix=>direct",
		"set:x-e1=>direct",
		"action:resolve", // once, before the first exception that needs an address
		"set:x-e2=>Proxy",
		"set:x-e3=>reject",
		"set:x-e4=>Proxy",
		"set:x-e5=>direct",
		"set:d-ai=>item-ai",
		"set:d-ads=>reject",
		"set:d-cn=>item-cn",
		"set:d-late=>item-late",
		"set:p-cnip=>item-cnip",
		"set:p-late-ip=>item-late-ip",
	}
	if got := describeAll(c.Route.Rules); !reflect.DeepEqual(got, wantRoute) {
		t.Fatalf("route rules:\n got %v\nwant %v", got, wantRoute)
	}
	if c.Route.Final != "item-fallback" {
		t.Errorf("final = %q", c.Route.Final)
	}

	// DNS follows the host-name rules: the names of local services and of
	// exceptions that go direct ask the domestic server, blocked ones are
	// refused, all others ask the remote one. Rules that are about addresses,
	// processes or ports do not take part.
	wantDNS := []string{
		"mode:Direct=>dns-domestic",
		"mode:Global=>dns-remote",
		"set:x-e1=>dns-domestic",
		"set:x-e3=>reject",
		"set:d-ai=>dns-remote",
		"set:d-ads=>reject",
		"set:d-cn=>dns-domestic",
		"set:d-late=>dns-remote",
	}
	if got := describeAll(c.DNS.Rules); !reflect.DeepEqual(got, wantDNS) {
		t.Fatalf("DNS rules:\n got %v\nwant %v", got, wantDNS)
	}
	if c.DNS.Final != "dns-remote" {
		t.Errorf("DNS final = %q", c.DNS.Final)
	}

	// With no exception that needs an address, the one resolve comes between
	// the host-name rules and the address rules.
	in.Exceptions = in.Exceptions[:1]
	c = parse(t, generate(t, in))
	got := describeAll(c.Route.Rules)
	at := -1
	for i, d := range got {
		if d == "action:resolve" {
			if at >= 0 {
				t.Fatal("two resolves")
			}
			at = i
		}
	}
	if at < 0 || got[at-1] != "set:d-late=>item-late" || got[at+1] != "set:p-cnip=>item-cnip" {
		t.Fatalf("resolve is in the wrong place: %v", got)
	}

	// No address rule anywhere: no resolve at all.
	in.Items = in.Items[:3]
	for _, r := range parse(t, generate(t, in)).Route.Rules {
		if r["action"] == "resolve" {
			t.Fatal("a resolve nobody needs")
		}
	}
}

func TestFallbacks(t *testing.T) {
	cases := []struct {
		exit      configgen.Exit
		final     string
		dnsFinal  string
		lastRule  string
		hasGroup  bool
		wantGroup string
	}{
		{main(configgen.ExitMain), "item-fallback", "dns-remote", "", true, "Proxy"},
		{region("HK"), "item-fallback", "dns-remote", "", true, "region-HK"},
		{main(configgen.ExitDirect), "direct", "dns-domestic", "", false, ""},
		{main(configgen.ExitReject), "direct", "dns-remote", "action:reject", false, ""},
	}
	for _, c := range cases {
		in := baseInput(t)
		in.Fallback = c.exit
		out := generate(t, in)
		accept(t, out)
		cfg := parse(t, out)
		if cfg.Route.Final != c.final || cfg.DNS.Final != c.dnsFinal {
			t.Errorf("%v: final %q / %q, want %q / %q", c.exit, cfg.Route.Final, cfg.DNS.Final, c.final, c.dnsFinal)
		}
		rules := describeAll(cfg.Route.Rules)
		if c.lastRule != "" && rules[len(rules)-1] != c.lastRule {
			t.Errorf("%v: the last rule is %q, want %q", c.exit, rules[len(rules)-1], c.lastRule)
		}
		if c.lastRule == "" && strings.HasPrefix(rules[len(rules)-1], "action:reject") {
			t.Errorf("%v: unexpected reject at the end", c.exit)
		}
		if g := cfg.outbound("item-fallback"); (g != nil) != c.hasGroup {
			t.Errorf("%v: fallback group present = %v", c.exit, g != nil)
		} else if g != nil && out.Selections["item-fallback"] != c.wantGroup {
			t.Errorf("%v: selected %q, want %q", c.exit, out.Selections["item-fallback"], c.wantGroup)
		}
	}
}

func TestGroupsAndSelections(t *testing.T) {
	ns := nodes(t)
	hk, us := nodeNamed(t, ns, "HK 02"), nodeNamed(t, ns, "US 01")
	in := baseInput(t)
	in.Main = pin(hk)
	in.Items[0].Exit = region("US")
	in.Items = append(in.Items, domainItem(t, "pinned", pin(us), "pinned.example"))
	out := generate(t, in)
	accept(t, out)
	c := parse(t, out)

	if got, want := out.Selections[configgen.TagProxy], out.Tags[hk.Key()]; got != want {
		t.Errorf("Proxy selects %q, want %q", got, want)
	}
	if got := out.Selections["item-ai"]; got != "region-US" {
		t.Errorf("a region item selects %q", got)
	}
	if got, want := out.Selections["item-pinned"], out.Tags[us.Key()]; got != want {
		t.Errorf("a pinned item selects %q, want %q", got, want)
	}
	if got := out.Selections["item-cn"]; got != "direct" {
		t.Errorf("a direct item selects %q", got)
	}
	if got := out.Selections["item-fallback"]; got != "Proxy" {
		t.Errorf("the fallback selects %q", got)
	}
	if _, ok := out.Selectors["ads"]; ok {
		t.Error("a blocked item has no group")
	}
	if out.Selectors["ai"] != "item-ai" || out.Selectors[configgen.FallbackID] != "item-fallback" {
		t.Errorf("selectors = %v", out.Selectors)
	}

	// The main exit chooses among Auto, the regions and every node; an item
	// among the main exit, direct, Auto, the regions and every node.
	proxy := c.outbound("Proxy")
	members := toStrings(proxy["outbounds"])
	if members[0] != "Auto" || !contains(members, "region-HK") || !contains(members, out.Tags[us.Key()]) || contains(members, "direct") {
		t.Errorf("Proxy members = %v", members)
	}
	item := toStrings(c.outbound("item-ai")["outbounds"])
	if item[0] != "Proxy" || item[1] != "direct" || !contains(item, "Auto") || !contains(item, "region-JP") || len(item) != 3+len(out.Groups)-2+len(ns) {
		t.Errorf("item members = %v", item)
	}
	if proxy["type"] != "preferred" || c.outbound("item-ai")["type"] != "preferred" {
		t.Error("the groups a person chooses in are preferred groups")
	}

	// Regions: one group for every region with a node, each a url-test over its nodes.
	for _, code := range []string{"HK", "JP", "US", "SG"} {
		g := c.outbound("region-" + code)
		if g == nil || g["type"] != "urltest" {
			t.Errorf("region %s has no group", code)
		}
	}
	if g := c.outbound("region-HK"); len(toStrings(g["outbounds"])) < 2 {
		t.Errorf("region-HK = %v", g)
	}
	want := []string{"Proxy", "Auto", "region-HK", "region-JP", "region-SG", "region-US"}
	if singbox.QUICCompiled {
		want = []string{"Proxy", "Auto", "region-HK", "region-JP", "region-KR", "region-SG", "region-US"}
	}
	if !reflect.DeepEqual(out.Groups, want) {
		t.Errorf("groups = %v, want %v", out.Groups, want)
	}
}

func TestStandIns(t *testing.T) {
	ns := nodes(t)
	hk := nodeNamed(t, ns, "HK 02")
	in := baseInput(t)
	in.Items[0].Exit = pin(hk)
	c := parse(t, generate(t, in))
	for _, tag := range []string{"Proxy", "item-ai", "item-cn", "item-fallback"} {
		if got := toStrings(c.outbound(tag)["stand_ins"]); !contains(got, "region-HK") || !contains(got, "region-US") {
			t.Errorf("%s: stand-ins = %v", tag, got)
		}
	}
	// Nothing to stand in with: the groups are plain choosers.
	in.Nodes = nil
	for _, n := range nodes(t) {
		n.Region = ""
		in.Nodes = append(in.Nodes, n)
	}
	in.Items[0].Exit = main(configgen.ExitMain)
	c = parse(t, generate(t, in))
	if c.outbound("item-ai")["stand_ins"] != nil {
		t.Error("no region groups, no stand-ins")
	}

	// A pinned node the person does not want replaced.
	in = baseInput(t)
	in.Items[0].Exit = configgen.Exit{Kind: configgen.ExitNode, Node: hk.Key(), NoStandIn: true}
	c = parse(t, generate(t, in))
	if c.outbound("item-ai")["stand_ins"] != nil {
		t.Error("an exit that opts out must not get stand-ins")
	}
	if c.outbound("item-cn")["stand_ins"] == nil {
		t.Error("the others keep theirs")
	}
}

func TestNodesOutOfTheAutomaticChoice(t *testing.T) {
	ns := nodes(t)
	for i := range ns {
		if strings.HasPrefix(ns[i].Name, "HK") {
			ns[i].NoAuto = true
		}
	}
	in := baseInput(t)
	in.Nodes = ns
	in.Main = main(configgen.ExitRegion)
	in.Main.Region = "HK" // gone: every node of the region is out of the automatic choice
	out := generate(t, in)
	accept(t, out)
	c := parse(t, out)
	if c.outbound("region-HK") != nil {
		t.Error("a region with no node in the automatic choice has no group")
	}
	auto := toStrings(c.outbound("Auto")["outbounds"])
	for _, n := range ns {
		if n.NoAuto && contains(auto, out.Tags[n.Key()]) {
			t.Errorf("%q is in Auto", n.Name)
		}
	}
	excluded := 0
	for _, n := range ns {
		if n.NoAuto {
			excluded++
		}
	}
	if excluded == 0 || len(auto) != len(ns)-excluded {
		t.Errorf("Auto has %d members for %d nodes of which %d are excluded", len(auto), len(ns), excluded)
	}
	// They can still be chosen, and the main exit says it could not use the region.
	if !contains(toStrings(c.outbound("Proxy")["outbounds"]), out.Tags[nodeNamed(t, ns, "HK 02").Key()]) {
		t.Error("an excluded node must stay selectable")
	}
	if got := out.Selections["Proxy"]; got != "Auto" {
		t.Errorf("main exit = %q, want Auto when its region is gone", got)
	}
	if !hasWarning(out, configgen.WarnRegionMissing) {
		t.Errorf("warnings = %+v", out.Warnings)
	}

	// Everything excluded: use everything rather than an empty group.
	for i := range ns {
		ns[i].NoAuto = true
	}
	in.Nodes = ns
	in.Main = main(configgen.ExitAuto)
	out = generate(t, in)
	accept(t, out)
	if !hasWarning(out, configgen.WarnAutoEmpty) || len(toStrings(parse(t, out).outbound("Auto")["outbounds"])) != len(ns) {
		t.Errorf("warnings = %+v", out.Warnings)
	}
}

func TestExitsThatCannotBeHonoured(t *testing.T) {
	in := baseInput(t)
	in.Main = configgen.Exit{Kind: configgen.ExitNode, Node: "gone"}
	in.Items[0].Exit = region("ZZ")
	in.Items[1].Exit = configgen.Exit{Kind: configgen.ExitNode, Node: "gone"}
	in.Items[2].Exit = main(configgen.ExitMain)
	in.Exceptions = []configgen.Exception{
		exception("e1", hostRule("suffix", "a.example"), region("ZZ")),
		exception("e2", hostRule("suffix", "b.example"), configgen.Exit{Kind: configgen.ExitNode, Node: "gone"}),
	}
	out := generate(t, in)
	accept(t, out)
	codes := map[string]int{}
	for _, w := range out.Warnings {
		codes[w.Code]++
	}
	if codes[configgen.WarnNodeMissing] != 3 || codes[configgen.WarnRegionMissing] != 2 {
		t.Errorf("warnings = %+v", out.Warnings)
	}
	if out.Selections["Proxy"] != "Auto" || out.Selections["item-ai"] != "Proxy" {
		t.Errorf("selections = %v", out.Selections)
	}
	rules := describeAll(parse(t, out).Route.Rules)
	if !contains(rules, "set:x-e1=>Proxy") || !contains(rules, "set:x-e2=>Proxy") {
		t.Errorf("an exception to nowhere follows the main exit: %v", rules)
	}
}

func TestStaticNodePinsShareAGroup(t *testing.T) {
	ns := nodes(t)
	hk := nodeNamed(t, ns, "HK 02")
	in := baseInput(t)
	in.Exceptions = []configgen.Exception{
		exception("e1", hostRule("suffix", "a.example"), pin(hk)),
		exception("e2", hostRule("suffix", "b.example"), pin(hk)),
		exception("e3", hostRule("suffix", "c.example"), configgen.Exit{Kind: configgen.ExitNode, Node: hk.Key(), NoStandIn: true}),
	}
	out := generate(t, in)
	accept(t, out)
	c := parse(t, out)
	rules := describeAll(c.Route.Rules)
	if !contains(rules, "set:x-e1=>pin-1") || !contains(rules, "set:x-e2=>pin-1") || !contains(rules, "set:x-e3=>"+out.Tags[hk.Key()]) {
		t.Fatalf("rules = %v", rules)
	}
	pinGroup := c.outbound("pin-1")
	if pinGroup == nil || !reflect.DeepEqual(toStrings(pinGroup["outbounds"]), []string{out.Tags[hk.Key()]}) || !contains(toStrings(pinGroup["stand_ins"]), "region-HK") {
		t.Errorf("pin group = %v", pinGroup)
	}
	if _, selectable := out.Selections["pin-1"]; selectable {
		t.Error("a pin is not something the interface switches")
	}
}

func TestInvalidExceptionsBecomeWarnings(t *testing.T) {
	in := baseInput(t)
	in.Exceptions = []configgen.Exception{
		exception("empty", option.HeadlessRule{Type: C.RuleTypeDefault}, main(configgen.ExitDirect)),
		exception("fine", hostRule("suffix", "fine.example"), main(configgen.ExitDirect)),
		exception("fine", hostRule("suffix", "again.example"), main(configgen.ExitDirect)),
	}
	out := generate(t, in)
	accept(t, out)
	n := 0
	for _, w := range out.Warnings {
		if w.Code == configgen.WarnExceptionInvalid {
			n++
		}
	}
	if n != 2 || !strings.Contains(string(out.JSON), "fine.example") || strings.Contains(string(out.JSON), "again.example") {
		t.Fatalf("warnings = %+v", out.Warnings)
	}
}

// The server that looks up an item's names is chosen by what the item is made
// of, never by where its traffic goes at the moment: the second can change in
// a running core, the first cannot.
func TestDNSServerDoesNotFollowTheItemsExit(t *testing.T) {
	dnsOf := func(local bool, exit configgen.Exit) string {
		in := baseInput(t)
		in.Items = []configgen.Item{domainItem(t, "svc", exit, "svc.example")}
		in.Items[0].Domestic = local
		c := parse(t, generate(t, in))
		for _, r := range describeAll(c.DNS.Rules) {
			if strings.HasPrefix(r, "set:d-svc=>") {
				return strings.TrimPrefix(r, "set:d-svc=>")
			}
		}
		return "none"
	}
	for _, exit := range []configgen.Exit{main(configgen.ExitMain), main(configgen.ExitDirect), main(configgen.ExitAuto), region("JP")} {
		if got := dnsOf(false, exit); got != "dns-remote" {
			t.Errorf("a foreign item (exit %v) asks %s", exit.Kind, got)
		}
		if got := dnsOf(true, exit); got != "dns-domestic" {
			t.Errorf("a local item (exit %v) asks %s", exit.Kind, got)
		}
	}
	for _, local := range []bool{false, true} {
		if got := dnsOf(local, main(configgen.ExitReject)); got != "reject" {
			t.Errorf("a blocked item (local %v) is %s", local, got)
		}
	}
}

func TestSwitches(t *testing.T) {
	ns := nodes(t)
	hk := nodeNamed(t, ns, "HK 02")
	gen := func(edit func(*configgen.Input)) *configgen.Output {
		in := baseInput(t)
		if edit != nil {
			edit(&in)
		}
		return generate(t, in)
	}
	prev := gen(nil)

	// Moving an item between exits is a selection, direct included: which
	// DNS server asks does not depend on where the traffic goes now.
	for name, exit := range map[string]configgen.Exit{
		"to a region": region("HK"), "to a node": pin(hk), "to auto": main(configgen.ExitAuto), "to direct": main(configgen.ExitDirect),
	} {
		next := gen(func(in *configgen.Input) { in.Items[0].Exit = exit })
		sw, ok := configgen.Switches(prev, next)
		if !ok || len(sw) != 1 || sw["item-ai"] == "" {
			t.Errorf("%s: switches = %v, ok = %v", name, sw, ok)
		}
	}
	// So is moving the main exit, and moving both at once.
	next := gen(func(in *configgen.Input) { in.Main = region("US"); in.Fallback = region("JP") })
	if sw, ok := configgen.Switches(prev, next); !ok || !reflect.DeepEqual(sw, map[string]string{"Proxy": "region-US", "item-fallback": "region-JP"}) {
		t.Errorf("main and fallback: %v %v", sw, ok)
	}
	// And nothing changing is nothing to do.
	if sw, ok := configgen.Switches(prev, gen(nil)); !ok || len(sw) != 0 {
		t.Errorf("same input: %v %v", sw, ok)
	}

	// Anything else needs the core to start again: whether an item is blocked,
	// whether the fallback goes direct (that one steers DNS), which items there
	// are, their order, their data, the exceptions, the nodes.
	for name, edit := range map[string]func(*configgen.Input){
		"item is blocked":        func(in *configgen.Input) { in.Items[0].Exit = main(configgen.ExitReject) },
		"blocked item unblocked": func(in *configgen.Input) { in.Items[1].Exit = main(configgen.ExitMain) },
		"fallback goes direct":   func(in *configgen.Input) { in.Fallback = main(configgen.ExitDirect) },
		"item removed":           func(in *configgen.Input) { in.Items = in.Items[1:] },
		"items reordered":        func(in *configgen.Input) { in.Items[0], in.Items[1] = in.Items[1], in.Items[0] },
		"item data changed": func(in *configgen.Input) {
			in.Items[0] = domainItem(t, "ai", main(configgen.ExitMain), "other.example")
		},
		"exception added": func(in *configgen.Input) {
			in.Exceptions = []configgen.Exception{exception("e", hostRule("suffix", "x.example"), main(configgen.ExitDirect))}
		},
		"node removed": func(in *configgen.Input) { in.Nodes = in.Nodes[1:] },
		"pin loses stand-in": func(in *configgen.Input) {
			in.Items[0].Exit = configgen.Exit{Kind: configgen.ExitNode, Node: hk.Key(), NoStandIn: true}
		},
		"preferences": func(in *configgen.Input) { in.Prefs.LANDirect = false },
	} {
		if sw, ok := configgen.Switches(prev, gen(edit)); ok {
			t.Errorf("%s: should need a restart, got switches %v", name, sw)
		}
	}
	// An item that is made of local services goes through a node and back at
	// runtime too.
	a := gen(func(in *configgen.Input) { in.Items[2].Domestic = true })
	b := gen(func(in *configgen.Input) { in.Items[2].Domestic = true; in.Items[2].Exit = main(configgen.ExitMain) })
	if sw, ok := configgen.Switches(a, b); !ok || sw["item-cn"] != "Proxy" {
		t.Errorf("a local item sent through the proxy: %v %v", sw, ok)
	}
	// What the item is made of is part of the configuration, though.
	if sw, ok := configgen.Switches(prev, a); ok {
		t.Errorf("an item that became local needs the core to start again, got %v", sw)
	}
	if _, ok := configgen.Switches(nil, prev); ok {
		t.Error("nothing running, nothing to switch")
	}
}

func TestResourcesAreContentAddressed(t *testing.T) {
	in := baseInput(t)
	shared := srs.DomainSuffixes(t, "shared.example")
	in.Items = []configgen.Item{
		{ID: "a", Name: "A", Exit: main(configgen.ExitMain), Domain: shared, DNS: shared},
		{ID: "b", Name: "B", Exit: main(configgen.ExitDirect), Domain: shared, DNS: shared},
	}
	out := generate(t, in)
	accept(t, out)
	if len(out.Resources) != 1 {
		t.Fatalf("identical rule data must be shipped once, got %d files", len(out.Resources))
	}
	if !strings.HasSuffix(out.Resources[0].Name, ".srs") || len(out.Resources[0].Name) != 16+4 {
		t.Errorf("name = %q", out.Resources[0].Name)
	}
	// Both items still have rule sets of their own, so they can be told apart.
	c := parse(t, out)
	tags := map[string]string{}
	for _, rs := range c.Route.RuleSet {
		tags[rs["tag"].(string)], _ = rs["path"].(string)
	}
	if tags["d-a"] == "" || tags["d-a"] != tags["d-b"] || out.Owners["d-a"].ID != "a" || out.Owners["d-b"].ID != "b" {
		t.Errorf("rule sets = %v owners = %v", tags, out.Owners)
	}

	// The same input gives the same files and names; other data other names.
	again := generate(t, in)
	if out.Resources[0].Name != again.Resources[0].Name {
		t.Error("names must not depend on anything but the content")
	}
	in.Items[1].Domain, in.Items[1].DNS = srs.DomainSuffixes(t, "other.example"), nil
	if got := generate(t, in); len(got.Resources) != 2 {
		t.Errorf("resources = %d", len(got.Resources))
	}

	// A DNS part that differs from the domain part is a rule set of its own.
	in.Items = []configgen.Item{{ID: "a", Name: "A", Exit: main(configgen.ExitMain), Domain: shared, DNS: srs.DomainSuffixes(t, "dns.example")}}
	out = generate(t, in)
	accept(t, out)
	if len(out.Resources) != 2 || out.Owners["h-a"].ID != "a" {
		t.Errorf("resources = %d owners = %v", len(out.Resources), out.Owners)
	}
	dns := describeAll(parse(t, out).DNS.Rules)
	if !contains(dns, "set:h-a=>dns-remote") || contains(dns, "set:d-a=>dns-remote") {
		t.Errorf("DNS rules = %v", dns)
	}
}

func TestResourceLimits(t *testing.T) {
	// One item more than the agent takes: the one with the lowest priority is left out.
	in := baseInput(t)
	in.Items = nil
	for i := 0; i < configgen.MaxResources+3; i++ {
		data := srs.DomainSuffixes(t, fmt.Sprintf("n%d.example", i))
		in.Items = append(in.Items, configgen.Item{ID: fmt.Sprintf("i%d", i), Name: fmt.Sprintf("Item %d", i), Exit: main(configgen.ExitMain), Domain: data, DNS: data})
	}
	out := generate(t, in)
	if len(out.Resources) != configgen.MaxResources || len(out.Skipped) != 3 || out.Skipped[0] != fmt.Sprintf("i%d", configgen.MaxResources) {
		t.Fatalf("resources = %d, skipped = %v", len(out.Resources), out.Skipped)
	}
	n := 0
	for _, w := range out.Warnings {
		if w.Code == configgen.WarnItemSkipped {
			n++
		}
	}
	if n != 3 {
		t.Errorf("warnings = %+v", out.Warnings)
	}
	if strings.Contains(string(out.JSON), "item-i"+fmt.Sprint(configgen.MaxResources)+"\"") {
		t.Error("a skipped item must not appear in the configuration")
	}
	// The agent takes exactly this.
	if len(out.Resources) > store.MaxResources {
		t.Errorf("the generator produced more files than the agent stores")
	}
	accept(t, out)

	// A single file the agent would refuse.
	huge := configgen.Item{ID: "huge", Name: "Huge", Exit: main(configgen.ExitMain), Domain: bytes.Repeat([]byte{1}, configgen.MaxResourceBytes+1)}
	in = baseInput(t)
	in.Items = append(in.Items, huge)
	out = generate(t, in)
	if len(out.Skipped) != 1 || out.Skipped[0] != "huge" {
		t.Errorf("skipped = %v", out.Skipped)
	}
	// Items with nothing to match are not an error and not a warning.
	in.Items = []configgen.Item{{ID: "empty", Name: "Empty", Exit: main(configgen.ExitMain)}}
	out = generate(t, in)
	if len(out.Skipped) != 0 || len(out.Warnings) != 0 || out.Selectors["empty"] != "" {
		t.Errorf("skipped = %v warnings = %+v", out.Skipped, out.Warnings)
	}
}

func TestLimitsMirrorTheAgentStore(t *testing.T) {
	if configgen.MaxResources != store.MaxResources || configgen.MaxResourceBytes != store.MaxResourceBytes || configgen.MaxResourceTotal != store.MaxResourceTotal {
		t.Fatalf("configgen limits (%d, %d, %d) differ from the store's (%d, %d, %d)",
			configgen.MaxResources, configgen.MaxResourceBytes, configgen.MaxResourceTotal,
			store.MaxResources, store.MaxResourceBytes, store.MaxResourceTotal)
	}
}

func TestNodeTagsNeverCollideWithGeneratedOnes(t *testing.T) {
	var ns []configgen.Node
	for i, name := range []string{"Proxy", "Auto", "direct", "reject", "tun-in", "mixed-in", "region-HK", "item-ai", "item-fallback", "pin-1", "dns-remote", "Same", "same", "SAME"} {
		n := subscription.Node{
			Name: name, Type: "trojan", Server: fmt.Sprintf("n%d.example.com", i), Port: 443,
			Config: map[string]any{"type": "trojan", "server": fmt.Sprintf("n%d.example.com", i), "server_port": 443, "password": "pw",
				"tls": map[string]any{"enabled": true, "server_name": "x.example.com"}},
		}
		ns = append(ns, configgen.Node{Node: n, Region: "HK"})
	}
	in := baseInput(t)
	in.Nodes = ns
	in.Items[0].Exit = region("HK")
	in.Exceptions = []configgen.Exception{exception("e", hostRule("suffix", "x.example"), pin(ns[6]))}
	out := generate(t, in)
	accept(t, out)
	seen := map[string]bool{}
	for _, o := range parse(t, out).Outbounds {
		tag := strings.ToLower(o["tag"].(string))
		if seen[tag] {
			t.Fatalf("tag %q appears twice", tag)
		}
		seen[tag] = true
	}
	for _, n := range ns {
		tag := out.Tags[n.Key()]
		for _, bad := range []string{"Proxy", "Auto", "direct", "reject", "tun-in", "mixed-in"} {
			if tag == bad {
				t.Errorf("node %q took the fixed tag %q", n.Name, bad)
			}
		}
	}
}

func TestModesExist(t *testing.T) {
	c := parse(t, generate(t, baseInput(t)))
	modes := map[string]bool{}
	for _, rules := range [][]map[string]any{c.Route.Rules, c.DNS.Rules} {
		for _, r := range rules {
			if m, ok := r["clash_mode"].(string); ok {
				modes[m] = true
			}
		}
	}
	if !modes["Direct"] || !modes["Global"] {
		t.Fatalf("Direct and Global must be switchable at runtime, got %v", modes)
	}
}

func TestOutputIsDeterministicAndInputIsUntouched(t *testing.T) {
	in := baseInput(t)
	in.Exceptions = []configgen.Exception{exception("e", hostRule("suffix", "x.example"), main(configgen.ExitDirect))}
	before, _ := json.Marshal(in.Nodes)
	itemsBefore := append([]configgen.Item(nil), in.Items...)
	a := generate(t, in)
	b := generate(t, in)
	if !bytes.Equal(a.JSON, b.JSON) || a.Digest != b.Digest || a.Digest == "" {
		t.Fatal("the same input must give the same bytes")
	}
	after, _ := json.Marshal(in.Nodes)
	if !bytes.Equal(before, after) || !reflect.DeepEqual(itemsBefore, in.Items) {
		t.Fatal("Generate modified its input")
	}
	// The digest does not move with a selector's default, and does with anything else.
	in.Main = region("US")
	if generate(t, in).Digest != a.Digest {
		t.Error("the digest must not depend on the choices a running core can switch")
	}
	in.Prefs.MTU = 1400
	if generate(t, in).Digest == a.Digest {
		t.Error("the digest must follow the configuration")
	}
}

func TestNoNodes(t *testing.T) {
	if _, err := configgen.Generate(configgen.Input{Prefs: configgen.DefaultPrefs()}); err != configgen.ErrNoNodes {
		t.Fatalf("err = %v", err)
	}
	// Nodes that fail validation count as none.
	bad := configgen.Node{Node: subscription.Node{Name: "x", Type: "trojan", Server: "", Port: 443, Config: map[string]any{"type": "trojan"}}}
	if _, err := configgen.Generate(configgen.Input{Nodes: []configgen.Node{bad}, Prefs: configgen.DefaultPrefs()}); err != configgen.ErrNoNodes {
		t.Fatalf("err = %v", err)
	}
}

func TestPrefsNormalizeClampsAndReports(t *testing.T) {
	p, notes := configgen.Prefs{
		DomesticDNS: "ftp://x", RemoteDNS: "https://", TunStack: "nope", MTU: 99999,
		MixedPort: 70000, TestURL: "ftp://x", TestIntervalMin: 99999, ToleranceMs: 99999,
	}.Normalize()
	if len(notes) != 6 {
		t.Fatalf("notes = %q", notes)
	}
	d := configgen.DefaultPrefs()
	if p.DomesticDNS != d.DomesticDNS || p.RemoteDNS != d.RemoteDNS || p.TunStack != d.TunStack ||
		p.MTU != d.MTU || p.MixedPort != 0 || p.TestURL != d.TestURL || p.TestIntervalMin != 24*60 || p.ToleranceMs != 5000 {
		t.Fatalf("prefs = %+v", p)
	}
	zero, notes := configgen.Prefs{}.Normalize()
	if len(notes) != 0 || zero.MTU != d.MTU || zero.TestIntervalMin != d.TestIntervalMin {
		t.Fatalf("zero prefs = %+v %q", zero, notes)
	}
}

func TestHealDropsOnlyTheNodeTheAgentRejects(t *testing.T) {
	ns := nodes(t)
	// A node whose settings the agent's policy refuses: a certificate read from a file path.
	evil := nodeNamed(t, ns, "HK 02")
	evil.Node = evil.Node.Clone()
	evil.Name = "Evil"
	evil.Config["tls"].(map[string]any)["certificate_path"] = "/etc/passwd"
	evil.Config["server"] = "evil.example.com"
	evil.Server = "evil.example.com"
	in := baseInput(t)
	in.Nodes = append([]configgen.Node{evil}, ns...)

	out := generate(t, in)
	pol := policy.New(singbox.PolicyOptions(singbox.RegistryOptions{}))
	dir := t.TempDir()
	res := map[string]bool{}
	for _, r := range out.Resources {
		res[r.Name] = true
		if err := os.WriteFile(filepath.Join(dir, r.Name), r.Data, 0o600); err != nil {
			t.Fatal(err)
		}
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
	if _, _, changed := configgen.Heal(in, out, []configgen.PathIssue{{Path: "$.dns.servers[0]"}, {Path: "$.outbounds[0]"}, {Path: "$.outbounds[1]"}, {Path: "$.outbounds[9999]"}}); changed {
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
		in := baseInput(t)
		in.Prefs.DomesticDNS = spec
		c := parse(t, generate(t, in))
		var got map[string]any
		for _, s := range c.DNS.Servers {
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
	in := baseInput(t)
	in.Prefs.DomesticDNS = "tls://dot.pub"
	if !strings.Contains(string(generate(t, in).JSON), `"domain_resolver": "dns-bootstrap"`) {
		t.Fatal("a DNS server named by host needs a bootstrap resolver")
	}
	if strings.Contains(string(generate(t, baseInput(t)).JSON), `"domain_resolver": "dns-bootstrap"`) {
		t.Fatal("literal addresses need no bootstrap resolver")
	}
	// The remote server goes through the main exit.
	c := parse(t, generate(t, baseInput(t)))
	for _, s := range c.DNS.Servers {
		if s["tag"] == "dns-remote" && s["detour"] != "Proxy" {
			t.Errorf("remote DNS detour = %v", s["detour"])
		}
	}
}

// ---------------------------------------------------------------- helpers

func toStrings(v any) []string {
	arr, _ := v.([]any)
	out := make([]string, len(arr))
	for i, s := range arr {
		out[i], _ = s.(string)
	}
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func hasWarning(out *configgen.Output, code string) bool {
	for _, w := range out.Warnings {
		if w.Code == code {
			return true
		}
	}
	return false
}

func TestTunCanBeTurnedOff(t *testing.T) {
	inbounds := func(out *configgen.Output) map[string]map[string]any {
		var doc struct {
			Inbounds []map[string]any `json:"inbounds"`
		}
		if err := json.Unmarshal(out.JSON, &doc); err != nil {
			t.Fatal(err)
		}
		m := map[string]map[string]any{}
		for _, in := range doc.Inbounds {
			m[in["type"].(string)] = in
		}
		return m
	}

	on := generate(t, baseInput(t))
	if got := inbounds(on); got["tun"] == nil || got["mixed"] != nil {
		t.Fatalf("by default: inbounds %v, want only a TUN", got)
	}
	if !accept(t, on).HasTun {
		t.Error("by default the agent should see a TUN")
	}

	in := baseInput(t)
	in.Prefs.TunDisabled = true
	off := generate(t, in)
	got := inbounds(off)
	if got["tun"] != nil {
		t.Fatalf("TUN off still has a TUN inbound: %s", off.JSON)
	}
	mixed := got["mixed"]
	if mixed == nil || mixed["listen"] != "127.0.0.1" || mixed["listen_port"] != float64(configgen.DefaultMixedPort) {
		t.Fatalf("TUN off without a port should open the local proxy on 127.0.0.1:%d, got %v", configgen.DefaultMixedPort, mixed)
	}
	if res := accept(t, off); res.HasTun {
		t.Error("TUN off: the agent should not see a TUN, or it would change the machine's routes and DNS")
	}
	if _, ok := configgen.Switches(on, off); ok {
		t.Error("turning TUN off needs the core to start again")
	}

	in.Prefs.MixedPort = 1080
	if p := inbounds(generate(t, in))["mixed"]; p == nil || p["listen_port"] != float64(1080) {
		t.Errorf("a chosen port is kept with TUN off, got %v", p)
	}
}

func TestShadowTLSNodeGoesThroughItsDetour(t *testing.T) {
	in := baseInput(t)
	st := nodeNamed(t, in.Nodes, "SG 04")
	out := generate(t, in)
	accept(t, out)

	var cfg struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(out.JSON, &cfg); err != nil {
		t.Fatal(err)
	}
	tag := out.Tags[st.Key()]
	byTag := map[string]map[string]any{}
	viaIndex := -1
	for i, o := range cfg.Outbounds {
		byTag[o["tag"].(string)] = o
		if o["tag"] == configgen.DetourPrefix+tag {
			viaIndex = i
		}
		if members, ok := o["outbounds"].([]any); ok {
			for _, m := range members {
				if s, _ := m.(string); strings.HasPrefix(s, configgen.DetourPrefix) {
					t.Fatalf("group %v offers the detour %q as a node", o["tag"], s)
				}
			}
		}
	}
	ss, via := byTag[tag], byTag[configgen.DetourPrefix+tag]
	if ss["type"] != "shadowsocks" || ss["detour"] != configgen.DetourPrefix+tag {
		t.Fatalf("shadowsocks outbound = %v", ss)
	}
	if via["type"] != "shadowtls" || via["server"] != "st.example.com" || via["password"] != "stpw" {
		t.Fatalf("shadowtls outbound = %v", via)
	}
	if st.Protocol() != "Shadowsocks + ShadowTLS" {
		t.Fatal(st.Protocol())
	}

	// An issue with the detour drops the node it belongs to, and only it.
	_, dropped, changed := configgen.Heal(in, out, []configgen.PathIssue{{Path: fmt.Sprintf("$.outbounds[%d].tls", viaIndex), Message: "x"}})
	if !changed || len(dropped) != 1 || dropped[0].Name != "SG 04" {
		t.Fatalf("dropped = %+v", dropped)
	}
}

func TestDetourTagsCannotBeTakenByANode(t *testing.T) {
	in := baseInput(t)
	st := nodeNamed(t, in.Nodes, "SG 04")
	impostor := nodeNamed(t, in.Nodes, "SG 01")
	impostor.Node = impostor.Node.Clone()
	impostor.Name = configgen.DetourPrefix + "SG 04"
	impostor.Config["server"], impostor.Server = "other.example.com", "other.example.com"
	in.Nodes = append(in.Nodes, impostor)
	out := generate(t, in)
	accept(t, out)
	if got := out.Tags[impostor.Key()]; got == configgen.DetourPrefix+out.Tags[st.Key()] {
		t.Fatalf("a node took the detour tag %q", got)
	}
}

func TestClashWireGuardWithSeveralPeersIsAccepted(t *testing.T) {
	if !singbox.WireGuardCompiled {
		t.Skip("WireGuard is not compiled in")
	}
	key := func(b byte) string { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32)) }
	res, err := subscription.Parse([]byte(`proxies:
  - name: WG multi
    type: wireguard
    ip: 172.16.0.2
    private-key: ` + key(1) + `
    peers:
      - {server: a.example.com, port: 2480, public-key: ` + key(2) + `, allowed-ips: ["0.0.0.0/0"]}
      - {server: b.example.com, port: 2481, public-key: ` + key(3) + `, allowed-ips: ["::/0"]}
`))
	if err != nil || len(res.Nodes) != 1 {
		t.Fatalf("parse: %v %+v", err, res)
	}
	in := baseInput(t)
	in.Nodes = append(in.Nodes, configgen.Node{Node: res.Nodes[0]})
	accept(t, generate(t, in))
}
