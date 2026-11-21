package gui

import (
	"testing"

	"eggbox/internal/library"
	"eggbox/internal/routing"
)

func TestRelayNodeMenuTwoTierAndReverseBlock(t *testing.T) {
	h := nodesHarness(t, nil)

	// Open relay picker for US Los Angeles 01 from its context menu.
	h.rightClick("US Los Angeles 01")
	h.mustHave("Relay…")
	h.pick("Relay…")
	h.settle()
	h.mustHave("Relay", "US Los Angeles 01", "Direct", "Automatic", "Fastest in a region")

	// Searching for itself shows the disabled "This node" reason.
	h.click("Search direct nodes")
	h.typeText("US Los Angeles 01")
	h.settle()
	h.mustHave("This node")

	// Search for Hong Kong 01 and pin it as the relay.
	if d, ok := h.app.dlg.(*relayDialog); ok {
		d.query = "Hong Kong 01"
	}
	h.settle()
	h.tabTo("Hong Kong 01")
	h.enter()
	h.settle()
	h.mustNotHave("SPECIFIC DIRECT NODE")

	usID := nodeID(t, h.be, "US Los Angeles 01")
	hkID := nodeID(t, h.be, "Hong Kong 01")
	pref := h.state().NodePref(usID)
	if pref.Relay == nil || pref.Relay.Kind != routing.RelayNode || pref.Relay.Node == nil || !pref.Relay.Node.Same(hkID) {
		t.Fatalf("US Los Angeles 01 relay = %+v, want Hong Kong 01", pref.Relay)
	}
	if pref.Relay.NoFallback {
		t.Fatalf("same-region stand-in should default to enabled")
	}
	if !pref.NoAuto {
		t.Fatalf("enabling relay for the first time should leave the node out of automatic choice")
	}

	// Card displays the active relay and context menu reflects it.
	h.mustHave("Via Hong Kong 01")
	h.rightClick("US Los Angeles 01")
	h.mustHave("Relay: Hong Kong 01…")
	// Dismiss menu by clicking empty area / switching picker directly.
	h.pick("Relay: Hong Kong 01…")
	h.settle()
	// Switch to Region auto (Singapore).
	h.click("Fastest in a region")
	h.pick("Singapore")
	h.settle()
	pref = h.state().NodePref(usID)
	if pref.Relay == nil || pref.Relay.Kind != routing.RelayRegion || pref.Relay.Region != "SG" {
		t.Fatalf("after region pick: relay = %+v, want SG", pref.Relay)
	}

	// Put Hong Kong 01 back so we can check two-tier + reverse dependency guard.
	h.be.arrange(func(st *routing.State) {
		r := routing.RelayNodeVal(hkID)
		_ = st.SetNodeRelay(usID, &r)
	})
	h.settle()

	// Searching for the relay node finds the relayed landing node too.
	h.app.node.query = "Hong Kong 01"
	h.settle()
	h.mustHave("US Los Angeles 01")
	h.app.node.query = ""
	h.settle()

	// Two-tier rule: opening relay picker for Singapore 01 shows US Los Angeles 01 as "Already relayed".
	sg, _ := h.app.nodeFor(nodeID(t, h.be, "Singapore 01"))
	h.app.openNodeRelay(sg)
	h.settle()
	if d, ok := h.app.dlg.(*relayDialog); ok {
		d.query = "US Los Angeles 01"
	}
	h.settle()
	h.mustHave("Already relayed")
	h.click("Cancel")
	h.settle()

	// Reverse dependency check: Hong Kong 01 cannot be made relayed while US Los Angeles 01 pins it.
	hk, _ := h.app.nodeFor(hkID)
	h.app.openNodeRelay(hk)
	h.settle()
	h.mustHave("US Los Angeles 01 uses this as its relay")
	h.click("Cancel")
	h.settle()

	// Clearing US Los Angeles 01 back to Direct unblocks Hong Kong 01.
	us, _ := h.app.nodeFor(usID)
	h.app.openNodeRelay(us)
	h.settle()
	h.click("Direct")
	h.settle()
	if p := h.state().NodePref(usID); p.Relay != nil {
		t.Fatalf("expected relay cleared to nil, got %+v", p.Relay)
	}
	hk, _ = h.app.nodeFor(hkID)
	h.app.openNodeRelay(hk)
	h.settle()
	h.mustNotHave("uses this as its relay")
}

