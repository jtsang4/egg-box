package planimport_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"eggbox/internal/planimport"
	"eggbox/internal/routing"
)

// The fixtures describe one provider profile twice, as a sing-box
// configuration and as a Clash profile, from the same invented spec, the way
// real providers publish the same routing in both formats. Everything in them
// is made up.

const (
	mirror    = "https://mirror.example/"
	siteBase  = "https://github.com/MetaCubeX/meta-rules-dat/raw/refs/heads/"
	main      = "🚀 Select"
	auto      = "⚡ Auto"
	finalName = "🐟 Final"
)

var nodes = []string{"HK 01", "HK 02", "JP 01", "JP 02", "US 01", "SG 01"}

type svc struct {
	name  string
	sites []string
	ips   []string
	// first is the member the group starts on: "main", "direct" or "reject".
	first string
}

var services = []svc{
	{name: "💬 AI", sites: []string{"category-ai-!cn"}, first: "main"},
	{name: "📺 Video", sites: []string{"bilibili"}, first: "main"},
	{name: "🔍 Google", sites: []string{"google"}, ips: []string{"google"}, first: "main"},
	{name: "🏠 Private", ips: []string{"private"}, first: "direct"},
	{name: "🔒 Domestic", sites: []string{"geolocation-cn", "cn"}, ips: []string{"cn"}, first: "direct"},
	{name: "🌐 Foreign", sites: []string{"geolocation-!cn"}, first: "main"},
	{name: "📲 Telegram", ips: []string{"telegram"}, first: "main"},
}

func singboxURL(kind, name string) string {
	return mirror + siteBase + "sing/geo/" + kind + "/" + name + ".srs"
}
func clashURL(kind, name string) string {
	return mirror + siteBase + "meta/geo/" + kind + "/" + name + ".mrs"
}

