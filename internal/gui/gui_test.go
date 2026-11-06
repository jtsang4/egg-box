package gui

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"eggbox/internal/agentapi"
	"eggbox/internal/agentclient"
	"eggbox/internal/app"
	"eggbox/internal/routing"
	"eggbox/internal/subscription"
)

// enter presses Return, the way a person submits a focused field.
func (h *harness) enter() {
	h.t.Helper()
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Advance(300 * time.Millisecond)
}

// typeText types into the focused field.
func (h *harness) typeText(s string) {
	h.t.Helper()
	h.ui.Type(s)
	h.ui.Advance(100 * time.Millisecond)
}

func (h *harness) disconnected() {
	h.be.set_(h.be.disconnectedSnapshot())
	h.settle()
}

func TestNavigation(t *testing.T) {
	h := newHarness(t, harnessOpt{})
	pages := []struct{ nav, title string }{
		{"Nodes", "nodes available"},
		{"Subscriptions", "subscriptions"},
		{"Routing", "items · Recommended"},
		{"Connections", "active connections"},
		{"Logs", "lines"},
		{"Settings", "Make it yours"},
		{"Dashboard", "PROTECTED"},
	}
	for _, p := range pages {
		h.click(p.nav)
		h.settle()
		h.mustHave(p.title)
	}
}

func TestConnectionsPageWatchesOnlyWhileShown(t *testing.T) {
	h := newHarness(t, harnessOpt{})
	h.click("Connections")
	if !h.be.took("watch true") {
		t.Fatalf("opening Connections should start watching: %v", h.be.called())
	}
	h.click("Logs")
	if !h.be.took("watch false") {
		t.Fatalf("leaving Connections should stop watching: %v", h.be.called())
	}
}

// The window closed to the tray is destroyed; nothing it alone shows may go on
// streaming meanwhile, and it takes up again when the window is made again.
func TestClosedWindowWatchesNothing(t *testing.T) {
	h := newHarness(t, harnessOpt{})
	watching := func() bool {
		h.be.mu.Lock()
		defer h.be.mu.Unlock()
		return h.be.watching
	}
	h.click("Connections")
	if !watching() {
		t.Fatalf("opening Connections should start watching: %v", h.be.called())
	}
	h.app.WindowClosed()
	if watching() {
		t.Fatalf("closing the window should stop watching: %v", h.be.called())
	}
	h.app.Navigate("logs")
	h.app.Navigate("connections") // from the tray menu, before the window shows
	if watching() {
		t.Fatalf("no window, no watching: %v", h.be.called())
	}
	h.app.Attach(nil) // the window made again, on the page it left
	h.settle()
	if !watching() {
		t.Fatalf("the window back on Connections should watch again: %v", h.be.called())
	}
	h.mustHave("active connections")
}

func TestConnectAndDisconnect(t *testing.T) {
	h := newHarness(t, harnessOpt{})
	h.mustHave("PROTECTED", "Disconnect")
	h.click("Disconnect")
	h.settle()
	if !h.be.took("disconnect") {
		t.Fatalf("calls: %v", h.be.called())
	}
	h.mustHave("NOT PROTECTED", "Connect")
	h.click("Connect")
	h.settle()
	if !h.be.took("connect ignore=false") {
		t.Fatalf("calls: %v", h.be.called())
	}
	h.mustHave("PROTECTED")
}

func TestRouteConflictAsksBeforeConnecting(t *testing.T) {
	h := newHarness(t, harnessOpt{})
	h.disconnected()
	h.be.failNext = &agentclient.OpError{Code: agentapi.ErrorCode_ERROR_CODE_ROUTE_CONFLICT, Message: "10.0.0.0/8 is owned by utun4"}
	h.click("Connect")
	h.settle()
	h.mustHave("Another VPN is in the way", "Take over and connect")
	h.click("Take over and connect")
	h.settle()
	if !h.be.took("connect ignore=true") {
		t.Fatalf("the second attempt should ignore conflicts: %v", h.be.called())
	}
}

