package gui

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"eggbox/internal/routing"
)

// ------------------------------------------------------------------ helpers

// textWith is the first text on screen that starts with prefix.
func (h *harness) textWith(prefix string) string {
	h.t.Helper()
	for _, s := range h.ui.Texts() {
		if strings.HasPrefix(s, prefix) {
			return s
		}
	}
	h.t.Fatalf("nothing on screen starts with %q; have %s", prefix, h.digest())
	return ""
}

// lastItem is the item the plan in force tries last.
func (h *harness) lastItem() routing.Item {
	h.t.Helper()
	rows := h.state().View().Rows
	return rows[len(rows)-1].Item
}

// itemNamed finds an item of the plan in force by what it is called.
func (h *harness) itemNamed(name string) (routing.Row, bool) {
	for _, r := range h.state().View().Rows {
		if r.Item.Name == name {
			return r, true
		}
	}
	return routing.Row{}, false
}

// addItemHarness is the Routing page with the dialog that adds an item open.
func addItemHarness(t *testing.T) *harness {
	t.Helper()
	h := routingHarness(t, harnessOpt{})
	h.click("Add item")
	h.mustHave("Add an item", "Choose where its rules come from", "Library", "A link", "Other plans", "Write it")
	return h
}

// ------------------------------------------------------------------ catalog

func TestAddItemFromTheCatalog(t *testing.T) {
	h := addItemHarness(t)
	n := len(h.state().View().Rows)
	// The catalog opens on what is recommended, by kind.
	h.mustHave("Search services, for example Netflix")
	h.typeText("Steam")
	h.mustHave("Steam")
	h.click("Steam")
	h.mustHave("New item", "Name", "Source", "Goes to", "Position", "You can change this later on the Routing page.")
	h.mustHave(h.textWith("Automatic (before"))

	h.submitDialog("Add item")
	h.settle()
	h.mustNotHave("New item")
	h.mustHave("“Steam” added")
	rows := h.state().View().Rows
	if len(rows) != n+1 {
		t.Fatalf("items: %d, want %d", len(rows), n+1)
	}
	row, ok := h.itemNamed("Steam")
	if !ok || row.Item.Dest.Kind != routing.DestFollow || !row.Item.Enabled || len(row.Item.Sources) == 0 {
		t.Fatalf("the new item is %+v", row.Item)
	}
	if row.Locked {
		t.Fatal("an item the person added is theirs to change")
	}
	h.mustHave("Yours")
}

func TestAddItemChoosesItsDestinationAndPlace(t *testing.T) {
	h := addItemHarness(t)
	h.typeText("Steam")
	h.click("Steam")
	h.chooseInDialog("Follow main exit", "Direct")
	// The position box: the one that says how the place is found now.
	h.tabTo(h.textWith("Automatic (before"))
	h.enter()
	h.pick("At")
	h.mustHave("At the end")
	h.submitDialog("Add item")
	h.settle()
	last := h.lastItem()
	if last.Name != "Steam" || last.Dest.Kind != routing.DestDirect {
		t.Fatalf("the last item is %q going %s, want Steam going direct", last.Name, last.Dest.Kind)
	}
}

func TestAddItemBeforeAnotherItem(t *testing.T) {
	h := addItemHarness(t)
	h.typeText("Steam")
	h.click("Steam")
	h.tabTo(h.textWith("Automatic (before"))
	h.enter()
	// The entries that start "Before" go down the plan: this is the second.
	h.pick("Before#1")
	h.mustHave("Before “AI services”")
	h.submitDialog("Add item")
	h.settle()
	rows := h.state().View().Rows
	if rows[0].Item.ID != "b-ads" || rows[1].Item.Name != "Steam" || rows[2].Item.ID != "b-ai" {
		t.Fatalf("the new item is not between Ad blocking and AI services: %q, %q, %q", rows[0].Item.ID, rows[1].Item.Name, rows[2].Item.ID)
	}
}

func TestAddItemSearchWithoutAnswerSaysSo(t *testing.T) {
	h := addItemHarness(t)
	h.typeText("zzzz-no-such-service")
	h.mustHave("Nothing found", "Try another name, or paste the link of a rule file.")
}

func TestAddItemCatalogMarksWhatIsThereAlready(t *testing.T) {
	h := addItemHarness(t)
	h.typeText("GitHub")
	h.mustHave("Added")
}

