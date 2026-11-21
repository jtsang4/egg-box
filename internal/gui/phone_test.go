package gui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"eggbox/internal/library"
	"eggbox/internal/routing"
)

func TestPhoneTabsReachEveryPage(t *testing.T) {
	h := newHarness(t, harnessOpt{phone: true})
	for _, step := range []struct {
		tap  string
		page page
	}{
		{"Nodes", pageNodes},
		{"Subscriptions", pageSubscriptions},
		{"Routing", pageRouting},
		{"Activity", pageConnections},
		{"Logs", pageLogs},
		{"Settings", pageSettings},
		{"Home", pageDashboard},
	} {
		h.click(step.tap)
		h.settle()
		if h.app.page != step.page {
			t.Fatalf("after tapping %q the page is %v, want %v", step.tap, h.app.page, step.page)
		}
	}
	// A tab opens on the page it showed last.
	h.click("Activity")
	h.settle()
	if h.app.page != pageLogs {
		t.Fatalf("the Activity tab opened on %v, want the logs it showed last", h.app.page)
	}
	// Nothing of the desktop's sidebar is there.
	if h.ui.HasText("PROXY") || h.ui.HasText("MONITOR") {
		t.Fatal("the sidebar's sections show on a phone")
	}
}

func TestPhoneConnectAsksForTheVPNFirst(t *testing.T) {
	h := newHarness(t, harnessOpt{phone: true, setup: offline})
	h.click("Connect")
	h.settle()
	calls := h.be.called()
	ask, conn := slices.Index(calls, "allow-vpn"), slices.Index(calls, "connect ignore=false")
	if ask < 0 || conn < 0 || ask > conn {
		t.Fatalf("calls %v: the VPN must be allowed before connecting", calls)
	}
}

func TestPhoneVPNDeclinedDoesNotConnect(t *testing.T) {
	h := newHarness(t, harnessOpt{phone: true, setup: func(be *fixtureBackend, o *Options) {
		offline(be, o)
		o.AllowVPN = func(context.Context) error { be.record("allow-vpn"); return fmt.Errorf("asked: %w", ErrVPNDeclined) }
	}})
	h.click("Connect")
	h.settle()
	if slices.Contains(h.be.called(), "connect ignore=false") {
		t.Fatal("connected although the VPN was declined")
	}
	h.mustHave("EggBox may not connect yet")
}

func TestPhoneVPNThatCannotBeSetUpSaysWhy(t *testing.T) {
	h := newHarness(t, harnessOpt{phone: true, setup: func(be *fixtureBackend, o *Options) {
		offline(be, o)
		o.AllowVPN = func(context.Context) error { return errors.New("IPC failed") }
	}})
	h.click("Connect")
	h.settle()
	if slices.Contains(h.be.called(), "connect ignore=false") {
		t.Fatal("connected although the VPN could not be set up")
	}
	h.mustHave("IPC failed")
}

func TestPhoneSettingsLeaveOutTheDesktop(t *testing.T) {
	desktopOnly := []string{
		"Keep running in the tray", "TUN mode", "Reconnect when the network changes", "Local proxy port", "Launch at login", "Authorized user",
	}
	h := newHarness(t, harnessOpt{phone: true, h: 2800})
	h.click("Settings")
	h.settle()
	for _, s := range desktopOnly {
		if h.ui.HasText(s) {
			t.Errorf("%q shows on a phone", s)
		}
	}
	h.mustHave("VPN engine", "What happens when EggBox opens.", "Android")

	// The desktop keeps them.
	d := newHarness(t, harnessOpt{h: 2800})
	d.click("Settings")
	d.settle()
	d.mustHave("Keep running in the tray", "TUN mode", "Reconnect when the network changes")
}

func TestPhoneWordsSayDevice(t *testing.T) {
	h := newHarness(t, harnessOpt{phone: true, setup: introduction})
	h.mustHave("Links and keys stay on this device, encrypted by the system.")
	if h.ui.HasText("Links and keys stay on this computer, in the system keychain.") {
		t.Fatal("the desktop's words show on a phone")
	}
}

// TestPhoneScreensAtTheSmallestPhone builds every page and the common dialogs
// on a small phone in both languages, connected and not: nothing may fail to
// build, and each page shows its title.
func TestPhoneScreensAtTheSmallestPhone(t *testing.T) {
	for _, lang := range []string{"en", "zh"} {
		for _, online := range []bool{false, true} {
			setup := func(be *fixtureBackend, o *Options) {
				if !online {
					offline(be, o)
				}
			}
			h := newHarness(t, harnessOpt{phone: true, w: 360, h: 640, lang: lang, setup: setup})
			for pg := page(0); pg < pageCount; pg++ {
				h.app.goTo(pg)
				h.settle()
				title := translate(lang, pageNavKey(pg))
				if pg == pageDashboard {
					title = "EggBox"
				}
				if !h.ui.HasText(title) {
					t.Errorf("%s online=%v: page %v shows no %q", lang, online, pg, title)
				}
			}
			for _, open := range []func(){h.app.openAddSubscription, h.app.openImport, h.app.openAddItem, func() { h.app.openTestURL("") }} {
				open()
				h.settle()
				if h.app.dlg == nil {
					t.Errorf("%s: a dialog did not open", lang)
				}
				h.app.closeDialog()
				h.settle()
			}
		}
	}
}

