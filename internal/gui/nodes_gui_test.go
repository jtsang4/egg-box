package gui

import (
	"testing"

	"eggbox/internal/library"
	"eggbox/internal/routing"
)

// nodesHarness is the Nodes page in a window tall enough for most of the cards.
func nodesHarness(t *testing.T, setup func(*fixtureBackend, *Options)) *harness {
	t.Helper()
	h := newHarness(t, harnessOpt{h: 1300, setup: setup})
	h.click("Nodes")
	h.settle()
	return h
}

// nodeID is the identity of a node of the fixture.
func nodeID(t *testing.T, be *fixtureBackend, name string) routing.NodeID {
	t.Helper()
	n, ok := be.node(name)
	if !ok {
		t.Fatalf("the fixture has no node %q", name)
	}
	return n.ID()
}

// withoutNode takes a node out of every subscription, as an update that
// dropped it does.
func withoutNode(be *fixtureBackend, name string) {
	var keep []library.ActiveNode
	for _, n := range be.nodes {
		if n.Node.Name != name {
			keep = append(keep, n)
		}
	}
	be.nodes = keep
	be.refresh()
}

// ------------------------------------------------------------- the main strip

func TestNodesStripShowsTheMainExit(t *testing.T) {
	h := nodesHarness(t, nil)
	// What fits of the regions is a card each; the rest are in a menu.
	h.mustHave("Automatic", "Now Tokyo 01 · 58 ms", "Fastest in Hong Kong", "Fastest in Japan", "More regions", "8 more regions")
	h.mustNotHave("Chosen by you")
}

func TestNodesStripChoosesARegionAndAutomatic(t *testing.T) {
	h := nodesHarness(t, nil)
	h.click("Fastest in Hong Kong")
	if !h.be.took("main region:HK") {
		t.Fatalf("the region was not chosen: %v", h.be.called())
	}
	h.settle()
	if m := h.state().Main; m.Kind != routing.DestRegion || m.Region != "HK" {
		t.Fatalf("main exit = %+v", m)
	}
	// What the region comes to now is told on its card, not on Automatic's.
	h.mustHave("Now Hong Kong 01")
	h.mustNotHave("Now Tokyo 01")

	// A region that does not fit is in the menu; once chosen it has a card.
	h.click("More regions")
	h.pick("Singapore")
	if m := h.state().Main; m.Kind != routing.DestRegion || m.Region != "SG" {
		t.Fatalf("main exit = %+v, want the region chosen from the menu", m)
	}
	h.mustHave("Fastest in Singapore", "Now Singapore 01")

	h.click("Automatic")
	if !h.be.took("main auto") || h.state().Main.Kind != routing.DestAuto {
		t.Fatalf("automatic was not chosen: %v", h.be.called())
	}
	h.settle()
	h.mustHave("Now Tokyo 01 · 58 ms")
}

func TestNodesCardChoosesANodeAsTheMainExit(t *testing.T) {
	h := nodesHarness(t, nil)
	h.click("Singapore 01")
	if !h.be.took("main node:Singapore 01") {
		t.Fatalf("the node was not chosen: %v", h.be.called())
	}
	h.settle()
	if m := h.state().Main; m.Kind != routing.DestNode || m.Node.Name != "Singapore 01" {
		t.Fatalf("main exit = %+v", m)
	}
	// The strip gains the card of the node chosen, which says it was chosen
	// (and not that another stands in: the node answers).
	h.mustHave("Chosen by you · 173 ms")
	h.mustNotHave("Now Singapore 01")
	h.click("Automatic")
	h.settle()
	h.mustNotHave("Chosen by you")
}

func TestNodesWithoutAConnectionDoNotClaimAnExit(t *testing.T) {
	h := nodesHarness(t, nil)
	h.disconnected()
	h.mustHave("Automatic", "Fastest node")
	h.mustNotHave("Now Tokyo 01")
}

// ----------------------------------------------------------- a node's menu