func TestPickingANode(t *testing.T) {
	h := newHarness(t, harnessOpt{})
	h.click("Nodes")
	h.settle()
	h.mustHave("Tokyo 02", "Automatic")
	h.click("Tokyo 02")
	h.settle()
	if !h.be.took("main node:Tokyo 02") {
		t.Fatalf("calls: %v", h.be.called())
	}
	// The choice is the main exit now: the card that is lit is the node's.
	if got := h.be.Routing().Main; got.Kind != routing.DestNode || got.Node == nil || got.Node.Name != "Tokyo 02" {
		t.Fatalf("main exit = %+v", got)
	}
}

func TestNodeSearchNarrowsTheList(t *testing.T) {
	h := newHarness(t, harnessOpt{})
	h.click("Nodes")
	h.settle()
	h.mustHave("Osaka 01", "Singapore 01")
	h.click("Search nodes")
	h.typeText("osaka")
	h.settle()
	h.mustHave("Osaka 01")
	h.mustNotHave("Singapore 01", "Tokyo 02")
}

func TestAddSubscription(t *testing.T) {
	h := newHarness(t, harnessOpt{})
	h.click("Subscriptions")
	h.click("Add subscription")
	h.settle()
	h.mustHave("Add a subscription", "Subscription link")
	h.typeText("https://sub.example.invalid/abc?token=demo")
	h.enter()
	h.settle()
	if !h.be.took("add-sub") {
		t.Fatalf("calls: %v", h.be.called())
	}
	h.mustNotHave("Add a subscription")
}

func TestAddSubscriptionFailureStaysOpenAndSaysWhy(t *testing.T) {
	h := newHarness(t, harnessOpt{})
	h.click("Subscriptions")
	h.click("Add subscription")
	h.settle()
	h.be.failNext = &subscription.FetchError{Kind: subscription.FetchStatus, Status: 403}
	h.typeText("https://sub.example.invalid/abc")
	h.enter()
	h.settle()
	h.mustHave("Add a subscription")
	if len(h.ui.Texts()) == 0 {
		t.Fatal("nothing on screen")
	}
}

func TestImportNodes(t *testing.T) {
	h := newHarness(t, harnessOpt{})
	h.click("Subscriptions")
	h.click("Import nodes")
	h.settle()
	h.mustHave("Import nodes", "Share links")
}

func TestSettingsPersistChoices(t *testing.T) {
	h := newHarness(t, harnessOpt{h: 2800})
	h.click("Settings")
	h.settle()
	h.click("Light")
	h.settle()
	if got := h.be.Settings().Theme; got != "light" {
		t.Fatalf("theme = %q", got)
	}
	before := h.be.Settings()
	h.click("Notifications")
	h.click("Keep running in the tray")
	h.click("Refresh subscriptions automatically")
	h.click("Apply new nodes while connected")
	h.click("Open EggBox at login")
	h.settle()
	after := h.be.Settings()
	if after.Notifications == before.Notifications || after.CloseToTray == before.CloseToTray ||
		after.AutoRefresh == before.AutoRefresh || after.ReapplyAfterRefresh == before.ReapplyAfterRefresh {
		t.Fatalf("switches did not flip: before %+v after %+v", before, after)
	}
	if !after.LaunchAtLogin || !h.be.took("launch-at-login true") {
		t.Fatalf("login item: %+v, calls %v", after, h.be.called())
	}
}

func TestServiceSwitchesReachTheService(t *testing.T) {
	h := newHarness(t, harnessOpt{h: 2800})
	h.click("Settings")
	h.settle()
	h.click("Run the network service at startup")
	h.click("Connect automatically")
	h.click("Reconnect when the network changes")
	h.click("Warnings")
	h.settle()
	if !h.be.took("service-boot false") {
		t.Fatalf("calls: %v", h.be.called())
	}
	n := 0
	for _, c := range h.be.called() {
		if c == "agent-settings" {
			n++
		}
	}
	if n != 3 {
		t.Fatalf("expected three service setting changes, got %d: %v", n, h.be.called())
	}
}

