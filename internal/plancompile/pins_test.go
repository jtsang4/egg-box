package plancompile_test

import (
	"slices"
	"testing"

	"eggbox/internal/configgen"
	"eggbox/internal/plancompile"
	"eggbox/internal/routing"
	"eggbox/internal/subscription"
)

// planWith is a plan of the user's own with the given items and exceptions.
func planWith(items []routing.Item, fallback routing.Dest, ex ...routing.Exception) routing.State {
	st := routing.State{}
	st.Plans = []*routing.Plan{{ID: "p1", Kind: routing.PlanMine, Name: "mine", Fallback: fallback, Items: items}}
	st.Active = "p1"
	st.Exceptions = ex
	return st
}

func inline(id, name string, dest routing.Dest, entries ...routing.Entry) routing.Item {
	return routing.Item{ID: id, Name: name, Enabled: true, Dest: dest, Sources: []routing.Source{routing.InlineSource(entries)}}
}

func suffix(v string) routing.Entry { return routing.Entry{Kind: routing.EntryDomainSuffix, Value: v} }

func nodeKey(t *testing.T, ns []plancompile.Node, name string) string {
	t.Helper()
	for _, n := range ns {
		if n.Node.Name == name {
			return (configgen.Node{Node: n.Node, Sub: n.Sub}).Key()
		}
	}
	t.Fatalf("no node %q", name)
	return ""
}

func ident(ns []plancompile.Node, name string) routing.NodeID {
	for _, n := range ns {
		if n.Node.Name == name {
			return routing.NodeID{Sub: n.Sub, Name: name, Hash: n.Node.Key(), Region: subscription.Region(name)}
		}
	}
	return routing.NodeID{}
}

func TestExceptionsComeFirstAndKeepTheirOwnExit(t *testing.T) {
	ns := fleet(t)
	pinned := ident(ns, "JP 01")
	st := planWith(nil, routing.Follow(),
		routing.Exception{ID: "x1", Kind: routing.EntryDomainSuffix, Value: "intranet.example", Dest: routing.Direct(), Enabled: true},
		routing.Exception{ID: "x2", Kind: routing.EntryDomain, Value: "off.example", Dest: routing.Direct(), Enabled: false},
		routing.Exception{ID: "x3", Kind: routing.EntryPort, Value: "22", Dest: routing.ToNode(pinned), Enabled: true},
		routing.Exception{ID: "x4", Kind: routing.EntryDomainKeyword, Value: "tracker", Dest: routing.Reject(), Enabled: true},
		routing.Exception{ID: "x5", Kind: routing.EntryIPCIDR, Value: "203.0.113.0/24", Dest: routing.ToRegion("US"), Enabled: true},
		routing.Exception{ID: "x6", Kind: routing.EntryDomainSuffix, Value: "docs.example", Dest: routing.Follow(), Enabled: true},
	)
	res := compile(t, plancompile.New(), st, newData(), ns)
	var ids []string
	exits := map[string]configgen.Exit{}
	for _, x := range res.Input.Exceptions {
		ids = append(ids, x.ID)
		exits[x.ID] = x.Exit
	}
	if want := []string{"x1", "x3", "x4", "x5", "x6"}; !slices.Equal(ids, want) {
		t.Fatalf("exceptions %v, want %v (the disabled one is left out, order kept)", ids, want)
	}
	if exits["x1"].Kind != configgen.ExitDirect || exits["x4"].Kind != configgen.ExitReject || exits["x6"].Kind != configgen.ExitMain {
		t.Errorf("exits %+v", exits)
	}
	if e := exits["x3"]; e.Kind != configgen.ExitNode || e.Node != nodeKey(t, ns, "JP 01") {
		t.Errorf("pinned exit %+v", e)
	}
	if e := exits["x5"]; e.Kind != configgen.ExitRegion || e.Region != "US" {
		t.Errorf("region exit %+v", e)
	}
	if len(res.Notices) != 0 {
		t.Errorf("notices %+v", res.Notices)
	}
	accept(t, res)
}

