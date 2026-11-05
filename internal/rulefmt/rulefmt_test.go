package rulefmt

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/route/rule"
	M "github.com/sagernet/sing/common/metadata"
)

func def(d option.DefaultHeadlessRule) option.HeadlessRule {
	return option.HeadlessRule{Type: C.RuleTypeDefault, DefaultOptions: d}
}

// coreMatch evaluates rules with sing-box's own rule engine: the ground
// truth the Matcher and Split must agree with.
func coreMatch(t *testing.T, rules []option.HeadlessRule, q Query) bool {
	t.Helper()
	for _, r := range rules {
		hr, err := rule.NewHeadlessRule(context.Background(), r)
		if err != nil {
			t.Fatalf("core refused a rule: %v", err)
		}
		md := &adapter.InboundContext{Network: q.Network}
		if q.Domain != "" {
			md.Destination = M.ParseSocksaddrHostPort(q.Domain, q.Port)
			md.DestinationAddresses = q.Addrs
		} else if len(q.Addrs) > 0 {
			md.Destination = M.SocksaddrFrom(q.Addrs[0], q.Port)
		}
		if hr.Match(md) {
			return true
		}
	}
	return false
}

func mustParse(t *testing.T, content string, hint Hint) *Parsed {
	t.Helper()
	p, err := Parse([]byte(content), hint)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return p
}

func matches(t *testing.T, p *Parsed, domain string) bool {
	t.Helper()
	m, err := NewMatcher(p.Rules)
	if err != nil {
		t.Fatal(err)
	}
	v := m.Match(Query{Domain: domain, Port: 7001, Network: "tcp"})
	// The matcher must agree with the core on every answer.
	core := coreMatch(t, p.Rules, Query{Domain: domain, Port: 7001, Network: "tcp"})
	if (v == Match) != core {
		t.Errorf("%s: matcher says %v, the core says %v", domain, v, core)
	}
	return v == Match
}

// The dot rules of docs/routing.md 5.3, one table per dialect.
func TestDomainListDotRules(t *testing.T) {
	list := "+.plus.example.net\n.dot.example.net\n*.star.example.net\nexact.example.net\n"
	cases := []struct {
		style Style
		name  string
		host  string
		want  bool
	}{
		// `+.` is the domain and every sub-domain, in both dialects.
		{StyleClash, "plus root", "plus.example.net", true},
		{StyleClash, "plus sub", "a.plus.example.net", true},
		{StyleClash, "plus deep", "a.b.plus.example.net", true},
		{StyleSurge, "plus root", "plus.example.net", true},
		// A leading dot is Clash's sub-domains only, Surge's everything.
		{StyleClash, "dot root", "dot.example.net", false},
		{StyleClash, "dot sub", "a.dot.example.net", true},
		{StyleClash, "dot deep", "a.b.dot.example.net", true},
		{StyleSurge, "dot root", "dot.example.net", true},
		{StyleSurge, "dot sub", "a.dot.example.net", true},
		{StyleSurge, "dot deep", "a.b.dot.example.net", true},
		// `*.` is one label, never the root and never two labels.
		{StyleClash, "star root", "star.example.net", false},
		{StyleClash, "star one", "a.star.example.net", true},
		{StyleClash, "star two", "a.b.star.example.net", false},
		{StyleSurge, "star one", "a.star.example.net", true},
		// A bare name is that name only.
		{StyleClash, "exact", "exact.example.net", true},
		{StyleClash, "exact sub", "a.exact.example.net", false},
		{StyleSurge, "exact", "exact.example.net", true},
		{StyleSurge, "exact sub", "a.exact.example.net", false},
		// Look-alikes must not match.
		{StyleClash, "suffix lookalike", "notplus.example.net", false},
		{StyleSurge, "suffix lookalike", "xdot.example.net", false},
	}
	for _, c := range cases {
		style := c.style
		p := mustParse(t, list, Hint{Style: &style})
		if got := matches(t, p, c.host); got != c.want {
			t.Errorf("style %d %s: %s = %v, want %v", c.style, c.name, c.host, got, c.want)
		}
	}
}

