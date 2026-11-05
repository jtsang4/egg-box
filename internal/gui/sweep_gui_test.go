package gui

import (
	"regexp"
	"strings"
	"testing"

	"eggbox/internal/catalog"
	"eggbox/internal/library"
	"eggbox/internal/routing"
)

// A placeholder that was not filled in.
var unfilled = regexp.MustCompile(`\{[a-z_]+\}`)

// leaks fails for every label on screen that is a catalog key or has a
// placeholder left in it: a missing translation or a forgotten argument.
func (h *harness) leaks(where string) {
	h.t.Helper()
	roots := keyRoots()
	for _, s := range h.ui.Texts() {
		s = strings.TrimSpace(s)
		switch {
		case leakedKey.MatchString(s) && roots[strings.SplitN(s, ".", 2)[0]]:
			h.t.Errorf("%s shows the raw key %q", where, s)
		case unfilled.MatchString(s):
			h.t.Errorf("%s shows a placeholder that was not filled in: %q", where, s)
		}
	}
}

// hasCJK says whether any label on screen has Chinese characters.
func (h *harness) hasCJK() bool {
	for _, s := range h.ui.Texts() {
		for _, r := range s {
			if r >= 0x4e00 && r <= 0x9fff {
				return true
			}
		}
	}
	return false
}

// shut closes whatever dialog is open.
func (h *harness) shut() {
	h.t.Helper()
	a := h.app
	a.dlg, a.dlgStack, a.dlgOpen = nil, nil, false
	h.settle()
}

// crowd brings every state the routing screens can be in to the fixture: items
// that wait for their rules, whose update failed or that only partly work, one
// that is off, ones that go to a region, a node that is gone and a node that does
// not answer, a hand-written item, and an item the plan lets the person
// change.
func crowd(be *fixtureBackend, _ *Options) {
	rows := be.Routing().View().Rows
	at := func(id string) routing.Item {
		for _, r := range rows {
			if r.Item.ID == id {
				return r.Item
			}
		}
		panic("no item " + id)
	}
	be.missing[at("b-github").Sources[0].URL] = true // nothing yet: the item waits
	be.missing[at("b-cn").Sources[0].URL] = true     // one of two files: the item works in part
	be.failing[at("b-ai").Sources[0].URL] = true
	be.partial[at("b-telegram").Sources[0].URL] = true
	seoul, _ := be.node("Seoul 01")
	osaka, _ := be.node("Osaka 01")
	be.arrange(func(st *routing.State) {
		must := func(err error) {
			if err != nil {
				panic(err)
			}
		}
		must(st.SetMain(routing.ToNode(osaka.ID())))
		must(st.SetItemDest("b-youtube", routing.ToRegion("JP")))
		must(st.SetItemDest("b-streaming", routing.ToNode(seoul.ID())))
		must(st.SetItemDest("b-github", routing.ToNode(osaka.ID())))
		must(st.SetItemEnabled("b-social", false))
		must(st.SetItemDest("b-apple", routing.Reject()))
		must(st.SetFallback(routing.ToRegion("SG")))
		_, err := st.AddItem(routing.Item{
			Name: "Handmade", Icon: "globe", Dest: routing.Direct(), Enabled: true,
			Sources: []routing.Source{routing.InlineSource([]routing.Entry{
				{Kind: routing.EntryDomainSuffix, Value: "example.org"},
				{Kind: routing.EntryProcessName, Value: "Updater"},
			})},
		}, "")
		must(err)
	})
	// The node chosen for the streaming item is gone from every subscription,
	// and another does not answer.
	be.down["Osaka 01"] = true
	var keep []library.ActiveNode
	for _, n := range be.nodes {
		if n.Node.Name != "Seoul 01" {
			keep = append(keep, n)
		}
	}
	be.nodes = keep
	be.refresh()
}

