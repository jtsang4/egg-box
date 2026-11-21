package plancompile

import (
	"testing"

	"eggbox/internal/configgen"
	"eggbox/internal/routing"
	"eggbox/internal/subscription"
)

func makeNode(name, host string) subscription.Node {
	return subscription.Node{
		Name: name, Type: "socks", Server: host, Port: 1080,
		Config: map[string]any{"type": "socks", "server": host, "server_port": 1080, "version": "5"},
	}
}

func TestCompileRelays(t *testing.T) {
	hk1 := makeNode("Hong Kong 01", "hk1.example.com")
	hk2 := makeNode("Hong Kong 02", "hk2.example.com")
	us1 := makeNode("US Home 01", "us1.example.com")
	us2 := makeNode("US Home 02", "us2.example.com")
	us3 := makeNode("US Home 03", "us3.example.com")
	usProv := makeNode("US Provider", "usp.example.com")
	usProv.Relay = &subscription.RelaySpec{Kind: subscription.RelayNode, Node: "Hong Kong 01"}

	st := routing.NewState()
	// us1 pinned to gone HK node (with fallback -> stands in with HK plain region)
	goneHK := routing.NodeID{Sub: "s1", Name: "Hong Kong 99", Hash: "dead", Region: "HK"}
	rGone := routing.RelayNodeVal(goneHK)
	_ = st.SetNodeRelay(routing.NodeID{Sub: "s2", Name: us1.Name}, &rGone)

	// us2 pinned to gone SG node (no SG plain node -> blocked!)
	goneSG := routing.NodeID{Sub: "s1", Name: "Singapore 99", Hash: "dead2", Region: "SG"}
	rGoneSG := routing.RelayNodeVal(goneSG)
	_ = st.SetNodeRelay(routing.NodeID{Sub: "s2", Name: us2.Name}, &rGoneSG)

	// us3 inherits subscription default relay (HK region)
	subRelay := routing.RelayRegionVal("HK")

	c := New()
	res, err := c.Compile(Options{
		State: st,
		Prefs: configgen.DefaultPrefs(),
		Nodes: []Node{
			{Sub: "s1", Node: hk1, Auto: true},
			{Sub: "s1", Node: hk2, Auto: true},
			{Sub: "s1", Node: usProv, Auto: false},
			{Sub: "s2", Node: us1, Auto: false},
			{Sub: "s2", Node: us2, Auto: false},
			{Sub: "s3", Node: us3, Auto: true, SubRelay: &subRelay},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	byName := map[string]configgen.Node{}
	for _, n := range res.Input.Nodes {
		byName[n.Name] = n
	}
	hk1Key := (configgen.Node{Node: hk1, Sub: "s1"}).Key()
	if byName["US Provider"].Relay.Kind != configgen.RelayNode || byName["US Provider"].Relay.Node != hk1Key {
		t.Errorf("US Provider relay = %+v, want node %q", byName["US Provider"].Relay, hk1Key)
	}
	if byName["US Home 01"].Relay.Kind != configgen.RelayRegion || byName["US Home 01"].Relay.Region != "HK" {
		t.Errorf("US Home 01 relay = %+v, want region HK stand-in", byName["US Home 01"].Relay)
	}
	if byName["US Home 02"].Relay.Kind != configgen.RelayBlocked {
		t.Errorf("US Home 02 relay = %+v, want blocked", byName["US Home 02"].Relay)
	}
	if byName["US Home 03"].Relay.Kind != configgen.RelayRegion || byName["US Home 03"].Relay.Region != "HK" {
		t.Errorf("US Home 03 relay = %+v, want region HK", byName["US Home 03"].Relay)
	}

	// Verify notices: one NoticeRelayGone (us1) and one NoticeRelayUnavailable (us2).
	var gotGone, gotUnavail bool
	for _, n := range res.Notices {
		switch n.Code {
		case NoticeRelayGone:
			if n.Target.Name == "US Home 01" && !n.Blocked && n.Region == "HK" {
				gotGone = true
			}
		case NoticeRelayUnavailable:
			if n.Target.Name == "US Home 02" && n.Blocked {
				gotUnavail = true
			}
		}
	}
	if !gotGone || !gotUnavail {
		t.Fatalf("notices = %+v, want gone(US Home 01) + unavail(US Home 02)", res.Notices)
	}
}

func TestCompileIdenticalServersInTwoSubscriptionsStayDistinct(t *testing.T) {
	shared := makeNode("US Reality 01", "us.example.com")
	relay := makeNode("Tokyo 01", "jp.example.com")
	subRelay := routing.RelayRegionVal("JP")

	st := routing.NewState()
	// Pin main exit specifically to sub-b's copy of the shared server.
	_ = st.SetMain(routing.ToNode(routing.NodeID{Sub: "sub-b", Name: shared.Name, Hash: shared.Key(), Region: "US"}))

	res, err := New().Compile(Options{
		State: st,
		Prefs: configgen.DefaultPrefs(),
		Nodes: []Node{
			{Sub: "sub-a", Node: relay, Auto: true},
			{Sub: "sub-a", Node: shared, Auto: true},
			{Sub: "sub-b", Node: shared, Auto: false, SubRelay: &subRelay},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Input.Nodes) != 3 {
		t.Fatalf("compiled %d nodes, want 3 (both subscriptions' copies kept)", len(res.Input.Nodes))
	}
	bySub := map[string]configgen.Node{}
	for _, n := range res.Input.Nodes {
		if n.Name == shared.Name {
			bySub[n.Sub] = n
		}
	}
	if len(bySub) != 2 {
		t.Fatalf("expected shared node in both sub-a and sub-b, got %+v", bySub)
	}
	if bySub["sub-a"].Key() == bySub["sub-b"].Key() {
		t.Fatalf("shared server across two subscriptions got identical keys: %q", bySub["sub-a"].Key())
	}
	if bySub["sub-a"].Relay.Kind != configgen.RelayNone {
		t.Errorf("sub-a copy relay = %+v, want direct", bySub["sub-a"].Relay)
	}
	if bySub["sub-b"].Relay.Kind != configgen.RelayRegion || bySub["sub-b"].Relay.Region != "JP" {
		t.Errorf("sub-b copy relay = %+v, want region JP", bySub["sub-b"].Relay)
	}
	if res.Input.Main.Kind != configgen.ExitNode || res.Input.Main.Node != bySub["sub-b"].Key() {
		t.Errorf("main exit = %+v, want sub-b copy (%q)", res.Input.Main, bySub["sub-b"].Key())
	}
}