func TestPinnedNodes(t *testing.T) {
	ns := fleet(t)
	jp := ident(ns, "JP 01")

	t.Run("found by name", func(t *testing.T) {
		st := planWith([]routing.Item{inline("i1", "Stream", routing.ToNode(jp), suffix("stream.example"))}, routing.Follow())
		res := compile(t, plancompile.New(), st, newData(), ns)
		it, ok := itemByID(res, "i1")
		if !ok || it.Exit.Kind != configgen.ExitNode || it.Exit.Node != nodeKey(t, ns, "JP 01") || it.Exit.NoStandIn {
			t.Fatalf("%+v ok=%v", it.Exit, ok)
		}
		if len(res.Notices) != 0 {
			t.Fatalf("notices %+v", res.Notices)
		}
		accept(t, res)
	})

	t.Run("without a stand-in", func(t *testing.T) {
		d := routing.ToNode(jp)
		d.NoFallback = true
		st := planWith([]routing.Item{inline("i1", "Stream", d, suffix("stream.example"))}, routing.Follow())
		res := compile(t, plancompile.New(), st, newData(), ns)
		it, _ := itemByID(res, "i1")
		if !it.Exit.NoStandIn {
			t.Fatalf("%+v", it.Exit)
		}
	})

	t.Run("renamed by the provider", func(t *testing.T) {
		renamed := slices.Clone(ns)
		for i := range renamed {
			if renamed[i].Node.Name == "JP 01" {
				renamed[i].Node.Name = "JP Tokyo"
			}
		}
		st := planWith([]routing.Item{inline("i1", "Stream", routing.ToNode(jp), suffix("stream.example"))}, routing.Follow())
		res := compile(t, plancompile.New(), st, newData(), renamed)
		it, _ := itemByID(res, "i1")
		if it.Exit.Kind != configgen.ExitNode || it.Exit.Node != nodeKey(t, renamed, "JP Tokyo") {
			t.Fatalf("a renamed node is found again by its settings: %+v", it.Exit)
		}
		if len(res.Notices) != 0 {
			t.Fatalf("notices %+v", res.Notices)
		}
	})

	t.Run("moved to another subscription", func(t *testing.T) {
		moved := slices.Clone(ns)
		for i := range moved {
			moved[i].Sub = "s2"
		}
		st := planWith([]routing.Item{inline("i1", "Stream", routing.ToNode(jp), suffix("stream.example"))}, routing.Follow())
		res := compile(t, plancompile.New(), st, newData(), moved)
		it, _ := itemByID(res, "i1")
		if it.Exit.Kind != configgen.ExitNode || it.Exit.Node != nodeKey(t, moved, "JP 01") {
			t.Fatalf("%+v", it.Exit)
		}
	})

	t.Run("gone, its region still has nodes", func(t *testing.T) {
		var rest []plancompile.Node
		for _, n := range ns {
			if n.Node.Name != "HK 01" {
				rest = append(rest, n)
			}
		}
		hk := ident(ns, "HK 01")
		st := planWith([]routing.Item{inline("i1", "Stream", routing.ToNode(hk), suffix("stream.example"))}, routing.Follow())
		res := compile(t, plancompile.New(), st, newData(), rest)
		it, _ := itemByID(res, "i1")
		if it.Exit.Kind != configgen.ExitRegion || it.Exit.Region != "HK" {
			t.Fatalf("the fastest node of its region stands in: %+v", it.Exit)
		}
		want := plancompile.Notice{Code: plancompile.NoticePinGone, Owner: plancompile.OwnerItem, ID: "i1", Node: hk}
		if len(res.Notices) != 1 || res.Notices[0] != want {
			t.Fatalf("notices %+v, want %+v", res.Notices, want)
		}
		accept(t, res)
	})

	t.Run("gone, its region too", func(t *testing.T) {
		var rest []plancompile.Node
		for _, n := range ns {
			if n.Node.Name != "JP 01" {
				rest = append(rest, n)
			}
		}
		st := planWith([]routing.Item{inline("i1", "Stream", routing.ToNode(jp), suffix("stream.example"))}, routing.Follow())
		res := compile(t, plancompile.New(), st, newData(), rest)
		it, _ := itemByID(res, "i1")
		if it.Exit.Kind != configgen.ExitMain {
			t.Fatalf("with no node in the region the item follows the main exit: %+v", it.Exit)
		}
		if len(res.Notices) != 1 || res.Notices[0].Code != plancompile.NoticePinGone {
			t.Fatalf("notices %+v", res.Notices)
		}
		accept(t, res)
	})

	t.Run("main exit pinned and gone", func(t *testing.T) {
		var rest []plancompile.Node
		for _, n := range ns {
			if n.Node.Name != "JP 01" {
				rest = append(rest, n)
			}
		}
		st := planWith(nil, routing.Follow())
		st.Main = routing.ToNode(jp)
		res := compile(t, plancompile.New(), st, newData(), rest)
		if res.Input.Main.Kind != configgen.ExitRegion && res.Input.Main.Kind != configgen.ExitAuto {
			t.Fatalf("main %+v", res.Input.Main)
		}
		if len(res.Notices) != 1 || res.Notices[0].Owner != plancompile.OwnerMain {
			t.Fatalf("notices %+v", res.Notices)
		}
		accept(t, res)
	})

	t.Run("fallback pinned", func(t *testing.T) {
		st := planWith(nil, routing.ToNode(ident(ns, "US 01")))
		res := compile(t, plancompile.New(), st, newData(), ns)
		if res.Input.Fallback.Kind != configgen.ExitNode || res.Input.Fallback.Node != nodeKey(t, ns, "US 01") {
			t.Fatalf("fallback %+v", res.Input.Fallback)
		}
		accept(t, res)
	})
}

