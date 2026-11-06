package gui

import (
	"strings"
	"testing"

	"eggbox/internal/sysproxy"
)

// proxyLeftBehind is the state after another VPN application was turned off
// and left its system proxy in place, while a second, idle service points at
// a port another program listens on.
func proxyLeftBehind(be *fixtureBackend, o *Options) {
	be.snap.ProxyIssues = []sysproxy.Issue{
		{Host: "127.0.0.1", Port: 3067, State: sysproxy.Dead, Active: true, Settings: []sysproxy.Setting{
			{Service: "Wi-Fi", Active: true, Kind: sysproxy.KindHTTP, Host: "127.0.0.1", Port: 3067},
			{Service: "Wi-Fi", Active: true, Kind: sysproxy.KindSOCKS, Host: "127.0.0.1", Port: 3067},
			{Service: "USB LAN", Kind: sysproxy.KindHTTP, Host: "127.0.0.1", Port: 3067},
		}},
		{Host: "127.0.0.1", Port: 7890, State: sysproxy.Foreign, Settings: []sysproxy.Setting{
			{Service: "Thunderbolt Bridge", Kind: sysproxy.KindHTTP, Host: "127.0.0.1", Port: 7890},
		}},
	}
	o.OpenProxySettings = func() { be.record("open-proxy-settings") }
}

func TestProxyLeftBehindIsShownWithItsFix(t *testing.T) {
	h := newHarness(t, harnessOpt{setup: proxyLeftBehind})
	h.mustHave("The system proxy for Wi-Fi, USB LAN is 127.0.0.1:3067, which is no longer available — usually left behind by a proxy app that has quit. Browsers and other apps that use the system proxy can’t go online until it’s turned off.")
	// Another program behind a service that carries nothing now is no news.
	h.mustNotHave("127.0.0.1:7890")

	h.click("Copy fix commands")
	want := "networksetup -setwebproxystate 'Wi-Fi' off\n" +
		"networksetup -setsocksfirewallproxystate 'Wi-Fi' off\n" +
		"networksetup -setwebproxystate 'USB LAN' off\n"
	if got := h.ui.Clipboard(); got != want {
		t.Fatalf("clipboard:\n%s\nwant:\n%s", got, want)
	}
	h.mustHave("Commands copied. Paste them into Terminal to turn these system proxies off.")

	h.click("Open Network Settings")
	if !h.be.took("open-proxy-settings") {
		t.Fatal("the network settings were not opened")
	}

	h.click("Got it")
	h.mustNotHave("127.0.0.1:3067")
}

func TestProxyBannerOnlyWhileConnectedAndWhereItCanHelp(t *testing.T) {
	h := newHarness(t, harnessOpt{setup: func(be *fixtureBackend, o *Options) {
		proxyLeftBehind(be, o)
		issues := be.snap.ProxyIssues
		be.snap = be.disconnectedSnapshot()
		be.snap.ProxyIssues = issues
	}})
	h.mustNotHave("127.0.0.1:3067")

	// Windows has no commands to copy, and without a settings page there is
	// no button for it.
	h = newHarness(t, harnessOpt{platform: "windows", setup: func(be *fixtureBackend, o *Options) {
		proxyLeftBehind(be, o)
		o.OpenProxySettings = nil
	}})
	h.mustHave("Got it")
	h.mustNotHave("Copy fix commands", "Open Network Settings")
}

func TestProxyBannerInChineseAndForAnotherProgram(t *testing.T) {
	h := newHarness(t, harnessOpt{lang: "zh", setup: func(be *fixtureBackend, o *Options) {
		proxyLeftBehind(be, o)
		be.snap.ProxyIssues[1].Active = true
		be.snap.ProxyIssues[1].Settings[0].Active = true
	}})
	h.mustHave(
		"Wi-Fi、USB LAN 设置的系统代理 127.0.0.1:3067 已经不可用，通常是之前用过的代理软件退出后留下的。在关闭它之前，浏览器等使用系统代理的应用都无法上网。",
		"Thunderbolt Bridge 设置的系统代理 127.0.0.1:7890 由另一个软件提供。浏览器等使用系统代理的应用会先经过那个软件，访问效果可能与 EggBox 的分流规则不一致。",
	)
	// The copy button belongs to the dead proxy only.
	if n := strings.Count(h.digest(), "复制修复命令"); n != 1 {
		t.Fatalf("copy buttons: %d", n)
	}
}