func TestStyleFor(t *testing.T) {
	for src, want := range map[string]Style{
		"":                                      StyleClash,
		"https://example.net/list.txt":          StyleClash,
		"https://example.net/rule/Clash/a.yaml": StyleClash,
		"https://example.net/rule/Surge/a.list": StyleSurge,
		"https://ruleset.skk.moe/List/domainset/a.conf": StyleSurge,
		"https://example.net/surge-rules/clash/a.yaml":  StyleClash, // the nearer word wins
		"https://example.net/clash-rules/surge/a.list":  StyleSurge,
	} {
		if got := StyleFor(src); got != want {
			t.Errorf("StyleFor(%q) = %d, want %d", src, got, want)
		}
	}
}

func TestDetect(t *testing.T) {
	srsData, _ := Compile([]option.HeadlessRule{def(option.DefaultHeadlessRule{DomainSuffix: []string{"example.net"}})})
	for name, c := range map[string]struct {
		in   string
		want Format
	}{
		"srs":         {string(srsData), FormatSRS},
		"mrs magic":   {"MRS\x01rest", FormatMRS},
		"zstd frame":  {"\x28\xb5\x2f\xfdrest", FormatMRS},
		"json":        {`{"version":2,"rules":[{"domain":["a.example.net"]}]}`, FormatSingBox},
		"json other":  {`{"inbounds":[]}`, ""},
		"clash yaml":  {"payload:\n  - '+.a.example.net'\n", FormatClashYAML},
		"classical":   {"# c\nDOMAIN-SUFFIX,a.example.net\nIP-CIDR,192.0.2.0/24,no-resolve\n", FormatClassical},
		"adguard":     {"[Adblock Plus 2.0]\n! Title\n||a.example.net^\n||b.example.net^$important\n", FormatAdGuard},
		"hosts":       {"127.0.0.1 localhost\n0.0.0.0 a.example.net\n0.0.0.0 b.example.net\n", FormatHosts},
		"cidr list":   {"192.0.2.0/24\n198.51.100.0/24\n2001:db8::/32\n", FormatCIDRList},
		"domain list": {"a.example.net\n.b.example.net\n+.c.example.net\n", FormatDomainList},
		"empty":       {"  \n\n", ""},
		"noise":       {"hello world, this is not a rule list\n", ""},
	} {
		if got := Detect([]byte(c.in)); got != c.want {
			t.Errorf("%s: Detect = %q, want %q", name, got, c.want)
		}
	}
	if _, err := Parse([]byte("MRS\x01..."), Hint{}); !errors.Is(err, ErrMRS) {
		t.Errorf("mrs must report ErrMRS, got %v", err)
	}
	if _, err := Parse([]byte("hello world, not rules"), Hint{}); !errors.Is(err, ErrUnrecognized) {
		t.Errorf("noise must report ErrUnrecognized, got %v", err)
	}
}

func TestClassicalConversion(t *testing.T) {
	in := `# comment
DOMAIN,full.example.net
DOMAIN-SUFFIX,suffix.example.net,Proxy
DOMAIN-KEYWORD,Keyword
DOMAIN-REGEX,^r[0-9]+\.example\.net$
DOMAIN-WILDCARD,w*.example.net
IP-CIDR,192.0.2.0/24,no-resolve
IP-CIDR6,2001:db8::/32,no-resolve
SRC-IP-CIDR,198.51.100.0/24
SRC-PORT,5000
DST-PORT,443
DST-PORT,8000-9000
PROCESS-NAME,tool.exe
PROCESS-PATH,/usr/bin/tool
NETWORK,udp
GEOSITE,Example
GEOIP,CN,no-resolve
IP-ASN,AS64500,no-resolve
USER-AGENT,Mozilla*
URL-REGEX,^https://x
IN-PORT,7890
USER-AGENT,Other*
`
	p := mustParse(t, in, Hint{})
	if p.Format != FormatClassical {
		t.Fatalf("format %s", p.Format)
	}
	if got := p.Stats.Unsupported; got["USER-AGENT"] != 2 || got["URL-REGEX"] != 1 || got["IN-PORT"] != 1 {
		t.Errorf("unsupported counted wrong: %v", got)
	}
	if p.Stats.UnsupportedTotal() != 4 {
		t.Errorf("total unsupported %d, want 4", p.Stats.UnsupportedTotal())
	}
	wantRefs := []Ref{{RefASN, "64500"}, {RefGeoIP, "cn"}, {RefGeoSite, "example"}}
	if !slices.Equal(p.Refs, wantRefs) {
		t.Errorf("refs %v, want %v", p.Refs, wantRefs)
	}
	var d option.DefaultHeadlessRule
	for _, r := range p.Rules {
		if len(r.DefaultOptions.Domain) > 0 {
			d = r.DefaultOptions
		}
	}
	if !slices.Equal(d.Domain, []string{"full.example.net"}) ||
		!slices.Equal(d.DomainSuffix, []string{"suffix.example.net"}) ||
		!slices.Equal(d.DomainKeyword, []string{"keyword"}) ||
		!slices.Equal(d.IPCIDR, []string{"192.0.2.0/24", "2001:db8::/32"}) {
		t.Errorf("domain rule wrong: %+v", d)
	}
	// Domain and IP counted apart.
	if p.Stats.Domain != 5 || p.Stats.IP != 2 {
		t.Errorf("stats %+v", p.Stats)
	}
	for _, host := range []string{"full.example.net", "a.suffix.example.net", "my-keyword-x.test", "r42.example.net", "wild.example.net", "w.example.net"} {
		if !matches(t, p, host) {
			t.Errorf("%s should match", host)
		}
	}
	for _, host := range []string{"a.full.example.net", "other.example.net", "r.example.net"} {
		if matches(t, p, host) {
			t.Errorf("%s should not match", host)
		}
	}
}