// Netflix is among the streaming item's files but is not that item: it can be
// added on its own, and goes before the item that would otherwise answer first.
func TestAddItemThatABroaderItemTakesIn(t *testing.T) {
	h := addItemHarness(t)
	h.typeText("Netflix")
	h.mustNotHave("Added")
	h.click("Netflix")
	h.mustHave("New item", "Automatic (before “Streaming”)")
	h.submitDialog("Add item")
	h.settle()
	h.mustHave("“Netflix” added")
	rows := h.state().View().Rows
	for i, r := range rows {
		if r.Item.Name == "Netflix" {
			if i+1 >= len(rows) || rows[i+1].Item.ID != "b-streaming" {
				t.Fatalf("Netflix is not right before Streaming")
			}
			return
		}
	}
	t.Fatal("no Netflix item")
}

// An entry the plan has, but turned off, does not say "Added" as if it worked:
// it says it is off, and a click turns it on.
func TestAddItemCatalogTurnsOnWhatIsThereButOff(t *testing.T) {
	h := addItemHarness(t)
	if h.row("b-ads").Item.Enabled {
		t.Fatal("the fixture's ad item is on")
	}
	h.typeText("Ad blocking")
	h.mustHave("Off")
	h.click("Off")
	h.settle()
	h.mustHave("“Ad blocking” turned on")
	if !h.row("b-ads").Item.Enabled {
		t.Fatal("the ad item is still off")
	}
	h.click("Add item")
	h.typeText("Ad blocking")
	h.mustHave("Added")
	h.mustNotHave("Off")
}

func TestAddItemBackFromTheConfirmingStep(t *testing.T) {
	h := addItemHarness(t)
	h.typeText("Steam")
	h.click("Steam")
	h.mustHave("New item")
	h.click("Back")
	h.mustHave("Add an item", "Library")
	h.mustNotHave("New item")
	if h.called("edit-routing") != 0 {
		t.Fatal("something was saved before it was confirmed")
	}
}

func TestAddItemNeedsAName(t *testing.T) {
	h := addItemHarness(t)
	n := len(h.state().View().Rows)
	h.click("Write it")
	h.typeText("DOMAIN-SUFFIX,corp.example")
	h.click("Next")
	h.mustHave("New item", "1 entry written by hand")
	// The name is empty and the box for it has the keyboard: Return does nothing.
	h.enter()
	h.settle()
	h.mustHave("New item")
	if h.called("edit-routing") != 0 || len(h.state().View().Rows) != n {
		t.Fatal("an item without a name was added")
	}
	h.typeText("Corp")
	h.enter()
	h.settle()
	if _, ok := h.itemNamed("Corp"); !ok {
		t.Fatalf("the item was not added once it had a name: %v", h.be.called())
	}
}

func TestAddItemCannotTakeTheNameOfAnother(t *testing.T) {
	h := addItemHarness(t)
	h.click("Write it")
	h.typeText("DOMAIN-SUFFIX,corp.example")
	h.click("Next")
	h.typeText("YouTube")
	h.enter()
	h.settle()
	h.mustHave("New item", "Another item already has this name.")
	if _, ok := h.itemNamed("YouTube"); !ok || len(h.state().View().Rows) != 12 {
		t.Fatal("the item was added under a name that is taken")
	}
}

// --------------------------------------------------------------------- link

const ruleLink = "https://rules.example/lists/my-list.json"

func TestAddItemFromALink(t *testing.T) {
	h := addItemHarness(t)
	n := len(h.state().View().Rows)
	h.click("A link")
	h.mustHave("Link to a rule file", "Check")
	h.typeText(ruleLink)
	h.enter()
	h.settle()
	h.mustHave("Recognized: ", "128 domains · 4 IP ranges · 1 other rule",
		"2 rules of a type the engine cannot use will be left out (process_name)", "Use this file")
	if !h.be.took("preview-rule-set " + ruleLink) {
		t.Fatalf("the file was not read: %v", h.be.called())
	}
	// Reading is not keeping.
	if h.be.took("keep-rule-set") || h.called("edit-routing") != 0 {
		t.Fatal("the file was kept before it was chosen")
	}
	h.click("Use this file")
	h.mustHave("New item")
	h.submitDialog("Add item")
	h.settle()
	if !h.be.took("keep-rule-set " + ruleLink) {
		t.Fatalf("the file was not kept: %v", h.be.called())
	}
	row, ok := h.itemNamed("my-list")
	if !ok || len(row.Item.Sources) != 1 || row.Item.Sources[0].URL != ruleLink || len(h.state().View().Rows) != n+1 {
		t.Fatalf("the new item is %+v", row.Item)
	}
}

