package gui

import (
	"testing"

	"eggbox/internal/app"
)

func TestAppName(t *testing.T) {
	for in, want := range map[string]string{
		"/Applications/Safari.app/Contents/MacOS/Safari": "Safari",
		"/Applications/Google Chrome.app/Contents/Frameworks/Google Chrome Framework.framework/Helpers/Google Chrome Helper.app/Contents/MacOS/Google Chrome Helper": "Google Chrome",
		"/Applications/Visual Studio Code.app/Contents/Frameworks/Code Helper.app/Contents/MacOS/Code Helper":                                                        "Visual Studio Code",
		"/usr/bin/curl":                    "curl",
		`C:\Program Files\Slack\slack.exe`: "slack",
		`C:\Windows\System32\SVCHOST.EXE`:  "SVCHOST",
		"node":                             "node",
		"/":                                "/",
		"":                                 "",
	} {
		if got := appName(in); got != want {
			t.Errorf("appName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestWhyLabel: a connection says why it went where it did in the words of the
// routing page (an item's name, an exception's number), never the core's own
// rule text.
func TestWhyLabel(t *testing.T) {
	h := newHarness(t, harnessOpt{})
	a := h.app
	cases := []struct {
		why  app.Why
		want string
	}{
		{app.Why{Kind: app.WhyItem, ID: "b-ai", Name: "AI services"}, "AI services"},
		// The item was deleted after the connection began: its name stays.
		{app.Why{Kind: app.WhyItem, ID: "gone", Name: "Old name"}, "Old name"},
		{app.Why{Kind: app.WhyException, Index: 3, Name: "Slack"}, "Exception #3"},
		{app.Why{Kind: app.WhyFallback}, "Everything else"},
		{app.Why{Kind: app.WhyLAN}, "Local network"},
		{app.Why{Kind: app.WhyMode}, "Routing mode"},
		{app.Why{}, ""},
	}
	for _, c := range cases {
		if got := a.whyLabel(c.why); got != c.want {
			t.Errorf("whyLabel(%+v) = %q, want %q", c.why, got, c.want)
		}
	}
	a.lang = "zh"
	if got := a.whyLabel(app.Why{Kind: app.WhyException, Index: 2}); got != "例外 #2" {
		t.Errorf("zh exception = %q", got)
	}
	if got := a.whyLabel(app.Why{Kind: app.WhyFallback}); got != "其他所有流量" {
		t.Errorf("zh fallback = %q", got)
	}
}

// TestExitLabel: where a connection left is a node's name, "Direct" or "Block",
// and a node with the name of one in another subscription says which it is.
func TestExitLabel(t *testing.T) {
	h := newHarness(t, harnessOpt{})
	a := h.app
	for _, c := range []struct {
		cn   app.Conn
		want string
	}{
		{app.Conn{Outbound: "direct", Chain: []string{"direct"}}, "Direct"},
		{app.Conn{Outbound: "reject", Chain: []string{"reject"}}, "Block"},
		{app.Conn{Outbound: "Tokyo 01", Chain: []string{"Tokyo 01", "Auto", "Proxy"}}, "Tokyo 01"},
		{app.Conn{}, ""},
		// A tag nobody knows is shown as it is.
		{app.Conn{Outbound: "mystery"}, "mystery"},
	} {
		if got := a.exitLabel(c.cn); got != c.want {
			t.Errorf("exitLabel(%+v) = %q, want %q", c.cn, got, c.want)
		}
	}
}