func TestMainExitDefaults(t *testing.T) {
	ns := fleet(t)
	res := compile(t, plancompile.New(), planWith(nil, routing.Follow()), newData(), ns)
	if res.Input.Main.Kind != configgen.ExitAuto {
		t.Fatalf("a main exit that was never chosen is the fastest node: %+v", res.Input.Main)
	}
	st := planWith(nil, routing.Follow())
	st.Main = routing.ToRegion("jp")
	res = compile(t, plancompile.New(), st, newData(), ns)
	if m := res.Input.Main; m.Kind != configgen.ExitRegion || m.Region != "JP" {
		t.Fatalf("main %+v", m)
	}
	// A main exit that cannot be one (it cannot follow itself or go direct) is the fastest node.
	for _, bad := range []routing.Dest{routing.Direct(), routing.Reject(), routing.Follow()} {
		st.Main = bad
		res = compile(t, plancompile.New(), st, newData(), ns)
		if res.Input.Main.Kind != configgen.ExitAuto {
			t.Errorf("main %v became %+v", bad, res.Input.Main)
		}
	}
}

func TestNodeChoices(t *testing.T) {
	ns := fleet(t)
	st := planWith(nil, routing.Follow())
	st.Nodes = map[string]routing.NodePref{
		routing.NodeID{Sub: "s1", Name: "Backup"}.Key(): {Region: "us"},
		routing.NodeID{Sub: "s1", Name: "HK 02"}.Key():  {NoAuto: true},
	}
	res := compile(t, plancompile.New(), st, newData(), ns)
	got := map[string]configgen.Node{}
	for _, n := range res.Input.Nodes {
		got[n.Name] = n
	}
	if got["Backup"].Region != "US" {
		t.Errorf("the region the person gave: %+v", got["Backup"])
	}
	if got["HK 01"].Region != "HK" || got["JP 01"].Region != "JP" {
		t.Errorf("recognised regions: %q %q", got["HK 01"].Region, got["JP 01"].Region)
	}
	if !got["HK 02"].NoAuto || got["HK 01"].NoAuto {
		t.Errorf("kept out of the automatic choice: %+v %+v", got["HK 02"], got["HK 01"])
	}

	// A subscription kept out of the automatic choice takes all its nodes with it.
	out := slices.Clone(ns)
	for i := range out {
		out[i].Auto = false
	}
	res = compile(t, plancompile.New(), planWith(nil, routing.Follow()), newData(), out)
	for _, n := range res.Input.Nodes {
		if !n.NoAuto {
			t.Errorf("%s should be kept out", n.Name)
		}
	}
}