func TestRelayUDPTipShownOnlyForUDPProtocols(t *testing.T) {
	h := nodesHarness(t, nil)
	// Singapore 01 is index 3 in the fixture → hysteria2 (UDP).
	sg, _ := h.app.nodeFor(nodeID(t, h.be, "Singapore 01"))
	if !sg.NeedsUDP {
		t.Fatalf("expected Singapore 01 (%s) to need UDP", sg.Protocol)
	}
	h.app.openNodeRelay(sg)
	h.settle()
	h.mustHave("UDP forwarding")
	h.click("Cancel")
	h.settle()

	// Tokyo 01 is vless (TCP) → no UDP tip.
	tk, _ := h.app.nodeFor(nodeID(t, h.be, "Tokyo 01"))
	h.app.openNodeRelay(tk)
	h.settle()
	h.mustNotHave("UDP forwarding")
}

func TestRelaySubscriptionDefaultAndNodeOverride(t *testing.T) {
	h := newHarness(t, harnessOpt{h: 1100})
	h.click("Subscriptions")
	h.settle()

	local := h.sub("sub-local")
	h.app.openEditSubscription(local)
	h.settle()
	h.mustHave("Edit subscription", "Default relay", "Direct")

	// Open subscription default relay picker.
	h.click("Direct")
	h.settle()
	h.mustHave("Default relay", "My nodes")
	// Filter to Sydney 01 to verify same-subscription nodes are disabled, then pick Hong Kong 01.
	if d, ok := h.app.dlg.(*relayDialog); ok {
		d.query = "Sydney"
		h.settle()
		h.mustHave("Same subscription")
		d.query = "Hong Kong 01"
		h.settle()
	}

	// Pick Hong Kong 01 as default relay for My nodes.
	h.click("Hong Kong 01")
	h.settle()
	// Back in Edit subscription: button shows Via Hong Kong 01 and auto was switched off.
	h.mustHave("Edit subscription", "Via Hong Kong 01")
	if sd, ok := h.app.dlg.(*subDialog); !ok || sd.auto {
		t.Fatalf("expected auto to be switched off when enabling subscription default relay")
	}
	h.submitDialog("Save")
	h.settle()

	after := h.sub("sub-local")
	if after.Relay == nil || after.Relay.Kind != routing.RelayNode || !after.NoAuto {
		t.Fatalf("saved subscription = %+v, want Relay=Hong Kong 01 and NoAuto=true", after)
	}
	h.mustHave("Via Hong Kong 01")

	// Nodes page shows inherited relay on Sydney 01, and allows per-node Direct override.
	h.click("Nodes")
	h.settle()
	sydID := nodeID(t, h.be, "Sydney 01")
	syd, _ := h.app.nodeFor(sydID)
	if !syd.Relay.Active() || syd.Relay.Source != routing.RelaySourceSub {
		t.Fatalf("Sydney 01 relay = %+v, want inherited from sub", syd.Relay)
	}
	h.app.openNodeRelay(syd)
	h.settle()
	h.mustHave("Follow subscription (Hong Kong 01)")
	h.click("Direct")
	h.settle()
	syd, _ = h.app.nodeFor(sydID)
	if syd.Relay.Active() {
		t.Fatalf("Sydney 01 should now be Direct override, got %+v", syd.Relay)
	}
	// Restoring "Follow subscription" works too.
	h.app.openNodeRelay(syd)
	h.settle()
	h.click("Follow subscription (Hong Kong 01)")
	h.settle()
	syd, _ = h.app.nodeFor(sydID)
	if !syd.Relay.Active() || syd.Relay.Source != routing.RelaySourceSub {
		t.Fatalf("Sydney 01 should inherit subscription relay again, got %+v", syd.Relay)
	}
}

