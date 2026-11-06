package gui

import (
	"context"
	"testing"
	"time"

	"eggbox/internal/app"
	"eggbox/internal/boot"
	"eggbox/internal/library"
	"eggbox/internal/routing"
)

// realService is the interface on the application's own service, in the
// demonstration world: an agent in this process on a simulated core, an invented
// network and a library in a directory that is deleted afterwards. The other
// tests drive the screens with a fixture; this one is what shows that the
// fixture says what the service says.
func realService(t *testing.T, o harnessOpt) (*harness, *boot.Runtime) {
	t.Helper()
	rt, err := boot.Start(context.Background(), boot.Config{Mode: boot.Demo, Seed: 5, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rt.Close)
	o.setup = func(_ *fixtureBackend, opt *Options) {
		opt.Backend = ServiceBackend{rt.Service}
		opt.Now = time.Now
	}
	h := newHarness(t, o)
	return h, rt
}

// eventually waits, showing the interface to the service while it does, for
// what a background job brings about.
func (h *harness) eventually(what string, ok func() bool) {
	h.t.Helper()
	for deadline := time.Now().Add(30 * time.Second); !ok(); {
		if time.Now().After(deadline) {
			h.t.Fatalf("still waiting for %s; the screen shows %s", what, h.digest())
		}
		time.Sleep(25 * time.Millisecond)
		h.settle()
	}
}

func TestTheScreensWorkOnTheRealService(t *testing.T) {
	if testing.Short() {
		t.Skip("starts the demonstration world")
	}
	h, rt := realService(t, harnessOpt{h: 1500})
	a := h.app

	// The plan the demonstration person has: the shipped one, an item of their
	// own and exceptions.
	h.click("Routing")
	h.settle()
	h.mustHave("Ad blocking", "AI services", "Intranet", "Everything else", "My exceptions")
	if n := len(rt.Library.Routing().Exceptions); n != 3 {
		t.Fatalf("%d exceptions", n)
	}

	// Connecting: the dashboard says so, and the rule files the plan needs have
	// come from the invented world.
	h.click("Dashboard")
	h.click("Connect")
	h.eventually("the connection", func() bool { return rt.Service.Snapshot().Phase == app.PhaseConnected })
	h.eventually("the plan's rule files", func() bool {
		p := rt.Service.Snapshot().Plan
		return p.Ready && p.Missing == 0
	})
	h.mustHave("PROTECTED")

	// Where Apple goes is a choice made in the page, and the person's choice
	// reaches the library and the running core without a new configuration.
	h.click("Routing")
	h.settle()
	before := rt.Service.Snapshot().Applied.Revision
	h.chooseDest("Apple", "Direct", "Follow main exit", "Follow main exit (Automatic)")
	h.eventually("the choice in the library", func() bool {
		r, _, ok := rt.Library.Routing().View().Row("b-apple")
		return ok && r.Item.Dest.Kind == routing.DestFollow
	})
	if got := rt.Service.Snapshot().Applied.Revision; got != before {
		t.Fatalf("moving an item between exits made a new configuration: revision %d, then %d", before, got)
	}

	// Asking where an address goes, with the node it leaves through, and making
	// an exception of the answer.
	h.click("Test a URL")
	h.typeText("https://www.netflix.com/browse")
	h.enter()
	h.eventually("the answer", func() bool { return h.ui.HasText("It matches") })
	h.mustHave("Streaming", "Right now it leaves through")
	n := len(rt.Library.Routing().Exceptions)
	h.tabTo("Direct")
	h.enter()
	h.eventually("the exception", func() bool { return len(rt.Library.Routing().Exceptions) == n+1 })
	h.eventually("its explanation", func() bool { return h.ui.HasText("It matches your exception") })
	h.shut()

	// The main exit is chosen on the nodes page, and the core is told.
	h.click("Nodes")
	h.settle()
	var node string
	h.eventually("a node in Japan that answers", func() bool {
		for _, n := range a.liveNodes() {
			if !n.Ambiguous && n.Region == "JP" && n.Delay > 0 {
				node = n.Name
				return true
			}
		}
		return false
	})
	h.click(node)
	h.eventually("the main exit", func() bool {
		return rt.Library.Routing().Main.Kind == routing.DestNode
	})
	h.eventually("the selection in the core", func() bool {
		for _, g := range rt.Service.Snapshot().Groups {
			if g.Tag == "Proxy" && g.Selected != "" {
				for _, it := range g.Items {
					if it.Tag == g.Selected && it.Node != nil && it.Node.Name == node {
						return true
					}
				}
			}
		}
		return false
	})

	// The connections, each with what decided it in the person's words.
	rt.Service.WatchConnections(true)
	h.click("Connections")
	h.eventually("connections", func() bool { return len(rt.Service.Snapshot().Conns) >= 3 })
	h.settle()
	h.leaks("the connections of the real service")
	// A connection of the list is made an exception, for its registrable domain.
	var host string
	for _, c := range rt.Service.Snapshot().Conns {
		if c.Host != "" && c.Open() && h.ui.HasText(c.Host) {
			host = c.Host
			break
		}
	}
	if host == "" {
		t.Fatalf("no connection with a name on the screen: %s", h.digest())
	}
	n = len(rt.Library.Routing().Exceptions)
	h.rightClick(host)
	h.pick("From#1", "Direct")
	h.eventually("the exception from the connection", func() bool { return len(rt.Library.Routing().Exceptions) == n+1 })
	rt.Service.WatchConnections(false)

	// Nothing on any screen is a key or a placeholder, in either language.
	for _, lang := range []string{"en", "zh"} {
		if err := a.be.UpdateSettings(func(s *library.Settings) { s.Language = lang }); err != nil {
			t.Fatal(err)
		}
		h.settle()
		for _, nav := range []string{"nav.dashboard", "nav.nodes", "nav.subscriptions", "nav.routing", "nav.connections", "nav.logs", "nav.settings"} {
			h.click(a.t(nav))
			h.settle()
			h.leaks(lang + " " + nav + " on the real service")
		}
	}
}
