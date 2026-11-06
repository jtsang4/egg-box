package gui

import (
	"context"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

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

// TestPanelFooterStaysInReach: however tall the body grows (a connection with
// several nodes to switch to, a long error, the other language), the footer
// with the way to the window and its settings stays whole where it is in the
// quietest panel, and works. It once slid out of the panel as soon as the
// connection had a few recent nodes, which left no way back to the window but
// disconnecting first.
func TestPanelFooterStaysInReach(t *testing.T) {
	recent := func(be *fixtureBackend, o *Options) {
		chosenLately("Osaka 01", "Tokyo 02", "Taipei 01")(be, o)
		be.arrange(func(st *routing.State) {
			n, _ := be.node("Seoul 01")
			if err := st.SetMain(routing.ToNode(n.ID())); err != nil {
				panic(err)
			}
		})
	}
	longError := func(be *fixtureBackend, o *Options) {
		failedConnection(be, o)
		be.snap.Status.LastError = strings.Repeat("the core could not start: the outbound handshake timed out; ", 4)
	}
	// Find reports what is visible of a control, so a footer pushed half out
	// of the panel shows as a smaller box, not a missing one.
	where := map[string]ui.Rect{}
	for _, lang := range []string{"en", "zh"} {
		h := newHarness(t, harnessOpt{panel: true, lang: lang, setup: emptyLibrary})
		open := map[string]string{"en": "Open EggBox", "zh": "打开 EggBox"}[lang]
		r, ok := h.ui.Find(open)
		if !ok || r.Y+r.H > PanelHeight {
			t.Fatalf("%s: the quietest panel has no whole %q: %+v %v", lang, open, r, ok)
		}
		where[lang] = r
	}
	cases := []struct {
		name, lang     string
		setup          func(*fixtureBackend, *Options)
		open, settings string
	}{
		{"connected", "en", recent, "Open EggBox", "Settings"},
		{"connected-zh", "zh", recent, "打开 EggBox", "设置"},
		{"failed", "en", longError, "Open EggBox", "Settings"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, harnessOpt{panel: true, lang: tc.lang, setup: tc.setup})
			if r, ok := h.ui.Find(tc.open); !ok || r != where[tc.lang] {
				t.Fatalf("%q is at %+v (found %v), not whole where it belongs (%+v)", tc.open, r, ok, where[tc.lang])
			}
			h.click(tc.settings)
			if !h.be.took("show-window") || !h.be.took("close-panel") || h.app.page != pageSettings {
				t.Fatalf("settings from the panel: page %v, calls %v", h.app.page, h.be.called())
			}
		})
	}
}

// TestPanelOpensAtTop: scrolled down to the nodes and closed, the panel opens
// again on the connection, at its top.
func TestPanelOpensAtTop(t *testing.T) {
	h := panelHarness(t, false, func(be *fixtureBackend, o *Options) {
		chosenLately("Osaka 01", "Tokyo 02", "Taipei 01")(be, o)
		be.arrange(func(st *routing.State) {
			n, _ := be.node("Seoul 01")
			if err := st.SetMain(routing.ToNode(n.ID())); err != nil {
				panic(err)
			}
		})
	})
	top, ok := h.ui.Find("PROTECTED")
	if !ok {
		t.Fatalf("no status; have %s", h.digest())
	}
	h.ui.Wheel(PanelWidth/2, PanelHeight/2, 0, 400)
	h.settle()
	if r, ok := h.ui.Find("PROTECTED"); ok && r.Y >= top.Y {
		t.Fatalf("the body did not scroll: status at %+v, was %+v", r, top)
	}
	h.app.PanelOpening()
	h.settle()
	if r, ok := h.ui.Find("PROTECTED"); !ok || r != top {
		t.Fatalf("opened again, the status is at %+v (found %v), not at the top (%+v)", r, ok, top)
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