func TestRelayVisibilityAcrossSurfaces(t *testing.T) {
	setup := func(be *fixtureBackend, _ *Options) {
		tkID := nodeID(t, be, "Tokyo 01")
		hkID := nodeID(t, be, "Hong Kong 01")
		be.arrange(func(st *routing.State) {
			r := routing.RelayNodeVal(hkID)
			_ = st.SetNodeRelay(tkID, &r)
			_ = st.SetMain(routing.ToNode(tkID))
		})
	}

	// Desktop Dashboard + Connections + Test URL + Destination picker.
	h := newHarness(t, harnessOpt{h: 1100, setup: setup})
	h.mustHave("Tokyo 01", "Via Hong Kong 01")

	// Connections page shows Via Hong Kong 01 and search matches the relay node.
	h.click("Connections")
	h.settle()
	h.mustHave("Via Hong Kong 01")
	h.app.conn.query = "Hong Kong 01"
	h.settle()
	h.mustHave("github.com")
	h.mustNotHave("www.baidu.com")

	// Test URL online shows full chain Hong Kong 01 → Tokyo 01.
	h.app.openTestURL("https://github.com")
	h.settle()
	h.mustHave("Hong Kong 01 → Tokyo 01")
	h.click("Done")
	h.settle()

	// Destination node picker shows Via Hong Kong 01 in subtitle.
	h.app.pickNodeFor("AI services", routing.Auto(), true, func(routing.Dest) {})
	h.settle()
	h.mustHave("Via Hong Kong 01")
	h.click("Cancel")
	h.settle()

	// Test URL offline shows configured relay for pinned destination.
	h.be.arrange(func(st *routing.State) {
		_ = st.SetItemDest("b-github", routing.ToNode(nodeID(t, h.be, "Tokyo 01")))
	})
	h.be.set_(h.be.disconnectedSnapshot())
	h.settle()
	h.app.openTestURL("https://github.com")
	h.settle()
	h.mustHave("Configured to connect via Hong Kong 01")

	// Tray panel shows Via Hong Kong 01.
	panel := newHarness(t, harnessOpt{panel: true, setup: setup})
	panel.mustHave("Tokyo 01", "Via Hong Kong 01")

	// Phone dashboard shows Via Hong Kong 01.
	phone := newHarness(t, harnessOpt{phone: true, setup: setup})
	phone.mustHave("Tokyo 01", "Via Hong Kong 01")
}

func TestRelayBannersStandInAndUnavailable(t *testing.T) {
	// 1. Pinned relay down -> same-region stand-in banner + Dashboard chip.
	h := nodesHarness(t, func(be *fixtureBackend, _ *Options) {
		usID := nodeID(t, be, "US Los Angeles 01")
		hkID := nodeID(t, be, "Hong Kong 01")
		be.arrange(func(st *routing.State) {
			r := routing.RelayNodeVal(hkID)
			_ = st.SetNodeRelay(usID, &r)
			_ = st.SetMain(routing.ToNode(usID))
		})
		be.down["Hong Kong 01"] = true
		be.refresh()
	})
	h.mustHave("not answering", "Hong Kong 03", "Choose again")
	h.click("Dashboard")
	h.settle()
	h.mustHave("Via Hong Kong 03", "Stand-in relay")

	// 2. Unavailable relay (region with no plain nodes) -> warning banner + never-direct badge + repick.
	h.click("Nodes")
	h.settle()
	h.be.arrange(func(st *routing.State) {
		usID := nodeID(t, h.be, "US Los Angeles 01")
		r := routing.RelayRegionVal("FR")
		_ = st.SetNodeRelay(usID, &r)
	})
	h.settle()
	h.mustHave("Relay unavailable", "will not fall back to direct")
	h.click("Choose again")
	h.settle()
	h.mustHave("Relay", "US Los Angeles 01")
}