// The preview says what was done to the address that was pasted, in words that
// fit what was done: a mirror's prefix taken off, a page changed to the file
// itself, an .mrs swapped for its sing-box twin.
func TestAddItemLinkPreviewSaysWhatWasDoneToTheAddress(t *testing.T) {
	for _, c := range []struct {
		in  string
		say string
		not []string
	}{
		{"https://rules.example/lists/plain.json", "", []string{"prefix was taken off", "changed to the address", "swapped for its sing-box twin"}},
		{"https://mirror.example/https://raw.githubusercontent.com/o/r/main/x.list", "A download mirror’s prefix was taken off the address.",
			[]string{"changed to the address", "swapped for its sing-box twin"}},
		{"https://github.com/o/r/blob/main/x.list", "The address was changed to the address of the file itself.",
			[]string{"prefix was taken off", "swapped for its sing-box twin"}},
		{"https://rules.example/meta/geo/geosite/x.mrs", "An .mrs address (the kind only mihomo reads) was swapped for its sing-box twin.",
			[]string{"prefix was taken off", "changed to the address"}},
	} {
		h := addItemHarness(t)
		h.click("A link")
		h.typeText(c.in)
		h.enter()
		h.settle()
		h.mustHave("Recognized: ", "Use this file")
		if c.say != "" {
			h.mustHave(c.say)
		}
		h.mustNotHave(c.not...)
	}
}

func TestAddItemLinkThatIsNoAddress(t *testing.T) {
	h := addItemHarness(t)
	h.click("A link")
	h.typeText("ftp://rules.example/x")
	h.enter()
	h.settle()
	h.mustHave("That is not the web address of a rule file.", "Check the address, or pick another mirror in Settings, and try again.")
	h.mustNotHave("Use this file")
	// What was typed since no longer describes the answer.
	h.typeText("x")
	h.mustNotHave("That is not the web address of a rule file.")
}

func TestAddItemLinkThatIsASubscription(t *testing.T) {
	h := addItemHarness(t)
	h.click("A link")
	h.typeText("https://provider.example/subscription?token=abc")
	h.enter()
	h.settle()
	h.mustHave("This looks like a subscription of nodes, not a list of rules.", "Add as a subscription")
	h.mustNotHave("Use this file")
	h.click("Add as a subscription")
	h.settle()
	h.mustHave("Add a subscription")
	// The link carried over; the token is not shown anywhere in the window.
	h.mustNotHave("token=abc")
}

func TestAddItemLinkPasted(t *testing.T) {
	h := addItemHarness(t)
	h.click("A link")
	h.ui.SetClipboard(ruleLink)
	h.click("Paste")
	h.settle()
	h.mustHave("Recognized: ", "Use this file")
}

// -------------------------------------------------------------- other plans

func TestAddItemFromAnotherPlan(t *testing.T) {
	h := addItemHarness(t)
	h.click("Other plans")
	h.mustHave("Copy from", "Aurora Cloud’s plan", "Games", "Local sites")
	h.click("Games")
	h.mustHave("New item", "Copied from “Aurora Cloud’s plan”")
	h.submitDialog("Add item")
	h.settle()
	row, ok := h.itemNamed("Games")
	if !ok || len(row.Item.Sources) == 0 {
		t.Fatalf("the copied item is %+v", row.Item)
	}
	// The plan it came from is not touched.
	if got := len(h.state().ViewOf(routing.SubPlanID("sub-aurora")).Rows); got != 3 {
		t.Fatalf("the plan it was copied from has %d items", got)
	}
}

func TestAddItemFromAnotherPlanWhenThereIsNone(t *testing.T) {
	h := routingHarness(t, harnessOpt{setup: func(be *fixtureBackend, _ *Options) {
		be.arrange(func(st *routing.State) { st.Plans = nil })
	}})
	h.click("Add item")
	h.click("Other plans")
	h.mustHave("No other plan")
}

// ------------------------------------------------------------------- write