func TestPhoneToastsStayAboveTheTabs(t *testing.T) {
	h := newHarness(t, harnessOpt{phone: true})
	h.app.toastInfo("A message for the phone")
	h.settle()
	b, ok := h.ui.Find("A message for the phone")
	if !ok {
		t.Fatal("the toast is not shown")
	}
	tab, ok := h.ui.Find(h.app.t("tab.home"))
	if !ok {
		t.Fatal("no tab bar")
	}
	if b.Y+b.H > tab.Y-8 {
		t.Fatalf("the toast (%v) reaches into the bar of tabs (%v)", b, tab)
	}
}

// manyPhoneGroups builds a fixture with 28 regions, 6 subscriptions and ~150
// nodes so the phone's nodes page is exercised at real-world group density.
func manyPhoneGroups(be *fixtureBackend, _ *Options) {
	subNames := []struct {
		id, name string
	}{
		{"sub-alpha", "Alpha Cloud"},
		{"sub-beta", "Beta Link"},
		{"sub-gamma", "Gamma Relay"},
		{"sub-delta", "Delta Net"},
		{"sub-epsilon", "Epsilon Route"},
		{"sub-zeta", "Zeta Direct"},
	}
	// 27 named regions + unplaced ("Other") = 28 region filter pills.
	regions := []struct {
		prefix string
		count  int
	}{
		{"Node Plain", 18},
		{"Hong Kong", 14},
		{"US Los Angeles", 12},
		{"Tokyo", 11},
		{"Singapore", 10},
		{"Taipei", 9},
		{"Seoul", 6},
		{"Vietnam", 5},
		{"Frankfurt", 5},
		{"Dubai", 3},
		{"Argentina", 3},
		{"Sydney", 3},
		{"Brazil", 3},
		{"Canada", 3},
		{"Paris", 3},
		{"London", 3},
		{"Indonesia", 3},
		{"India", 3},
		{"Italy", 3},
		{"Malaysia", 3},
		{"Nigeria", 3},
		{"Norway", 3},
		{"Philippines", 3},
		{"Thailand", 3},
		{"Turkey", 3},
		{"Ukraine", 3},
		{"Macao", 2},
		{"Netherlands", 2},
	}

	be.subs = nil
	be.nodes = nil
	counts := map[string]int{}
	idx := 0
	for _, rg := range regions {
		for k := 1; k <= rg.count; k++ {
			sub := subNames[idx%len(subNames)]
			name := fmt.Sprintf("%s %02d", rg.prefix, k)
			node := fixtureNode(idx, name)
			be.nodes = append(be.nodes, library.ActiveNode{
				Node: node, SubID: sub.id, SubName: sub.name, Auto: true,
			})
			counts[sub.id]++
			idx++
		}
	}
	for _, s := range subNames {
		be.subs = append(be.subs, library.Subscription{
			ID: s.id, Name: s.name, Kind: library.KindRemote, Enabled: true,
			Host: s.id + ".example", IntervalHours: 24,
			CreatedAt: fixtureNow.Add(-30 * 24 * time.Hour),
			UpdatedAt: fixtureNow.Add(-time.Hour),
			CheckedAt: fixtureNow.Add(-time.Hour),
			NodeCount: counts[s.id], Format: "sing-box",
		})
	}
	be.refresh()
}

func swipeFinger(h *harness, x0, y0, x1, y1 float32) {
	h.ui.FingerDown(x0, y0)
	h.ui.Advance(20 * time.Millisecond)
	const steps = 6
	for i := 1; i <= steps; i++ {
		t := float32(i) / steps
		h.ui.FingerMove(x0+(x1-x0)*t, y0+(y1-y0)*t)
		h.ui.Advance(16 * time.Millisecond)
	}
	h.ui.FingerUp(x1, y1)
	h.settle()
}

