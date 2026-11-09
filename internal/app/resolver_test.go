package app_test

import (
	"testing"

	"eggbox/internal/app"
	"eggbox/internal/routing"
)

// The test of an address follows the order the core uses: a rule may claim a
// private range before the local network is let go, and a name whose exit
// looks it up (dns=exit) is never looked up here, so an exception about
// addresses earlier in the list does not turn it away.
func TestTestURLWithResolversAndPrivateRanges(t *testing.T) {
	h := newHarness(t, nil)
	h.addSubscription()
	h.waitPlan("the built-in plan's rule files", func(p app.PlanInfo) bool { return len(p.Items) == 12 && p.Missing == 0 })

	h.testURL("10.1.2.3", func(r *app.TestURLResult) bool { return r.Why.Kind == app.WhyLAN })

	var narrow, intranet, intra routing.Exception
	h.edit(func(st *routing.State) (err error) {
		if narrow, err = st.AddException(routing.Exception{Kind: routing.EntryIPCIDR, Value: "10.99.0.0/24", Dest: routing.Direct(), Enabled: true}); err != nil {
			return err
		}
		if intranet, err = st.AddException(routing.Exception{Kind: routing.EntryIPCIDR, Value: "10.0.0.0/8", Dest: routing.Follow(), Enabled: true}); err != nil {
			return err
		}
		intra, err = st.AddException(routing.Exception{Kind: routing.EntryDomainSuffix, Value: "intra.example", Dest: routing.Follow(), Enabled: true, Resolver: "exit"})
		return err
	})
	res := h.testURL("10.1.2.3", func(r *app.TestURLResult) bool { return r.Why.Kind == app.WhyException })
	if res.Why.ID != intranet.ID || res.Dest.Kind != routing.DestFollow {
		t.Errorf("a rule claims a private range before the local network: %+v", res)
	}
	res = h.testURL("10.99.0.5", func(r *app.TestURLResult) bool { return r.Why.Kind == app.WhyException })
	if res.Why.ID != narrow.ID || res.Dest.Kind != routing.DestDirect {
		t.Errorf("the narrower range comes first: %+v", res)
	}
	h.testURL("192.168.1.20", func(r *app.TestURLResult) bool { return r.Why.Kind == app.WhyLAN })
	h.testURL("printer.local", func(r *app.TestURLResult) bool { return r.Why.Kind == app.WhyLAN })

	// Nobody here can look the name up; the exceptions about addresses before
	// it do not try, and it goes where its own rule says.
	res = h.testURL("wiki.intra.example", func(r *app.TestURLResult) bool { return r.Why.Kind == app.WhyException })
	if res.Why.ID != intra.ID || res.Resolved || res.ResolveErr != "" {
		t.Errorf("a name the exit looks up is not looked up here: %+v", res)
	}
	// A rule before it that looks the name up with a server of its own comes
	// first: the name is looked up, and what this machine's resolver does not
	// know is left to that server, so the name is matched by name.
	var first routing.Exception
	h.edit(func(st *routing.State) (err error) {
		if first, err = st.AddException(routing.Exception{Kind: routing.EntryDomain, Value: "wiki.intra.example", Dest: routing.Direct(), Enabled: true, Resolver: "10.0.0.53"}); err != nil {
			return err
		}
		return st.MoveException(first.ID, -2)
	})
	res = h.testURL("wiki.intra.example", func(r *app.TestURLResult) bool { return r.Why.Kind == app.WhyException && r.Why.ID == first.ID })
	if !res.Resolved || res.ResolveErr != "" {
		t.Errorf("looked up, and not turned away: %+v", res)
	}
	h.edit(func(st *routing.State) error { return st.DeleteException(first.ID) })
	h.testURL("wiki.intra.example", func(r *app.TestURLResult) bool { return r.Why.Kind == app.WhyException })

	// Without dns=exit the same name is turned away, as the core would.
	h.edit(func(st *routing.State) error {
		return st.ReplaceExceptions(st.Exceptions, []routing.Exception{narrow, intranet,
			{ID: intra.ID, Kind: intra.Kind, Value: intra.Value, Dest: intra.Dest, Enabled: true}})
	})
	h.testURL("wiki.intra.example", func(r *app.TestURLResult) bool { return r.Why.Kind == app.WhyUnresolved })
}

// A name whose rule asks a server of its own is not turned away because this
// machine's resolver does not know it: that server may. It is matched by name.
func TestTestURLLeavesANameToTheServerItsRuleNames(t *testing.T) {
	h := newHarness(t, nil)
	h.addSubscription()
	h.waitPlan("the built-in plan's rule files", func(p app.PlanInfo) bool { return len(p.Items) == 12 && p.Missing == 0 })
	var corp routing.Exception
	h.edit(func(st *routing.State) (err error) {
		if _, err = st.AddException(routing.Exception{Kind: routing.EntryIPCIDR, Value: "192.0.2.0/24", Dest: routing.Reject(), Enabled: true}); err != nil {
			return err
		}
		corp, err = st.AddException(routing.Exception{Kind: routing.EntryDomainSuffix, Value: "corp.internal", Dest: routing.Direct(), Enabled: true, Resolver: "10.0.0.53"})
		return err
	})
	res := h.testURL("git.corp.internal", func(r *app.TestURLResult) bool { return r.Why.Kind == app.WhyException })
	if res.Why.ID != corp.ID || res.ResolveErr != "" {
		t.Fatalf("%+v", res)
	}
	// What this machine's resolver does find still counts.
	h.dns.set("git.corp.internal", "192.0.2.9")
	res = h.testURL("git.corp.internal", func(r *app.TestURLResult) bool {
		return r.Why.Kind == app.WhyException && r.Dest.Kind == routing.DestReject
	})
	if res.Why.ID == corp.ID {
		t.Fatalf("%+v", res)
	}
}

// The system's resolver is the one the test asks: what it cannot find, a rule
// that names it cannot find either, and the core turns the name away.
func TestTestURLRefusesWhatTheSystemResolverCannotFind(t *testing.T) {
	h := newHarness(t, nil)
	h.addSubscription()
	h.waitPlan("the built-in plan's rule files", func(p app.PlanInfo) bool { return len(p.Items) == 12 && p.Missing == 0 })
	h.edit(func(st *routing.State) error {
		if _, err := st.AddException(routing.Exception{Kind: routing.EntryIPCIDR, Value: "192.0.2.0/24", Dest: routing.Reject(), Enabled: true}); err != nil {
			return err
		}
		_, err := st.AddException(routing.Exception{Kind: routing.EntryDomainSuffix, Value: "corp.internal", Dest: routing.Direct(), Enabled: true, Resolver: "system"})
		return err
	})
	h.testURL("git.corp.internal", func(r *app.TestURLResult) bool { return r.Why.Kind == app.WhyUnresolved })
}