func TestAddItemWrittenByHand(t *testing.T) {
	h := addItemHarness(t)
	n := len(h.state().View().Rows)
	h.click("Write it")
	h.mustHave("Write rules of your own, one per line", "Read lines as", "Detect")
	h.typeText("DOMAIN-SUFFIX,corp.example,DIRECT\nIP-CIDR,10.1.0.0/16,DIRECT\nGEOIP,CN")
	h.mustHave("2 entries recognized", "1 line was not understood", "GEOIP rules are not supported here", "Next")
	h.click("Next")
	h.mustHave("New item", "2 entries written by hand")
	// Every line said DIRECT, so that is what is suggested.
	h.mustHave("Direct")
	h.typeText("Corp")
	h.enter()
	h.settle()
	row, ok := h.itemNamed("Corp")
	if !ok || len(h.state().View().Rows) != n+1 {
		t.Fatalf("the new item is missing: %+v", row)
	}
	if row.Item.Dest.Kind != routing.DestDirect || len(row.Item.Sources) != 1 || row.Item.Sources[0].Kind != routing.SourceInline || len(row.Item.Sources[0].Entries) != 2 {
		t.Fatalf("the new item is %+v", row.Item)
	}
	h.mustHave("Corp", "2 hand-written entries")
}

func TestAddItemWrittenByHandReadsLinesAsOneKind(t *testing.T) {
	h := addItemHarness(t)
	h.click("Write it")
	h.typeText("alpha\nbeta.example")
	// Detected, "alpha" is a program and "beta.example" a domain.
	h.mustHave("Program name  1", "Domain and subdomains  1")
	h.tabTo("Detect")
	h.enter()
	h.pick("Domain#0")
	h.mustHave("2 entries recognized", "Domain and subdomains  2")
	h.mustNotHave("Program name  1")
}

func TestAddItemWrittenByHandHasNothingToAddYet(t *testing.T) {
	h := addItemHarness(t)
	h.click("Write it")
	h.mustHave("Paste one rule per line")
	h.typeText("GEOIP,CN")
	h.mustHave("Nothing recognized yet", "Next")
	h.enter()
	h.mustNotHave("New item")
}

// ------------------------------------------------------------------ sources

func TestSourcesAreAddedToAnItemOfAPlanThatIsKeptUpToDate(t *testing.T) {
	h := routingHarness(t, harnessOpt{})
	h.click("Telegram")
	h.mustHave("Telegram", "Recommended", "Goes to", "Sources", "Add source")
	// The plan's own item cannot be renamed, but it can be added to.
	h.mustNotHave("Edit entries")
	h.click("Add source")
	h.mustHave("Add a source", "Telegram")
	h.click("Write it")
	h.typeText("DOMAIN-SUFFIX,telegram-extra.example")
	h.mustHave("1 entry recognized")
	h.click("Add 1 entry")
	h.settle()
	h.mustNotHave("Add a source")
	row := h.row("b-telegram")
	if len(row.Item.Sources) != 3 || len(row.Extra) != 1 || !row.Locked {
		t.Fatalf("Telegram has %d sources, %d of them extra, locked=%v", len(row.Item.Sources), len(row.Extra), row.Locked)
	}
	// Back in the item's detail: the new source is there, and can be edited and removed.
	h.mustHave("1 hand-written entry", "Edit entries", "Remove source")
}

func TestHandWrittenEntriesAreEditedAndTakenAway(t *testing.T) {
	h := routingHarness(t, harnessOpt{})
	h.click("Telegram")
	h.click("Add source")
	h.click("Write it")
	h.typeText("DOMAIN-SUFFIX,telegram-extra.example")
	h.click("Add 1 entry")
	h.settle()

	h.click("Edit entries")
	// The box has the keyboard, with what is there already in it.
	h.mustHave("1 entry recognized", "Cancel", "Save")
	h.ui.Key(ui.Cmd, ui.KeyA)
	h.typeText("DOMAIN-SUFFIX,telegram-extra.example\nDOMAIN-KEYWORD,telegram-cdn")
	h.mustHave("2 entries recognized")
	// The buttons are below what the dialog shows at once: the keyboard brings them in.
	h.submitDialog("Save")
	row := h.row("b-telegram")
	var extra routing.Source
	for _, src := range row.Item.Sources {
		if src.Kind == routing.SourceInline {
			extra = src
		}
	}
	if len(extra.Entries) != 2 {
		t.Fatalf("the hand-written source holds %d entries, want 2", len(extra.Entries))
	}
	h.mustHave("2 hand-written entries", "Edit entries")

	// Taking it away leaves the plan's own sources as they were.
	h.submitDialog("Remove source")
	row = h.row("b-telegram")
	if len(row.Item.Sources) != 2 || len(row.Extra) != 0 {
		t.Fatalf("Telegram has %d sources (%d extra) after the removal", len(row.Item.Sources), len(row.Extra))
	}
	h.mustNotHave("Remove source", "Edit entries")
}

