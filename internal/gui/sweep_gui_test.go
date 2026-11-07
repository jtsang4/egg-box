package gui

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

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

// dialogCase is a dialog and the way to open it.
type dialogCase struct {
	name string
	open func()
}

// dialogCases are all the dialogs of the application, each in a state where it
// has something to show.
func (h *harness) dialogCases() []dialogCase {
	h.t.Helper()
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

	return []dialogCase{
		{"add exception", func() { a.openException(nil) }},
		{"edit exception", func() { a.openException(&ex) }},
		{"exception for a connection", func() {
			a.openExceptionFor(routing.Entry{Kind: routing.EntryDomainSuffix, Value: "npmjs.org"}, routing.Direct())
		}},
		{"exceptions as text", func() { a.openExceptionsEditor() }},
		{"exceptions as text, pasted", func() { openEditorWith(a, pastedList) }},
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
}

func TestNoRawKeysOrPlaceholdersInDialogs(t *testing.T) {
	for _, lang := range []string{"en", "zh"} {
		h := newHarness(t, harnessOpt{lang: lang, h: 1800, setup: crowd})
		h.click(h.app.t("nav.routing"))
		h.settle()
		a := h.app
		for _, tc := range h.dialogCases() {
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

// Where the window is as small as it can be made, every dialog is still all
// in it: what does not fit scrolls, under a header that stays and above buttons
// that stay.
func TestEveryDialogFitsTheSmallestWindow(t *testing.T) {
	for _, flavour := range []struct{ platform, lang string }{{"darwin", "en"}, {"darwin", "zh"}, {"windows", "en"}} {
		platform, lang := flavour.platform, flavour.lang
		h := newHarness(t, harnessOpt{lang: lang, platform: platform, w: MinWindowWidth, h: MinWindowHeight, setup: crowd})
		h.click(h.app.t("nav.routing"))
		h.settle()
		for _, tc := range h.dialogCases() {
			tc.open()
			h.settle()
			b, foot := h.app.dlgBounds, h.app.dlgFooter
			switch {
			case b.H == 0:
				t.Errorf("%s %s: the dialog %q was not measured", platform, lang, tc.name)
			case b.X < 0 || b.Y < 0 || b.X+b.W > MinWindowWidth || b.Y+b.H > MinWindowHeight:
				t.Errorf("%s %s: the dialog %q is %.0f x %.0f at (%.0f, %.0f) and does not fit %d x %d",
					platform, lang, tc.name, b.W, b.H, b.X, b.Y, MinWindowWidth, MinWindowHeight)
			case foot.H > 0 && foot.Y+foot.H > b.Y+b.H+0.5:
				// The panel gives way to the window; what it holds spills over its edge.
				t.Errorf("%s %s: the buttons of the dialog %q end at %.0f, below its panel's end at %.0f",
					platform, lang, tc.name, foot.Y+foot.H, b.Y+b.H)
			}
			h.shut()
		}
	}
}

// tallHeroDialog is a dialog with a hero and more to say than any window holds.
type tallHeroDialog struct{}

func (tallHeroDialog) frame(a *App, c *ui.Context) dlgFrame {
	return dlgFrame{
		width: 560,
		hero:  func() { ui.Box(c).Height(welcomeHeroH) },
		heroH: welcomeHeroH,
		body: func() {
			for i := range 40 {
				a.txt(c, fmt.Sprintf("Line %d of a body that is far too tall", i+1), 13.5, 400, a.pl.Text2)
			}
		},
		buttons: func() { a.btn(c, btnSpec{kind: btnPrimary, label: "Done"}) },
	}
}

// A hero is far taller than a header, and what it takes the body gives up: the
// buttons stay in the panel and the panel in the window, whatever the width of
// the letters of the machine's font makes of the body's height. (The first-run
// introduction overflowed by a few pixels with the wide fonts of Linux, where
// the sweep above saw it, while a Mac's fonts leave it room.)
func TestADialogWithAHeroKeepsItsButtonsInTheSmallestWindow(t *testing.T) {
	h := newHarness(t, harnessOpt{w: MinWindowWidth, h: MinWindowHeight})
	h.app.openDialog(tallHeroDialog{})
	h.settle()
	b, foot := h.app.dlgBounds, h.app.dlgFooter
	if b.H == 0 || foot.H == 0 {
		t.Fatal("the dialog was not measured")
	}
	if b.Y < 0 || b.Y+b.H > MinWindowHeight {
		t.Errorf("the panel is %.0f tall at %.0f and does not fit a window %d tall", b.H, b.Y, MinWindowHeight)
	}
	if foot.Y+foot.H > b.Y+b.H+0.5 {
		t.Errorf("the buttons end at %.0f, below the panel's end at %.0f", foot.Y+foot.H, b.Y+b.H)
	}
}
