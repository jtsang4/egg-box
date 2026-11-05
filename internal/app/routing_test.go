package app_test

// What the service does with a person's routing: the plan a subscription
// brings, the files behind it, the answer to "where would this go?", and the
// words for why a connection went where it did. The pieces run for real (the
// library, the rule data, the compiler, the generator and the agent's
// controller); only the core and the network are made up.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"eggbox/internal/agentapi"
	"eggbox/internal/app"
	"eggbox/internal/library"
	"eggbox/internal/routing"
	"eggbox/internal/ruledata"
	"eggbox/internal/rulefmt"
	"eggbox/internal/testsupport/srs"
)

// ------------------------------------------------------------------ helpers

const siteRaw = "https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/sing/geo/"

// routedProfile is what a provider with a routing plan serves: two nodes, and
// a group of rules for each zone asked for ("video", "domestic" or "chat").
// The names are the provider's own and mean nothing to the application.
func routedProfile(zones ...string) string {
	type zone struct {
		name         string
		goesDirect   bool
		sites, geoip []string
	}
	known := map[string]zone{
		"video":    {name: "Video Zone", sites: []string{"netflix"}},
		"domestic": {name: "Home Zone", goesDirect: true, sites: []string{"cn"}, geoip: []string{"cn"}},
		"chat":     {name: "Chat Zone", sites: []string{"telegram"}},
	}
	outs := []any{
		map[string]any{"type": "selector", "tag": "Proxy Select", "outbounds": []any{"Fastest", "HK 01", "JP 01", "DIRECT"}},
		map[string]any{"type": "urltest", "tag": "Fastest", "outbounds": []any{"HK 01", "JP 01"}},
		map[string]any{"type": "direct", "tag": "DIRECT"},
	}
	for i, n := range []string{"HK 01", "JP 01"} {
		host := fmt.Sprintf("srv%d-%s.example.net", i, strings.ToLower(strings.Fields(n)[0]))
		outs = append(outs, map[string]any{
			"type": "trojan", "tag": n, "server": host, "server_port": 443, "password": fmt.Sprintf("pw%d", i),
			"tls": map[string]any{"enabled": true, "server_name": host},
		})
	}
	var sets, rules []any
	addSet := func(tag, kind, name string) {
		sets = append(sets, map[string]any{"tag": tag, "type": "remote", "format": "binary", "url": siteRaw + kind + "/" + name + ".srs"})
	}
	for _, key := range zones {
		z := known[key]
		members := []any{"Proxy Select", "DIRECT"}
		if z.goesDirect {
			members = []any{"DIRECT", "Proxy Select"}
		}
		outs = append(outs, map[string]any{"type": "selector", "tag": z.name, "outbounds": members})
		var tags []any
		for _, s := range z.sites {
			addSet(s, "geosite", s)
			tags = append(tags, s)
		}
		rules = append(rules, map[string]any{"rule_set": tags, "outbound": z.name})
		for _, ip := range z.geoip {
			addSet(ip+"-ip", "geoip", ip)
			rules = append(rules, map[string]any{"rule_set": []any{ip + "-ip"}, "outbound": z.name})
		}
	}
	root := map[string]any{
		"outbounds": outs,
		"route":     map[string]any{"rules": rules, "rule_set": sets, "final": "Proxy Select"},
	}
	raw, err := json.Marshal(root)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// pathOf is where the rule server finds the file at a canonical address.
func pathOf(t *testing.T, canonical string) string {
	t.Helper()
	u, err := url.Parse(canonical)
	if err != nil {
		t.Fatal(err)
	}
	return u.Path
}

// eventually waits for something the service does in the background.
func (h *harness) eventually(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	h.t.Fatalf("timed out waiting for %s", what)
}

// waitPlan waits until the plan has been compiled and cond holds for it.
func (h *harness) waitPlan(what string, cond func(app.PlanInfo) bool) app.PlanInfo {
	h.t.Helper()
	return h.waitFor(what, func(s *app.Snapshot) bool { return s.Plan.Ready && cond(s.Plan) }).Plan
}

// edit changes the routing the way the interface does, and waits for the plan
// to be looked at again.
func (h *harness) edit(fn func(*routing.State) error) {
	h.t.Helper()
	seq := h.svc.Snapshot().Plan.Seq
	if err := h.svc.EditRouting(h.ctx(), fn); err != nil {
		h.t.Fatal(err)
	}
	h.waitFor("the plan to be looked at again", func(s *app.Snapshot) bool { return s.Plan.Seq > seq })
}

// testURL asks where an address would go until the answer is the one wanted:
// the plan is compiled in the background, so an edit takes a moment to count.
func (h *harness) testURL(target string, want func(*app.TestURLResult) bool) *app.TestURLResult {
	h.t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for {
		res, err := h.svc.TestURL(h.ctx(), target)
		if err == nil && want(res) {
			return res
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("TestURL(%q) = %+v, %v", target, res, err)
		}
		time.Sleep(15 * time.Millisecond)
	}
}

// ruleSetTags lists the rule sets of the configuration the core runs.
func (h *harness) ruleSetTags() []string {
	var cfg struct {
		Route struct {
			RuleSet []struct {
				Tag string `json:"tag"`
			} `json:"rule_set"`
		} `json:"route"`
	}
	if json.Unmarshal(h.agent.Engine.Current().JSON, &cfg) != nil {
		return nil
	}
	var tags []string
	for _, rs := range cfg.Route.RuleSet {
		tags = append(tags, rs.Tag)
	}
	return tags
}

func countPrefix(tags []string, prefix string) int {
	n := 0
	for _, t := range tags {
		if strings.HasPrefix(t, prefix) {
			n++
		}
	}
	return n
}

func noticesOf(s *app.Snapshot, code string) []app.Notice {
	var out []app.Notice
	for _, n := range s.Notices {
		if n.Code == code {
			out = append(out, n)
		}
	}
	return out
}

func rowNamed(t *testing.T, st routing.State, name string) routing.Row {
	t.Helper()
	for _, r := range st.View().Rows {
		if r.Item.Name == name {
			return r
		}
	}
	t.Fatalf("no item named %q", name)
	return routing.Row{}
}

func (h *harness) connect() {
	h.t.Helper()
	if err := h.svc.Connect(h.ctx(), app.ConnectOptions{}); err != nil {
		h.t.Fatal(err)
	}
	h.waitFor("connected", func(s *app.Snapshot) bool { return s.Phase == app.PhaseConnected })
}

// ------------------------------------------------------------ plans arrive

func TestASubscriptionsRoutingIsAdoptedAndDrivesTheCore(t *testing.T) {
	h := newHarness(t, nil)
	h.prov.body.Store(routedProfile("video", "domestic"))
	sub, res, err := h.svc.AddSubscription(h.ctx(), "", h.prov.link(), library.RemoteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Plan.Stored || !res.Plan.Adopted || res.Plan.Items != 2 {
		t.Fatalf("the plan the subscription brought: %+v", res.Plan)
	}

	// The first subscription's plan is the one in force, with the main exit
	// its provider started on and each item where its provider sent it.
	st := h.lib.Routing()
	if st.ActiveID() != routing.SubPlanID(sub.ID) {
		t.Fatalf("the plan in force is %q", st.ActiveID())
	}
	if got := rowNamed(t, st, "Video Zone").Item.Dest.Kind; got != routing.DestFollow {
		t.Errorf("Video Zone goes %v", got)
	}
	if got := rowNamed(t, st, "Home Zone").Item.Dest.Kind; got != routing.DestDirect {
		t.Errorf("Home Zone goes %v", got)
	}
	if st.Main.Kind != routing.DestAuto {
		t.Errorf("the main exit starts as %v", st.Main.Kind)
	}
	if got := noticesOf(h.svc.Snapshot(), "plan_adopted"); len(got) != 1 || got[0].Args["name"] != sub.Name {
		t.Errorf("the person is told what happened: %+v", got)
	}

	// Its rule files come with it, and the core is given rule sets for them.
	p := h.waitPlan("the plan's rule files", func(p app.PlanInfo) bool { return len(p.Items) == 2 && p.Missing == 0 })
	if len(p.Sources) != 3 {
		t.Errorf("the plan uses %d files, want 3", len(p.Sources))
	}
	for u, src := range p.Sources {
		if !src.Present || src.Err != "" || src.Format != "srs" {
			t.Errorf("%s: %+v", u, src)
		}
	}
	h.connect()
	h.eventually("the core to run the new plan", func() bool {
		tags := h.ruleSetTags()
		return countPrefix(tags, "d-") == 2 && countPrefix(tags, "p-") == 1
	})
	for _, tag := range h.ruleSetTags() {
		if strings.HasPrefix(tag, "d-b-") || strings.HasPrefix(tag, "p-b-") {
			t.Errorf("rule set %q belongs to the built-in plan, which is not in force", tag)
		}
	}
}

func TestAPastedProfileBringsItsRoutingToo(t *testing.T) {
	h := newHarness(t, nil)
	sub, err := h.svc.ImportNodes(h.ctx(), "pasted", routedProfile("video", "chat"))
	if err != nil {
		t.Fatal(err)
	}
	st := h.lib.Routing()
	if st.ActiveID() != routing.SubPlanID(sub.ID) || len(st.View().Rows) != 2 {
		t.Fatalf("plan in force %q with %d items", st.ActiveID(), len(st.View().Rows))
	}
	snap := h.svc.Snapshot()
	if len(noticesOf(snap, "nodes_imported")) != 1 || len(noticesOf(snap, "plan_adopted")) != 1 {
		t.Errorf("notices: %+v", snap.Notices)
	}
	// Plain share links carry no routing: the plan in force stays.
	if _, err := h.svc.ImportNodes(h.ctx(), "links", links("US 01", "SG 01")); err != nil {
		t.Fatal(err)
	}
	if got := h.lib.Routing().ActiveID(); got != routing.SubPlanID(sub.ID) {
		t.Errorf("a list of links changed the plan in force: %q", got)
	}
}

func TestAProvidersChangesToItsPlanReachARunningConnectionAndKeepChoices(t *testing.T) {
	h := newHarness(t, nil)
	h.prov.body.Store(routedProfile("video", "domestic"))
	sub := h.addSubscription()
	h.waitPlan("the plan's rule files", func(p app.PlanInfo) bool { return len(p.Items) == 2 && p.Missing == 0 })

	// The person sends Video Zone straight out; the provider will not know.
	video := rowNamed(t, h.lib.Routing(), "Video Zone")
	h.edit(func(st *routing.State) error { return st.SetItemDest(video.Item.ID, routing.Direct()) })
	h.connect()
	h.eventually("the core to run the plan", func() bool { return countPrefix(h.ruleSetTags(), "p-") == 1 })
	revision := h.svc.Snapshot().Applied.Revision

	// The provider drops one zone and adds another.
	h.prov.body.Store(routedProfile("video", "chat"))
	res, err := h.svc.RefreshSubscription(h.ctx(), sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Plan.Changed || len(res.Plan.Change.Added) != 1 || len(res.Plan.Change.Removed) != 1 {
		t.Fatalf("the refresh reported %+v", res.Plan)
	}
	snap := h.svc.Snapshot()
	for _, code := range []string{"plan_items_added", "plan_items_removed"} {
		got := noticesOf(snap, code)
		if len(got) != 1 || got[0].Args["count"] != "1" || got[0].Args["name"] != sub.Name {
			t.Errorf("%s: %+v", code, got)
		}
	}
	if got := rowNamed(t, h.lib.Routing(), "Video Zone").Item.Dest; got.Kind != routing.DestDirect {
		t.Errorf("the person's choice for Video Zone was lost: %+v", got)
	}
	if st := h.lib.Routing(); len(st.View().Rows) != 2 {
		t.Errorf("the plan has %d items", len(st.View().Rows))
	}

	// The running core got the new plan without being asked: Home Zone's
	// address rules are gone, Chat Zone's names are in. The data for the
	// new item may still be on its way, hence "eventually".
	h.eventually("the core to run the provider's new plan", func() bool {
		tags := h.ruleSetTags()
		return h.svc.Snapshot().Applied.Revision > revision && countPrefix(tags, "p-") == 0 && countPrefix(tags, "d-") == 2
	})
}

// A refresh the scheduler makes on its own is no different from one the
// person asks for: a provider that changes only its plan reaches the core.
func TestAScheduledRefreshThatOnlyChangesThePlanReachesTheCore(t *testing.T) {
	h := newHarness(t, func(c *app.Config) {
		c.ScheduleDelay = 10 * time.Millisecond
		c.ScheduleEvery = 30 * time.Millisecond
	})
	h.prov.body.Store(routedProfile("video", "domestic"))
	h.addSubscription()
	h.waitPlan("the plan's rule files", func(p app.PlanInfo) bool { return len(p.Items) == 2 && p.Missing == 0 })
	h.connect()
	h.eventually("the core to run the plan", func() bool { return countPrefix(h.ruleSetTags(), "p-") == 1 })
	revision := h.svc.Snapshot().Applied.Revision

	h.prov.body.Store(routedProfile("video")) // the nodes stay as they are
	h.clock.Advance(25 * time.Hour)           // the subscription is due
	h.eventually("the scheduled refresh to reach the core", func() bool {
		return h.svc.Snapshot().Applied.Revision > revision && countPrefix(h.ruleSetTags(), "p-") == 0
	})
	if got := noticesOf(h.svc.Snapshot(), "subscriptions_auto_updated"); len(got) == 0 {
		t.Error("the person is not told the subscriptions were updated")
	}
}

// ---------------------------------------------------- where would it go?

// A name no item lists is looked up, and where it leads can decide: the
// address part of an item (a service's published ranges) comes after every
// name in the plan, and a name nobody can look up falls to the fallback.
func TestAnAddressTheNameLeadsToDecidesWhenNoNameDoes(t *testing.T) {
	h := newHarness(t, nil)
	h.addSubscription()
	h.waitPlan("the built-in plan's rule files", func(p app.PlanInfo) bool { return len(p.Items) == 12 && p.Missing == 0 })

	inside := func(set string) string { return netip.MustParsePrefix(cidrOf(set)).Addr().Next().Next().String() }
	h.dns.set("video.unlisted.example", inside("netflix"))
	h.dns.set("elsewhere.unlisted.example", "198.51.100.9")
	h.dns.set("both.unlisted.example", inside("google"), inside("netflix")) // streaming comes before Google in the plan

	res := h.testURL("https://video.unlisted.example/", func(r *app.TestURLResult) bool { return r.Why.ID == "b-streaming" })
	if !res.Resolved || res.ResolveErr != "" || len(res.Addrs) != 1 || res.Addrs[0].String() != inside("netflix") || res.Source == nil {
		t.Errorf("a name that leads into an item's addresses: %+v", res)
	}
	res = h.testURL("elsewhere.unlisted.example", func(r *app.TestURLResult) bool { return r.Why.Kind == app.WhyFallback })
	if !res.Resolved || res.ResolveErr != "" || len(res.Addrs) != 1 || res.Source != nil {
		t.Errorf("a name that leads nowhere in the plan: %+v", res)
	}
	res = h.testURL("both.unlisted.example", func(r *app.TestURLResult) bool { return r.Why.Kind == app.WhyItem })
	if res.Why.ID != "b-streaming" || len(res.Addrs) != 2 {
		t.Errorf("the item earlier in the plan decides, whatever the order of the addresses: %+v", res)
	}
	res = h.testURL("nobody-knows.unlisted.example", func(r *app.TestURLResult) bool { return r.Why.Kind == app.WhyFallback })
	if !res.Resolved || res.ResolveErr != "no answer" || len(res.Addrs) != 0 {
		t.Errorf("a name nobody can look up says so and falls back: %+v", res)
	}
}

func TestTestURLEvaluatesThePlanTheWayTheCoreDoes(t *testing.T) {
	h := newHarness(t, nil)
	h.addSubscription()
	h.waitPlan("the built-in plan's rule files", func(p app.PlanInfo) bool { return len(p.Items) == 12 && p.Missing == 0 })

	check := func(target string, why app.WhyKind, id string, dest routing.DestKind) *app.TestURLResult {
		t.Helper()
		return h.testURL(target, func(r *app.TestURLResult) bool {
			return r.Why.Kind == why && r.Why.ID == id && r.Dest.Kind == dest
		})
	}

	// A name an item knows: no lookup is needed, and the answer says which
	// file the name came from.
	res := check("https://chat.category-ai-cn.example/a?b=c", app.WhyItem, "b-ai", routing.DestFollow)
	if res.Host != "chat.category-ai-cn.example" || res.Port != 443 || res.Resolved || len(res.Addrs) != 0 {
		t.Errorf("%+v", res)
	}
	if res.Source == nil || res.Source.Name != "geosite: category-ai-!cn" || res.Source.Project != routing.MetaCubeX || res.Source.URL == "" {
		t.Errorf("source %+v", res.Source)
	}
	check("microsoft.example", app.WhyItem, "b-microsoft", routing.DestDirect)
	res = check("HTTPS://API.GitHub.Example./x", app.WhyItem, "b-github", routing.DestFollow)
	if res.Host != "api.github.example" || res.Port != 443 {
		t.Errorf("an address is read the way a browser reads it: %+v", res)
	}
	if res = check("http://www.cn.example:8080/", app.WhyItem, "b-cn", routing.DestDirect); res.Port != 8080 {
		t.Errorf("port %d", res.Port)
	}

	// The local network, and what nothing claims: addresses need no lookup.
	if res = check("192.168.1.20:5000", app.WhyLAN, "", routing.DestDirect); res.Port != 5000 || len(res.Addrs) != 1 {
		t.Errorf("%+v", res)
	}
	check("printer.local", app.WhyLAN, "", routing.DestDirect)
	res = check("198.51.100.7", app.WhyFallback, "", routing.DestFollow)
	if res.Port != 80 || res.Resolved || len(res.Addrs) != 1 || res.Addrs[0] != netip.MustParseAddr("198.51.100.7") {
		t.Errorf("%+v", res)
	}
	check("[2001:db8::7]:8443", app.WhyFallback, "", routing.DestFollow)

	// What cannot be an address is refused, whatever it looks like.
	for _, bad := range []string{"", "   ", "ftp://example.org/x", "exa mple.org", "http://", "host:99999", "-bad-.example", "a..b"} {
		if _, err := h.svc.TestURL(h.ctx(), bad); !errors.Is(err, app.ErrBadTarget) {
			t.Errorf("TestURL(%q) = %v, want ErrBadTarget", bad, err)
		}
	}

	// The exceptions come first, in order, and an off one is not asked.
	var corp, gh routing.Exception
	h.edit(func(st *routing.State) (err error) {
		if corp, err = st.AddException(routing.Exception{Kind: routing.EntryDomainSuffix, Value: "Corp.Example", Dest: routing.Reject(), Enabled: true}); err != nil {
			return err
		}
		gh, err = st.AddException(routing.Exception{Kind: routing.EntryDomainSuffix, Value: "github.example", Dest: routing.Direct(), Enabled: true})
		return err
	})
	res = h.testURL("https://wiki.corp.example", func(r *app.TestURLResult) bool { return r.Why.Kind == app.WhyException })
	if res.Why.ID != corp.ID || res.Why.Index != 1 || res.Why.Name != "corp.example" || res.Dest.Kind != routing.DestReject || res.Exit != nil {
		t.Errorf("the first exception: %+v", res)
	}
	res = h.testURL("api.github.example", func(r *app.TestURLResult) bool { return r.Why.Kind == app.WhyException })
	if res.Why.ID != gh.ID || res.Why.Index != 2 || res.Dest.Kind != routing.DestDirect {
		t.Errorf("an exception goes before the items: %+v", res)
	}
	h.edit(func(st *routing.State) error { return st.SetExceptionEnabled(gh.ID, false) })
	check("api.github.example", app.WhyItem, "b-github", routing.DestFollow)

	// Exceptions that ask about a port or a program: the port is part of the
	// address, the program is not, and the answer says what it could not tell.
	h.edit(func(st *routing.State) error {
		if _, err := st.AddException(routing.Exception{Kind: routing.EntryPort, Value: "8443", Dest: routing.Reject(), Enabled: true}); err != nil {
			return err
		}
		_, err := st.AddException(routing.Exception{Kind: routing.EntryProcessPath, Value: "/opt/chat/bin/chat", Dest: routing.Direct(), Enabled: true})
		return err
	})
	h.testURL("198.51.100.7:8443", func(r *app.TestURLResult) bool {
		return r.Why.Kind == app.WhyException && r.Dest.Kind == routing.DestReject
	})
	res = h.testURL("198.51.100.7:8444", func(r *app.TestURLResult) bool { return r.Why.Kind == app.WhyFallback && r.Skipped > 0 })
	if res.Skipped != 1 {
		t.Errorf("%d rules could not be decided, want 1 (the program)", res.Skipped)
	}

	// An address-only rule counts once the host-name rules had their say. The
	// invented geoip ranges are private ones, so the local network is let go.
	prefs := h.lib.Prefs()
	prefs.LANDirect = false
	if _, err := h.svc.SavePrefs(h.ctx(), prefs); err != nil {
		t.Fatal(err)
	}
	cn := netip.MustParsePrefix(cidrOf("cn")).Addr().Next().Next()
	res = h.testURL(cn.String(), func(r *app.TestURLResult) bool { return r.Why.Kind == app.WhyItem })
	if res.Why.ID != "b-cn" || res.Dest.Kind != routing.DestDirect || res.Source == nil || res.Source.Name != "geoip: cn" {
		t.Errorf("an address in a geoip range: %+v", res)
	}
}

func TestTestURLSaysWhichItemsCouldNotTakePart(t *testing.T) {
	aiPath := pathOf(t, routing.GeoSite("category-ai-!cn"))
	h := newHarnessWith(t, nil, func(rs *ruleServer) { rs.fail(aiPath, true) })
	h.addSubscription()
	h.waitPlan("the files that can be had", func(p app.PlanInfo) bool { return len(p.Items) == 12 && p.Missing == 1 && !p.Updating })

	res := h.testURL("198.51.100.7", func(r *app.TestURLResult) bool { return r.Why.Kind == app.WhyFallback })
	if res.Waiting != 1 {
		t.Errorf("%d items are waiting for data, want 1", res.Waiting)
	}
	// What that item would have claimed is, for now, nobody's.
	h.testURL("chat.category-ai-cn.example", func(r *app.TestURLResult) bool { return r.Why.Kind != app.WhyItem || r.Why.ID != "b-ai" })
}

// ------------------------------------------------------ from a connection

func TestConnectionsOfferTheExceptionsThatMakeSense(t *testing.T) {
	type cand struct {
		scope app.Scope
		kind  routing.EntryKind
		value string
	}
	cases := []struct {
		name string
		conn app.Conn
		want []cand
	}{
		{"all four", app.Conn{Host: "www.bbc.co.uk", Destination: "151.101.0.81:443", Process: "/usr/bin/curl"}, []cand{
			{app.ScopeHost, routing.EntryDomain, "www.bbc.co.uk"},
			{app.ScopeDomain, routing.EntryDomainSuffix, "bbc.co.uk"},
			{app.ScopeIP, routing.EntryIPCIDR, "151.101.0.81/32"},
			{app.ScopeProcess, routing.EntryProcessPath, "/usr/bin/curl"},
		}},
		{"case and the final dot", app.Conn{Host: "WWW.Example.COM."}, []cand{
			{app.ScopeHost, routing.EntryDomain, "www.example.com"},
			{app.ScopeDomain, routing.EntryDomainSuffix, "example.com"},
		}},
		// The public suffix list says where one site ends and another begins.
		{"a site on a shared suffix", app.Conn{Host: "a.b.github.io"}, []cand{
			{app.ScopeHost, routing.EntryDomain, "a.b.github.io"},
			{app.ScopeDomain, routing.EntryDomainSuffix, "b.github.io"},
		}},
		{"a private suffix", app.Conn{Host: "foo.appspot.com"}, []cand{
			{app.ScopeHost, routing.EntryDomain, "foo.appspot.com"},
			{app.ScopeDomain, routing.EntryDomainSuffix, "foo.appspot.com"},
		}},
		// A name that is a suffix itself has no site of its own to widen to.
		{"a suffix itself", app.Conn{Host: "github.io"}, []cand{{app.ScopeHost, routing.EntryDomain, "github.io"}}},
		{"a name without a dot", app.Conn{Host: "localhost"}, []cand{{app.ScopeHost, routing.EntryDomain, "localhost"}}},
		{"a name the list does not know", app.Conn{Host: "my-pc.lan"}, []cand{
			{app.ScopeHost, routing.EntryDomain, "my-pc.lan"},
			{app.ScopeDomain, routing.EntryDomainSuffix, "my-pc.lan"},
		}},
		// An address is not a name: only the address is offered.
		{"an address as the name", app.Conn{Host: "93.184.216.34", Destination: "93.184.216.34:443"}, []cand{
			{app.ScopeIP, routing.EntryIPCIDR, "93.184.216.34/32"},
		}},
		{"an IPv6 destination", app.Conn{Destination: "[2001:db8::1]:443"}, []cand{{app.ScopeIP, routing.EntryIPCIDR, "2001:db8::1/128"}}},
		{"an IPv4 address in IPv6 clothes", app.Conn{Destination: "[::ffff:1.2.3.4]:80"}, []cand{{app.ScopeIP, routing.EntryIPCIDR, "1.2.3.4/32"}}},
		{"a program on Windows", app.Conn{Process: `C:\Program Files\App\app.exe`}, []cand{{app.ScopeProcess, routing.EntryProcessPath, `C:\Program Files\App\app.exe`}}},
		{"nothing known", app.Conn{}, nil},
	}
	for _, c := range cases {
		var got []cand
		for _, x := range app.ConnCandidates(c.conn) {
			got = append(got, cand{x.Scope, x.Entry.Kind, x.Entry.Value})
			// Whatever is offered can be made into an exception as it is.
			if err := x.Entry.Valid(); err != nil {
				t.Errorf("%s: %v is not a usable entry: %v", c.name, x.Entry, err)
			}
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: offered %v, want %v", c.name, got, c.want)
		}
	}
}

func TestConnectionsAreExplainedByWhatMatchedThem(t *testing.T) {
	h := newHarness(t, nil)
	h.addSubscription()
	var corp routing.Exception
	h.edit(func(st *routing.State) (err error) {
		corp, err = st.AddException(routing.Exception{Kind: routing.EntryDomainSuffix, Value: "corp.example", Dest: routing.Direct(), Enabled: true})
		return err
	})
	h.connect()

	// The core's rule text names the rule sets by the tags the application
	// wrote; wait until the configuration it runs has them.
	exTag, itemTag, ipTag := "x-"+corp.ID, "d-b-ai", "p-b-cn"
	h.eventually("the core to run the exception and the items", func() bool {
		tags := h.ruleSetTags()
		return slices.Contains(tags, exTag) && slices.Contains(tags, itemTag) && slices.Contains(tags, ipTag)
	})
	h.svc.WatchConnections(true)
	t.Cleanup(func() { h.svc.WatchConnections(false) })

	cases := []struct {
		id, rule, outbound string
		want               app.Why
	}{
		{"c1", "rule_set=" + exTag, "direct", app.Why{Kind: app.WhyException, ID: corp.ID, Name: "corp.example", Index: 1}},
		{"c2", "rule_set=" + itemTag, "item-b-ai", app.Why{Kind: app.WhyItem, ID: "b-ai"}},
		// Several rule sets of one rule are printed in brackets.
		{"c3", "rule_set=[" + ipTag + " somebody-elses]", "direct", app.Why{Kind: app.WhyItem, ID: "b-cn"}},
		{"c4", "ip_is_private=true", "direct", app.Why{Kind: app.WhyLAN}},
		{"c5", "domain_suffix=[local lan localdomain home.arpa]", "direct", app.Why{Kind: app.WhyLAN}},
		{"c6", "clash_mode=Global", "Proxy", app.Why{Kind: app.WhyMode}},
		// What no rule claimed is the fallback: the core prints no rule at all.
		{"c7", "", "Proxy", app.Why{Kind: app.WhyFallback}},
		// A rule the application did not write is not explained.
		{"c8", "rule_set=their-own-set", "Proxy", app.Why{}},
		{"c9", "", "", app.Why{}},
	}
	for _, c := range cases {
		h.agent.Engine.AddConnection(&agentapi.Connection{
			Id: c.id, Network: "tcp", Destination: "198.51.100.7:443", Domain: "x.example",
			Rule: c.rule, Outbound: c.outbound, CreatedUnixMs: time.Now().UnixMilli(),
		})
	}
	for _, c := range cases {
		var got app.Conn
		h.waitFor("connection "+c.id, func(s *app.Snapshot) bool {
			for _, x := range s.Conns {
				if x.ID == c.id {
					got = x
					return true
				}
			}
			return false
		})
		want := c.want
		if want.Kind == app.WhyItem {
			want.Name = got.Why.Name // the item's stored name; the interface words it itself
			if want.Name == "" {
				t.Errorf("%s: the item has no name: %+v", c.id, got.Why)
			}
		}
		if got.Why != want {
			t.Errorf("%s (%q): explained as %+v, want %+v", c.id, c.rule, got.Why, want)
		}
	}
}

// --------------------------------------------------------------- rule data

func TestMissingRuleDataLeavesItemsWaitingNotTheConnectionDown(t *testing.T) {
	aiURL := routing.GeoSite("category-ai-!cn")
	aiPath := pathOf(t, aiURL)
	h := newHarnessWith(t, nil, func(rs *ruleServer) { rs.fail(aiPath, true) })
	h.addSubscription()
	h.connect() // the AI item has no data, and that is no reason not to connect

	ai := rowByKey(t, h.lib.Routing(), "ai").Item
	snap := h.waitFor("the missing file to be known", func(s *app.Snapshot) bool {
		return s.Plan.Ready && s.Plan.Missing == 1 && !s.Busy.RuleData && len(noticesOf(s, "rule_data_failed")) > 0
	})
	st := snap.Plan.Items[ai.ID]
	if st.State != app.ItemUnavailable || st.Missing != 1 || !st.Failing {
		t.Errorf("the item: %+v", st)
	}
	src := snap.Plan.Sources[aiURL]
	if src.Present || src.Err == "" || strings.Contains(src.Err, "raw.githubusercontent") || strings.Contains(src.Err, "category-ai") {
		t.Errorf("the file's status must say why without repeating its address: %+v", src)
	}
	if got := noticesOf(snap, "items_waiting"); len(got) != 1 || got[0].Args["count"] != "1" {
		t.Errorf("the person is told one item waits: %+v", got)
	}
	failed := noticesOf(snap, "rule_data_failed")
	if len(failed) != 1 || failed[0].Args["failed"] != "1" || strings.Contains(failed[0].Detail, "category-ai") {
		t.Errorf("rule_data_failed: %+v", failed)
	}
	tags := h.ruleSetTags()
	if slices.Contains(tags, "d-b-ai") || !slices.Contains(tags, "d-b-github") {
		t.Errorf("the core runs %v: only the item without data is left out", tags)
	}

	// The file comes back: asking for an update brings it, and the running
	// core is given the item's rules without a connection being dropped.
	h.rules.fail(aiPath, false)
	revision := snap.Applied.Revision
	if err := h.svc.UpdateRuleData(h.ctx(), false); err != nil {
		t.Fatal(err)
	}
	h.waitPlan("the item to have its data", func(p app.PlanInfo) bool { return p.Missing == 0 && p.Items[ai.ID].State == app.ItemReady })
	h.eventually("the core to run the item", func() bool {
		return h.svc.Snapshot().Applied.Revision > revision && slices.Contains(h.ruleSetTags(), "d-b-ai")
	})
	// Asking for an update fetches the file of an item that is off as well
	// (turning it on later then needs nothing), so it counts one more.
	if got := noticesOf(h.svc.Snapshot(), "rule_data_updated"); len(got) == 0 || got[len(got)-1].Args["updated"] != "2" {
		t.Errorf("the update is reported: %+v", got)
	}
	if got := h.svc.Snapshot().Phase; got != app.PhaseConnected {
		t.Errorf("phase %s", got)
	}
}

func TestUpdatingRuleDataSaysWhatChangedAndKeepsWhatItHas(t *testing.T) {
	h := newHarness(t, nil)
	h.addSubscription()
	h.waitPlan("the plan's files", func(p app.PlanInfo) bool { return len(p.Items) == 12 && p.Missing == 0 && !p.Updating })
	aiPath := pathOf(t, routing.GeoSite("category-ai-!cn"))
	last := func(code string) app.Notice {
		t.Helper()
		got := noticesOf(h.svc.Snapshot(), code)
		if len(got) == 0 {
			t.Fatalf("no %s notice among %+v", code, h.svc.Snapshot().Notices)
		}
		return got[len(got)-1]
	}

	// The first request brings the file of the one item that is off (the
	// plan fetched only what is on); after that nothing has changed and the
	// files are looked at and found current.
	if err := h.svc.UpdateRuleData(h.ctx(), false); err != nil {
		t.Fatal(err)
	}
	if n := last("rule_data_updated"); n.Args["updated"] != "1" {
		t.Errorf("the file of the item that is off: %+v", n)
	}
	if err := h.svc.UpdateRuleData(h.ctx(), false); err != nil {
		t.Fatal(err)
	}
	if n := last("rule_data_current"); n.Args["updated"] != "0" || n.Args["failed"] != "0" {
		t.Errorf("%+v", n)
	}

	// A file changed at its source: it is taken, and the plan follows.
	h.rules.set(aiPath, srs.DomainSuffixes(t, "ai-renamed.example"))
	if err := h.svc.UpdateRuleData(h.ctx(), false); err != nil {
		t.Fatal(err)
	}
	if n := last("rule_data_updated"); n.Args["updated"] != "1" {
		t.Errorf("%+v", n)
	}
	h.testURL("chat.ai-renamed.example", func(r *app.TestURLResult) bool { return r.Why.ID == "b-ai" })
	h.testURL("chat.category-ai-cn.example", func(r *app.TestURLResult) bool { return r.Why.Kind == app.WhyFallback || r.Why.ID != "b-ai" })

	// A forced update fetches every file again, changed or not.
	before := h.rules.hitCount(aiPath)
	if err := h.svc.UpdateRuleData(h.ctx(), true); err != nil {
		t.Fatal(err)
	}
	if got := h.rules.hitCount(aiPath); got != before+1 {
		t.Errorf("a forced update asked for the file %d more times, want 1", got-before)
	}

	// A source that stops answering costs nothing already downloaded.
	h.rules.fail(aiPath, true)
	err := h.svc.UpdateRuleData(h.ctx(), false)
	var status ruledata.StatusError
	if !errors.As(err, &status) || int(status) != 503 || app.Explain(err).Code != "rule_http_status" {
		t.Fatalf("UpdateRuleData = %v", err)
	}
	if n := last("rule_data_failed"); n.Args["failed"] != "1" {
		t.Errorf("%+v", n)
	}
	h.testURL("chat.ai-renamed.example", func(r *app.TestURLResult) bool { return r.Why.ID == "b-ai" })
	p := h.waitPlan("the failure to show", func(p app.PlanInfo) bool { return p.Items["b-ai"].Failing })
	if p.Items["b-ai"].State != app.ItemReady || p.Missing != 0 {
		t.Errorf("an item whose update failed still has its data: %+v, %d missing", p.Items["b-ai"], p.Missing)
	}
}

func TestRuleDataIsCheckedOnItsScheduleAndOnlyWhenDue(t *testing.T) {
	h := newHarness(t, func(c *app.Config) {
		c.ScheduleDelay = 10 * time.Millisecond
		c.ScheduleEvery = 25 * time.Millisecond
	})
	h.addSubscription()
	h.waitPlan("the plan's files", func(p app.PlanInfo) bool { return len(p.Items) == 12 && p.Missing == 0 && !p.Updating })
	aiPath := pathOf(t, routing.GeoSite("category-ai-!cn"))

	// Files looked at a moment ago are left alone, however often the scheduler wakes up.
	before := h.rules.hitCount(aiPath)
	time.Sleep(300 * time.Millisecond)
	if got := h.rules.hitCount(aiPath); got != before {
		t.Fatalf("a file checked a moment ago was asked for again (%d, was %d)", got, before)
	}

	// A day later the file is due, and what changed at its source is taken.
	h.rules.set(aiPath, srs.DomainSuffixes(t, "ai-next.example"))
	h.clock.Advance(25 * time.Hour)
	h.testURL("chat.ai-next.example", func(r *app.TestURLResult) bool { return r.Why.ID == "b-ai" })
	h.eventually("the scheduled check to say what it found", func() bool {
		got := noticesOf(h.svc.Snapshot(), "rule_data_updated")
		return len(got) > 0 && got[len(got)-1].Args["updated"] == "1"
	})
	if got := noticesOf(h.svc.Snapshot(), "rule_data_updated"); got[len(got)-1].Level != app.LevelInfo {
		t.Errorf("a check nobody asked for says what it found quietly: %+v", got[len(got)-1])
	}

	// With checking turned off nothing is asked, however long it has been.
	if err := h.lib.UpdateSettings(func(s *library.Settings) { s.RuleUpdateHours = 0 }); err != nil {
		t.Fatal(err)
	}
	h.rules.set(aiPath, srs.DomainSuffixes(t, "ai-never.example"))
	h.clock.Advance(100 * time.Hour)
	time.Sleep(300 * time.Millisecond)
	res, err := h.svc.TestURL(h.ctx(), "chat.ai-never.example")
	if err != nil || res.Why.ID == "b-ai" {
		t.Errorf("rule data was updated though checking is off: %+v, %v", res, err)
	}
}

// ---------------------------------------------------------- pasted rule lists

func TestARuleListIsPreviewedBeforeItIsKept(t *testing.T) {
	h := newHarness(t, nil)
	const listPath = "/o/r/main/lists/work.list"
	canonical := "https://raw.githubusercontent.com" + listPath
	h.rules.set(listPath, []byte("DOMAIN-SUFFIX,intranet.example\nDOMAIN-KEYWORD,wiki\nIP-CIDR,203.0.113.0/24,no-resolve\nPROCESS-NAME,Chat\nUSER-AGENT,thing\n"))

	// A pasted address that went through a mirror is cleaned up.
	prev, err := h.svc.PreviewRuleSet(h.ctx(), "https://gh-proxy.example/"+canonical+"?token=hunter2hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if prev.URL != canonical+"?token=hunter2hunter2" || !prev.Stripped || prev.Format != rulefmt.FormatClassical {
		t.Errorf("%+v", prev)
	}
	if strings.Contains(prev.Display, "hunter2") || !strings.Contains(prev.Display, "work.list") {
		t.Errorf("the address shown must not carry what follows a \"?\": %q", prev.Display)
	}
	if prev.Stats.Domain != 2 || prev.Stats.IP != 1 || prev.Stats.Other != 1 || prev.Stats.UnsupportedTotal() != 1 {
		t.Errorf("the file holds %+v", prev.Stats)
	}
	if prev.Name != "work" || app.RuleSetName(prev.URL) != "work" {
		t.Errorf("a name for it: %q / %q", prev.Name, app.RuleSetName(prev.URL))
	}
	if _, err := h.data.Parsed(prev.URL); err == nil {
		t.Fatal("a preview must not keep the file")
	}

	// Keeping it needs no second download.
	hits := h.rules.hitCount(listPath)
	if err := h.svc.KeepRuleSet(h.ctx(), prev.URL); err != nil {
		t.Fatal(err)
	}
	if got := h.rules.hitCount(listPath); got != hits {
		t.Errorf("keeping a previewed file downloaded it again (%d, was %d)", got, hits)
	}
	if p, err := h.data.Parsed(prev.URL); err != nil || p.Stats.Domain != 2 {
		t.Errorf("the kept file: %v, %v", p, err)
	}

	// A file that was not previewed is downloaded when it is kept.
	const otherPath = "/o/r/main/lists/other.list"
	h.rules.set(otherPath, []byte("DOMAIN-SUFFIX,other.example\n"))
	other := "https://raw.githubusercontent.com" + otherPath
	if err := h.svc.KeepRuleSet(h.ctx(), other); err != nil {
		t.Fatal(err)
	}
	if h.rules.hitCount(otherPath) != 1 {
		t.Errorf("the file was fetched %d times", h.rules.hitCount(otherPath))
	}
	if _, err := h.data.Parsed(other); err != nil {
		t.Error(err)
	}
	// ... and when it cannot be, keeping it says so.
	err = h.svc.KeepRuleSet(h.ctx(), "https://raw.githubusercontent.com/o/r/main/lists/gone.list")
	var status ruledata.StatusError
	if !errors.As(err, &status) || int(status) != 404 {
		t.Errorf("KeepRuleSet(missing) = %v", err)
	}
}

func TestWhatIsNotARuleListIsNamedForWhatItIs(t *testing.T) {
	h := newHarness(t, nil)
	raw := "https://raw.githubusercontent.com/o/r/main/lists/"
	h.rules.set("/o/r/main/lists/nodes.txt", []byte(links("HK 01", "JP 01")))
	h.rules.set("/o/r/main/lists/page.html", []byte("<!doctype html><html><body><h1>Sign in</h1></body></html>"))
	h.rules.set("/o/r/main/lists/comments.list", []byte("# nothing here\n"))
	h.rules.set("/o/r/main/lists/empty.json", []byte(`{"version":3,"rules":[]}`))
	var names []string
	for i := 0; i < 10; i++ {
		names = append(names, fmt.Sprintf("DOMAIN-SUFFIX,site%d.example", i))
	}
	h.rules.set("/o/r/main/lists/big.list", []byte(strings.Join(names, "\n")))

	cases := []struct {
		target string
		code   string
	}{
		{raw + "nodes.txt", "looks_like_subscription"},
		{raw + "page.html", "rule_unreadable"},
		{raw + "comments.list", "rule_unreadable"},
		{raw + "empty.json", "rule_empty"},
		{raw + "missing.list", "rule_http_status"},
		{"not an address", "rule_bad_address"},
		{"ftp://rules.example/x.list", "rule_bad_address"},
	}
	for _, c := range cases {
		_, err := h.svc.PreviewRuleSet(h.ctx(), c.target)
		if err == nil {
			t.Errorf("PreviewRuleSet(%q) succeeded", c.target)
			continue
		}
		ex := app.Explain(err)
		if ex.Code != c.code {
			t.Errorf("PreviewRuleSet(%q): %q (%v), want %q", c.target, ex.Code, err, c.code)
		}
		if strings.Contains(ex.Detail, "o/r/main") || strings.Contains(ex.Detail, "raw.githubusercontent") {
			t.Errorf("the explanation repeats the address: %q", ex.Detail)
		}
	}
	if _, err := h.svc.PreviewRuleSet(h.ctx(), raw+"nodes.txt"); !errors.Is(err, app.ErrLooksLikeSubscription) {
		t.Errorf("a list of nodes: %v", err)
	}
	if prev, err := h.svc.PreviewRuleSet(h.ctx(), raw+"big.list"); err != nil || prev.Stats.Domain != 10 {
		t.Errorf("a plain list: %+v, %v", prev, err)
	}
}

// ------------------------------------------------------------- what leaves

func TestDiagnosticsHoldOnlyCountsOfTheRouting(t *testing.T) {
	h := newHarness(t, nil)
	h.prov.body.Store(routedProfile("video", "domestic"))
	h.addSubscription()

	// What the person writes is as private as what the provider sends.
	const listPath = "/o/r/main/lists/mine.list"
	h.rules.set(listPath, []byte("DOMAIN-SUFFIX,private-list.example\n"))
	prev, err := h.svc.PreviewRuleSet(h.ctx(), "https://raw.githubusercontent.com"+listPath+"?token=hunter2hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.svc.KeepRuleSet(h.ctx(), prev.URL); err != nil {
		t.Fatal(err)
	}
	video := rowNamed(t, h.lib.Routing(), "Video Zone")
	h.edit(func(st *routing.State) error {
		if _, err := st.AddException(routing.Exception{Kind: routing.EntryDomainSuffix, Value: "secret-corp.example", Dest: routing.Direct(), Enabled: true}); err != nil {
			return err
		}
		if _, err := st.AddItem(routing.Item{
			Name: "Private Wiki Things", Enabled: true, Dest: routing.Direct(),
			Sources: []routing.Source{routing.InlineSource([]routing.Entry{{Kind: routing.EntryDomainSuffix, Value: "hidden-wiki.example"}})},
		}, ""); err != nil {
			return err
		}
		return st.AddSource(video.Item.ID, routing.URLSource(prev.URL, "mine", ""))
	})
	h.waitPlan("the plan with the person's own rules", func(p app.PlanInfo) bool { return len(p.Items) == 3 && p.Missing == 0 })
	h.connect()

	_, data, err := h.svc.ExportDiagnostics(h.ctx())
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, private := range []string{
		"Video Zone", "Home Zone", "Proxy Select", // the provider's groups
		"secret-corp.example", "Private Wiki Things", "hidden-wiki.example", "private-list.example", // the person's own
		"hunter2hunter2", // what a rule list's address carries
	} {
		if strings.Contains(text, private) {
			t.Errorf("the report contains %q", private)
		}
	}
	var doc struct {
		App struct {
			Plan struct {
				Kind       string         `json:"kind"`
				Items      int            `json:"items"`
				Enabled    int            `json:"enabled"`
				Exceptions int            `json:"exceptions"`
				States     map[string]int `json:"states"`
				Dests      map[string]int `json:"destinations"`
			} `json:"plan"`
			RuleData []struct {
				URL     string `json:"url"`
				Present bool   `json:"present"`
			} `json:"ruleData"`
		} `json:"app"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	p := doc.App.Plan
	if p.Kind != "subscription" || p.Items != 3 || p.Enabled != 3 || p.Exceptions != 1 || p.States["ready"] != 3 || p.Dests["direct"] != 2 || p.Dests["follow"] != 1 {
		t.Errorf("the plan in the report: %+v", p)
	}
	if len(doc.App.RuleData) != 4 {
		t.Errorf("%d rule files are listed, want 4: %+v", len(doc.App.RuleData), doc.App.RuleData)
	}
	for _, f := range doc.App.RuleData {
		if !f.Present || strings.Contains(f.URL, "?") {
			t.Errorf("rule file %+v", f)
		}
	}
}

// ------------------------------------------------------- nodes that left

func TestChosenNodesThatLeaveAreSaidSoOnceTheyAreMissed(t *testing.T) {
	h := newHarness(t, nil)
	h.addSubscription() // HK 01, HK 02, JP 01
	nodes, _ := h.lib.ActiveNodes()
	idOf := func(i int) routing.NodeID { return routing.NodeID{Sub: nodes[i].SubID, Name: nodes[i].Node.Name} }
	ai := rowByKey(t, h.lib.Routing(), "ai").Item
	h.edit(func(st *routing.State) error {
		if err := st.SetMain(routing.ToNode(idOf(0))); err != nil {
			return err
		}
		if err := st.SetFallback(routing.ToNode(idOf(1))); err != nil {
			return err
		}
		return st.SetItemDest(ai.ID, routing.ToNode(idOf(2)))
	})

	// The provider replaces every node.
	h.prov.body.Store(links("SG 01"))
	if _, err := h.svc.RefreshSubscription(h.ctx(), nodes[0].SubID); err != nil {
		t.Fatal(err)
	}
	p := h.waitPlan("the missing nodes to show", func(p app.PlanInfo) bool { return len(p.Gone) == 3 })
	owners := map[string]string{}
	for _, g := range p.Gone {
		owners[string(g.Owner)] = g.Node.Name
	}
	if owners["main"] != "HK 01" || owners["fallback"] != "HK 02" || owners["item"] != "JP 01" {
		t.Errorf("the nodes that are gone, by who chose them: %v", owners)
	}

	h.connect()
	snap := h.svc.Snapshot()
	main, fallback, item := noticesOf(snap, "pin_gone_main"), noticesOf(snap, "pin_gone_fallback"), noticesOf(snap, "pin_gone")
	if len(main) != 1 || main[0].Args["node"] != "HK 01" ||
		len(fallback) != 1 || fallback[0].Args["node"] != "HK 02" ||
		len(item) != 1 || item[0].Args["node"] != "JP 01" || item[0].Args["item"] != ai.ID || item[0].Args["for"] != ai.Name {
		t.Errorf("one notice for each, in words of their own: %+v / %+v / %+v", main, fallback, item)
	}
	for _, n := range append(append(main, fallback...), item...) {
		if n.Level != app.LevelWarning {
			t.Errorf("%+v", n)
		}
	}

	// Said once: connecting again does not repeat it.
	if err := h.svc.Disconnect(h.ctx()); err != nil {
		t.Fatal(err)
	}
	h.waitFor("disconnected", func(s *app.Snapshot) bool { return s.Phase == app.PhaseDisconnected })
	h.connect()
	if got := len(noticesOf(h.svc.Snapshot(), "pin_gone")) + len(noticesOf(h.svc.Snapshot(), "pin_gone_main")) + len(noticesOf(h.svc.Snapshot(), "pin_gone_fallback")); got != 3 {
		t.Errorf("%d notices after connecting twice, want 3", got)
	}
}