func TestLogicalRules(t *testing.T) {
	in := `AND,((DOMAIN-SUFFIX,both.example.net),(DST-PORT,443))
OR,((DOMAIN,one.example.net),(DOMAIN,two.example.net))
NOT,((DOMAIN-SUFFIX,except.example.net))
AND,((DOMAIN,x.example.net),(NOT,((NETWORK,udp))))
AND,((DOMAIN,bad.example.net),(USER-AGENT,x))
NOT,((DOMAIN,a.example.net),(DOMAIN,b.example.net))
`
	p := mustParse(t, in, Hint{})
	if p.Stats.Unsupported["USER-AGENT"] != 1 || p.Stats.Unsupported["NOT"] != 1 {
		t.Errorf("unsupported: %v", p.Stats.Unsupported)
	}
	m, err := NewMatcher(p.Rules)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		q    Query
		want Verdict
	}{
		{Query{Domain: "a.both.example.net", Port: 443, Network: "tcp"}, Match},
		{Query{Domain: "a.both.example.net", Port: 80, Network: "tcp"}, Match}, // the NOT rule matches everything else
		{Query{Domain: "one.example.net", Port: 80, Network: "tcp"}, Match},
		{Query{Domain: "two.example.net", Port: 80, Network: "tcp"}, Match},
		{Query{Domain: "x.example.net", Port: 80, Network: "tcp"}, Match},
		{Query{Domain: "x.example.net", Port: 80, Network: "udp"}, Match}, // NOT except matches anyway
	} {
		if got := m.Match(c.q); got != c.want {
			t.Errorf("%+v = %v, want %v", c.q, got, c.want)
		}
	}
	// Against the core, on a rule set without the catch-all NOT.
	only := mustParse(t, "AND,((DOMAIN-SUFFIX,both.example.net),(DST-PORT,443))\nOR,((DOMAIN,one.example.net),(DOMAIN,two.example.net))\nAND,((DOMAIN,x.example.net),(NOT,((NETWORK,udp))))\n", Hint{})
	om, _ := NewMatcher(only.Rules)
	for _, q := range []Query{
		{Domain: "a.both.example.net", Port: 443, Network: "tcp"},
		{Domain: "a.both.example.net", Port: 80, Network: "tcp"},
		{Domain: "one.example.net", Port: 80, Network: "tcp"},
		{Domain: "three.example.net", Port: 80, Network: "tcp"},
		{Domain: "x.example.net", Port: 80, Network: "tcp"},
		{Domain: "x.example.net", Port: 80, Network: "udp"},
	} {
		if got, core := om.Match(q) == Match, coreMatch(t, only.Rules, q); got != core {
			t.Errorf("%+v: matcher %v, core %v", q, got, core)
		}
	}
}

