package gui

import (
	"context"
	"testing"

	"eggbox/internal/app"
	"eggbox/internal/library"
	"eggbox/internal/routing"
)

func panelHarness(t *testing.T, dark bool, setup func(*fixtureBackend, *Options)) *harness {
	t.Helper()
	return newHarness(t, harnessOpt{dark: dark, panel: true, setup: setup})
}

func offline(be *fixtureBackend, _ *Options) { be.snap = be.disconnectedSnapshot() }

func noService(be *fixtureBackend, o *Options) {
	be.snap = be.disconnectedSnapshot()
	be.snap.Phase = app.PhaseNoAgent
	be.snap.Status = nil
	o.Authorize = func(context.Context) error { return nil }
}

func emptyLibrary(be *fixtureBackend, _ *Options) {
	be.snap = be.disconnectedSnapshot()
	be.subs, be.nodes = nil, nil
}

// TestPanelShots photographs the panel in each state and both appearances.
func TestPanelShots(t *testing.T) {
	if testing.Short() {
		t.Skip("the full-resolution screenshot job covers the panel matrix")
	}
	cases := []struct {
		name  string
		setup func(*fixtureBackend, *Options)
	}{
		{"online", nil},
		{"offline", offline},
		{"noservice", noService},
		{"empty", emptyLibrary},
	}
	for _, c := range cases {
		for _, dark := range []bool{true, false} {
			name := "panel-" + c.name + "-light"
			if dark {
				name = "panel-" + c.name + "-dark"
			}
			t.Run(name, func(t *testing.T) {
				h := newHarness(t, harnessOpt{dark: dark, panel: true, motion: true, setup: c.setup})
				h.shot(name)
			})
		}
	}
}

// TestPanelConnects: the orb and the button do what the window's do.
func TestPanelConnects(t *testing.T) {
	h := panelHarness(t, true, offline)
	h.mustHave("Not connected", "Connect")
	h.click("Connect")
	if !h.be.took("connect") {
		t.Fatalf("Connect did not connect: %v", h.be.called())
	}

	h = panelHarness(t, true, nil)
	h.mustHave("PROTECTED", "MAIN EXIT")
	h.click("Connect or disconnect")
	if !h.be.took("disconnect") {
		t.Fatalf("the orb did not disconnect: %v", h.be.called())
	}
}

// chosenLately makes the fixture remember that the person switched the main
// exit to these nodes (the first name the latest), and leaves it on automatic.
func chosenLately(names ...string) func(*fixtureBackend, *Options) {
	return func(be *fixtureBackend, _ *Options) {
		be.arrange(func(st *routing.State) {
			for i := len(names) - 1; i >= 0; i-- {
				n, ok := be.node(names[i])
				if !ok {
					panic("no fixture node " + names[i])
				}
				if err := st.SetMain(routing.ToNode(n.ID())); err != nil {
					panic(err)
				}
			}
			if err := st.SetMain(routing.Auto()); err != nil {
				panic(err)
			}
		})
	}
}

// TestPanelQuickSwitch: the panel offers a few ways to move the main exit (the
// automatic choice, the busiest region, the nodes used last), and one click
// moves the traffic.
func TestPanelQuickSwitch(t *testing.T) {
	h := panelHarness(t, true, chosenLately("Seoul 01", "Osaka 01"))
	h.mustHave("MAIN EXIT", "Automatic", "Fastest in Hong Kong", "Seoul 01", "Osaka 01")
	h.mustNotHave("Taipei 01", "Tokyo 02")

	h.click("Seoul 01")
	if !h.be.took("main node:Seoul 01") {
		t.Fatalf("clicking a node did not choose it: %v", h.be.called())
	}
	h.settle()
	// The choice in force needs no click of its own: it is marked, and the
	// nodes used before stay in the list.
	if got := h.be.Routing().Main; got.Kind != routing.DestNode || got.Node.Name != "Seoul 01" {
		t.Fatalf("main exit = %+v", got)
	}
	h.mustHave("Seoul 01", "Osaka 01", "Automatic")

	h.click("Fastest in Hong Kong")
	if !h.be.took("main region:HK") {
		t.Fatalf("the region was not chosen: %v", h.be.called())
	}
	h.settle()
	h.click("Automatic")
	if !h.be.took("main auto") {
		t.Fatalf("automatic was not chosen: %v", h.be.called())
	}
}

// TestPanelQuickSwitchSkipsNodesThatAreGone: a node chosen once and no longer
// offered by any subscription is not a way to switch.
func TestPanelQuickSwitchSkipsNodesThatAreGone(t *testing.T) {
	h := panelHarness(t, true, func(be *fixtureBackend, o *Options) {
		chosenLately("Seoul 01", "Osaka 01")(be, o)
		var keep []library.ActiveNode
		for _, n := range be.nodes {
			if n.Node.Name != "Seoul 01" {
				keep = append(keep, n)
			}
		}
		be.nodes = keep
		be.refresh()
	})
	h.mustHave("Osaka 01")
	h.mustNotHave("Seoul 01")
}

// TestPanelMode: the routing mode switches from the panel.
func TestPanelMode(t *testing.T) {
	h := panelHarness(t, true, nil)
	h.click("Global")
	if !h.be.took("mode Global") {
		t.Fatalf("Global did not change the mode: %v", h.be.called())
	}
}

// TestPanelFooter: the window opens and the application quits from the panel.
func TestPanelFooter(t *testing.T) {
	h := panelHarness(t, true, nil)
	h.click("Open EggBox")
	if !h.be.took("show-window") || !h.be.took("close-panel") {
		t.Fatalf("opening the window: %v", h.be.called())
	}
	h.click("Quit EggBox")
	if !h.be.took("quit") {
		t.Fatalf("quitting: %v", h.be.called())
	}
}

// TestPanelStates: what the panel says in each state.
func TestPanelStates(t *testing.T) {
	h := panelHarness(t, false, noService)
	h.mustHave("Service unavailable", "Authorize service")
	h.click("Authorize service")

	h = panelHarness(t, false, emptyLibrary)
	h.mustHave("Add subscription")
	h.click("Add subscription")
	if !h.be.took("show-window") {
		t.Fatalf("adding a subscription happens in the window: %v", h.be.called())
	}
}

// TestPanelFollowsLanguage: the panel speaks the chosen language.
func TestPanelFollowsLanguage(t *testing.T) {
	h := newHarness(t, harnessOpt{panel: true, lang: "zh", setup: offline})
	h.mustHave("未连接", "连接")
}
