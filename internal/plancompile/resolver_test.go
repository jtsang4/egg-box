package plancompile_test

import (
	"encoding/json"
	"testing"

	"eggbox/internal/plancompile"
	"eggbox/internal/routing"
)

// The way a person switches environments: a plan of their own with an item
// per environment, each with its destination and resolver, turned on and off.
// The resolvers reach the configuration, and it is one the agent and the core
// accept.
func TestEnvironmentItemsWithResolvers(t *testing.T) {
	data := newData()
	data.fill(t)
	nodes := fleet(t)
	st := routing.NewState()
	if _, err := st.SaveAsMine("Mine"); err != nil {
		t.Fatal(err)
	}
	direct, err := st.AddItem(routing.Item{Name: "Intranet, direct", Dest: routing.Direct(), Enabled: true, Sources: []routing.Source{
		routing.InlineSource([]routing.Entry{{Kind: routing.EntryDomainSuffix, Value: "corp.example"}, {Kind: routing.EntryDomainKeyword, Value: "intra"}}),
	}}, "")
	if err != nil {
		t.Fatal(err)
	}
	viaNode, err := st.AddItem(routing.Item{Name: "Intranet, through a node", Dest: routing.ToNode(routing.NodeID{Sub: "s1", Name: "HK 01"}), Enabled: false, Sources: []routing.Source{
		routing.InlineSource([]routing.Entry{{Kind: routing.EntryDomainSuffix, Value: "corp.example"}, {Kind: routing.EntryIPCIDR, Value: "10.0.0.0/8"}}),
	}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetItemResolver(direct.ID, " 10.0.0.53 "); err != nil {
		t.Fatal(err)
	}
	if err := st.SetItemResolver(viaNode.ID, "Exit"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetItemResolver(viaNode.ID, "gopher://x"); err == nil {
		t.Fatal("a resolver that is not a server must be refused")
	}
	st.Exceptions = []routing.Exception{
		{ID: "x1", Kind: routing.EntryDomainSuffix, Value: "always.example", Dest: routing.Direct(), Enabled: true, Resolver: "system"},
	}

	c := plancompile.New()
	res := compile(t, c, st, data, nodes)
	it, ok := itemByID(res, direct.ID)
	if !ok || it.Resolver != "10.0.0.53" {
		t.Fatalf("direct item resolver = %q (%v)", it.Resolver, ok)
	}
	if _, on := itemByID(res, viaNode.ID); on {
		t.Fatal("an item that is off is not compiled")
	}
	if len(res.Input.Exceptions) != 1 || res.Input.Exceptions[0].Resolver != "system" {
		t.Fatalf("exceptions = %+v", res.Input.Exceptions)
	}
	accept(t, res)

	// The other environment: the first item off, the second on.
	if err := st.SetItemEnabled(direct.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := st.SetItemEnabled(viaNode.ID, true); err != nil {
		t.Fatal(err)
	}
	res = compile(t, c, st, data, nodes)
	if it, ok := itemByID(res, viaNode.ID); !ok || it.Resolver != "exit" {
		t.Fatalf("node item resolver = %q (%v)", it.Resolver, ok)
	}
	out := accept(t, res)
	var cfg struct {
		DNS struct {
			Servers []map[string]any `json:"servers"`
		} `json:"dns"`
	}
	if err := json.Unmarshal(out.JSON, &cfg); err != nil {
		t.Fatal(err)
	}
	fake := false
	for _, s := range cfg.DNS.Servers {
		fake = fake || s["type"] == "fakeip"
	}
	if !fake {
		t.Fatal("the second item leaves its names to its exit: there must be a fake-ip server")
	}
}

// On a plan kept up to date elsewhere a resolver is laid over the plan's item,
// survives a switch away and back, goes with "save as mine" and is cleared by
// "restore default".
func TestResolverOnAFollowedPlan(t *testing.T) {
	st := routing.NewState()
	ai := routing.Builtin().Items[1] // "ai", on by default
	if err := st.SetItemResolver(ai.ID, "tcp://10.0.0.53"); err != nil {
		t.Fatal(err)
	}
	if !st.Customized() {
		t.Fatal("a resolver is a choice of the person's")
	}
	row, _, _ := st.View().Row(ai.ID)
	if row.Item.Resolver != "tcp://10.0.0.53" || !row.Overridden {
		t.Fatalf("row = %+v", row)
	}
	data := newData()
	data.fill(t)
	res := compile(t, plancompile.New(), st, data, fleet(t))
	if it, ok := itemByID(res, ai.ID); !ok || it.Resolver != "tcp://10.0.0.53" {
		t.Fatalf("compiled resolver = %q", it.Resolver)
	}
	accept(t, res)

	mine, err := st.SaveAsMine("Copy")
	if err != nil {
		t.Fatal(err)
	}
	if mine.Items[1].Resolver != "tcp://10.0.0.53" {
		t.Fatalf("save as mine dropped the resolver: %+v", mine.Items[1])
	}
	if err := st.Switch(routing.BuiltinID); err != nil {
		t.Fatal(err)
	}
	if row, _, _ := st.View().Row(ai.ID); row.Item.Resolver != "tcp://10.0.0.53" {
		t.Fatalf("switching back lost the resolver: %q", row.Item.Resolver)
	}
	if err := st.RestoreDefault(); err != nil {
		t.Fatal(err)
	}
	if row, _, _ := st.View().Row(ai.ID); row.Item.Resolver != "" {
		t.Fatalf("restore default kept the resolver: %q", row.Item.Resolver)
	}
	// Choosing the plan's own value again is no choice at all.
	if err := st.SetItemResolver(ai.ID, "10.0.0.53"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetItemResolver(ai.ID, ""); err != nil {
		t.Fatal(err)
	}
	if len(st.Overlays[routing.BuiltinID].Resolver) != 0 {
		t.Fatalf("overlay = %+v", st.Overlays[routing.BuiltinID].Resolver)
	}
}