func singboxProfile(t *testing.T, mutate func(root map[string]any)) []byte {
	t.Helper()
	var outbounds []any
	members := func(first string) []any {
		var m []any
		switch first {
		case "main":
			m = append(m, main)
		case "direct":
			m = append(m, "DIRECT", main)
		}
		for _, n := range nodes {
			m = append(m, n)
		}
		if first != "direct" {
			m = append(m, "DIRECT")
		}
		return m
	}
	outbounds = append(outbounds, map[string]any{"type": "selector", "tag": main, "outbounds": append([]any{auto}, append(toAny(nodes), "DIRECT")...)})
	outbounds = append(outbounds, map[string]any{"type": "urltest", "tag": auto, "outbounds": toAny(nodes)})
	for _, s := range services {
		outbounds = append(outbounds, map[string]any{"type": "selector", "tag": s.name, "outbounds": members(s.first)})
	}
	outbounds = append(outbounds, map[string]any{"type": "selector", "tag": finalName, "outbounds": members("main")})
	outbounds = append(outbounds, map[string]any{"type": "direct", "tag": "DIRECT"})
	for i, n := range nodes {
		outbounds = append(outbounds, map[string]any{"type": "trojan", "tag": n, "server": fmt.Sprintf("n%d.example.net", i), "server_port": 443, "password": "x"})
	}

	var sets, rules []any
	addSet := func(tag, kind, name string) {
		sets = append(sets, map[string]any{"tag": tag, "type": "remote", "format": "binary", "url": singboxURL(kind, name)})
	}
	rules = append(rules,
		map[string]any{"action": "sniff"},
		map[string]any{"protocol": "dns", "action": "hijack-dns"},
		map[string]any{"clash_mode": "direct", "outbound": "DIRECT"},
		map[string]any{"clash_mode": "global", "outbound": main},
	)
	addSet("category-ads-all", "geosite", "category-ads-all")
	rules = append(rules, map[string]any{"rule_set": []any{"category-ads-all"}, "action": "reject"})
	for _, s := range services {
		if len(s.sites) == 0 {
			continue
		}
		var tags []any
		for _, site := range s.sites {
			addSet(site, "geosite", site)
			tags = append(tags, site)
		}
		rules = append(rules, map[string]any{"rule_set": tags, "outbound": s.name})
	}
	for _, s := range services {
		for _, ip := range s.ips {
			addSet(ip+"-ip", "geoip", ip)
			rules = append(rules, map[string]any{"rule_set": []any{ip + "-ip"}, "outbound": s.name})
		}
	}
	root := map[string]any{
		"dns":       map[string]any{"servers": []any{map[string]any{"type": "udp", "tag": "d", "server": "192.0.2.1"}}},
		"inbounds":  []any{map[string]any{"type": "tun", "tag": "tun-in"}},
		"outbounds": outbounds,
		"route":     map[string]any{"rules": rules, "rule_set": sets, "final": finalName, "auto_detect_interface": true},
	}
	if mutate != nil {
		mutate(root)
	}
	raw, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func toAny(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

func clashProfile() []byte {
	var b strings.Builder
	b.WriteString("port: 7890\nmode: rule\ndns:\n  enable: true\n  nameserver: [192.0.2.1]\nproxies:\n")
	for i, n := range nodes {
		fmt.Fprintf(&b, "  - {name: %q, type: trojan, server: n%d.example.net, port: 443, password: x}\n", n, i)
	}
	list := func(first string) string {
		var m []string
		switch first {
		case "main":
			m = append(m, main)
		case "direct":
			m = append(m, "DIRECT", main)
		case "reject":
			m = append(m, main, "REJECT")
		}
		m = append(m, nodes...)
		if first == "main" {
			m = append(m, "DIRECT", "REJECT")
		}
		var q []string
		for _, x := range m {
			q = append(q, fmt.Sprintf("%q", x))
		}
		return "[" + strings.Join(q, ", ") + "]"
	}
	b.WriteString("proxy-groups:\n")
	fmt.Fprintf(&b, "  - {name: %q, type: select, proxies: [%q, %s]}\n", main, auto, strings.Trim(list("none"), "[]")+`, "DIRECT", "REJECT"`)
	var an []string
	for _, n := range nodes {
		an = append(an, fmt.Sprintf("%q", n))
	}
	fmt.Fprintf(&b, "  - {name: %q, type: url-test, url: 'https://www.gstatic.com/generate_204', interval: 300, proxies: [%s]}\n", auto, strings.Join(an, ", "))
	fmt.Fprintf(&b, "  - {name: \"🛑 Ads\", type: select, proxies: %s}\n", list("reject"))
	for _, s := range services {
		fmt.Fprintf(&b, "  - {name: %q, type: select, proxies: %s}\n", s.name, list(s.first))
	}
	fmt.Fprintf(&b, "  - {name: %q, type: select, proxies: %s}\n", finalName, list("main"))
	b.WriteString("rule-providers:\n")
	prov := func(tag, kind, name string) {
		fmt.Fprintf(&b, "  %s: {type: http, behavior: domain, format: mrs, url: %q, path: ./%s.mrs, interval: 86400}\n", tag, clashURL(kind, name), tag)
	}
	prov("category-ads-all", "geosite", "category-ads-all")
	for _, s := range services {
		for _, site := range s.sites {
			prov(site, "geosite", site)
		}
		for _, ip := range s.ips {
			prov(ip+"-ip", "geoip", ip)
		}
	}
	b.WriteString("rules:\n  - RULE-SET,category-ads-all,🛑 Ads\n")
	for _, s := range services {
		for _, site := range s.sites {
			fmt.Fprintf(&b, "  - RULE-SET,%s,%s\n", site, s.name)
		}
	}
	for _, s := range services {
		for _, ip := range s.ips {
			fmt.Fprintf(&b, "  - RULE-SET,%s-ip,%s,no-resolve\n", ip, s.name)
		}
	}
	fmt.Fprintf(&b, "  - MATCH,%s\n", finalName)
	return []byte(b.String())
}

func imported(t *testing.T, body []byte) *planimport.Result {
	t.Helper()
	res, err := planimport.Import(body, planimport.Options{SubID: "sAurora", SubName: "Aurora"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	return res
}

func kinds(p *routing.Plan) []routing.DestKind {
	var out []routing.DestKind
	for _, it := range p.Items {
		out = append(out, it.Dest.Kind)
	}
	return out
}

func urls(it routing.Item) []string {
	var out []string
	for _, s := range it.Sources {
		if s.Kind == routing.SourceURL {
			out = append(out, s.URL)
		}
	}
	return out
}

func TestBothFormatsGiveTheSamePlan(t *testing.T) {
	sb, cl := imported(t, singboxProfile(t, nil)), imported(t, clashProfile())

	if sb.Report.Format != "singbox" || cl.Report.Format != "clash" {
		t.Fatalf("formats %q %q", sb.Report.Format, cl.Report.Format)
	}
	// Seven things are sorted somewhere; the private network is the application's own layer.
	for name, res := range map[string]*planimport.Result{"sing-box": sb, "clash": cl} {
		p := res.Plan
		if len(p.Items) != 7 {
			var names []string
			for _, it := range p.Items {
				names = append(names, it.Name)
			}
			t.Fatalf("%s: %d items: %v", name, len(p.Items), names)
		}
		if !res.Report.LAN {
			t.Errorf("%s: the private network rule was not recognised", name)
		}
		if p.ID != routing.SubPlanID("sAurora") || p.Kind != routing.PlanSubscription || p.SubID != "sAurora" || p.Name != "Aurora" {
			t.Errorf("%s: plan identity %+v", name, p)
		}
		if p.Fallback.Kind != routing.DestFollow {
			t.Errorf("%s: fallback %+v", name, p.Fallback)
		}
		if p.Main == nil || p.Main.Kind != routing.DestAuto {
			t.Errorf("%s: the main exit starts on the automatic group: %+v", name, p.Main)
		}
		if res.Report.Items != 7 || res.Report.Sources == 0 {
			t.Errorf("%s: report %+v", name, res.Report)
		}
	}
	want := []routing.DestKind{routing.DestReject, routing.DestFollow, routing.DestFollow, routing.DestFollow, routing.DestDirect, routing.DestFollow, routing.DestFollow}
	if got := kinds(sb.Plan); !reflect.DeepEqual(got, want) {
		t.Errorf("sing-box destinations %v, want %v", got, want)
	}
	if got := kinds(cl.Plan); !reflect.DeepEqual(got, want) {
		t.Errorf("clash destinations %v, want %v", got, want)
	}
	// The same files, whichever way the provider named them: the mirror prefix is gone and mihomo's
	// binary files are the sing-box files of the same project.
	for i := range sb.Plan.Items {
		a, b := urls(sb.Plan.Items[i]), urls(cl.Plan.Items[i])
		if !reflect.DeepEqual(a, b) {
			t.Errorf("item %d: sing-box sources %v, clash sources %v", i, a, b)
		}
		for _, u := range a {
			if !strings.HasPrefix(u, "https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/sing/geo/") || strings.Contains(u, "mirror.example") || !strings.HasSuffix(u, ".srs") {
				t.Errorf("source address %q", u)
			}
		}
	}
	// The ad item has a name of its own in the Clash profile and is named for what it does in the other.
	if sb.Plan.Items[0].Name != routing.BuiltinName("ads", "zh") || sb.Plan.Items[0].Key != "ads" {
		t.Errorf("sing-box ad item %q key %q", sb.Plan.Items[0].Name, sb.Plan.Items[0].Key)
	}
	if cl.Plan.Items[0].Name != "🛑 Ads" {
		t.Errorf("clash ad item %q", cl.Plan.Items[0].Name)
	}
	for i := 1; i < 7; i++ {
		if sb.Plan.Items[i].Name != cl.Plan.Items[i].Name {
			t.Errorf("item %d named %q and %q", i, sb.Plan.Items[i].Name, cl.Plan.Items[i].Name)
		}
	}
	// Rules of one group that were separated by other groups' rules are merged where the first one was.
	google := sb.Plan.Items[3]
	if google.Name != "🔍 Google" || len(urls(google)) != 2 || !contains(google.Notes, "reordered") {
		t.Errorf("google %+v", google)
	}
	// Stable names and ids, so a refresh finds the choices the person made.
	again := imported(t, singboxProfile(t, nil))
	if !reflect.DeepEqual(again.Plan, sb.Plan) {
		t.Error("importing the same profile twice gave different plans")
	}
	seen := map[string]bool{}
	for _, it := range sb.Plan.Items {
		if it.ID == "" || seen[it.ID] || !it.Enabled {
			t.Errorf("item %q id %q enabled=%v", it.Name, it.ID, it.Enabled)
		}
		seen[it.ID] = true
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func TestNothingOfTheProvidersMachineIsTaken(t *testing.T) {
	res := imported(t, singboxProfile(t, nil))
	raw, _ := json.Marshal(res.Plan)
	for _, leak := range []string{"192.0.2.1", "tun-in", "example.net", "n0.", "auto_detect_interface", "sniff", "hijack"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("the plan carries %q", leak)
		}
	}
	for k := range res.Report.Skipped {
		if k == "sniff" || k == "hijack-dns" || k == "dns" {
			t.Errorf("the application's own concerns are not 'skipped rules': %v", res.Report.Skipped)
		}
	}
}

func TestRegionGroupsAndPinnedNodes(t *testing.T) {
	// An automatic group of one region means "the fastest node of that region"; of several, "follow the main exit".
	// A group that starts on a node pins it, to the subscription it came from.
	body := singboxProfile(t, func(root map[string]any) {
		outs := root["outbounds"].([]any)
		outs = append(outs,
			map[string]any{"type": "urltest", "tag": "HK auto", "outbounds": []any{"HK 01", "HK 02"}},
			map[string]any{"type": "urltest", "tag": "Mixed auto", "outbounds": []any{"HK 01", "JP 01"}},
			map[string]any{"type": "selector", "tag": "Pinned", "outbounds": []any{"JP 02", main, "DIRECT"}},
			map[string]any{"type": "selector", "tag": "ViaHK", "outbounds": []any{"HK auto", main}},
			map[string]any{"type": "selector", "tag": "ViaMixed", "outbounds": []any{"Mixed auto", main}},
		)
		root["outbounds"] = outs
		route := root["route"].(map[string]any)
		route["rules"] = append(route["rules"].([]any),
			map[string]any{"domain_suffix": []any{"a.example"}, "outbound": "HK auto"},
			map[string]any{"domain_suffix": []any{"b.example"}, "outbound": "Pinned"},
			map[string]any{"domain_suffix": []any{"c.example"}, "outbound": "ViaHK"},
			map[string]any{"domain_suffix": []any{"d.example"}, "outbound": "ViaMixed"},
			map[string]any{"domain_suffix": []any{"e.example"}, "outbound": "Mixed auto"},
			map[string]any{"domain_suffix": []any{"f.example"}, "outbound": "US 01"},
		)
	})
	p := imported(t, body).Plan
	byName := map[string]routing.Item{}
	for _, it := range p.Items {
		byName[it.Name] = it
	}
	if d := byName["HK auto"].Dest; d.Kind != routing.DestRegion || d.Region != "HK" {
		t.Errorf("HK auto: %+v", d)
	}
	pin := byName["Pinned"].Dest
	if pin.Kind != routing.DestNode || pin.Node == nil || pin.Node.Sub != "sAurora" || pin.Node.Name != "JP 02" || pin.Node.Region != "JP" {
		t.Errorf("Pinned: %+v %+v", pin, pin.Node)
	}
	if d := byName["ViaHK"].Dest; d.Kind != routing.DestRegion || d.Region != "HK" {
		t.Errorf("a selector that starts on a region group goes there: %+v", d)
	}
	for _, n := range []string{"ViaMixed", "Mixed auto"} {
		it := byName[n]
		if it.Dest.Kind != routing.DestFollow || !contains(it.Notes, "multi_region_auto") {
			t.Errorf("%s: %+v notes %v", n, it.Dest, it.Notes)
		}
	}
	if d := byName["US 01"].Dest; d.Kind != routing.DestNode || d.Node.Name != "US 01" {
		t.Errorf("a rule that names a server pins it: %+v", d)
	}
}

func TestTheMainExitStartsWhereTheProviderStarted(t *testing.T) {
	cases := map[string]struct {
		first string
		check func(t *testing.T, m *routing.Dest)
	}{
		"a node":   {"HK 02", func(t *testing.T, m *routing.Dest) { mustBe(t, m, routing.DestNode, "") }},
		"a region": {"HK auto", func(t *testing.T, m *routing.Dest) { mustBe(t, m, routing.DestRegion, "HK") }},
		"auto":     {auto, func(t *testing.T, m *routing.Dest) { mustBe(t, m, routing.DestAuto, "") }},
		"direct": {"DIRECT", func(t *testing.T, m *routing.Dest) {
			if m != nil {
				t.Errorf("the main exit cannot go direct: %+v", m)
			}
		}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			body := singboxProfile(t, func(root map[string]any) {
				outs := root["outbounds"].([]any)
				outs = append(outs, map[string]any{"type": "urltest", "tag": "HK auto", "outbounds": []any{"HK 01", "HK 02"}})
				for _, o := range outs {
					m := o.(map[string]any)
					if m["tag"] == main {
						m["outbounds"] = append([]any{c.first}, m["outbounds"].([]any)...)
					}
				}
				root["outbounds"] = outs
			})
			c.check(t, imported(t, body).Plan.Main)
		})
	}
	// A selector's own default overrides its order.
	body := singboxProfile(t, func(root map[string]any) {
		for _, o := range root["outbounds"].([]any) {
			if m := o.(map[string]any); m["tag"] == main {
				m["default"] = "JP 01"
			}
		}
	})
	if m := imported(t, body).Plan.Main; m == nil || m.Kind != routing.DestNode || m.Node.Name != "JP 01" {
		t.Errorf("main %+v", m)
	}
}

func mustBe(t *testing.T, m *routing.Dest, kind routing.DestKind, region string) {
	t.Helper()
	if m == nil || m.Kind != kind || m.Region != region {
		t.Errorf("main %+v, want %s %s", m, kind, region)
	}
}

func TestMainGroupIsFoundByHowOthersUseIt(t *testing.T) {
	// The profile lists the main group last and calls nothing "main"; the groups that start on it give it away.
	body := singboxProfile(t, func(root map[string]any) {
		outs := root["outbounds"].([]any)
		var head, tail []any
		for _, o := range outs {
			if o.(map[string]any)["tag"] == main {
				tail = append(tail, o)
			} else {
				head = append(head, o)
			}
		}
		root["outbounds"] = append(head, tail...)
	})
	res := imported(t, body)
	if got := kinds(res.Plan); got[1] != routing.DestFollow {
		t.Errorf("destinations %v", got)
	}
}

func TestRulesThePlanCannotCarryAreCounted(t *testing.T) {
	cl := `
proxies:
  - {name: A, type: trojan, server: a.example.net, port: 443, password: x}
proxy-groups:
  - {name: Main, type: select, proxies: [A, DIRECT]}
  - {name: Svc, type: select, proxies: [Main, A, DIRECT]}
  - {name: Work, type: select, proxies: [DIRECT, Main]}
rules:
  - DOMAIN-SUFFIX,svc.example,Svc
  - USER-AGENT,Some*,Svc
  - URL-REGEX,^https://x,Svc
  - AND,((DOMAIN,a.example),(DST-PORT,443)),Svc
  - IN-PORT,7890,Svc
  - SRC-IP-CIDR,192.0.2.0/24,Svc
  - DOMAIN-SUFFIX,corp.example,Work
  - DOMAIN-KEYWORD,intranet,Work
  - IP-CIDR,203.0.113.0/24,Work,no-resolve
  - DST-PORT,22,Work
  - PROCESS-NAME,ssh,Work
  - DOMAIN-WILDCARD,*.wild.example,Work
  - DOMAIN-SUFFIX,direct.example,DIRECT
  - DOMAIN-SUFFIX,blocked.example,REJECT
  - GEOIP,private,DIRECT,no-resolve
  - MATCH,Main
`
	res := imported(t, []byte(cl))
	p := res.Plan
	if len(p.Items) != 4 {
		t.Fatalf("%d items: %+v", len(p.Items), p.Items)
	}
	skipped := res.Report.Skipped
	for k, want := range map[string]int{"user-agent": 1, "url-regex": 1, "and": 1, "in-port": 1, "src-ip-cidr": 1} {
		if skipped[k] != want {
			t.Errorf("skipped %q = %d, want %d (%v)", k, skipped[k], want, skipped)
		}
	}
	svc, work, direct, rej := p.Items[0], p.Items[1], p.Items[2], p.Items[3]
	if svc.Name != "Svc" || svc.Dest.Kind != routing.DestFollow || len(svc.Sources) != 1 || len(svc.Sources[0].Entries) != 1 {
		t.Errorf("svc %+v", svc)
	}
	if work.Name != "Work" || work.Dest.Kind != routing.DestDirect {
		t.Errorf("work %+v", work)
	}
	var kinds []routing.EntryKind
	for _, e := range work.Sources[0].Entries {
		kinds = append(kinds, e.Kind)
	}
	wantKinds := []routing.EntryKind{routing.EntryDomainSuffix, routing.EntryDomainKeyword, routing.EntryIPCIDR, routing.EntryPort, routing.EntryProcessName, routing.EntryDomainRegex}
	if !reflect.DeepEqual(kinds, wantKinds) {
		t.Errorf("work entry kinds %v, want %v", kinds, wantKinds)
	}
	if direct.Key != "direct" || direct.Dest.Kind != routing.DestDirect || direct.Sources[0].Entries[0].Value != "direct.example" {
		t.Errorf("direct %+v", direct)
	}
	if rej.Key != "reject" || rej.Dest.Kind != routing.DestReject {
		t.Errorf("reject %+v", rej)
	}
	if !res.Report.LAN {
		t.Error("GEOIP,private was not taken as the local network")
	}
	for _, e := range work.Sources[0].Entries {
		if e.Valid() != nil {
			t.Errorf("entry %+v is not valid", e)
		}
	}
}

func TestSingBoxRulesThePlanCannotCarry(t *testing.T) {
	body := singboxProfile(t, func(root map[string]any) {
		route := root["route"].(map[string]any)
		route["rules"] = append(route["rules"].([]any),
			map[string]any{"domain_suffix": []any{"net.example"}, "network": "udp", "outbound": "💬 AI"},
			map[string]any{"type": "logical", "mode": "and", "rules": []any{}, "outbound": "💬 AI"},
			map[string]any{"domain_suffix": []any{"inv.example"}, "invert": true, "outbound": "💬 AI"},
			map[string]any{"ip_is_private": true, "outbound": "DIRECT"},
			map[string]any{"domain": []any{"x.example"}, "port": []any{22, 8080}, "port_range": []any{"9000:9100"}, "process_name": []any{"curl"}, "outbound": "🔒 Domestic"},
			map[string]any{"rule_set": []any{"does-not-exist"}, "outbound": "💬 AI"},
		)
	})
	res := imported(t, body)
	for k, want := range map[string]int{"network": 1, "logical": 1, "invert": 1, "unknown_rule_set": 1} {
		if res.Report.Skipped[k] != want {
			t.Errorf("skipped %q = %d, want %d (%v)", k, res.Report.Skipped[k], want, res.Report.Skipped)
		}
	}
	// None of the skipped rules leaked into AI as a looser rule.
	for _, it := range res.Plan.Items {
		if it.Name == "💬 AI" {
			for _, s := range it.Sources {
				if s.Kind == routing.SourceInline {
					t.Errorf("AI got hand-written entries from a rule that could not be carried: %+v", s.Entries)
				}
			}
		}
		if it.Name == "🔒 Domestic" {
			var got []string
			for _, s := range it.Sources {
				for _, e := range s.Entries {
					got = append(got, string(e.Kind)+"="+e.Value)
				}
			}
			want := []string{"domain=x.example", "port=22", "port=8080", "port=9000:9100", "process_name=curl"}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("domestic entries %v, want %v", got, want)
			}
		}
	}
}

func TestInlineRuleSetsAndLegacyFields(t *testing.T) {
	body := singboxProfile(t, func(root map[string]any) {
		route := root["route"].(map[string]any)
		route["rule_set"] = append(route["rule_set"].([]any),
			map[string]any{"tag": "mine", "type": "inline", "rules": []any{map[string]any{"domain_suffix": []any{"inl.example"}, "domain": []any{"one.example"}}}},
			map[string]any{"tag": "elsewhere", "type": "local", "path": "/etc/x.srs"},
		)
		route["rules"] = append(route["rules"].([]any),
			map[string]any{"rule_set": []any{"mine"}, "outbound": "💬 AI"},
			map[string]any{"rule_set": []any{"elsewhere"}, "outbound": "💬 AI"},
			map[string]any{"geosite": []any{"netflix"}, "geoip": []any{"netflix", "private"}, "outbound": "Streaming"},
		)
		root["outbounds"] = append(root["outbounds"].([]any), map[string]any{"type": "selector", "tag": "Streaming", "outbounds": []any{main, "DIRECT"}})
	})
	res := imported(t, body)
	var ai, st routing.Item
	for _, it := range res.Plan.Items {
		switch it.Name {
		case "💬 AI":
			ai = it
		case "Streaming":
			st = it
		}
	}
	var inline []routing.Entry
	for _, s := range ai.Sources {
		inline = append(inline, s.Entries...)
	}
	if len(inline) != 2 {
		t.Errorf("inline rule set entries %+v", inline)
	}
	if res.Report.Skipped["local_rule_set"] != 1 {
		t.Errorf("skipped %v", res.Report.Skipped)
	}
	if got := urls(st); len(got) != 2 || !strings.HasSuffix(got[0], "/geosite/netflix.srs") || !strings.HasSuffix(got[1], "/geoip/netflix.srs") {
		t.Errorf("legacy geosite/geoip fields: %v", got)
	}
}

func TestDocumentsWithoutRouting(t *testing.T) {
	for name, body := range map[string]string{
		"share links":      "trojan://pw@a.example.net:443#A\nvless://00000000-0000-4000-8000-000000000001@b.example.net:443#B\n",
		"empty":            "",
		"servers only":     `{"outbounds":[{"type":"trojan","tag":"A","server":"a.example.net","server_port":443,"password":"x"}]}`,
		"no rules (clash)": "proxies:\n  - {name: A, type: trojan, server: a.example.net, port: 443, password: x}\nproxy-groups:\n  - {name: M, type: select, proxies: [A]}\n",
		"bare array":       `[{"type":"trojan","tag":"A"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := planimport.Import([]byte(body), planimport.Options{SubID: "s1"})
			if !errors.Is(err, planimport.ErrNoPlan) && err == nil {
				t.Fatal("a plan was made of nothing")
			}
			if err != nil && !errors.Is(err, planimport.ErrNoPlan) {
				// Documents that are not profiles at all may fail to parse; they must not produce a plan either way.
				t.Logf("%s: %v", name, err)
			}
		})
	}
}

func TestOnlyDirectRulesStillMakeAPlan(t *testing.T) {
	// A profile that does nothing but send some names straight out and the rest through the proxy.
	res := imported(t, []byte(`
proxy-groups:
  - {name: Main, type: select, proxies: [DIRECT]}
rules:
  - DOMAIN-SUFFIX,only.example,DIRECT
  - MATCH,Main
`))
	if len(res.Plan.Items) != 1 || res.Plan.Items[0].Dest.Kind != routing.DestDirect || res.Plan.Fallback.Kind != routing.DestFollow {
		t.Fatalf("%+v", res.Plan)
	}
}

func TestAdListsInAnyGroup(t *testing.T) {
	// A group that merely lists "reject" among its choices is not a blocking group; one named for ads is,
	// and so is one that is fed ad lists only.
	res := imported(t, []byte(`
proxy-groups:
  - {name: Main, type: select, proxies: [DIRECT, REJECT]}
  - {name: Choice, type: select, proxies: [Main, REJECT]}
  - {name: Block, type: select, proxies: [Main, REJECT]}
  - {name: Privacy, type: select, proxies: [Main, REJECT]}
  - {name: Plain, type: select, proxies: [Main, DIRECT]}
rule-providers:
  adlist: {type: http, behavior: domain, url: "https://raw.githubusercontent.com/privacy-protection-tools/anti-AD/master/anti-ad-clash.yaml"}
  other: {type: http, behavior: domain, url: "https://raw.githubusercontent.com/example/rules/main/other.yaml"}
  more: {type: http, behavior: domain, url: "https://raw.githubusercontent.com/example/rules/main/more.yaml"}
rules:
  - RULE-SET,other,Choice
  - RULE-SET,adlist,Privacy
  - RULE-SET,more,Block
  - RULE-SET,more,Plain
  - MATCH,Main
`))
	got := map[string]routing.DestKind{}
	for _, it := range res.Plan.Items {
		got[it.Name] = it.Dest.Kind
	}
	want := map[string]routing.DestKind{"Choice": routing.DestFollow, "Privacy": routing.DestReject, "Block": routing.DestReject, "Plain": routing.DestFollow}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("destinations %v, want %v", got, want)
	}
}

func TestDuplicateGroupNamesGetDistinctIDs(t *testing.T) {
	res := imported(t, []byte(`
proxy-groups:
  - {name: "🅰 Same", type: select, proxies: [DIRECT]}
  - {name: "Same", type: select, proxies: [DIRECT]}
rule-providers:
  a: {type: http, url: "https://raw.githubusercontent.com/example/rules/main/a.list"}
  b: {type: http, url: "https://raw.githubusercontent.com/example/rules/main/b.list"}
rules:
  - RULE-SET,a,🅰 Same
  - RULE-SET,b,Same
  - MATCH,DIRECT
`))
	if len(res.Plan.Items) != 2 || res.Plan.Items[0].ID == res.Plan.Items[1].ID {
		t.Fatalf("%+v", res.Plan.Items)
	}
}
