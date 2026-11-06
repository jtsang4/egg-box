package gui

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"eggbox/internal/routing"
)

// ------------------------------------------------------------ subscriptions

func TestSubscriptionOffersThePlanItBrings(t *testing.T) {
	h := newHarness(t, harnessOpt{h: 1100})
	h.click("Subscriptions")
	h.settle()
	h.mustHave("Brings a routing plan with 3 items", "Switch to it to route the way this provider suggests", "Use this plan")
	h.mustNotHave("This plan is in use")
	if h.state().ActiveID() == "sub-aurora" {
		t.Fatal("the plan was in force before it was chosen")
	}
	// Only one of the three brought a plan.
	if n := h.countOf("Brings a routing plan with 3 items"); n != 1 {
		t.Fatalf("%d subscriptions bring a plan, want 1", n)
	}

	h.click("Use this plan")
	h.settle()
	if got := h.state().ActivePlan().Name; got != "Aurora Cloud" {
		t.Fatalf("the plan in force is %q, want Aurora Cloud's", got)
	}
	h.mustHave("This plan is in use", "In use")
	h.mustNotHave("Use this plan")

	// The Routing page follows.
	h.click("Routing")
	h.settle()
	h.mustHave("3 items · Aurora Cloud’s plan", "Games", "Local sites")
}

func TestDeletingASubscriptionThatBroughtAPlanSaysTheyKeepIt(t *testing.T) {
	h := newHarness(t, harnessOpt{h: 1100})
	h.click("Subscriptions")
	h.settle()
	h.mustHave("Brings a routing plan with 3 items")

	h.click("More") // the first card's menu: Aurora Cloud
	h.pick("Delete")
	h.mustHave("Delete “Aurora Cloud”?", "The routing plan it brought stays, as one of your own.")
	if h.be.took("remove") {
		t.Fatal("the subscription was removed before the person agreed")
	}
	h.click("Delete")
	h.settle()
	if !h.be.took("remove sub-aurora") {
		t.Fatalf("the subscription was not removed: %v", h.be.called())
	}
	h.mustNotHave("Brings a routing plan")
}

func TestDeletingASubscriptionWithoutAPlanSaysNothingOfPlans(t *testing.T) {
	h := newHarness(t, harnessOpt{h: 1100})
	h.be.arrange(func(st *routing.State) {
		st.Plans = slices.DeleteFunc(st.Plans, func(p *routing.Plan) bool { return p.ID == routing.SubPlanID("sub-aurora") })
	})
	h.click("Subscriptions")
	h.settle()
	h.mustNotHave("Brings a routing plan")
	h.click("More")
	h.pick("Delete")
	h.mustHave("Delete “Aurora Cloud”?", "Its nodes leave the list and its link is removed from the keychain. This cannot be undone.")
	h.mustNotHave("routing plan it brought")
}

// countOf says how many labels on screen read exactly s.
func (h *harness) countOf(s string) int {
	n := 0
	for _, x := range h.ui.Texts() {
		if x == s {
			n++
		}
	}
	return n
}

// ----------------------------------------------------------------- settings

func settingsHarness(t *testing.T, o harnessOpt) *harness {
	t.Helper()
	// The window shows the whole page, as a click outside it lands on nothing.
	// How far down a row is depends on the machine's fonts: with a Mac's the
	// rule data rows sat a few pixels inside a window of 2000, and with the
	// wider ones of the Linux that CI runs on they were outside it.
	if o.h == 0 {
		o.h = 3200
	}
	h := newHarness(t, o)
	if o.lang == "zh" {
		h.click("设置")
	} else {
		h.click("Settings")
	}
	h.settle()
	return h
}

func TestSettingsKeepTheLocalNetworkDirect(t *testing.T) {
	h := settingsHarness(t, harnessOpt{})
	h.mustHave("Keep the local network direct", "Printers, NAS and other devices on your network stay reachable.", "Use IPv6")
	was := h.be.Prefs()
	h.click("Keep the local network direct")
	h.settle()
	if !h.be.took("save-prefs") || h.be.Prefs().LANDirect == was.LANDirect {
		t.Fatalf("the switch did nothing: %v", h.be.called())
	}
	if h.be.Prefs().IPv6 != was.IPv6 {
		t.Fatal("another switch moved")
	}
	h.click("Use IPv6")
	h.settle()
	if h.be.Prefs().IPv6 == was.IPv6 {
		t.Fatal("IPv6 did not change")
	}
}

func TestSettingsRuleData(t *testing.T) {
	h := settingsHarness(t, harnessOpt{})
	h.mustHave("Rule data", "The rule files the routing items use.", "Status", "rule files downloaded", "checked 2 hours ago",
		"Download from", "Never", "Daily", "Every 3 days", "Weekly")
	h.mustNotHave("failed to update")

	h.click("Update rules")
	h.settle()
	if !h.be.took("update-rule-data true") {
		t.Fatalf("the files were not asked for afresh: %v", h.be.called())
	}

	h.click("GitHub")
	h.settle()
	if got := h.be.Settings().RuleMirror; got != "github" {
		t.Fatalf("mirror = %q", got)
	}
	h.click("Daily")
	h.settle()
	if got := h.be.Settings().RuleUpdateHours; got != 24 {
		t.Fatalf("update interval = %d hours", got)
	}
	h.click("Weekly")
	h.settle()
	if got := h.be.Settings().RuleUpdateHours; got != 168 {
		t.Fatalf("update interval = %d hours", got)
	}
}