func TestServiceManagedBySystemSettingsOpensThem(t *testing.T) {
	h := newHarness(t, harnessOpt{h: 2800, platform: "darwin", setup: func(be *fixtureBackend, o *Options) {
		o.ServiceStartup = nil // macOS keeps this switch in System Settings
		o.OpenServiceSettings = func() { be.record("open-service-settings") }
	}})
	h.click("Settings")
	h.settle()
	h.mustHave("Run the network service at startup", "Login Items")
	h.click("Open Login Items")
	h.settle()
	if !h.be.took("open-service-settings") {
		t.Fatalf("calls: %v", h.be.called())
	}
}

func TestNoManagedServiceRowWhereTheSwitchIsOurs(t *testing.T) {
	h := newHarness(t, harnessOpt{h: 2800, setup: func(be *fixtureBackend, o *Options) {
		o.OpenServiceSettings = func() { be.record("open-service-settings") }
	}})
	h.click("Settings")
	h.settle()
	h.mustNotHave("Open Login Items")
}

func TestNoticesAreOfferedOnlyWhereTheBuildCarriesThem(t *testing.T) {
	h := newHarness(t, harnessOpt{h: 2800, setup: func(be *fixtureBackend, o *Options) {
		o.OpenNotices = func() { be.record("open-notices") }
	}})
	h.click("Settings")
	h.settle()
	h.mustHave("Open-source licenses")
	h.click("View licenses")
	h.settle()
	if !h.be.took("open-notices") {
		t.Fatalf("the licenses were not opened: %v", h.be.called())
	}

	// A development build or a demonstration has none to show.
	h = newHarness(t, harnessOpt{h: 2800})
	h.click("Settings")
	h.settle()
	h.mustNotHave("Open-source licenses")
}

func TestRemovingTheServiceAsksFirstAndDisconnectsBeforeRemoving(t *testing.T) {
	h := newHarness(t, harnessOpt{h: 2800, platform: "darwin", setup: func(be *fixtureBackend, o *Options) {
		o.RemoveService = func(context.Context) error { be.record("remove-service"); return nil }
	}})
	h.click("Settings")
	h.settle()
	h.mustHave("Remove the service")
	h.click("Remove…")
	h.settle()
	h.mustHave("Remove the network service?")
	if h.be.took("remove-service") {
		t.Fatalf("the service was removed before the person confirmed: %v", h.be.called())
	}
	h.click("Remove service")
	h.settle()
	calls := h.be.called()
	di, ri := slices.Index(calls, "disconnect"), slices.Index(calls, "remove-service")
	if di < 0 || ri < 0 || di > ri {
		t.Fatalf("the connection must be closed before the service is removed: %v", calls)
	}
}

func TestRemovingTheServiceCancelledChangesNothing(t *testing.T) {
	h := newHarness(t, harnessOpt{h: 2800, platform: "darwin", setup: func(be *fixtureBackend, o *Options) {
		o.RemoveService = func(context.Context) error { be.record("remove-service"); return nil }
	}})
	h.click("Settings")
	h.settle()
	h.click("Remove…")
	h.settle()
	h.click("Cancel")
	h.settle()
	if calls := h.be.called(); slices.Contains(calls, "remove-service") || slices.Contains(calls, "disconnect") {
		t.Fatalf("cancelling must change nothing: %v", calls)
	}
}

func TestNoRemoveActionWhereTheServiceCannotBeRemovedOrIsNotThere(t *testing.T) {
	// The platform has no such step: nothing is offered.
	h := newHarness(t, harnessOpt{h: 2800})
	h.click("Settings")
	h.settle()
	h.mustNotHave("Remove the service")

	// The service is not allowed yet: nothing to remove.
	h = newHarness(t, harnessOpt{h: 2800, setup: func(be *fixtureBackend, o *Options) {
		noService(be, o)
		o.RemoveService = func(context.Context) error { return nil }
	}})
	h.click("Settings")
	h.settle()
	h.mustNotHave("Remove the service")
}

