package configgen_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"

	"eggbox/internal/configgen"
)

// server finds a DNS server of the configuration by tag.
func (c config) server(tag string) map[string]any {
	for _, s := range c.DNS.Servers {
		if s["tag"] == tag {
			return s
		}
	}
	return nil
}

func (c config) routeRule(action string) map[string]any {
	for _, r := range c.Route.Rules {
		if r["action"] == action {
			return r
		}
	}
	return nil
}

func withResolver(x configgen.Exception, r string) configgen.Exception {
	x.Resolver = r
	return x
}

// A rule that names a DNS server has its names asked of that server, through
// the rule's own exit: none for direct (the core refuses a detour to a plain
// direct outbound), the exception's fixed exit, the item's group (so the
// server follows the item's exit at runtime). The system resolver is always
// asked here. The same server through the same exit is made once.
func TestResolverServers(t *testing.T) {
	in := baseInput(t)
	hk := nodeNamed(t, in.Nodes, "HK 02")
	lan := domainItem(t, "lan", main(configgen.ExitDirect), "lan.example")
	lan.Resolver = "10.0.0.53"
	named := domainItem(t, "named", main(configgen.ExitMain), "named.example")
	named.Resolver = "tls://dns.corp.example"
	in.Items = append(in.Items, lan, named)
	in.Exceptions = []configgen.Exception{
		withResolver(exception("e1", hostRule("suffix", "corp.example"), main(configgen.ExitDirect)), "10.0.0.53"),
		withResolver(exception("e2", hostRule("suffix", "intra.example"), configgen.Exit{Kind: configgen.ExitNode, Node: hk.Key(), NoStandIn: true}), "tcp://10.0.0.53"),
		withResolver(exception("e3", hostRule("suffix", "sys.example"), main(configgen.ExitMain)), "system"),
		withResolver(exception("e4", hostRule("domain", "also.corp.example"), main(configgen.ExitDirect)), "10.0.0.53"),
	}
	out := generate(t, in)
	if len(out.Warnings) != 0 {
		t.Fatalf("warnings: %+v", out.Warnings)
	}
	accept(t, out)
	c := parse(t, out)

	want := []string{
		"mode:Direct=>dns-domestic",
		"mode:Global=>dns-remote",
		"set:x-e1=>dns-rule-1",
		"set:x-e2=>dns-rule-2",
		"set:x-e3=>dns-rule-3",
		"set:x-e4=>dns-rule-1",
		"set:d-ai=>dns-remote",
		"set:d-ads=>reject",
		"set:d-cn=>dns-remote",
		"set:d-lan=>dns-rule-4",
		"set:d-named=>dns-rule-5",
	}
	if got := describeAll(c.DNS.Rules); !reflect.DeepEqual(got, want) {
		t.Fatalf("DNS rules:\n got %v\nwant %v", got, want)
	}
	check := func(tag, wantJSON string) {
		t.Helper()
		s := c.server(tag)
		delete(s, "tag")
		got, _ := json.Marshal(s)
		if string(got) != wantJSON {
			t.Errorf("%s = %s, want %s", tag, got, wantJSON)
		}
	}
	check("dns-rule-1", `{"server":"10.0.0.53","type":"udp"}`)
	check("dns-rule-2", `{"detour":"`+out.Tags[hk.Key()]+`","server":"10.0.0.53","type":"tcp"}`)
	check("dns-rule-3", `{"type":"local"}`)
	check("dns-rule-4", `{"detour":"item-lan","server":"10.0.0.53","type":"udp"}`)
	check("dns-rule-5", `{"detour":"item-named","domain_resolver":"dns-domestic","server":"dns.corp.example","type":"tls"}`)
	for _, r := range c.DNS.Rules {
		if r["server"] == "dns-fakeip" {
			t.Errorf("no rule leaves its names to the exit, yet %v answers with fake addresses", r)
		}
	}
}