func TestSameServerDeduplicatedPerSubscriptionOnly(t *testing.T) {
	ns := fleet(t)
	// Same server twice inside the same subscription collapses to one entry.
	sameSub := append(slices.Clone(ns), plancompile.Node{Sub: ns[0].Sub, Node: ns[0].Node, Auto: true})
	res := compile(t, plancompile.New(), planWith(nil, routing.Follow()), newData(), sameSub)
	if len(res.Input.Nodes) != len(ns) {
		t.Fatalf("same subscription: %d nodes, want %d", len(res.Input.Nodes), len(ns))
	}
	accept(t, res)

	// Same server in a second subscription stays as its own entry with a distinct key.
	otherSub := append(slices.Clone(ns), plancompile.Node{Sub: "s2", Node: ns[0].Node, Auto: true})
	res = compile(t, plancompile.New(), planWith(nil, routing.Follow()), newData(), otherSub)
	if len(res.Input.Nodes) != len(ns)+1 {
		t.Fatalf("other subscription: %d nodes, want %d", len(res.Input.Nodes), len(ns)+1)
	}
	accept(t, res)
}

func TestHandWrittenItems(t *testing.T) {
	ns := fleet(t)
	items := []routing.Item{
		inline("i1", "Mine", routing.Follow(), suffix("Corp.Example"),
			routing.Entry{Kind: routing.EntryDomain, Value: "login.example.org"},
			routing.Entry{Kind: routing.EntryIPCIDR, Value: "203.0.113.0/24"}),
		inline("i2", "Ports", routing.Direct(),
			routing.Entry{Kind: routing.EntryPort, Value: "22"},
			routing.Entry{Kind: routing.EntryProcessName, Value: "curl"}),
		inline("i3", "Names and a process", routing.Direct(), suffix("local.example"),
			routing.Entry{Kind: routing.EntryProcessName, Value: "ssh"}),
		inline("i4", "Empty", routing.Follow()),
	}
	res := compile(t, plancompile.New(), planWith(items, routing.Follow()), newData(), ns)
	if res.Items["i4"].State != plancompile.ItemUnavailable {
		t.Errorf("an item without entries has nothing to match: %+v", res.Items["i4"])
	}
	if _, ok := itemByID(res, "i4"); ok {
		t.Error("an empty item must not reach the configuration")
	}

	i1, ok := itemByID(res, "i1")
	if !ok || res.Items["i1"].State != plancompile.ItemReady {
		t.Fatalf("i1: %+v ok=%v", res.Items["i1"], ok)
	}
	dom, suf, _, _ := decode(t, i1.Domain)
	_, _, cidrs, _ := decode(t, i1.IP)
	if !slices.Equal(suf, []string{"corp.example"}) || !slices.Equal(dom, []string{"login.example.org"}) || !slices.Equal(cidrs, []string{"203.0.113.0/24"}) {
		t.Errorf("i1: suffixes %v domains %v cidrs %v", suf, dom, cidrs)
	}
	if string(i1.DNS) != string(i1.Domain) || i1.Process {
		t.Errorf("a pure host-name item steers DNS with the same rule set and needs no process lookup: dns %d domain %d process %v", len(i1.DNS), len(i1.Domain), i1.Process)
	}

	i2, _ := itemByID(res, "i2")
	if !i2.Process || len(i2.DNS) != 0 {
		t.Errorf("a port and process item has nothing for DNS: dns=%d process=%v", len(i2.DNS), i2.Process)
	}

	i3, _ := itemByID(res, "i3")
	if len(i3.DNS) == 0 || string(i3.DNS) == string(i3.Domain) {
		t.Errorf("only the host-name part of a mixed item steers DNS: dns=%d domain=%d", len(i3.DNS), len(i3.Domain))
	}
	_, suf, _, _ = decode(t, i3.DNS)
	if !slices.Equal(suf, []string{"local.example"}) {
		t.Errorf("dns part %v", suf)
	}
	accept(t, res)
}