// TestRelayShots renders screenshots of the chained-proxy UI surfaces in EN/ZH
// and dark/light when EGGBOX_SHOTS is set.
func TestRelayShots(t *testing.T) {
	if testing.Short() {
		t.Skip("renders screenshots in both themes")
	}
	setupRelayed := func(be *fixtureBackend, _ *Options) {
		tkID := nodeID(t, be, "Tokyo 01")
		usID := nodeID(t, be, "US Los Angeles 01")
		sgID := nodeID(t, be, "Singapore 01")
		hkID := nodeID(t, be, "Hong Kong 01")
		// Set Tokyo 01 -> Hong Kong 01, US Los Angeles 01 -> unavailable region FR,
		// and sub-local default relay -> HK region auto.
		be.arrange(func(st *routing.State) {
			r1 := routing.RelayNodeVal(hkID)
			_ = st.SetNodeRelay(tkID, &r1)
			st.SetNodeAuto(tkID, false)
			r2 := routing.RelayRegionVal("FR")
			_ = st.SetNodeRelay(usID, &r2)
			st.SetNodeAuto(usID, false)
			r3 := routing.RelayRegionVal("HK")
			_ = st.SetNodeRelay(sgID, &r3)
			st.SetNodeAuto(sgID, false)
			_ = st.SetMain(routing.ToNode(tkID))
		})
		_ = be.EditSubscription(t.Context(), "sub-local", func(s *library.Subscription) {
			r := routing.RelayRegionVal("HK")
			s.Relay = &r
			s.NoAuto = true
		})
	}
	cases := []struct {
		name  string
		lang  string
		panel bool
		phone bool
		run   func(*harness)
	}{
		{name: "relay-nodes", lang: "en", run: func(h *harness) { h.click("Nodes") }},
		{name: "relay-nodes-zh", lang: "zh", run: goTo("zh", "nav.nodes")},
		{name: "relay-dialog", lang: "en", run: func(h *harness) {
			h.click("Nodes")
			sg, _ := h.app.nodeFor(nodeID(t, h.be, "Singapore 01"))
			h.app.openNodeRelay(sg)
		}},
		{name: "relay-dialog-zh", lang: "zh", run: func(h *harness) {
			goTo("zh", "nav.nodes")(h)
			sg, _ := h.app.nodeFor(nodeID(t, h.be, "Singapore 01"))
			h.app.openNodeRelay(sg)
		}},
		{name: "relay-dashboard", lang: "en"},
		{name: "relay-dashboard-zh", lang: "zh"},
		{name: "relay-panel", lang: "en", panel: true},
		{name: "relay-panel-zh", lang: "zh", panel: true},
		{name: "relay-connections", lang: "en", run: func(h *harness) { h.click("Connections") }},
		{name: "relay-sub-edit-zh", lang: "zh", run: func(h *harness) {
			goTo("zh", "nav.subscriptions")(h)
			h.app.openEditSubscription(h.sub("sub-local"))
		}},
		{name: "relay-phone-dashboard-zh", lang: "zh", phone: true},
	}
	for _, tc := range cases {
		for _, dark := range []bool{true, false} {
			theme := "light"
			if dark {
				theme = "dark"
			}
			t.Run(tc.name+"-"+theme, func(t *testing.T) {
				h := newHarness(t, harnessOpt{dark: dark, lang: tc.lang, panel: tc.panel, phone: tc.phone, motion: true, setup: setupRelayed})
				if tc.run != nil {
					tc.run(h)
					h.settle()
				}
				h.shot(tc.name + "-" + theme)
			})
		}
	}
}