func TestNetworkEditsApplyTogether(t *testing.T) {
	h := newHarness(t, harnessOpt{h: 2800})
	h.click("Settings")
	h.settle()
	h.mustNotHave("Apply changes")
	h.click("1500")
	h.ui.Key(ui.Cmd, ui.KeyA)
	h.typeText("1400")
	h.settle()
	h.mustHave("Apply changes", "Discard")
	h.click("Apply changes")
	h.settle()
	if got := h.be.Prefs().MTU; got != 1400 {
		t.Fatalf("MTU = %d; calls %v", got, h.be.called())
	}
	h.mustNotHave("Apply changes")
}

func TestNetworkEditsAreValidated(t *testing.T) {
	h := newHarness(t, harnessOpt{h: 2800})
	h.click("Settings")
	h.settle()
	h.click("1500")
	h.ui.Key(ui.Cmd, ui.KeyA)
	h.typeText("abc")
	h.settle()
	h.mustHave("The MTU must be a number.")
	h.click("Discard")
	h.settle()
	h.mustNotHave("The MTU must be a number.")
}

func TestExportDiagnostics(t *testing.T) {
	h := newHarness(t, harnessOpt{h: 2800})
	h.click("Settings")
	h.settle()
	h.click("Export report")
	h.settle()
	if !h.be.took("diagnostics") || !h.be.took("save-file egg-box-diagnostics.json") {
		t.Fatalf("calls: %v", h.be.called())
	}
}

func TestSwitchingLanguageRetranslates(t *testing.T) {
	h := newHarness(t, harnessOpt{})
	h.click("Settings")
	h.settle()
	h.click("简体中文")
	h.settle()
	h.mustHave("设置", "外观", "语言")
	h.mustNotHave("Appearance")
	h.click("English")
	h.settle()
	h.mustHave("Appearance")
}

func TestLogsFilterAndClear(t *testing.T) {
	h := newHarness(t, harnessOpt{})
	h.click("Logs")
	h.settle()
	h.mustHave("dial udp: i/o timeout", "selected Tokyo 01 (58 ms)")
	h.click("Errors")
	h.settle()
	h.mustHave("dial udp: i/o timeout")
	h.mustNotHave("selected Tokyo 01 (58 ms)")
	h.click("All")
	h.settle()
	h.click("Clear")
	h.settle()
	h.mustHave("Nothing logged")
}

func TestConnectionsFilterAndClose(t *testing.T) {
	h := newHarness(t, harnessOpt{})
	h.click("Connections")
	h.settle()
	h.mustHave("github.com", "api.openai.com")
	h.mustNotHave("store.steampowered.com")
	h.click("Closed")
	h.settle()
	h.mustHave("store.steampowered.com", "ads-tracker.example")
	h.mustNotHave("api.openai.com")
	h.click("All")
	h.settle()
	h.mustHave("github.com", "api.openai.com")
	h.click("Close all")
	h.settle()
	h.mustHave("Close every connection?")
	// The dialog's button bears the name of the page's, which it covers.
	h.submitDialog("Close all")
	if !h.be.took("close-all") {
		t.Fatalf("confirming did not close the connections: %v", h.be.called())
	}
	h.mustNotHave("Close every connection?")
}

func TestIntroductionIsShownOnce(t *testing.T) {
	h := newHarness(t, harnessOpt{setup: introduction})
	h.mustHave("Welcome to EggBox")
	h.click("Continue")
	h.mustHave("Allow the network service", "Authorize service")
	h.click("Not now")
	h.mustHave("Bring your nodes")
	h.click("Not now")
	h.settle()
	h.mustNotHave("Bring your nodes")
	if !h.be.Settings().OnboardingDone {
		t.Fatal("finishing the introduction should be remembered")
	}
	// A second window of the same settings does not repeat it.
	h2 := newHarness(t, harnessOpt{setup: func(be *fixtureBackend, o *Options) { be.set.OnboardingDone = true }})
	h2.mustNotHave("Welcome to EggBox")
}

