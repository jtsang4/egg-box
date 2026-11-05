package gui

import (
	"context"
	"sort"
	"testing"

	"eggbox/internal/app"
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
				h := panelHarness(t, dark, c.setup)
				h.settle()
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
	h.mustHave("PROTECTED", "FASTEST NODES")
	h.click("Connect or disconnect")
	if !h.be.took("disconnect") {
		t.Fatalf("the orb did not disconnect: %v", h.be.called())
	}
}

// TestPanelNodes: the panel lists the fastest nodes, and a click moves traffic.
func TestPanelNodes(t *testing.T) {
	h := panelHarness(t, true, nil)
	nodes := h.app.liveNodes()
	var timed []nodeEntry
	for _, n := range nodes {
		if n.Delay > 0 {
			timed = append(timed, n)
		}
	}
	sort.SliceStable(timed, func(i, j int) bool { return timed[i].Delay < timed[j].Delay })
	if len(timed) < 3 {
		t.Fatalf("the fixture has %d timed nodes", len(timed))
	}
	for _, n := range timed[:3] {
		h.mustHave(n.Name)
	}
	h.mustNotHave(timed[3].Name)

	// The node in use needs no click; any other one moves the traffic.
	use := ""
	if it, _, ok := h.app.exitNode(); ok && it.Node != nil {
		use = it.Node.Key
	}
	for _, n := range timed[:3] {
		if n.Key == use {
			continue
		}
		h.click(n.Name)
		if !h.be.took("select " + n.Key) {
			t.Fatalf("clicking %q did not select it: %v", n.Name, h.be.called())
		}
		return
	}
	t.Fatalf("every listed node is the one in use?")
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
