package gui

import "testing"

// The dashboard's exit card says where traffic leaves the computer: the main
// exit and the node that carries it right now. The big header above it names
// the node as well, so what is looked for is what only the card says: the
// protocol and transport under the name, the delay with its unit, and the note
// of a stand-in.

// exitLine is how the card describes a node under its name.
func exitLine(t *testing.T, h *harness, name string) string {
	t.Helper()
	n, ok := h.be.node(name)
	if !ok {
		t.Fatalf("the fixture has no node %q", name)
	}
	return n.Node.Protocol() + "  ·  " + n.Node.Transport()
}

func TestDashboardShowsTheNodeThatCarriesTheMainExit(t *testing.T) {
	h := newHarness(t, harnessOpt{h: 900})
	h.mustHave("Exit node", "Tokyo 01", exitLine(t, h, "Tokyo 01"), "58 ms", "Test all")
	h.mustNotHave("A stand-in is used for now")
}

func TestDashboardShowsTheNodeChosenForTheMainExit(t *testing.T) {
	h := newHarness(t, harnessOpt{h: 900, setup: seoulIsChosen})
	h.mustHave("Exit node", "Seoul 01", exitLine(t, h, "Seoul 01"))
	h.mustNotHave("Tokyo 01", "A stand-in is used for now")
}

func TestDashboardSaysWhenAStandInCarriesTheMainExit(t *testing.T) {
	h := newHarness(t, harnessOpt{h: 900, setup: func(be *fixtureBackend, o *Options) {
		seoulIsChosen(be, o)
		be.down["Seoul 01"] = true
		be.refresh()
	}})
	h.mustHave("Exit node", "Tokyo 01", exitLine(t, h, "Tokyo 01"), "“Seoul 01” is not answering. A stand-in is used for now.")
	h.mustNotHave("The node “Seoul 01” you chose") // that is the Nodes page's banner
}

func TestDashboardWithoutAConnectionSaysWhatTheMainExitIsSetTo(t *testing.T) {
	h := newHarness(t, harnessOpt{h: 900})
	h.disconnected()
	h.mustHave("Exit node", "Automatic", "The fastest node is chosen for you")
	h.mustNotHave("Tokyo 01")

	h = newHarness(t, harnessOpt{h: 900, setup: seoulIsChosen})
	h.disconnected()
	h.mustHave("Exit node", "Seoul 01", exitLine(t, h, "Seoul 01"))
	h.mustNotHave("A stand-in is used for now")
}
