package gui

import (
	"strings"
	"testing"

	"eggbox/internal/catalog"
	"eggbox/internal/routing"
)

// The routing screens and dialogs, photographed in both languages: reached by
// opening the dialog the way the page does, so that the same steps serve both.
func init() {
	page := func(key string) func(h *harness) {
		return func(h *harness) {
			h.click(h.app.t(key))
			h.settle()
		}
	}
	onRouting := func(open func(h *harness)) func(h *harness) {
		return func(h *harness) {
			h.click(h.app.t("nav.routing"))
			h.settle()
			open(h)
			h.settle()
		}
	}
	for _, lang := range []string{"", "zh"} {
		prefix := ""
		if lang == "zh" {
			prefix = "zh-"
		}
		add := func(s shot) {
			s.name, s.lang = prefix+s.name, lang
			shots = append(shots, s)
		}
		add(shot{name: "dashboard-crowded", setup: crowd})
		add(shot{name: "routing-crowded", h: 1900, setup: crowd, run: page("nav.routing")})
		add(shot{name: "routing-plan-menu", run: onRouting(func(h *harness) { h.click(h.app.t("subs.more")) })})
		add(shot{name: "routing-plan-switch", run: onRouting(func(h *harness) { h.click(h.app.t("plan.builtin")) })})
		add(shot{name: "dlg-plan-new", run: onRouting(func(h *harness) { h.app.openNewPlan("") })})
		add(shot{name: "dlg-plan-empty", run: onRouting(func(h *harness) { h.app.openNewPlan(""); h.settle(); h.click(h.app.t("plan.new.empty")) })})
		add(shot{name: "nodes-crowded", h: 1100, setup: crowd, run: page("nav.nodes")})
		add(shot{name: "dlg-exception-add", run: onRouting(func(h *harness) { h.app.openException(nil) })})
		add(shot{name: "dlg-exception-from-connection", run: onRouting(func(h *harness) {
			h.app.openExceptionFor(routing.Entry{Kind: routing.EntryDomainSuffix, Value: "npmjs.org"}, routing.Direct())
		})})
		add(shot{name: "dlg-exceptions-text", setup: nodesOfTheSameName, run: onRouting(func(h *harness) { h.app.openExceptionsEditor() })})
		add(shot{name: "dlg-exceptions-text-edited", setup: streamingInSingapore, run: onRouting(func(h *harness) {
			openEditorWith(h.app, "DOMAIN-SUFFIX,intranet.example,DIRECT\nDOMAIN-KEYWORD,ads-tracker,Streaming\nGEOIP,CN,DIRECT\nPROCESS-NAME,Updater,Somewhere\nDOMAIN-SUFFIX,new.example,REGION:JP\n# DOMAIN,off.example,REJECT\n")
		})})
		add(shot{name: "dlg-exceptions-text-dns", run: onRouting(func(h *harness) {
			openEditorWith(h.app, "DOMAIN-SUFFIX,intranet.example,DIRECT,dns=10.0.0.53\nDOMAIN-KEYWORD,ads-tracker,REJECT\nPROCESS-NAME,Slack,dns=exit\nDOMAIN-SUFFIX,corp.example,dns=exit\n")
		})})
		add(shot{name: "dlg-test-url", run: onRouting(func(h *harness) { h.app.openTestURL("https://registry.npmjs.org/left-pad") })})
		add(shot{name: "dlg-test-url-refused", run: onRouting(func(h *harness) { h.app.openTestURL("gone.nowhere.example") })})
		add(shot{name: "dlg-additem-catalog", h: 900, run: onRouting(func(h *harness) { h.app.openAddItem() })})
		add(shot{name: "dlg-additem-link", run: onRouting(func(h *harness) {
			d := newAddDialog("")
			d.tab = tabLink
			h.app.openDialog(d)
		})})
		add(shot{name: "dlg-additem-write", run: onRouting(func(h *harness) {
			d := newAddDialog("")
			d.tab = 3
			h.app.openDialog(d)
		})})
		add(shot{name: "dlg-additem-confirm", h: 900, run: onRouting(func(h *harness) {
			d := newAddDialog("")
			es := catalog.Entries()
			d.pickEntry(h.app, es[len(es)-1])
			h.app.openDialog(d)
		})})
		add(shot{name: "dlg-item", h: 900, run: onRouting(func(h *harness) { h.app.openItem("b-ai") })})
		add(shot{name: "dlg-item-waiting", h: 900, setup: crowd, run: onRouting(func(h *harness) { h.app.openItem("b-ads") })})
		add(shot{name: "dlg-item-own", h: 900, setup: crowd, run: onRouting(func(h *harness) { h.app.openItem(h.lastItem().ID) })})
		add(shot{name: "dlg-item-intranet", h: 900, setup: nodeIntranet, run: onRouting(func(h *harness) { h.app.openItem(h.lastItem().ID) })})
		add(shot{name: "dlg-item-dns-server", h: 900, setup: internalDNS, run: onRouting(func(h *harness) { h.app.openItem("b-ai") })})
		add(shot{name: "dlg-node-picker", h: 900, run: onRouting(func(h *harness) {
			h.app.pickNodeFor("Telegram", routing.Follow(), true, func(routing.Dest) {})
		})})
		add(shot{name: "dlg-region-picker", run: onRouting(func(h *harness) { h.app.openRegionPicker(h.app.liveNodes()[0]) })})
		add(shot{name: "dlg-node-link", run: onRouting(func(h *harness) { h.app.openImportedNode("sub-local", "My nodes", h.nodeNamed("Sydney 01").Key) })})
		add(shot{name: "dlg-node-link-none", run: onRouting(func(h *harness) { h.app.openImportedNode("sub-local", "My nodes", h.nodeNamed("Amsterdam 01").Key) })})
	}
}