func TestClashProviderBehaviors(t *testing.T) {
	domain := mustParse(t, "payload:\n  - '+.a.example.net'\n  - 'b.example.net'\n", Hint{})
	if domain.Behavior != "domain" || domain.Stats.Domain != 2 {
		t.Errorf("domain behavior: %+v", domain)
	}
	cidr := mustParse(t, "payload:\n  - 192.0.2.0/24\n  - 2001:db8::/32\n", Hint{})
	if cidr.Behavior != "ipcidr" || cidr.Stats.IP != 2 {
		t.Errorf("ipcidr behavior: %+v", cidr)
	}
	classical := mustParse(t, "payload:\n  - DOMAIN-SUFFIX,a.example.net\n  - IP-CIDR,192.0.2.0/24,no-resolve\n  - USER-AGENT,x\n", Hint{})
	if classical.Behavior != "classical" || classical.Stats.Domain != 1 || classical.Stats.IP != 1 || classical.Stats.Unsupported["USER-AGENT"] != 1 {
		t.Errorf("classical behavior: %+v", classical)
	}
}

func TestTextFormats(t *testing.T) {
	ag := mustParse(t, "[Adblock Plus 2.0]\n! x\n||ads.example.net^\n||tracker.example.net^$important\n@@||ok.ads.example.net^\n", Hint{})
	if ag.Format != FormatAdGuard {
		t.Fatalf("format %s", ag.Format)
	}
	if !matches(t, ag, "ads.example.net") || !matches(t, ag, "x.ads.example.net") || !matches(t, ag, "tracker.example.net") {
		t.Error("adguard blocks must match")
	}
	if matches(t, ag, "ok.ads.example.net") || matches(t, ag, "fine.example.net") {
		t.Error("adguard exceptions and unrelated names must not match")
	}
	hosts := mustParse(t, "# hosts\n127.0.0.1 localhost\n::1 localhost\n0.0.0.0 a.example.net b.example.net # note\n0.0.0.0 0.0.0.0\n", Hint{})
	if hosts.Format != FormatHosts || hosts.Stats.Domain != 2 || !matches(t, hosts, "a.example.net") || matches(t, hosts, "localhost") {
		t.Errorf("hosts: %+v", hosts)
	}
	cidr := mustParse(t, "# nets\n192.0.2.0/24\n198.51.100.7\nnot-an-address\n2001:db8::/32\n", Hint{})
	if cidr.Format != FormatCIDRList || cidr.Stats.IP != 3 || cidr.Stats.Ignored != 1 {
		t.Errorf("cidr list: %+v", cidr)
	}
	if !slices.Equal(cidr.Rules[0].DefaultOptions.IPCIDR, []string{"192.0.2.0/24", "198.51.100.7/32", "2001:db8::/32"}) {
		t.Errorf("cidr normalization: %v", cidr.Rules[0].DefaultOptions.IPCIDR)
	}
}

func TestSingBoxJSONAndSRSRoundTrip(t *testing.T) {
	js := `{"version":3,"rules":[
	  {"domain_suffix":["a.example.net",".sub-only.example.net"],"domain":["x.example.net"]},
	  {"ip_cidr":["192.0.2.0/24"]},
	  {"type":"logical","mode":"and","rules":[{"domain_suffix":["b.example.net"]},{"port":[443]}]},
	  {"domain_keyword":["kw"],"invert":true}
	]}`
	p := mustParse(t, js, Hint{})
	if p.Format != FormatSingBox || len(p.Rules) != 4 {
		t.Fatalf("json: %+v", p)
	}
	data, err := Compile(p.Rules)
	if err != nil {
		t.Fatal(err)
	}
	back := mustParse(t, string(data), Hint{})
	if back.Format != FormatSRS {
		t.Fatalf("compiled file reads as %s", back.Format)
	}
	// What was written reads back with the same meaning.
	for _, host := range []string{"a.example.net", "q.a.example.net", "sub-only.example.net", "q.sub-only.example.net", "x.example.net", "b.example.net", "other.test", "has-kw.test"} {
		q := Query{Domain: host, Port: 443, Network: "tcp"}
		a, b := coreMatch(t, p.Rules, q), coreMatch(t, back.Rules, q)
		if a != b {
			t.Errorf("%s: before %v, after round trip %v", host, a, b)
		}
		m1, _ := NewMatcher(p.Rules)
		m2, _ := NewMatcher(back.Rules)
		if m1.Match(q) != m2.Match(q) || (m1.Match(q) == Match) != a {
			t.Errorf("%s: matcher disagrees with the core", host)
		}
	}
	data2, _ := Compile(back.Rules)
	if Digest(data) == "" || len(data2) == 0 {
		t.Error("digest/compile empty")
	}
}