func TestRegionGroupExistsOnlyWithAutoNodes(t *testing.T) {
	ns := fleet(t)
	for i := range ns {
		if subscription.Region(ns[i].Node.Name) == "JP" {
			ns[i].Auto = false
		}
	}
	// The only JP node is kept out of the automatic choice: pinning it still works,
	// and once it is gone there is no region group to fall back on.
	jp := ident(ns, "JP 01")
	st := planWith([]routing.Item{inline("i1", "Stream", routing.ToNode(jp), suffix("stream.example"))}, routing.Follow())
	res := compile(t, plancompile.New(), st, newData(), ns)
	it, _ := itemByID(res, "i1")
	if it.Exit.Kind != configgen.ExitNode {
		t.Fatalf("%+v", it.Exit)
	}
	var rest []plancompile.Node
	for _, n := range ns {
		if n.Node.Name != "JP 01" {
			rest = append(rest, n)
		}
	}
	res = compile(t, plancompile.New(), st, newData(), rest)
	it, _ = itemByID(res, "i1")
	if it.Exit.Kind != configgen.ExitMain {
		t.Fatalf("%+v", it.Exit)
	}
	accept(t, res)
}

func TestCompileIsRepeatableAndRemembersRuleSets(t *testing.T) {
	data := newData()
	data.fill(t)
	ns := fleet(t)
	c := plancompile.New()
	a := compile(t, c, routing.State{}, data, ns)
	b := compile(t, c, routing.State{}, data, ns)
	oa, err := configgen.Generate(a.Input)
	if err != nil {
		t.Fatal(err)
	}
	ob, err := configgen.Generate(b.Input)
	if err != nil {
		t.Fatal(err)
	}
	if oa.Digest != ob.Digest || string(oa.JSON) != string(ob.JSON) {
		t.Fatal("compiling the same inputs twice must give the same configuration")
	}
	// A fresh compiler (nothing remembered) agrees.
	cc := compile(t, plancompile.New(), routing.State{}, data, ns)
	oc, _ := configgen.Generate(cc.Input)
	if oc.Digest != oa.Digest {
		t.Fatal("remembered and fresh compilations differ")
	}
	// A file that changed is compiled again.
	u := routing.GeoSite("cn")
	data.put(t, u, "DOMAIN-SUFFIX,changed.example\n")
	d := compile(t, c, routing.State{}, data, ns)
	od, _ := configgen.Generate(d.Input)
	if od.Digest == oa.Digest {
		t.Fatal("a changed file must change the configuration")
	}
	cn, _ := itemByID(d, "b-cn")
	_, suf, _, _ := decode(t, cn.Domain)
	if !slices.Equal(suf, []string{"changed.example"}) {
		t.Fatalf("suffixes %v", suf)
	}
}

func TestItemOffAndFallbackExits(t *testing.T) {
	ns := fleet(t)
	off := inline("i2", "Off", routing.Direct(), suffix("off.example"))
	off.Enabled = false
	st := planWith([]routing.Item{inline("i1", "On", routing.Reject(), suffix("on.example")), off}, routing.Direct())
	res := compile(t, plancompile.New(), st, newData(), ns)
	if res.Items["i2"].State != plancompile.ItemOff {
		t.Fatalf("%+v", res.Items["i2"])
	}
	if _, ok := itemByID(res, "i2"); ok {
		t.Fatal("an item that is off must not reach the configuration")
	}
	if it, _ := itemByID(res, "i1"); it.Exit.Kind != configgen.ExitReject {
		t.Fatalf("%+v", it.Exit)
	}
	if res.Input.Fallback.Kind != configgen.ExitDirect {
		t.Fatalf("fallback %+v", res.Input.Fallback)
	}
	accept(t, res)
}
