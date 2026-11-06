package gui

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"eggbox/internal/routing"
)

// ---------------------------------------------------------------- helpers

// routingHarness is the Routing page of a window tall enough to show every
// row of the shipped plan.
func routingHarness(t *testing.T, o harnessOpt) *harness {
	t.Helper()
	if o.h == 0 {
		o.h = 1500
	}
	h := newHarness(t, o)
	h.click(h.app.t("nav.routing"))
	h.settle()
	return h
}

// state is what the person has chosen so far.
func (h *harness) state() routing.State { return h.app.be.Routing() }

// row is an item of the plan in force.
func (h *harness) row(id string) routing.Row {
	h.t.Helper()
	r, _, ok := h.state().View().Row(id)
	if !ok {
		h.t.Fatalf("the plan in force has no item %q", id)
	}
	return r
}

// middle is the vertical middle of the first line showing text.
func (h *harness) middle(text string) float32 {
	h.t.Helper()
	r, ok := h.ui.Find(text)
	if !ok {
		h.t.Fatalf("nothing shows %q; have %s", text, h.digest())
	}
	return r.Y + r.H/2
}

// clickOnLine clicks the control that sits in the column of anchor (a text
// that is on screen) on the line of row (another one).
func (h *harness) clickOnLine(row, anchor string) {
	h.t.Helper()
	a, ok := h.ui.Find(anchor)
	if !ok {
		h.t.Fatalf("nothing shows %q; have %s", anchor, h.digest())
	}
	h.ui.ClickAt(a.X+a.W/2, h.middle(row))
	h.ui.Advance(400 * time.Millisecond)
}

// rightClick opens the menu of the line showing text.
func (h *harness) rightClick(text string) {
	h.t.Helper()
	if err := h.ui.RightClick(text); err != nil {
		h.t.Fatalf("right-click %q: %v", text, err)
	}
	h.ui.Advance(300 * time.Millisecond)
}

// pick chooses an entry of the menu that is open (an entry of a submenu when
// the path has more than one step) the way a keyboard does, by typing the
// start of its name: the one way that tells the menu's own entries from the
// page's that read the same. Only the first word of each name is typed (a
// space chooses), so when entries of a menu share it a step says how many
// entries further down the one meant is: "From#1" is the second entry that
// starts with "From". An entry that is disabled is skipped by the menu, so
// choosing it changes nothing.
func (h *harness) pick(path ...string) {
	h.t.Helper()
	for i, step := range path {
		label, down := step, 0
		if at := strings.LastIndex(step, "#"); at > 0 {
			n, err := strconv.Atoi(step[at+1:])
			if err != nil {
				h.t.Fatalf("bad menu step %q", step)
			}
			label, down = step[:at], n
		}
		h.typeAhead(label)
		for ; down > 0; down-- {
			h.ui.Key(0, ui.KeyDown)
		}
		if i == len(path)-1 {
			h.ui.Key(0, ui.KeyEnter)
		} else {
			h.ui.Key(0, ui.KeyRight)
		}
	}
	h.ui.Advance(300 * time.Millisecond)
	h.settle()
}

// typeAhead types the first word of a name, in the keys of a keyboard.
func (h *harness) typeAhead(label string) {
	h.t.Helper()
	h.ui.Advance(1200 * time.Millisecond) // what was typed before is forgotten
	for _, r := range strings.ToLower(label) {
		var k ui.Key
		switch {
		case r >= 'a' && r <= 'z':
			k = ui.KeyA + ui.Key(r-'a')
		case r >= '0' && r <= '9':
			k = ui.Key0 + ui.Key(r-'0')
		default:
			return
		}
		h.ui.Key(0, k)
	}
}

// closeMenu puts away whatever menu is open.
func (h *harness) closeMenu() {
	h.t.Helper()
	for i := 0; i < 3; i++ {
		h.ui.Key(0, ui.KeyEscape)
	}
	h.ui.Advance(200 * time.Millisecond)
}

// chooseDest opens the destination menu on the line of row (its button is in
// the column of anchor) and chooses from it.
func (h *harness) chooseDest(row, anchor string, path ...string) {
	h.t.Helper()
	h.clickOnLine(row, anchor)
	h.pick(path...)
}

// toggleOnLine flips the switch of the line showing row. Its place is the
// column right of the destination buttons, whose column anchor names.
func (h *harness) toggleOnLine(row, anchor string) {
	h.t.Helper()
	a, ok := h.ui.Find(anchor)
	if !ok {
		h.t.Fatalf("nothing shows %q; have %s", anchor, h.digest())
	}
	h.ui.ClickAt(a.X+a.W+10+22, h.middle(row))
	h.ui.Advance(400 * time.Millisecond)
}