// A rule that leaves its names to its exit answers them with fake addresses
// (A only while IPv6 is off), gives other questions an empty answer, keeps the
// fake addresses across restarts, and is never resolved on this side.
func TestResolverExit(t *testing.T) {
	for _, ipv6 := range []bool{false, true} {
		in := baseInput(t)
		in.Prefs.IPv6 = ipv6
		hk := nodeNamed(t, in.Nodes, "HK 02")
		remote := domainItem(t, "remote", pin(hk), "remote.example")
		remote.Resolver = "exit"
		blocked := domainItem(t, "blocked", main(configgen.ExitReject), "blocked.example")
		blocked.Resolver = "exit" // blocked names are refused whatever the resolver
		in.Items = append(in.Items, remote, blocked, ipItem(t, "net10", pin(hk), "10.0.0.0/8"))
		in.Exceptions = []configgen.Exception{
			exception("e1", hostRule("cidr", "10.99.0.0/24"), main(configgen.ExitDirect)),
			withResolver(exception("e2", hostRule("keyword", "intra"), pin(hk)), "EXIT"),
		}
		out := generate(t, in)
		if len(out.Warnings) != 0 {
			t.Fatalf("warnings: %+v", out.Warnings)
		}
		accept(t, out)
		c := parse(t, out)

		wantDNS := []string{
			"mode:Direct=>dns-domestic",
			"mode:Global=>dns-remote",
			"set:x-e2=>dns-fakeip",
			"set:x-e2=>predefined",
			"set:d-ai=>dns-remote",
			"set:d-ads=>reject",
			"set:d-cn=>dns-remote",
			"set:d-remote=>dns-fakeip",
			"set:d-remote=>predefined",
			"set:d-blocked=>reject",
		}
		if got := describeAll(c.DNS.Rules); !reflect.DeepEqual(got, wantDNS) {
			t.Fatalf("ipv6=%v DNS rules:\n got %v\nwant %v", ipv6, got, wantDNS)
		}
		wantTypes := []string{"A"}
		if ipv6 {
			wantTypes = append(wantTypes, "AAAA")
		}
		for _, r := range c.DNS.Rules {
			if r["server"] == "dns-fakeip" && !reflect.DeepEqual(toStrings(r["query_type"]), wantTypes) {
				t.Errorf("ipv6=%v fake-ip query types = %v", ipv6, r["query_type"])
			}
			if r["server"] == "dns-fakeip" && fmt.Sprint(r["rewrite_ttl"]) != "1" {
				t.Errorf("a fake address must not outlive a change of configuration: ttl %v", r["rewrite_ttl"])
			}
			if r["action"] == "predefined" && r["rcode"] != "NOERROR" {
				t.Errorf("other questions get %v, want an empty NOERROR answer", r["rcode"])
			}
		}
		fake := c.server("dns-fakeip")
		if fake == nil || fake["inet4_range"] != "198.18.0.0/15" || (fake["inet6_range"] != nil) != ipv6 {
			t.Errorf("ipv6=%v fake-ip server = %v", ipv6, fake)
		}
		// The exceptions' resolve skips a name whose first DNS rule leaves it
		// to the exit: e2's names, and remote's names that no rule before remote
		// decides.
		resolve := c.routeRule("resolve")
		delete(resolve, "strategy")
		guard, _ := json.Marshal(resolve)
		wantGuard := `{"action":"resolve","invert":true,"mode":"or","rules":[{"rule_set":["x-e2"]},` +
			`{"mode":"and","rules":[{"rule_set":["d-remote"]},{"invert":true,"rule_set":["d-ai","d-ads","d-cn"]}],"type":"logical"}],"type":"logical"}`
		if string(guard) != wantGuard {
			t.Errorf("resolve guard:\n got %s\nwant %s", guard, wantGuard)
		}
		var raw struct {
			Experimental struct {
				CacheFile map[string]any `json:"cache_file"`
			} `json:"experimental"`
		}
		if err := json.Unmarshal(out.JSON, &raw); err != nil {
			t.Fatal(err)
		}
		if raw.Experimental.CacheFile["store_fakeip"] != true {
			t.Errorf("fake addresses must outlive a restart: %v", raw.Experimental.CacheFile)
		}
	}
}