func TestPhoneNodesWithManyGroupsShowsAndScrollsList(t *testing.T) {
	h := newHarness(t, harnessOpt{phone: true, lang: "zh", setup: manyPhoneGroups})
	h.click("节点")
	h.settle()

	tab, ok := h.ui.Find("首页")
	if !ok {
		t.Fatal("missing bottom tab bar")
	}

	// Region and subscription filters each stay on a single horizontal strip.
	if h.app.node.regionScroll.MaxX <= 0 {
		t.Fatalf("region filter strip did not overflow horizontally (MaxX=%v)", h.app.node.regionScroll.MaxX)
	}
	if h.app.node.subScroll.MaxX <= 0 {
		t.Fatalf("subscription filter strip did not overflow horizontally (MaxX=%v)", h.app.node.subScroll.MaxX)
	}
	allRg, ok := h.ui.Find("全部")
	if !ok {
		t.Fatal("missing All region pill")
	}
	otherRg, ok := h.ui.Find("其他")
	if !ok {
		t.Fatal("missing Other region pill")
	}
	if allRg.Y != otherRg.Y {
		t.Fatalf("region pills wrapped onto multiple rows: All=%v Other=%v", allRg, otherRg)
	}
	allSub, ok := h.ui.Find("全部订阅")
	if !ok {
		t.Fatal("missing All subscriptions pill")
	}
	alphaSub, ok := h.ui.Find("Alpha Cloud")
	if !ok {
		t.Fatal("missing first subscription pill")
	}
	if allSub.Y != alphaSub.Y {
		t.Fatalf("subscription pills wrapped onto multiple rows: AllSubs=%v Alpha=%v", allSub, alphaSub)
	}

	// Multiple node cards must already be visible above the bottom tab bar on
	// initial load, even with 28 regions and 6 subscriptions.
	visibleCards := 0
	for _, name := range []string{"Node Plain 01", "Node Plain 02", "Node Plain 03"} {
		if r, ok := h.ui.Find(name); ok && r.Y+r.H < tab.Y {
			visibleCards++
		}
	}
	if visibleCards < 2 {
		t.Fatalf("only %d node cards visible above tab bar on initial phone load; texts: %s", visibleCards, h.digest())
	}
	h.shot("phone-nodes-many-zh-light")

	// Horizontal swipe on the region strip scrolls regions without scrolling the page.
	swipeFinger(h, 340, allRg.Y+allRg.H/2, 80, allRg.Y+allRg.H/2)
	if h.app.node.regionScroll.X <= 0 {
		t.Fatalf("horizontal finger swipe did not scroll region strip (X=%v)", h.app.node.regionScroll.X)
	}
	h.mustHave("自动选择")

	// Vertical swipe starting on top of the pill strip scrolls the whole page
	// down so the main strip leaves the viewport and deeper nodes appear.
	swipeFinger(h, 195, allRg.Y+allRg.H/2, 195, 160)
	swipeFinger(h, 195, 650, 195, 160)
	if h.ui.HasText("自动选择") {
		t.Fatal("main exit strip did not scroll out of view on vertical swipe")
	}
	if r, ok := h.ui.Find("Node Plain 10"); !ok || r.Y+r.H > tab.Y {
		t.Fatalf("expected deeper node card after scrolling, got ok=%v rect=%v tab=%v", ok, r, tab)
	}
	h.shot("phone-nodes-many-scrolled-zh-light")

	// Tapping the active Nodes tab returns straight to the top of the page.
	tabNodes, ok := h.ui.Find("节点#2")
	if !ok {
		// Find the bottom tab bar item (below tab.Y - 10).
		tabNodes = tab
		tabNodes.X = 390 * 0.3
	}
	h.ui.ClickAt(tabNodes.X+tabNodes.W/2, tabNodes.Y+tabNodes.H/2)
	h.settle()
	h.mustHave("自动选择", "Node Plain 01")

	// Filtering by a subscription updates the list while keeping matching cards visible.
	h.click("Alpha Cloud")
	h.settle()
	h.mustHave("Node Plain 01")
	h.mustNotHave("Node Plain 02")
	h.click("Alpha Cloud")
	h.settle()
	h.mustHave("Node Plain 02")
}

func TestPhoneNodesManyGroupsOnSmallestPhoneWithBannerAndUsage(t *testing.T) {
	h := newHarness(t, harnessOpt{
		phone: true, w: 360, h: 640, lang: "zh",
		setup: func(be *fixtureBackend, o *Options) {
			manyPhoneGroups(be, o)
			n, _ := be.node("Hong Kong 01")
			be.arrange(func(st *routing.State) {
				if err := st.SetItemDest("b-telegram", routing.ToNode(n.ID())); err != nil {
					panic(err)
				}
			})
			be.down["Hong Kong 01"] = true
			be.refresh()
		},
	})
	h.click("节点")
	h.settle()
	h.mustHave("Telegram", "自动选择")

	// Even on a 360x640 phone with a warning banner, an in-use row, 28 regions
	// and 6 subscriptions, swiping vertically scrolls straight into the node list.
	swipeFinger(h, 180, 520, 180, 140)
	swipeFinger(h, 180, 520, 180, 140)
	tab, ok := h.ui.Find("首页")
	if !ok {
		t.Fatal("missing bottom tab bar")
	}
	foundCard := false
	for _, name := range []string{"Node Plain 01", "Node Plain 02", "Node Plain 03", "Node Plain 04", "Node Plain 05", "Node Plain 06"} {
		if r, ok := h.ui.Find(name); ok && r.Y+r.H < tab.Y {
			foundCard = true
			break
		}
	}
	if !foundCard {
		t.Fatalf("no node card reachable on small phone after scrolling; texts: %s", h.digest())
	}
}

func TestPhoneNodesLiveDataDoesNotKeepAnimating(t *testing.T) {
	h := newHarness(t, harnessOpt{phone: true})
	h.click("Nodes")
	h.settle()
	for n := 0; n < 3; n++ {
		h.be.set_(liveTick(*h.be.Snapshot(), n))
		h.ui.Frame()
		if h.ui.FramePending() {
			t.Fatalf("tick %d: phone nodes page keeps asking for frames after a live data tick", n)
		}
	}
}