func TestNodeMenuUsesTheNodeAsTheMainExit(t *testing.T) {
	h := nodesHarness(t, nil)
	h.rightClick("Osaka 01")
	h.mustHave("Use as main exit", "Use for an item…", "Leave out of automatic choice", "Set region…", "Copy name", "Copy server address")
	h.pick("Use")
	if m := h.state().Main; m.Kind != routing.DestNode || m.Node.Name != "Osaka 01" {
		t.Fatalf("main exit = %+v", m)
	}
}

func TestNodeMenuUsesTheNodeForAnItem(t *testing.T) {
	h := nodesHarness(t, nil)
	h.rightClick("Osaka 01")
	h.pick("Use#1", "Telegram")
	d := h.row("b-telegram").Item.Dest
	if d.Kind != routing.DestNode || d.Node == nil || d.Node.Name != "Osaka 01" {
		t.Fatalf("Telegram goes %+v", d)
	}
	// The main exit was not touched, and the item shows where it goes.
	if h.state().Main.Kind != routing.DestAuto {
		t.Fatal("the main exit changed")
	}
	h.settle()
	h.mustHave("Used by items", "Telegram")
	h.mustHave("Osaka 01")
}

func TestNodeMenuLeavesTheNodeOutOfTheAutomaticChoice(t *testing.T) {
	h := nodesHarness(t, nil)
	id := nodeID(t, h.be, "Tokyo 01")
	h.rightClick("Tokyo 01")
	h.pick("Leave")
	if !h.state().NodePref(id).NoAuto {
		t.Fatal("the node is still part of the automatic choice")
	}
	// It was the fastest: another answers for Automatic now.
	h.settle()
	h.mustNotHave("Now Tokyo 01")
	// Putting it back is the same entry.
	h.rightClick("Tokyo 01")
	h.pick("Leave")
	if h.state().NodePref(id).NoAuto {
		t.Fatal("the node is still left out")
	}
}

func TestNodeMenuSetsTheRegionByHand(t *testing.T) {
	h := nodesHarness(t, nil)
	id := nodeID(t, h.be, "Seoul 01")
	h.rightClick("Seoul 01")
	h.pick("Set")
	h.mustHave("Set region", "Detect automatically")
	h.typeText("Japan")
	// "Japan" is on the page behind too (a filter), so the keyboard goes to the row.
	h.tabTo("Japan")
	h.enter()
	h.settle()
	h.mustNotHave("Detect automatically")
	if got := h.state().NodePref(id).Region; got != "JP" {
		t.Fatalf("the region of Seoul 01 is %q, want JP", got)
	}
	// And back to what the name says.
	h.rightClick("Seoul 01")
	h.pick("Set")
	h.tabTo("Detect automatically")
	h.enter()
	h.settle()
	if got := h.state().NodePref(id).Region; got != "" {
		t.Fatalf("the region of Seoul 01 is %q, want it read from the name again", got)
	}
}

func TestNodeMenuCopies(t *testing.T) {
	h := nodesHarness(t, nil)
	h.rightClick("Tokyo 01")
	h.pick("Copy")
	if got := h.ui.Clipboard(); got != "Tokyo 01" {
		t.Fatalf("copied %q", got)
	}
	h.rightClick("Tokyo 01")
	h.pick("Copy#1")
	if got := h.ui.Clipboard(); got != "node00.example.net:443" {
		t.Fatalf("copied %q", got)
	}
}

// ---------------------------------------------------------------- banners

// seoulIsChosen makes Seoul 01 the main exit and the exit of Telegram.
func seoulIsChosen(be *fixtureBackend, _ *Options) {
	n, _ := be.node("Seoul 01")
	be.arrange(func(st *routing.State) {
		if err := st.SetMain(routing.ToNode(n.ID())); err != nil {
			panic(err)
		}
		if err := st.SetItemDest("b-telegram", routing.ToNode(n.ID())); err != nil {
			panic(err)
		}
	})
}