// Without a resolver the configuration is what it always was.
func TestNoResolverChangesNothing(t *testing.T) {
	in := baseInput(t)
	before := generate(t, in)
	in.Items[0].Resolver = ""
	after := generate(t, in)
	if string(before.JSON) != string(after.JSON) {
		t.Fatal("an empty resolver changed the configuration")
	}
	c := parse(t, before)
	if c.routeRule("resolve")["invert"] != nil {
		t.Fatal("no guarded resolve without a rule that asks for one")
	}
	// Without a rule that asks for fake addresses there is no fake-ip server:
	// with one, a real host in its range could not be reached.
	if c.server("dns-fakeip") != nil {
		t.Fatal("a fake-ip server without a rule that uses it")
	}
}

// A resolver that is not a server is reported, and the names are looked up as
// usual; changing a resolver is a new configuration, not a runtime switch.
func TestResolverInvalidAndDigest(t *testing.T) {
	in := baseInput(t)
	in.Items[0].Resolver = "gopher://nope"
	out := generate(t, in)
	if !hasWarning(out, configgen.WarnResolverInvalid) {
		t.Fatalf("warnings: %+v", out.Warnings)
	}
	c := parse(t, out)
	if got := describeAll(c.DNS.Rules)[2]; got != "set:d-ai=>dns-remote" {
		t.Fatalf("an unusable resolver must fall back: %s", got)
	}
	in.Items[0].Resolver = "10.0.0.53"
	next := generate(t, in)
	if _, ok := configgen.Switches(out, next); ok {
		t.Fatal("a resolver change cannot be made at runtime")
	}
}

// Connections that arrive as names (through the local proxy) are looked up
// with the server the rule names, right before the rule, and only for its
// names; the exit's own lookups (dns=exit) get no such step.
func TestResolverServerAlsoResolvesNamedConnections(t *testing.T) {
	in := baseInput(t)
	lan := domainItem(t, "lan", main(configgen.ExitDirect), "lan.example")
	lan.Resolver = "10.0.0.53"
	remote := domainItem(t, "remote", main(configgen.ExitMain), "remote.example")
	remote.Resolver = "exit"
	in.Items = append(in.Items, lan, remote)
	in.Exceptions = []configgen.Exception{
		withResolver(exception("e1", hostRule("suffix", "corp.example"), main(configgen.ExitDirect)), "system"),
	}
	out := generate(t, in)
	accept(t, out)
	c := parse(t, out)
	var got []string
	for _, r := range c.Route.Rules {
		if r["action"] == "resolve" && r["rule_set"] != nil && r["invert"] == nil {
			got = append(got, describe(r)+" via "+r["server"].(string)+" "+fmt.Sprint(r["strategy"]))
		}
	}
	want := []string{"set:x-e1=>dns-rule-1 via dns-rule-1 ipv4_only", "set:d-lan=>dns-rule-2 via dns-rule-2 ipv4_only"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolves = %v, want %v", got, want)
	}
	all := describeAll(c.Route.Rules)
	for i, d := range all {
		if d == "set:d-lan=>dns-rule-2" && all[i+1] != "set:d-lan=>item-lan" {
			t.Fatalf("the resolve must come right before the item's rule: %v", all)
		}
	}
}

// The local network goes straight out first, except for the private ranges
// the person's own rules claim, which the rules decide in order.
func TestLANClaims(t *testing.T) {
	in := baseInput(t)
	narrow := exception("e1", hostRule("cidr", "10.99.0.0/24"), main(configgen.ExitDirect))
	narrow.LANClaim = "10.99.0.0/24"
	bogus := exception("e2", hostRule("suffix", "x.example"), main(configgen.ExitDirect))
	bogus.LANClaim = "bogus"
	dropped := exception("e3", option.HeadlessRule{Type: C.RuleTypeDefault}, main(configgen.ExitDirect)) // matches nothing: left out
	dropped.LANClaim = "192.168.9.0/24"
	in.Exceptions = []configgen.Exception{narrow, bogus, dropped}
	intranet := ipItem(t, "intranet", main(configgen.ExitMain), "10.0.0.0/8")
	intranet.LANClaims = []string{"10.0.0.0/8", "10.99.0.0/24"}
	in.Items = append(in.Items, intranet)
	out := generate(t, in)
	accept(t, out)
	if !reflect.DeepEqual(out.LANClaims, []string{"10.99.0.0/24", "10.0.0.0/8"}) {
		t.Fatalf("output claims = %v", out.LANClaims)
	}
	c := parse(t, out)
	all := describeAll(c.Route.Rules)
	if all[4] != "private-unclaimed=>direct" || all[5] != "suffix=>direct" {
		t.Fatalf("route rules = %v", all)
	}
	rule := c.Route.Rules[4]
	parts := rule["rules"].([]any)
	if len(parts) != 2 || parts[0].(map[string]any)["ip_is_private"] != true ||
		!reflect.DeepEqual(toStrings(parts[1].(map[string]any)["ip_cidr"]), []string{"10.99.0.0/24", "10.0.0.0/8"}) ||
		parts[1].(map[string]any)["invert"] != true || rule["mode"] != "and" {
		t.Fatalf("the local network's rule = %v", rule)
	}
	in.Prefs.LANDirect = false
	c = parse(t, generate(t, in))
	for _, d := range describeAll(c.Route.Rules) {
		if strings.HasPrefix(d, "private") || strings.HasPrefix(d, "suffix") {
			t.Fatalf("no local network rule when it is off: %v", describeAll(c.Route.Rules))
		}
	}
}