// A mirror of one's own is the last place the rule files are asked for. The box
// takes a web address, says so when what was written is none (and keeps
// nothing), saves with Return or the button, and an empty box takes the mirror
// away again.
func TestSettingsTakeAMirrorOfTheUsersOwn(t *testing.T) {
	h := settingsHarness(t, harnessOpt{})
	h.mustHave("A mirror of your own", "Asked last, when the others cannot deliver a file.")
	h.mustNotHave("This is not a web address")
	if got := h.be.Settings().RuleMirrorPrefix; got != "" {
		t.Fatalf("a mirror was set from the start: %q", got)
	}

	// Something that is no address is told so and kept nowhere.
	h.click("https://mirror.example/")
	h.typeText("mirror.example")
	h.click("Save")
	h.settle()
	h.mustHave("This is not a web address. Write one like https://mirror.example/")
	if got := h.be.Settings().RuleMirrorPrefix; got != "" {
		t.Fatalf("something that is no address was kept: %q", got)
	}

	// Writing something else takes the complaint away; Return keeps the address.
	h.click("https://mirror.example/")
	h.ui.Key(ui.Cmd, ui.KeyA)
	h.typeText("https://mirror.example/")
	h.mustNotHave("This is not a web address")
	h.enter()
	h.settle()
	if got := h.be.Settings().RuleMirrorPrefix; got != "https://mirror.example/" {
		t.Fatalf("the mirror is %q", got)
	}
	h.mustNotHave("This is not a web address")
	if h.shows("Save") {
		t.Fatal("the button to keep a change is there although nothing is changed")
	}

	// A second address is kept with the button.
	h.ui.Key(ui.Cmd, ui.KeyA)
	h.typeText("https://gh-proxy.example/")
	if !h.shows("Save") {
		t.Fatal("a change was typed and there is no button to keep it")
	}
	h.click("Save")
	h.settle()
	if got := h.be.Settings().RuleMirrorPrefix; got != "https://gh-proxy.example/" {
		t.Fatalf("the mirror is %q", got)
	}
	if h.shows("Save") {
		t.Fatal("the button to keep a change is there although the change was kept")
	}

	// An empty box takes the mirror away.
	h.click("https://mirror.example/")
	h.ui.Key(ui.Cmd, ui.KeyA)
	h.ui.Key(0, ui.KeyBackspace)
	h.ui.Advance(100 * time.Millisecond)
	h.enter()
	h.settle()
	if got := h.be.Settings().RuleMirrorPrefix; got != "" {
		t.Fatalf("the mirror is %q, want none", got)
	}
}

// shows says whether a line of text on screen is exactly this one (mustHave
// finds a word inside a longer sentence as well).
func (h *harness) shows(text string) bool {
	h.t.Helper()
	return slices.Contains(h.ui.Texts(), text)
}

func TestSettingsTakeAMirrorOfTheUsersOwnInChinese(t *testing.T) {
	h := settingsHarness(t, harnessOpt{lang: "zh"})
	h.mustHave("自己的镜像", "其他下载源都下载不了时，最后试这个。")
	h.click("https://mirror.example/")
	h.typeText("不是网址")
	h.click("保存")
	h.settle()
	h.mustHave("这不是一个网址，请写成 https://mirror.example/ 这样")
}

func TestSettingsRuleDataSaysWhatIsMissingAndWhatFailed(t *testing.T) {
	h := settingsHarness(t, harnessOpt{setup: func(be *fixtureBackend, _ *Options) {
		rows := be.Routing().View().Rows
		be.missing[rows[1].Item.Sources[0].URL] = true
		be.failing[rows[2].Item.Sources[0].URL] = true
		be.refresh()
	}})
	total := len(h.be.Snapshot().Plan.Sources)
	if total < 3 {
		t.Fatalf("the plan has %d rule files", total)
	}
	// One file is not here: the count is one short; one that is here could not
	// be brought up to date.
	h.mustHave(fmt.Sprintf("%d of %d rule files downloaded", total-1, total), "1 failed to update")
}

func TestSettingsRuleDataWhenThePlanNeedsNoFiles(t *testing.T) {
	h := settingsHarness(t, harnessOpt{setup: func(be *fixtureBackend, _ *Options) {
		cp := *be.Snapshot()
		cp.Plan.Sources = nil
		be.set_(&cp)
	}})
	h.mustHave("The plan in force needs no rule files")
}

func TestSettingsInChinese(t *testing.T) {
	h := settingsHarness(t, harnessOpt{lang: "zh"})
	h.mustHave("规则数据", "局域网直连", "使用 IPv6", "分流项所用的规则文件。")
}