func TestCompileIsDeterministic(t *testing.T) {
	a := []option.HeadlessRule{def(option.DefaultHeadlessRule{DomainSuffix: []string{"b.example.net", "a.example.net", "a.example.net"}})}
	b := []option.HeadlessRule{def(option.DefaultHeadlessRule{DomainSuffix: []string{"a.example.net"}}), def(option.DefaultHeadlessRule{DomainSuffix: []string{"b.example.net"}})}
	da, db := splitCompile(t, a), splitCompile(t, b)
	if da != db {
		t.Error("the same set of rules must compile to the same file whatever the order")
	}
}

func splitCompile(t *testing.T, rules []option.HeadlessRule) string {
	t.Helper()
	dom, _ := Split(rules)
	data, err := Compile(dom)
	if err != nil {
		t.Fatal(err)
	}
	return Digest(data)
}

// Splitting into a domain part and an IP part must not change what matches:
// the parts together are the original, and only the IP part needs addresses.
func TestSplitKeepsMeaning(t *testing.T) {
	rules := []option.HeadlessRule{
		def(option.DefaultHeadlessRule{DomainSuffix: []string{"a.example.net"}, IPCIDR: []string{"192.0.2.0/24"}}),
		def(option.DefaultHeadlessRule{Domain: []string{"exact.example.net"}}),
		def(option.DefaultHeadlessRule{IPCIDR: []string{"198.51.100.0/24"}}),
		def(option.DefaultHeadlessRule{DomainSuffix: []string{"both.example.net"}, IPCIDR: []string{"203.0.113.0/24"}, Port: []uint16{443}}),
		def(option.DefaultHeadlessRule{ProcessName: []string{"tool"}}),
		def(option.DefaultHeadlessRule{DomainKeyword: []string{"kw"}, Invert: true}),
		{Type: C.RuleTypeLogical, LogicalOptions: option.LogicalHeadlessRule{Mode: C.LogicalTypeOr, Rules: []option.HeadlessRule{
			def(option.DefaultHeadlessRule{DomainSuffix: []string{"l.example.net"}}),
			def(option.DefaultHeadlessRule{IPCIDR: []string{"100.64.0.0/10"}}),
		}}},
	}
	dom, ip := Split(rules)
	for _, r := range dom {
		if needsAddress(r) {
			t.Errorf("domain part holds a rule that needs an address: %+v", r)
		}
	}
	if len(ip) == 0 {
		t.Fatal("no IP part")
	}
	hasIP := false
	for _, r := range ip {
		hasIP = hasIP || needsAddress(r)
	}
	if !hasIP {
		t.Error("IP part has nothing that needs an address")
	}
	ips := func(s ...string) []netip.Addr {
		var out []netip.Addr
		for _, v := range s {
			out = append(out, netip.MustParseAddr(v))
		}
		return out
	}
	queries := []Query{
		{Domain: "a.example.net", Port: 443, Addrs: ips("203.0.113.9")},
		{Domain: "x.a.example.net", Port: 80, Addrs: ips("10.0.0.1")},
		{Domain: "exact.example.net", Port: 80, Addrs: ips("10.0.0.1")},
		{Domain: "nothing.test", Port: 80, Addrs: ips("198.51.100.20")},
		{Domain: "nothing.test", Port: 80, Addrs: ips("192.0.2.77")},
		{Domain: "nothing.test", Port: 80, Addrs: ips("203.0.113.5")},
		{Domain: "nothing.test", Port: 443, Addrs: ips("203.0.113.5")},
		{Domain: "both.example.net", Port: 443, Addrs: ips("10.0.0.1")},
		{Domain: "both.example.net", Port: 80, Addrs: ips("10.0.0.1")},
		{Domain: "l.example.net", Port: 80, Addrs: ips("10.0.0.1")},
		{Domain: "nothing.test", Port: 80, Addrs: ips("100.64.1.1")},
		{Domain: "has-kw.test", Port: 80, Addrs: ips("10.0.0.1")},
		{Domain: "plain.test", Port: 80, Addrs: ips("10.0.0.1")},
		{Addrs: ips("192.0.2.5"), Port: 80},
		{Addrs: ips("10.9.9.9"), Port: 80},
	}
	for _, q := range queries {
		q.Network = "tcp"
		want := coreMatch(t, rules, q)
		got := coreMatch(t, dom, q) || coreMatch(t, ip, q)
		if want != got {
			t.Errorf("%+v: original %v, split %v", q, want, got)
		}
	}
}

