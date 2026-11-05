package plancompile_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sagernet/sing-box/common/srs"
	"github.com/sagernet/sing-box/option"

	"eggbox/internal/agent/engine"
	"eggbox/internal/agent/engine/singbox"
	"eggbox/internal/agent/policy"
	"eggbox/internal/configgen"
	"eggbox/internal/plancompile"
	"eggbox/internal/routing"
	"eggbox/internal/rulefmt"
	"eggbox/internal/ruleurl"
	"eggbox/internal/subscription"
)

// fakeData is a rule file store with invented content.
type fakeData struct {
	files map[string]*rulefmt.Parsed
	reads int
}

func newData() *fakeData { return &fakeData{files: map[string]*rulefmt.Parsed{}} }

func (d *fakeData) Parsed(u string) (*rulefmt.Parsed, error) {
	d.reads++
	if p, ok := d.files[u]; ok {
		return p, nil
	}
	return nil, errors.New("not stored")
}

// put stores a file given as text, in whatever format the text is.
func (d *fakeData) put(t *testing.T, u, text string) *rulefmt.Parsed {
	t.Helper()
	p, err := rulefmt.Parse([]byte(text), rulefmt.Hint{Source: u})
	if err != nil {
		t.Fatalf("parse %q: %v", text, err)
	}
	d.files[u] = p
	return p
}

// fill gives every source of the builtin plan one invented rule.
func (d *fakeData) fill(t *testing.T, skip ...string) {
	t.Helper()
	for _, it := range routing.Builtin().Items {
		for i, s := range it.Sources {
			if slices.Contains(skip, s.URL) {
				continue
			}
			if strings.Contains(s.URL, "/geoip/") {
				d.put(t, s.URL, "IP-CIDR,198.51.100."+string(rune('0'+i%10))+"/32\n")
			} else {
				d.put(t, s.URL, "DOMAIN-SUFFIX,"+it.Key+string(rune('a'+i))+".example\n")
			}
		}
	}
}

const uuid = "00000000-0000-4000-8000-000000000001"

func node(t *testing.T, link string) subscription.Node {
	t.Helper()
	res, err := subscription.Parse([]byte(link))
	if err != nil || len(res.Nodes) != 1 {
		t.Fatalf("parse %q: %v %+v", link, err, res)
	}
	return res.Nodes[0]
}

// fleet is a few invented nodes of one subscription.
func fleet(t *testing.T) []plancompile.Node {
	t.Helper()
	var out []plancompile.Node
	for _, l := range []string{
		"trojan://pw@hk1.example.com:443#HK%2001",
		"trojan://pw@hk2.example.com:443#HK%2002",
		"trojan://pw@jp1.example.com:443#JP%2001",
		"vless://" + uuid + "@us1.example.com:443?security=tls&sni=us1.example.com#US%2001",
		"trojan://pw@plain.example.com:443#Backup",
	} {
		out = append(out, plancompile.Node{Sub: "s1", Node: node(t, l), Auto: true})
	}
	return out
}