func TestWelcomeAuthorizationFailureCanOpenLogs(t *testing.T) {
	calls := 0
	h := newHarness(t, harnessOpt{setup: func(be *fixtureBackend, o *Options) {
		introduction(be, o)
		be.snap.Logs = nil
		o.Authorize = func(context.Context) error {
			calls++
			return fmt.Errorf("%w: registration refused (code 42)", app.ErrServiceSetup)
		}
	}})
	h.click("Continue")
	// The dashboard behind the modal has the same button label. Click the
	// footer button beside Not now, inside the modal's input scope.
	skip, ok := h.ui.Find("Not now")
	if !ok {
		t.Fatal("missing welcome footer")
	}
	h.ui.ClickAt(skip.X+skip.W+80, skip.Y+skip.H/2)
	h.settle()
	if calls != 1 {
		t.Fatalf("authorization requests = %d", calls)
	}
	h.mustHave("Open Logs for the system's error details.")
	toast := h.app.toasts[len(h.app.toasts)-1]
	if toast.action != "Logs" || toast.do == nil {
		t.Fatal("the setup failure needs an action to open Logs")
	}
	// The error remains in the welcome panel, whose footer offers Logs
	// even while the modal prevents interaction with the sidebar.
	back, ok := h.ui.Find("Back")
	if !ok {
		t.Fatal("missing welcome footer after failure")
	}
	h.ui.ClickAt(back.X+back.W+48, back.Y+back.H/2)
	h.settle()
	h.mustNotHave("Allow the network service")
	h.mustHave("Service setup, connection and network engine logs")
}

func TestDashboardAuthorizationChecksTheService(t *testing.T) {
	calls := 0
	h := newHarness(t, harnessOpt{setup: func(be *fixtureBackend, o *Options) {
		introduction(be, o)
		be.set.OnboardingDone = true
		o.Authorize = func(context.Context) error {
			calls++
			be.set_(be.disconnectedSnapshot())
			return nil
		}
	}})
	h.click("Authorize service")
	h.settle()
	if calls != 1 {
		t.Fatalf("authorization requests = %d", calls)
	}
	h.mustHave("The network service is ready.")
	h.mustNotHave("Service unavailable")
}

// A key that leaked to the screen is a missing translation.
var leakedKey = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)+$`)

// keyRoots are the first segments of the catalog's keys.
func keyRoots() map[string]bool {
	roots := map[string]bool{}
	for k := range catalogEN {
		roots[strings.SplitN(k, ".", 2)[0]] = true
	}
	return roots
}

func TestNoRawKeysOnAnyPage(t *testing.T) {
	for _, lang := range []string{"en", "zh"} {
		h := newHarness(t, harnessOpt{lang: lang})
		for _, nav := range []string{"nav.dashboard", "nav.nodes", "nav.subscriptions", "nav.routing", "nav.connections", "nav.logs", "nav.settings"} {
			h.app.lang = lang
			h.click(h.app.t(nav))
			h.settle()
			for _, s := range h.ui.Texts() {
				if leakedKey.MatchString(strings.TrimSpace(s)) && keyRoots()[strings.SplitN(s, ".", 2)[0]] {
					t.Errorf("[%s] %s shows a raw key %q", lang, nav, s)
				}
			}
		}
	}
}

func TestSmallAndLargeWindows(t *testing.T) {
	for _, sz := range []struct{ w, h int }{{900, 620}, {1920, 1080}, {1280, 720}} {
		h := newHarness(t, harnessOpt{w: sz.w, h: sz.h})
		for _, nav := range []string{"Nodes", "Subscriptions", "Routing", "Connections", "Logs", "Settings", "Dashboard"} {
			h.click(nav)
			h.settle()
		}
	}
}

func TestWindowsFlavour(t *testing.T) {
	h := newHarness(t, harnessOpt{platform: "windows"})
	h.click("Settings")
	h.settle()
	h.mustHave("Windows service starts together with Windows")
}