func TestMatcherUnknownAndEmpty(t *testing.T) {
	m, err := NewMatcher([]option.HeadlessRule{def(option.DefaultHeadlessRule{ProcessName: []string{"tool"}})})
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Match(Query{Domain: "a.example.net"}); got != Unknown {
		t.Errorf("a process rule cannot be decided from a URL: %v", got)
	}
	m, _ = NewMatcher([]option.HeadlessRule{def(option.DefaultHeadlessRule{IPCIDR: []string{"192.0.2.0/24"}})})
	if got := m.Match(Query{Domain: "a.example.net"}); got != Unknown {
		t.Errorf("an IP rule without addresses is undecided: %v", got)
	}
	if got := m.Match(Query{Domain: "a.example.net", Addrs: []netip.Addr{netip.MustParseAddr("198.51.100.1")}}); got != NoMatch {
		t.Errorf("an IP outside the range: %v", got)
	}
	if got := m.Match(Query{Addrs: []netip.Addr{netip.MustParseAddr("192.0.2.9")}}); got != Match {
		t.Errorf("an IP inside the range: %v", got)
	}
	if out, err := Compile(nil); out != nil || err != nil {
		t.Errorf("an empty list compiles to nothing: %v %v", out, err)
	}
}

func TestBadInputIsDroppedNotFatal(t *testing.T) {
	p := mustParse(t, "DOMAIN-REGEX,([unclosed\nDOMAIN-SUFFIX,ok.example.net\nIP-CIDR,not-an-ip\nDST-PORT,notaport\n", Hint{})
	if p.Stats.Ignored != 3 {
		t.Errorf("ignored %d, want 3", p.Stats.Ignored)
	}
	// A bad regular expression coming from an .srs or JSON file must not
	// poison the compiled set either.
	rules := []option.HeadlessRule{def(option.DefaultHeadlessRule{DomainRegex: []string{"([bad", `^ok\.example\.net$`}, DomainSuffix: []string{""}})}
	dom, _ := Split(rules)
	data, err := Compile(dom)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if got := mustParse(t, string(data), Hint{}); got.Stats.Domain != 1 {
		t.Errorf("only the good expression should remain: %+v", got.Stats)
	}
	if _, err := Parse([]byte("# nothing\n\n"), Hint{}); err == nil || strings.Contains(err.Error(), "panic") {
		t.Errorf("empty text: %v", err)
	}
}

func FuzzParse(f *testing.F) {
	srsData, _ := Compile([]option.HeadlessRule{def(option.DefaultHeadlessRule{DomainSuffix: []string{"example.net"}, IPCIDR: []string{"192.0.2.0/24"}})})
	for _, s := range []string{
		string(srsData), "payload:\n  - '+.a.example.net'\n", "DOMAIN-SUFFIX,a.example.net\nAND,((DOMAIN,x),(NOT,((NETWORK,udp))))\n",
		"||a.example.net^\n@@||b.example.net^\n", "0.0.0.0 a.example.net\n", "192.0.2.0/24\n", ".a.example.net\n*.b.example.net\n",
		`{"version":2,"rules":[{"domain":["a"]}]}`, "AND,(((((((((", "NOT,()", "DST-PORT,1-2-3", "MRS\x01", "\x28\xb5\x2f\xfd",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		p, err := Parse(data, Hint{})
		if err != nil {
			return
		}
		dom, ip := Split(p.Rules)
		for _, part := range [][]option.HeadlessRule{dom, ip} {
			if _, err := Compile(part); err != nil {
				t.Fatalf("a parsed list must compile: %v", err)
			}
			if _, err := NewMatcher(part); err != nil {
				t.Fatalf("a parsed list must be matchable: %v", err)
			}
		}
	})
}