func TestNoRawKeysOrPlaceholdersOnTheScreensOfACrowdedFixture(t *testing.T) {
	for _, lang := range []string{"en", "zh"} {
		h := newHarness(t, harnessOpt{lang: lang, h: 1800, setup: crowd})
		for _, nav := range []string{"nav.dashboard", "nav.nodes", "nav.subscriptions", "nav.routing", "nav.connections", "nav.logs", "nav.settings"} {
			h.click(h.app.t(nav))
			h.settle()
			h.leaks(lang + " " + nav)
		}
		// The routing page's menu of the plan.
		h.click(h.app.t("nav.routing"))
		h.settle()
		h.click(h.app.t("subs.more"))
		h.leaks(lang + " plan menu")
		h.closeMenu()
	}
}

func TestNoRawKeysOrPlaceholdersInTheTrayPanel(t *testing.T) {
	for _, lang := range []string{"en", "zh"} {
		for _, setup := range []func(*fixtureBackend, *Options){nil, crowd} {
			h := newHarness(t, harnessOpt{lang: lang, panel: true, setup: setup})
			h.leaks(lang + " tray panel")
		}
	}
}

func TestNoRawKeysOrPlaceholdersInDialogs(t *testing.T) {
	for _, lang := range []string{"en", "zh"} {
		h := newHarness(t, harnessOpt{lang: lang, h: 1800, setup: crowd})
		h.click(h.app.t("nav.routing"))
		h.settle()
		a := h.app
		ex := h.state().Exceptions[0]
		subs := h.be.Subscriptions()
		first := h.state().ActivePlan().Items[0]
		nodes := a.liveNodes()

		write := newAddDialog("")
		write.tab = 3
		link := newAddDialog("")
		link.tab = 1
		plans := newAddDialog("")
		plans.tab = 2
		confirm := newAddDialog("")
		if es := catalog.Entries(); len(es) > 0 {
			confirm.pickEntry(a, es[len(es)-1])
		}
		more := newAddDialog(first.ID)

		cases := []struct {
			name string
			open func()
		}{
			{"add exception", func() { a.openException(nil) }},
			{"edit exception", func() { a.openException(&ex) }},
			{"exception for a connection", func() {
				a.openExceptionFor(routing.Entry{Kind: routing.EntryDomainSuffix, Value: "npmjs.org"}, routing.Direct())
			}},
			{"batch, empty", func() { a.openBatchException("") }},
			{"batch, pasted", func() { a.openBatchException(pastedList) }},
			{"test an address", func() { a.openTestURL("") }},
			{"test an address, with one", func() { a.openTestURL("https://registry.npmjs.org/left-pad") }},
			{"add item, catalog", func() { a.openAddItem() }},
			{"add item, link", func() { a.openDialog(link) }},
			{"add item, plans", func() { a.openDialog(plans) }},
			{"add item, write", func() { a.openDialog(write) }},
			{"add item, confirm", func() { a.openDialog(confirm) }},
			{"add source", func() { a.openDialog(more) }},
			{"item of a plan", func() { a.openItem("b-ai") }},
			{"item waiting for its rules", func() { a.openItem("b-ads") }},
			{"item whose update failed", func() { a.openItem("b-telegram") }},
			{"item that is off", func() { a.openItem("b-social") }},
			{"item of one's own", func() { a.openItem(h.lastItem().ID) }},
			{"save the plan", func() { a.openPlanName(false) }},
			{"rename the plan", func() { a.openPlanName(true) }},
			{"choose a node", func() { a.pickNodeFor("x", routing.Follow(), true, func(routing.Dest) {}) }},
			{"choose a region", func() { a.openRegionPicker(nodes[0]) }},
			{"add a subscription", func() { a.openAddSubscription() }},
			{"edit a subscription", func() { a.openEditSubscription(subs[0]) }},
			{"import nodes", func() { a.openImport() }},
			{"welcome", func() { a.openWelcome() }},
		}
		for _, tc := range cases {
			tc.open()
			h.settle()
			if !a.dlgOpen {
				t.Errorf("%s: the dialog %q did not open", lang, tc.name)
			}
			h.leaks(lang + " dialog: " + tc.name)
			if lang == "zh" && !h.hasCJK() {
				t.Errorf("zh: the dialog %q has no Chinese on it", tc.name)
			}
			h.shut()
		}
	}
}