func TestSourceThatIsAlreadyThereIsNotAddedTwice(t *testing.T) {
	h := routingHarness(t, harnessOpt{})
	h.click("Telegram")
	h.click("Add source")
	h.typeText("Telegram")
	// The catalog's own Telegram is what the item is made of: choosing it
	// again is refused, and says why. (The name is on the page behind as
	// well, so the keyboard goes to it.)
	h.tabTo("Telegram")
	h.enter()
	h.settle()
	h.mustHave("Add a source", "The item already has this source.")
	if got := len(h.row("b-telegram").Item.Sources); got != 2 {
		t.Fatalf("Telegram has %d sources, want the 2 it had", got)
	}
}

// ------------------------------------------------------------ items of your own

// workHarness is the Routing page with the shipped plan saved as a plan of the person's own.
func workHarness(t *testing.T) *harness {
	t.Helper()
	return routingHarness(t, harnessOpt{setup: func(be *fixtureBackend, _ *Options) {
		be.arrange(func(st *routing.State) {
			if _, err := st.SaveAsMine("Work"); err != nil {
				panic(err)
			}
		})
	}})
}

// clickBelow clicks the control under a field's name.
func (h *harness) clickBelow(label string) {
	h.t.Helper()
	r, ok := h.ui.Find(label)
	if !ok {
		h.t.Fatalf("nothing shows %q; have %s", label, h.digest())
	}
	h.ui.ClickAt(r.X+40, r.Y+r.H+26)
	h.ui.Advance(200 * time.Millisecond)
}

func TestItemOfYourOwnIsRenamed(t *testing.T) {
	h := workHarness(t)
	h.click("Telegram")
	h.mustHave("Work", "Name", "Goes to", "Sources")
	h.clickBelow("Name")
	h.ui.Key(ui.Cmd, ui.KeyA)
	h.typeText("Chat apps")
	h.enter()
	h.settle()
	if _, ok := h.itemNamed("Chat apps"); !ok {
		t.Fatalf("the item was not renamed: %v", h.be.called())
	}
	if _, ok := h.itemNamed("Telegram"); ok {
		t.Fatal("the old name is still taken")
	}
	// A name that is taken is refused, and the dialog says why.
	h.clickBelow("Name")
	h.ui.Key(ui.Cmd, ui.KeyA)
	h.typeText("YouTube")
	h.enter()
	h.settle()
	h.mustHave("Another item already has this name.")
	if _, ok := h.itemNamed("Chat apps"); !ok {
		t.Fatal("a refused name changed the item")
	}
}

func TestItemOfYourOwnWearsAPicture(t *testing.T) {
	h := workHarness(t)
	h.click("Telegram")
	h.click("Globe") // the pictures are named for people who cannot see them
	h.settle()
	row, _ := h.itemNamed("Telegram")
	if row.Item.Icon != "globe" {
		t.Fatalf("the item's picture is %q", row.Item.Icon)
	}
}

func TestSourcesOfAnItemOfYourOwnAreTakenAwayButTheLast(t *testing.T) {
	h := workHarness(t)
	h.click("Telegram")
	h.mustHave("Remove source")
	h.click("Remove source")
	h.settle()
	row, _ := h.itemNamed("Telegram")
	if len(row.Item.Sources) != 1 {
		t.Fatalf("Telegram has %d sources, want 1", len(row.Item.Sources))
	}
	// An item needs a source: the last one cannot be taken away.
	h.mustNotHave("Remove source")
}

func TestItemDialogChangesWhereAnItemGoes(t *testing.T) {
	h := routingHarness(t, harnessOpt{})
	h.click("Telegram")
	h.mustHave("Goes to", "On")
	h.chooseInDialog("Follow main exit", "Direct")
	if got := h.row("b-telegram").Item.Dest.Kind; got != routing.DestDirect {
		t.Fatalf("Telegram goes %s", got)
	}
	// The switch of the dialog is the page's.
	h.tabTo("Telegram")
	h.ui.Key(0, ui.KeySpace)
	h.settle()
	if h.row("b-telegram").Item.Enabled {
		t.Fatal("Telegram should be off")
	}
	h.mustHave("Off")
}