// The routing screens where the window is small, large and set the way
// Windows sets it (its buttons sit at the right of the title bar, where the page
// keeps its own buttons).
func init() {
	onRouting := func(open func(h *harness)) func(h *harness) {
		return func(h *harness) {
			h.click(h.app.t("nav.routing"))
			h.settle()
			if open != nil {
				open(h)
				h.settle()
			}
		}
	}
	for _, lang := range []string{"", "zh"} {
		prefix := ""
		if lang == "zh" {
			prefix = "zh-"
		}
		add := func(s shot) {
			s.name, s.lang = prefix+s.name, lang
			shots = append(shots, s)
		}
		add(shot{name: "windows-routing", platform: "windows", h: 1300, setup: crowd, run: onRouting(nil)})
		add(shot{name: "windows-nodes", platform: "windows", h: 900, setup: crowd, run: func(h *harness) { h.click(h.app.t("nav.nodes")); h.settle() }})
		add(shot{name: "windows-dlg-additem", platform: "windows", h: 900, run: onRouting(func(h *harness) { h.app.openAddItem() })})
		add(shot{name: "windows-dlg-test-url", platform: "windows", run: onRouting(func(h *harness) { h.app.openTestURL("https://registry.npmjs.org/left-pad") })})
		add(shot{name: "small-routing", w: 960, h: 640, setup: crowd, run: onRouting(nil)})
		add(shot{name: "small-nodes", w: 960, h: 640, setup: crowd, run: func(h *harness) { h.click(h.app.t("nav.nodes")); h.settle() }})
		// Every dialog that is tall enough to matter, in the smallest window the
		// application allows.
		for _, name := range []string{
			"exceptions as text, pasted", "add item, catalog", "add item, confirm", "add item, write", "choose a node",
			"add a subscription", "item of one's own", "item whose update failed", "test an address, with one", "welcome",
		} {
			name := name
			add(shot{name: "small-dlg-" + strings.NewReplacer(" ", "-", ",", "", "'", "").Replace(name), w: 960, h: 640, setup: crowd,
				run: onRouting(func(h *harness) {
					for _, tc := range h.dialogCases() {
						if tc.name == name {
							tc.open()
							return
						}
					}
					panic("no dialog " + name)
				})})
		}
		add(shot{name: "large-routing", w: 1920, h: 1080, setup: crowd, run: onRouting(nil)})
	}
}

// TestPanelShotsOfACrowdedPlan photographs the tray panel with the quick switch
// of a plan that has every kind of destination, in both languages.
func TestPanelShotsOfACrowdedPlan(t *testing.T) {
	if testing.Short() {
		t.Skip("the full-resolution screenshot job covers the panel matrix")
	}
	for _, lang := range []string{"en", "zh"} {
		for _, dark := range []bool{true, false} {
			name := "panel-crowded-" + lang + "-light"
			if dark {
				name = "panel-crowded-" + lang + "-dark"
			}
			t.Run(name, func(t *testing.T) {
				h := newHarness(t, harnessOpt{dark: dark, panel: true, motion: true, lang: lang, setup: crowd})
				h.shot(name)
			})
		}
	}
}
