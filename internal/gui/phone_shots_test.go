package gui

import (
	"testing"
)

// phoneShots are the screens of a phone, reached by tapping as a person would.
var phoneShots = []shot{
	{name: "phone-home-empty", setup: emptyLibrary},
	{name: "phone-home"},
	{name: "phone-home-failed", setup: failedConnection},
	{name: "phone-welcome", setup: introduction},
	{name: "phone-nodes", run: tapTab("tab.nodes")},
	{name: "phone-subscriptions", run: func(h *harness) { tapTab("tab.nodes")(h); h.click(translate(h.app.lang, "nav.subscriptions")) }},
	{name: "phone-routing", run: tapTab("tab.routing")},
	{name: "phone-connections", run: tapTab("tab.activity")},
	{name: "phone-logs", run: func(h *harness) { tapTab("tab.activity")(h); h.click(translate(h.app.lang, "nav.logs")) }},
	{name: "phone-settings", h: 2400, run: tapTab("tab.settings")},
	{name: "phone-add-subscription", run: func(h *harness) {
		tapTab("tab.nodes")(h)
		h.click(translate(h.app.lang, "nav.subscriptions"))
		h.app.openAddSubscription()
	}},
	{name: "phone-import", run: func(h *harness) { h.app.openImport() }},
}

// tapTab taps a tab of the bar.
func tapTab(key string) func(h *harness) {
	return func(h *harness) { h.click(translate(h.app.lang, key)) }
}

// TestPhoneShots renders the phone's screens in both themes and languages;
// with EGGBOX_SHOTS set the images are written for review.
func TestPhoneShots(t *testing.T) {
	if testing.Short() {
		t.Skip("renders every phone screen in both themes")
	}
	for _, s := range phoneShots {
		for _, lang := range []string{"en", "zh"} {
			for _, dark := range []bool{false, true} {
				name := s.name + "-" + lang + map[bool]string{false: "-light", true: "-dark"}[dark]
				t.Run(name, func(t *testing.T) {
					h := newHarness(t, harnessOpt{phone: true, dark: dark, w: s.w, h: s.h, lang: lang, motion: true, setup: s.setup})
					if s.run != nil {
						s.run(h)
						h.ui.Frame()
					}
					h.shot(name)
				})
			}
		}
	}
}
