package gui

import (
	"context"
	"errors"
	"slices"
	"testing"
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
		o.AllowVPN = func(context.Context) error { be.record("allow-vpn"); return errors.New("declined") }
	}})
	h.click("Connect")
	h.settle()
	if slices.Contains(h.be.called(), "connect ignore=false") {
		t.Fatal("connected although the VPN was declined")
	}
	h.mustHave("EggBox may not connect yet")
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