func compile(t *testing.T, c *plancompile.Compiler, st routing.State, data plancompile.Data, nodes []plancompile.Node) *plancompile.Result {
	t.Helper()
	res, err := c.Compile(plancompile.Options{State: st, Nodes: nodes, Data: data, Prefs: configgen.DefaultPrefs()})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// accept runs the compiled plan through the generator, the agent's policy and
// the real core, exactly as applying it would.
func accept(t *testing.T, res *plancompile.Result) *configgen.Output {
	t.Helper()
	out, err := configgen.Generate(res.Input)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	known := map[string]bool{}
	for _, r := range out.Resources {
		known[r.Name] = true
		if err := os.WriteFile(filepath.Join(dir, r.Name), r.Data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reg := singbox.RegistryOptions{}
	result := policy.New(singbox.PolicyOptions(reg)).Normalize(out.JSON, policy.Env{
		ResourceDir: dir, Resources: known, CachePath: filepath.Join(dir, "cache.db"), LogLevel: "info",
		Tun: policy.TunSettings{Addresses: []string{"172.19.0.1/30", "fdfe:dcba:9876::1/126"}, DNSMode: "disabled"},
	})
	if result.HasErrors() {
		t.Fatalf("the policy rejected the configuration:\n%v\n%s", result.Errors(), out.JSON)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := singbox.New(reg).Check(ctx, engine.Prepared{Revision: 1, JSON: result.JSON, HasTun: result.HasTun, TunTag: result.TunTag}); err != nil {
		t.Fatalf("the core rejected the configuration: %v\n%s", err, out.JSON)
	}
	return out
}

func itemByID(res *plancompile.Result, id string) (configgen.Item, bool) {
	for _, it := range res.Input.Items {
		if it.ID == id {
			return it, true
		}
	}
	return configgen.Item{}, false
}

func decode(t *testing.T, data []byte) (domains, suffixes, cidrs []string, other int) {
	t.Helper()
	set, err := srs.Read(bytes.NewReader(data), true)
	if err != nil {
		t.Fatalf("read compiled rule set: %v", err)
	}
	for _, r := range set.Options.Rules {
		d := r.DefaultOptions
		domains = append(domains, d.Domain...)
		suffixes = append(suffixes, d.DomainSuffix...)
		cidrs = append(cidrs, d.IPCIDR...)
		if len(d.Port)+len(d.PortRange)+len(d.ProcessName)+len(d.ProcessPath) > 0 || r.Type != "default" {
			other++
		}
	}
	return
}

func TestBuiltinPlanWithoutData(t *testing.T) {
	res := compile(t, plancompile.New(), routing.State{}, newData(), fleet(t))
	if len(res.Input.Items) != 0 {
		t.Fatalf("items without data must not be compiled: %+v", res.Input.Items)
	}
	for _, it := range routing.Builtin().Items {
		info, ok := res.Items[it.ID]
		if !ok {
			t.Fatalf("no info for %s", it.ID)
		}
		want := plancompile.ItemUnavailable
		if !it.Enabled {
			want = plancompile.ItemOff
		}
		if info.State != want {
			t.Errorf("%s: state %v, want %v", it.ID, info.State, want)
		}
	}
	// The files the enabled items need are listed so they can be downloaded.
	want := map[string]bool{}
	for _, it := range routing.Builtin().Items {
		if it.Enabled {
			for _, s := range it.Sources {
				want[s.URL] = true
			}
		}
	}
	if len(res.Missing) != len(want) {
		t.Fatalf("missing %d files, want %d: %v", len(res.Missing), len(want), res.Missing)
	}
	for _, u := range res.Missing {
		if !want[u] {
			t.Errorf("%s listed as missing but no enabled item needs it", u)
		}
	}
	if !slices.IsSorted(res.Missing) {
		t.Errorf("missing files must be sorted: %v", res.Missing)
	}
	// Nothing to route by, yet the configuration is still valid: everything follows the main exit.
	if res.Input.Main.Kind != configgen.ExitAuto || res.Input.Fallback.Kind != configgen.ExitMain {
		t.Errorf("main %+v fallback %+v", res.Input.Main, res.Input.Fallback)
	}
	accept(t, res)
}

func TestBuiltinPlanWithData(t *testing.T) {
	data := newData()
	data.fill(t)
	res := compile(t, plancompile.New(), routing.State{}, data, fleet(t))
	if len(res.Missing) != 0 {
		t.Fatalf("missing: %v", res.Missing)
	}
	enabled := 0
	for _, it := range routing.Builtin().Items {
		if it.Enabled {
			enabled++
			if res.Items[it.ID].State != plancompile.ItemReady {
				t.Errorf("%s: %+v", it.ID, res.Items[it.ID])
			}
		} else if res.Items[it.ID].State != plancompile.ItemOff {
			t.Errorf("%s should be off: %+v", it.ID, res.Items[it.ID])
		}
	}
	if len(res.Input.Items) != enabled {
		t.Fatalf("%d items compiled, want %d", len(res.Input.Items), enabled)
	}
	// The plan's order is the rule order, and each item keeps its exit.
	var order []string
	for _, it := range res.Input.Items {
		order = append(order, it.ID)
	}
	var want []string
	for _, it := range routing.Builtin().Items {
		if it.Enabled {
			want = append(want, it.ID)
		}
	}
	if !slices.Equal(order, want) {
		t.Fatalf("order %v, want %v", order, want)
	}
	cn, _ := itemByID(res, "b-cn")
	if cn.Exit.Kind != configgen.ExitDirect {
		t.Errorf("cn exit %+v", cn.Exit)
	}
	ai, _ := itemByID(res, "b-ai")
	if ai.Exit.Kind != configgen.ExitMain {
		t.Errorf("ai exit %+v", ai.Exit)
	}
	// "cn" has names and addresses: both halves exist, and names steer DNS.
	if len(cn.Domain) == 0 || len(cn.IP) == 0 || len(cn.DNS) == 0 {
		t.Errorf("cn: domain %d ip %d dns %d bytes", len(cn.Domain), len(cn.IP), len(cn.DNS))
	}
	if info := res.Items["b-cn"]; info.Domain != 1 || info.IP != 1 {
		t.Errorf("cn counts %+v", info)
	}
	accept(t, res)
}

func TestPartialItem(t *testing.T) {
	data := newData()
	data.fill(t, routing.GeoIP("telegram"))
	res := compile(t, plancompile.New(), routing.State{}, data, fleet(t))
	info := res.Items["b-telegram"]
	if info.State != plancompile.ItemPartial || info.Missing != 1 {
		t.Fatalf("telegram: %+v", info)
	}
	if !slices.Equal(res.Missing, []string{routing.GeoIP("telegram")}) {
		t.Fatalf("missing: %v", res.Missing)
	}
	if _, ok := itemByID(res, "b-telegram"); !ok {
		t.Fatal("an item with some of its data still takes part")
	}
}

func TestUnsupportedRulesAreCounted(t *testing.T) {
	data := newData()
	u := ruleurl.GeoSite("ai-test")
	data.put(t, u, "DOMAIN-SUFFIX,ai.example\nGEOIP,CN\nIP-ASN,13335\nDOMAIN-SUFFIX,chat.example\n")
	st := routing.State{}
	st.Plans = []*routing.Plan{{
		ID: "p1", Kind: routing.PlanMine, Name: "mine", Fallback: routing.Follow(),
		Items: []routing.Item{{ID: "i1", Name: "AI", Enabled: true, Dest: routing.Follow(),
			Sources: []routing.Source{routing.URLSource(u, "x", "")}}},
	}}
	st.Active = "p1"
	res := compile(t, plancompile.New(), st, data, fleet(t))
	info := res.Items["i1"]
	// The list refers to datasets by name (GEOIP, IP-ASN), which are not stored here: they are missing, not lost.
	if info.Missing == 0 || len(res.Missing) == 0 {
		t.Fatalf("referenced datasets must be reported missing: %+v %v", info, res.Missing)
	}
	if info.State != plancompile.ItemPartial {
		t.Fatalf("state %v", info.State)
	}
	// Once the datasets are there, the item is complete.
	data.put(t, ruleurl.GeoIP("cn"), "IP-CIDR,198.51.100.0/24\n")
	data.put(t, ruleurl.ASN("13335"), "IP-CIDR,203.0.113.0/24\n")
	res = compile(t, plancompile.New(), st, data, fleet(t))
	info = res.Items["i1"]
	if info.State != plancompile.ItemReady || len(res.Missing) != 0 {
		t.Fatalf("after the datasets arrived: %+v %v", info, res.Missing)
	}
	item, _ := itemByID(res, "i1")
	_, suffixes, _, _ := decode(t, item.Domain)
	_, _, cidrs, _ := decode(t, item.IP)
	if !slices.Contains(suffixes, "ai.example") || !slices.Contains(suffixes, "chat.example") {
		t.Errorf("suffixes %v", suffixes)
	}
	if !slices.Contains(cidrs, "198.51.100.0/24") || !slices.Contains(cidrs, "203.0.113.0/24") {
		t.Errorf("cidrs %v", cidrs)
	}
	accept(t, res)
}

func TestUnsupportedKindsMakeAnItemPartial(t *testing.T) {
	data := newData()
	u := "https://raw.githubusercontent.com/example/rules/main/mixed.list"
	data.put(t, u, "DOMAIN-SUFFIX,site.example\nUSER-AGENT,Some*\nURL-REGEX,^https://x\nDOMAIN-KEYWORD,ads\n")
	st := planWith([]routing.Item{{ID: "i1", Name: "Mixed", Enabled: true, Dest: routing.Follow(),
		Sources: []routing.Source{routing.URLSource(u, "mixed", "")}}}, routing.Follow())
	res := compile(t, plancompile.New(), st, data, fleet(t))
	info := res.Items["i1"]
	if info.State != plancompile.ItemPartial || info.Unsupported != 2 || info.Missing != 0 || info.Domain != 2 {
		t.Fatalf("%+v", info)
	}
	if _, ok := itemByID(res, "i1"); !ok {
		t.Fatal("what the core can evaluate still takes part")
	}
	accept(t, res)
}

func TestEntryRule(t *testing.T) {
	cases := []struct {
		e       routing.Entry
		wantErr bool
	}{
		{e: routing.Entry{Kind: routing.EntryDomainSuffix, Value: "*.Example.COM"}},
		{e: routing.Entry{Kind: routing.EntryDomain, Value: "a.example.com"}},
		{e: routing.Entry{Kind: routing.EntryDomainKeyword, Value: "tracker"}},
		{e: routing.Entry{Kind: routing.EntryDomainRegex, Value: `^ad\d+\.example\.com$`}},
		{e: routing.Entry{Kind: routing.EntryIPCIDR, Value: "203.0.113.7"}},
		{e: routing.Entry{Kind: routing.EntryPort, Value: "22"}},
		{e: routing.Entry{Kind: routing.EntryPort, Value: "8000-9000"}},
		{e: routing.Entry{Kind: routing.EntryProcessName, Value: "curl"}},
		{e: routing.Entry{Kind: routing.EntryProcessPath, Value: "/usr/bin/curl"}},
		{e: routing.Entry{Kind: routing.EntryPort, Value: "70000"}, wantErr: true},
		{e: routing.Entry{Kind: routing.EntryDomainRegex, Value: `(`}, wantErr: true},
		{e: routing.Entry{Kind: routing.EntryIPCIDR, Value: "not an address"}, wantErr: true},
		{e: routing.Entry{Kind: "nonsense", Value: "x"}, wantErr: true},
		{e: routing.Entry{Kind: routing.EntryDomain, Value: ""}, wantErr: true},
	}
	for _, c := range cases {
		r, err := plancompile.EntryRule(c.e)
		if (err != nil) != c.wantErr {
			t.Errorf("%+v: error %v, want error %v", c.e, err, c.wantErr)
			continue
		}
		if err != nil {
			continue
		}
		// Every rule it produces compiles to a rule set the core reads.
		raw, err := rulefmt.Compile([]option.HeadlessRule{r})
		if err != nil || len(raw) == 0 {
			t.Errorf("%+v: compile: %v", c.e, err)
		}
	}
	r, _ := plancompile.EntryRule(routing.Entry{Kind: routing.EntryDomainSuffix, Value: "*.Example.COM"})
	if got := r.DefaultOptions.DomainSuffix; len(got) != 1 || got[0] != "example.com" {
		t.Errorf("suffix %v", got)
	}
	r, _ = plancompile.EntryRule(routing.Entry{Kind: routing.EntryPort, Value: "8000-9000"})
	if got := r.DefaultOptions.PortRange; len(got) != 1 || got[0] != "8000:9000" {
		t.Errorf("port range %v", got)
	}
	r, _ = plancompile.EntryRule(routing.Entry{Kind: routing.EntryPort, Value: "22"})
	if got := r.DefaultOptions.Port; len(got) != 1 || got[0] != 22 {
		t.Errorf("port %v", got)
	}
}