func (h *harness) called(prefix string) int {
	n := 0
	for _, c := range h.be.called() {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// ------------------------------------------------------------ the page

func TestRoutingPageShowsThePlan(t *testing.T) {
	h := routingHarness(t, harnessOpt{})
	h.mustHave("12 items · Recommended", "PLAN IN FORCE", "Ships with the application and is updated with it.",
		"My exceptions", "Items", "Ad blocking", "AI services", "Telegram", "YouTube", "Streaming", "Google", "Social media",
		"GitHub", "Microsoft", "Apple", "China sites", "International sites", "Everything else", "Test a URL", "Add item")
	// The exceptions are folded until they are looked at.
	h.mustNotHave("intranet.example", "ads-tracker")
	h.click("My exceptions")
	h.mustHave("intranet.example", "ads-tracker", "Slack")
	h.click("My exceptions")
	h.mustNotHave("intranet.example")
}

func TestRoutingPageInChinese(t *testing.T) {
	h := routingHarness(t, harnessOpt{lang: "zh"})
	h.mustHave("12 个分流项 · 内置推荐", "AI 服务", "国内网站", "其他所有流量")
	h.mustNotHave("AI services", "China sites")
}

func TestRoutingChangesWhereAnItemGoes(t *testing.T) {
	h := routingHarness(t, harnessOpt{})
	if got := h.row("b-apple").Item.Dest.Kind; got != routing.DestDirect {
		t.Fatalf("the shipped plan sends Apple %q", got)
	}
	// Direct to the main exit.
	h.chooseDest("Apple", "Follow main exit", "Follow main exit (Automatic)")
	if r := h.row("b-apple"); r.Item.Dest.Kind != routing.DestFollow || !r.Overridden {
		t.Fatalf("Apple = %+v, overridden %v", r.Item.Dest, r.Overridden)
	}
	if h.called("edit-routing") == 0 {
		t.Fatalf("nothing was saved: %v", h.be.called())
	}
	// The plan itself is untouched: restoring its choice is offered.
	h.rightClick("Apple")
	h.mustHave("Restore the plan’s choice")
	h.pick("Restore the plan’s choice")
	if r := h.row("b-apple"); r.Item.Dest.Kind != routing.DestDirect || r.Overridden {
		t.Fatalf("Apple after restoring = %+v, overridden %v", r.Item.Dest, r.Overridden)
	}
	h.rightClick("Apple")
	h.mustNotHave("Restore the plan’s choice")
	h.closeMenu()

	// Blocked, then the fastest node of a region.
	h.chooseDest("Telegram", "Follow main exit", "Block")
	if got := h.row("b-telegram").Item.Dest.Kind; got != routing.DestReject {
		t.Fatalf("Telegram = %q", got)
	}
	h.chooseDest("AI services", "Follow main exit", "Fastest in a region", "Japan  (3)")
	if d := h.row("b-ai").Item.Dest; d.Kind != routing.DestRegion || d.Region != "JP" {
		t.Fatalf("AI services = %+v", d)
	}
	h.mustHave("Japan · auto")
}

// Where the window is set the way Windows sets it (its buttons in the title
// bar, other words for the service), the routing screens and everything they open
// work as they do elsewhere.
func TestRoutingScreensInTheWindowsFlavour(t *testing.T) {
	for _, lang := range []string{"en", "zh"} {
		h := routingHarness(t, harnessOpt{platform: "windows", lang: lang, setup: crowd})
		a := h.app
		for _, nav := range []string{"nav.dashboard", "nav.nodes", "nav.subscriptions", "nav.routing", "nav.connections", "nav.logs", "nav.settings"} {
			h.click(a.t(nav))
			h.settle()
			h.leaks(lang + " windows " + nav)
		}
		h.click(a.t("nav.routing"))
		h.settle()
		// A destination is changed in the page itself, and what it opens fits
		// the window.
		if lang == "en" {
			h.chooseDest("Microsoft", "Direct", "Follow main exit", "Follow main exit (Automatic)")
			if got := h.row("b-microsoft").Item.Dest.Kind; got != routing.DestFollow {
				t.Fatalf("Microsoft = %q after the person chose the main exit", got)
			}
		}
		for _, tc := range h.dialogCases() {
			tc.open()
			h.settle()
			if !a.dlgOpen {
				t.Errorf("windows %s: the dialog %q did not open", lang, tc.name)
			}
			h.leaks(lang + " windows dialog: " + tc.name)
			h.shut()
		}
	}
}

func TestRoutingPinsAnItemToANode(t *testing.T) {
	h := routingHarness(t, harnessOpt{})
	h.chooseDest("GitHub", "Follow main exit", "A specific node…")
	h.mustHave("Choose a node", "GitHub")
	h.click("Search nodes")
	h.typeText("seoul")
	h.settle()
	h.mustHave("Use a nearby node while this one is down")
	h.click("Seoul 01")
	d := h.row("b-github").Item.Dest
	if d.Kind != routing.DestNode || d.Node == nil || d.Node.Name != "Seoul 01" || d.NoFallback {
		t.Fatalf("GitHub = %+v", d)
	}
	h.mustNotHave("Choose a node")
	// The button says which node.
	h.mustHave("Seoul 01")
}

func TestRoutingPinnedNodeWithoutStandIn(t *testing.T) {
	h := routingHarness(t, harnessOpt{})
	h.chooseDest("GitHub", "Follow main exit", "A specific node…")
	h.click("Search nodes")
	h.typeText("seoul")
	h.settle()
	h.click("Use a nearby node while this one is down")
	h.click("Seoul 01")
	if d := h.row("b-github").Item.Dest; d.Kind != routing.DestNode || !d.NoFallback {
		t.Fatalf("GitHub = %+v, want a node without a stand-in", d)
	}
}

func TestRoutingSwitchesItemsOnAndOff(t *testing.T) {
	h := routingHarness(t, harnessOpt{})
	if h.row("b-ads").Item.Enabled {
		t.Fatal("ad blocking ships switched off")
	}
	h.toggleOnLine("Ad blocking", "Block")
	if !h.row("b-ads").Item.Enabled {
		t.Fatalf("the switch did nothing: %v", h.be.called())
	}
	h.toggleOnLine("GitHub", "Follow main exit")
	if h.row("b-github").Item.Enabled {
		t.Fatal("GitHub should be off")
	}
	// An item that is off says nothing is wrong with it, and nothing goes there.
	if _, ok := h.be.Snapshot().Exits["b-github"]; ok {
		t.Fatal("an item that is off still has an exit")
	}
}

func TestRoutingMovingNeedsAPlanOfYourOwn(t *testing.T) {
	h := routingHarness(t, harnessOpt{})
	// The shipped plan is kept up to date: its rows do not move.
	h.rightClick("Telegram")
	h.mustHave("Move up", "Move down")
	h.mustNotHave("Delete")
	h.pick("Move")
	if h.called("edit-routing") != 0 || h.state().View().Rows[2].Item.ID != "b-telegram" {
		t.Fatalf("an item of the shipped plan was moved: %v", h.be.called())
	}
	h.closeMenu()

	h.click("More")
	h.pick("Save as my plan…")
	h.mustHave("Save as my plan", "Name")
	h.ui.Key(ui.Cmd, ui.KeyA)
	h.typeText("Work")
	h.enter()
	h.settle()
	st := h.state()
	if pl := st.ActivePlan(); pl == nil || pl.Kind != routing.PlanMine || pl.Name != "Work" || len(pl.Items) != 12 {
		t.Fatalf("active plan after saving: %+v", pl)
	}
	h.mustHave("12 items · Work", "Your own plan, made from Recommended")

	// Now the rows can be moved and deleted.
	h.rightClick("Telegram")
	h.pick("Move") // the first of the two: up
	rows := h.state().View().Rows
	if rows[1].Item.ID != "b-telegram" || rows[2].Item.ID != "b-ai" {
		t.Fatalf("order after moving up: %s %s %s", rows[0].Item.ID, rows[1].Item.ID, rows[2].Item.ID)
	}
	h.rightClick("Telegram")
	h.pick("Delete")
	h.mustHave("Delete “Telegram”?")
	h.click("Delete")
	if _, _, ok := h.state().View().Row("b-telegram"); ok {
		// The item of a plan of one's own has its own id: look it up by name.
		for _, r := range h.state().View().Rows {
			if r.Item.Name == "Telegram" {
				t.Fatal("Telegram is still in the plan")
			}
		}
	}
	h.mustHave("11 items · Work")
}

func TestRoutingPlansAreSwitchedAndDeleted(t *testing.T) {
	h := routingHarness(t, harnessOpt{})
	h.click("Recommended")
	h.mustHave("Aurora Cloud’s plan")
	h.pick("Aurora Cloud’s plan")
	if got := h.state().ActiveID(); got != routing.SubPlanID("sub-aurora") {
		t.Fatalf("active plan = %q", got)
	}
	h.mustHave("3 items · Aurora Cloud’s plan", "Games", "Local sites", "Comes with “Aurora Cloud”")
	// Items of a plan that comes with a subscription are not the person's to move.
	h.rightClick("Games")
	h.pick("Move")
	if rows := h.state().View().Rows; rows[1].Item.ID != "aur-2" {
		t.Fatalf("an item of a subscription's plan was moved: %+v", rows[1].Item)
	}
	h.closeMenu()

	// Back to the shipped plan; the plan of one's own is made and removed.
	h.click("Aurora Cloud’s plan")
	h.pick("Recommended")
	h.mustHave("12 items · Recommended")
	h.click("More")
	h.pick("Save as my plan…")
	h.enter()
	h.settle()
	h.mustHave("My plan")
	h.click("More")
	h.pick("Rename…")
	h.mustHave("Rename the plan")
	h.ui.Key(ui.Cmd, ui.KeyA)
	h.typeText("Travel")
	h.enter()
	h.settle()
	h.mustHave("12 items · Travel")

	h.click("More")
	h.pick("Delete a plan of mine", "Travel")
	h.mustHave("Delete “Travel”?")
	h.click("Delete")
	st := h.state()
	if st.ActiveID() != routing.BuiltinID {
		t.Fatalf("after deleting the plan in force the shipped plan is back, got %q", st.ActiveID())
	}
	for _, pl := range st.Plans {
		if pl.Kind == routing.PlanMine {
			t.Fatalf("a plan of one's own is left: %+v", pl)
		}
	}
	h.mustHave("12 items · Recommended")
}

func TestRoutingRestoresThePlansChoices(t *testing.T) {
	h := routingHarness(t, harnessOpt{})
	// Nothing differs from the plan yet: the entry is there but does nothing.
	h.click("More")
	h.pick("Restore the plan’s choices")
	h.mustNotHave("Restore the plan’s choices?")
	h.closeMenu()
	h.chooseDest("Apple", "Follow main exit", "Block")
	h.toggleOnLine("GitHub", "Follow main exit")
	h.click("More")
	h.pick("Restore the plan’s choices")
	h.mustHave("Restore the plan’s choices?")
	h.click("Restore the plan’s choices")
	if r := h.row("b-apple"); r.Overridden || r.Item.Dest.Kind != routing.DestDirect {
		t.Fatalf("Apple = %+v", r)
	}
	if !h.row("b-github").Item.Enabled {
		t.Fatal("GitHub is still off")
	}
}

func TestRoutingFailureIsToldAndNothingKept(t *testing.T) {
	h := routingHarness(t, harnessOpt{})
	h.be.failNext = routing.ErrLocked
	h.chooseDest("Apple", "Follow main exit", "Block")
	h.mustHave("This item belongs to a plan that is kept up to date elsewhere.")
	if got := h.row("b-apple").Item.Dest.Kind; got != routing.DestDirect {
		t.Fatalf("a failed change was kept: %q", got)
	}
}

// ------------------------------------------------ rules that are not there

func TestRoutingSaysWhatIsNotReady(t *testing.T) {
	ai, tgip := routing.GeoSite("category-ai-!cn"), routing.GeoIP("telegram")
	h := routingHarness(t, harnessOpt{setup: func(be *fixtureBackend, _ *Options) {
		be.missing[ai], be.missing[tgip] = true, true
		be.refresh()
	}})
	h.mustHave("Not ready yet", "Partly ready", "2 rule files have not been downloaded yet.", "Download now")
	h.click("Download now")
	if !h.be.took("update-rule-data false") {
		t.Fatalf("the files were not asked for: %v", h.be.called())
	}
	h.mustNotHave("Not ready yet", "Partly ready", "Download now")
}

func TestRoutingSaysWhenUpdatesFail(t *testing.T) {
	gh := routing.GeoSite("github")
	h := routingHarness(t, harnessOpt{setup: func(be *fixtureBackend, _ *Options) {
		be.failing[gh] = true
		be.refresh()
	}})
	h.mustHave("Not updated for 4 days")
	h.click("GitHub")
	h.mustHave("The last update failed", "HTTP 503")
	h.click("Done")
	h.mustNotHave("The last update failed")
}

func TestRoutingSaysWhenRulesAreLeftOut(t *testing.T) {
	tg := routing.GeoSite("telegram")
	h := routingHarness(t, harnessOpt{setup: func(be *fixtureBackend, _ *Options) {
		be.partial[tg] = true
		be.refresh()
	}})
	h.mustHave("Some rules unsupported")
	h.click("Telegram")
	h.mustHave("2 rules of a type the engine cannot use are left out (process_name)")
}

func TestRoutingUpdateButtonAsksForFreshFiles(t *testing.T) {
	h := routingHarness(t, harnessOpt{})
	h.click("Update rules")
	if !h.be.took("update-rule-data true") {
		t.Fatalf("calls: %v", h.be.called())
	}
}
