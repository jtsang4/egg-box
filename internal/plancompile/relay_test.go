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
	if byName["US Provider"].Relay.Kind != configgen.RelayNode || byName["US Provider"].Relay.Node != hk1.Key() {
		t.Errorf("US Provider relay = %+v", byName["US Provider"].Relay)
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