// A name an earlier rule looks up with a server of its own is resolved like
// any other, even when a later rule would leave it to its exit: the DNS rules
// ask the earlier rule first, and so does the resolve.
func TestResolveGuardFollowsTheFirstRule(t *testing.T) {
	in := baseInput(t)
	hk := nodeNamed(t, in.Nodes, "HK 02")
	remote := domainItem(t, "remote", pin(hk), "corp.example")
	remote.Resolver = "exit"
	in.Items = []configgen.Item{remote}
	in.Exceptions = []configgen.Exception{
		exception("e1", hostRule("cidr", "10.0.0.0/8"), pin(hk)),
		withResolver(exception("e2", hostRule("suffix", "corp.example"), main(configgen.ExitDirect)), "10.0.0.53"),
	}
	out := generate(t, in)
	accept(t, out)
	resolve := parse(t, out).routeRule("resolve")
	delete(resolve, "strategy")
	got, _ := json.Marshal(resolve)
	want := `{"action":"resolve","invert":true,"mode":"or","rules":[{"mode":"and","rules":[{"rule_set":["d-remote"]},{"invert":true,"rule_set":["x-e2"]}],"type":"logical"}],"type":"logical"}`
	if string(got) != want {
		t.Fatalf("resolve:\n got %s\nwant %s", got, want)
	}
	// After the exceptions' resolve, the DNS rules already looked e2's names
	// up with its server: no second resolve for them.
	for _, r := range parse(t, out).Route.Rules {
		if r["action"] == "resolve" && r["server"] != nil {
			t.Fatalf("a second resolve: %v", r)
		}
	}
	// Without a rule that leaves names to the exit after it, no guard at all.
	in.Items = nil
	if r := parse(t, generate(t, in)).routeRule("resolve"); r["type"] != nil || r["invert"] != nil {
		t.Fatalf("resolve = %v", r)
	}
}

// An item that does not make it into the configuration (no room for its
// rules) claims nothing: its range stays with the local network.
func TestLANClaimsOfAnItemLeftOut(t *testing.T) {
	in := baseInput(t)
	big := ipItem(t, "big", main(configgen.ExitMain), "10.0.0.0/8")
	big.LANClaims = []string{"10.0.0.0/8"}
	big.IP = append(append([]byte(nil), big.IP...), make([]byte, configgen.MaxResourceBytes)...) // too large to carry
	in.Items = append(in.Items, big)
	out := generate(t, in)
	if !hasWarning(out, configgen.WarnItemSkipped) {
		t.Fatalf("warnings %+v", out.Warnings)
	}
	if got := describeAll(parse(t, out).Route.Rules)[4]; got != "private=>direct" {
		t.Fatalf("the local network's rule = %s", got)
	}
	if got := in.Claims(); !reflect.DeepEqual(got, []string{"10.0.0.0/8"}) {
		t.Fatalf("claims of the input = %v", got)
	}
	if len(out.LANClaims) != 0 {
		t.Fatalf("the configuration claims %v for an item it left out", out.LANClaims)
	}
}
