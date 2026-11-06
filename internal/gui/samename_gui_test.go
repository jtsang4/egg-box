package gui

import (
	"testing"

	"eggbox/internal/library"
	"eggbox/internal/routing"
)

// Two subscriptions with a node called "Tokyo 01": wherever the name is all
// there is to go by (the tray, the routing page, the exit card), it says which
// subscription the node is of; names that are only in one place stay as they
// are.

// twinOfTokyo gives Nimbus Net a "Tokyo 01" of its own (another server than
// Aurora Cloud's), and that subscription a short name when alias is not empty.
func twinOfTokyo(alias string) func(*fixtureBackend, *Options) {
	return func(be *fixtureBackend, _ *Options) {
		tokyo, ok := be.node("Tokyo 01")
		if !ok {
			panic("no Tokyo 01")
		}
		twin := tokyo
		twin.SubID, twin.SubName = "sub-nimbus", "Nimbus Net"
		twin.Node = fixtureNode(len(be.nodes), "Tokyo 01")
		be.nodes = append(be.nodes, twin)
		for i := range be.nodes {
			if be.nodes[i].SubID == "sub-nimbus" {
				be.nodes[i].SubAlias = alias
			}
		}
		be.refresh()
	}
}

// theTwin is the Tokyo 01 of Nimbus Net.
func theTwin(t *testing.T, be *fixtureBackend) library.ActiveNode {
	t.Helper()
	for _, n := range be.nodes {
		if n.Node.Name == "Tokyo 01" && n.SubID == "sub-nimbus" {
			return n
		}
	}
	t.Fatal("the fixture has no twin of Tokyo 01")
	return library.ActiveNode{}
}

func TestTheTrayNamesTheSubscriptionOfANodeWhoseNameIsNotUnique(t *testing.T) {
	h := panelHarness(t, true, func(be *fixtureBackend, o *Options) {
		twinOfTokyo("Nim")(be, o)
		be.arrange(func(st *routing.State) {
			seoul, _ := be.node("Seoul 01")
			for _, n := range []library.ActiveNode{seoul, theTwin(t, be)} {
				if err := st.SetMain(routing.ToNode(n.ID())); err != nil {
					panic(err)
				}
			}
			if err := st.SetMain(routing.Auto()); err != nil {
				panic(err)
			}
		})
	})
	// The node used last is the one of Nimbus Net, by its short name; the
	// node nobody else has the name of is the name alone.
	h.mustHave("MAIN EXIT", "Tokyo 01 · Nim", "Seoul 01")
	h.mustNotHave("Tokyo 01 · Nimbus Net")
	h.click("Tokyo 01 · Nim")
	if got := h.be.Routing().Main; got.Kind != routing.DestNode || got.Node.Sub != "sub-nimbus" || got.Node.Name != "Tokyo 01" {
		t.Fatalf("the click chose %+v, not the node of Nimbus Net", got)
	}
}

func TestTheRoutingPageNamesTheSubscriptionOfANodeWhoseNameIsNotUnique(t *testing.T) {
	h := newHarness(t, harnessOpt{h: 1300, setup: func(be *fixtureBackend, o *Options) {
		twinOfTokyo("")(be, o)
		be.arrange(func(st *routing.State) {
			if err := st.SetItemDest("b-telegram", routing.ToNode(theTwin(t, be).ID())); err != nil {
				panic(err)
			}
		})
	}})
	h.click("Routing")
	h.settle()
	// Without a short name it is the name of the subscription.
	h.mustHave("Tokyo 01 · Nimbus Net")

	// The dashboard says it for the node the automatic choice ends on, which
	// is the one of Aurora Cloud.
	h.click("Dashboard")
	h.settle()
	h.mustHave("Tokyo 01 · Aurora Cloud")
}

func TestTheNodesPageTellsTwoNodesOfTheSameNameApart(t *testing.T) {
	h := nodesHarness(t, twinOfTokyo(""))
	// Each card says which subscription it is of.
	h.mustHave("Aurora Cloud", "Nimbus Net")
	// Narrowed to one subscription, its node is chosen as the main exit, and
	// the strip names it with the subscription.
	h.click("Nimbus Net")
	h.click("Tokyo 01")
	if got := h.state().Main; got.Kind != routing.DestNode || got.Node.Sub != "sub-nimbus" || got.Node.Name != "Tokyo 01" {
		t.Fatalf("the card chose %+v", got)
	}
	h.settle()
	h.mustHave("Tokyo 01 · Nimbus Net", "Chosen by you · 58 ms")
	h.mustNotHave("Tokyo 01 · Aurora Cloud")
}