func TestNodesBannerSaysWhenAChosenNodeIsGone(t *testing.T) {
	h := nodesHarness(t, func(be *fixtureBackend, o *Options) {
		seoulIsChosen(be, o)
		withoutNode(be, "Seoul 01")
	})
	h.mustHave("The node “Seoul 01” you chose for “Main exit” is no longer in any subscription.",
		"The node “Seoul 01” you chose for “Telegram” is no longer in any subscription.",
		"Choose again", "Got it", "No longer in any subscription")
}

func TestNodesBannerSaysWhenAChosenNodeDoesNotAnswer(t *testing.T) {
	h := nodesHarness(t, func(be *fixtureBackend, o *Options) {
		seoulIsChosen(be, o)
		be.down["Seoul 01"] = true
		be.refresh()
	})
	h.mustHave("The node “Seoul 01” you chose for “Main exit” is not answering.",
		"The node “Seoul 01” you chose for “Telegram” is not answering.", "is used meanwhile.")
	h.mustNotHave("is no longer in any subscription")
	// The main exit says so on its own card as well, in place of "Chosen by you".
	h.mustHave("Now Tokyo 01 · 58 ms")
	h.mustNotHave("Chosen by you")
	// The main exit's banner comes first, whatever the order of the plan.
	if a, b := h.middle("The node “Seoul 01” you chose for “Main exit” is not answering. “Tokyo 01” is used meanwhile."),
		h.middle("The node “Seoul 01” you chose for “Telegram” is not answering. “Tokyo 01” is used meanwhile."); a >= b {
		t.Fatalf("the banners are not in order: %v, %v", a, b)
	}
}

func TestNodesBannerIsDismissed(t *testing.T) {
	h := nodesHarness(t, func(be *fixtureBackend, o *Options) {
		seoulIsChosen(be, o)
		be.down["Seoul 01"] = true
		be.refresh()
	})
	h.mustHave("you chose for “Main exit”")
	h.click("Got it")
	h.mustNotHave("you chose for “Main exit”")
	h.mustHave("you chose for “Telegram”")
	h.click("Got it")
	h.mustNotHave("is not answering.")
	// It stays away while nothing changes.
	h.be.refresh()
	h.settle()
	h.mustNotHave("is not answering.")
}

func TestNodesBannerChoosesAgain(t *testing.T) {
	h := nodesHarness(t, func(be *fixtureBackend, o *Options) {
		seoulIsChosen(be, o)
		withoutNode(be, "Seoul 01")
	})
	h.click("Choose again")
	h.mustHave("Choose a node", "Main exit")
	// The main exit has its own logic: no stand-in to choose.
	h.mustNotHave("Use a stand-in")
	h.typeText("Osaka")
	h.tabTo("Osaka 01")
	h.enter()
	h.settle()
	if m := h.state().Main; m.Kind != routing.DestNode || m.Node.Name != "Osaka 01" {
		t.Fatalf("main exit = %+v", m)
	}
	h.mustNotHave("you chose for “Main exit”")
}

func TestNodesBannerIsGoneWhenTheNodeIsBack(t *testing.T) {
	h := nodesHarness(t, func(be *fixtureBackend, o *Options) {
		seoulIsChosen(be, o)
		be.down["Seoul 01"] = true
		be.refresh()
	})
	h.mustHave("is not answering.")
	h.be.down["Seoul 01"] = false
	h.be.refresh()
	h.settle()
	h.mustNotHave("is not answering.", "Got it")
}

// ------------------------------------------------------------ filters

func TestNodesAreFilteredByRegionAndSubscription(t *testing.T) {
	h := nodesHarness(t, nil)
	h.mustHave("All subscriptions", "Aurora Cloud", "Nimbus Net", "My nodes")
	h.click("Nimbus Net")
	h.mustHave("Frankfurt 01")
	h.mustNotHave("Osaka 01")
	h.click("Nimbus Net")
	h.mustHave("Osaka 01")
}
