package gui

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"eggbox/internal/routing"
)

// ------------------------------------------------------------------ helpers

// exceptionsHarness is the Routing page with the exceptions unfolded.
func exceptionsHarness(t *testing.T) *harness {
	t.Helper()
	h := routingHarness(t, harnessOpt{})
	h.click("My exceptions")
	return h
}

// exception finds an exception by what it matches.
func (h *harness) exception(value string) (routing.Exception, bool) {
	for _, x := range h.state().Exceptions {
		if x.Value == value {
			return x, true
		}
	}
	return routing.Exception{}, false
}

// tabTo moves the keyboard focus on, as the Tab key does, until it is on the
// control that shows text. It is the way into a dialog's controls whose names
// the page behind the dialog shows as well.
func (h *harness) tabTo(text string) {
	h.t.Helper()
	for i := 0; i < 16; i++ {
		if h.ui.Focused(text) {
			return
		}
		h.ui.Key(0, ui.KeyTab)
	}
	h.t.Fatalf("the keyboard never came to %q", text)
}

// chooseInDialog opens the destination menu of a dialog (the button that
// shows what the destination is now) and chooses from it.
func (h *harness) chooseInDialog(now string, path ...string) {
	h.t.Helper()
	h.tabTo(now)
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Advance(300 * time.Millisecond)
	h.pick(path...)
}

// submitDialog presses the dialog's main button, whose name the page behind
// shows as well.
func (h *harness) submitDialog(button string) {
	h.t.Helper()
	h.tabTo(button)
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Advance(300 * time.Millisecond)
	h.settle()
}

// paste puts text on the clipboard and pastes it into the focused field.
func (h *harness) paste(text string) {
	h.t.Helper()
	h.ui.SetClipboard(text)
	h.ui.Key(ui.Cmd, ui.KeyV)
	h.ui.Advance(300 * time.Millisecond)
}

// ------------------------------------------------------------- one exception

func TestExceptionsAreListedInOrder(t *testing.T) {
	h := exceptionsHarness(t)
	h.mustHave("intranet.example", "ads-tracker", "Slack", "Domain and subdomains", "Domain keyword", "Program name")
	if a, b := h.middle("intranet.example"), h.middle("Slack"); a >= b {
		t.Fatalf("exceptions are not in the order of the list: %v, %v", a, b)
	}
}

func TestExceptionIsAddedFromTheDialog(t *testing.T) {
	h := exceptionsHarness(t)
	n := len(h.state().Exceptions)
	h.click("Add")
	h.mustHave("Add an exception", "Matches", "Value", "Goes to")
	h.typeText("corp.example")
	h.enter()
	h.settle()
	h.mustNotHave("Add an exception")
	st := h.state()
	if len(st.Exceptions) != n+1 {
		t.Fatalf("exceptions: %d, want %d", len(st.Exceptions), n+1)
	}
	x, ok := h.exception("corp.example")
	if !ok || x.Kind != routing.EntryDomainSuffix || x.Dest.Kind != routing.DestFollow || !x.Enabled {
		t.Fatalf("the new exception is %+v", x)
	}
	// It is the last of them, and the card stays open to show it.
	if st.Exceptions[len(st.Exceptions)-1].Value != "corp.example" {
		t.Fatal("a new exception does not come last")
	}
	h.mustHave("corp.example")
}

func TestExceptionIsAddedWithItsOwnDestination(t *testing.T) {
	h := exceptionsHarness(t)
	h.click("Add")
	h.typeText("corp.example")
	h.chooseInDialog("Follow main exit", "Direct")
	h.submitDialog("Add")
	h.mustNotHave("Add an exception")
	x, ok := h.exception("corp.example")
	if !ok || x.Dest.Kind != routing.DestDirect {
		t.Fatalf("the exception is %+v, want one that goes direct", x)
	}
}

func TestExceptionKindIsChosenFromTheList(t *testing.T) {
	h := exceptionsHarness(t)
	h.click("Add")
	h.typeText("10.0.0.0/8")
	// An address range is not a domain: the dialog says so and keeps what was typed.
	h.enter()
	h.settle()
	h.mustHave("Add an exception", "That is not a valid entry of the chosen kind.")
	if _, ok := h.exception("10.0.0.0/8"); ok {
		t.Fatal("an address range was added as a domain")
	}
	// Back to the box of kinds (Shift-Tab goes back), and choose the right one.
	h.ui.Key(ui.Shift, ui.KeyTab)
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Advance(300 * time.Millisecond)
	h.pick("IP")
	h.mustNotHave("That is not a valid entry of the chosen kind.")
	h.submitDialog("Add")
	x, ok := h.exception("10.0.0.0/8")
	if !ok || x.Kind != routing.EntryIPCIDR {
		t.Fatalf("the exception is %+v, want an address range", x)
	}
}

func TestExceptionDialogSaysWhatIsWrong(t *testing.T) {
	h := exceptionsHarness(t)
	n := len(h.state().Exceptions)
	h.click("Add")
	h.typeText("not a domain!")
	h.enter()
	h.settle()
	h.mustHave("Add an exception", "That is not a valid entry of the chosen kind.")
	if len(h.state().Exceptions) != n {
		t.Fatal("a bad entry was added")
	}
	if h.called("edit-routing") != 0 {
		t.Fatal("a bad entry was sent to be saved")
	}
	// Writing again clears the message.
	h.ui.Key(ui.Cmd, ui.KeyA)
	h.typeText("fine.example")
	h.mustNotHave("That is not a valid entry of the chosen kind.")
	h.enter()
	h.settle()
	if _, ok := h.exception("fine.example"); !ok {
		t.Fatalf("the corrected entry was not added: %v", h.be.called())
	}
}

func TestExceptionDialogInChinese(t *testing.T) {
	h := routingHarness(t, harnessOpt{lang: "zh"})
	h.click("我的例外")
	h.click("添加")
	h.mustHave("添加例外", "匹配", "走向")
	h.typeText("bad value!")
	h.enter()
	h.settle()
	h.mustHave("添加例外", "这条内容不符合所选类型的写法。")
}

func TestExceptionIsEdited(t *testing.T) {
	h := exceptionsHarness(t)
	h.rightClick("intranet.example")
	h.pick("Edit")
	h.mustHave("Edit exception")
	h.ui.Key(ui.Cmd, ui.KeyA)
	h.typeText("intranet.test")
	h.click("Save")
	h.settle()
	h.mustNotHave("Edit exception")
	if _, ok := h.exception("intranet.example"); ok {
		t.Fatal("the old value is still there")
	}
	x, ok := h.exception("intranet.test")
	if !ok || x.Dest.Kind != routing.DestDirect || x.Kind != routing.EntryDomainSuffix {
		t.Fatalf("edited exception = %+v", x)
	}
	if h.state().Exceptions[0].Value != "intranet.test" {
		t.Fatal("editing moved the exception")
	}
}

func TestExceptionDestinationAndSwitch(t *testing.T) {
	h := exceptionsHarness(t)
	// The anchor is the exception that follows the main exit.
	h.chooseDest("ads-tracker", "Follow main exit", "Direct")
	if x, _ := h.exception("ads-tracker"); x.Dest.Kind != routing.DestDirect {
		t.Fatalf("ads-tracker = %+v", x.Dest)
	}
	h.toggleOnLine("Slack", "Follow main exit")
	if x, _ := h.exception("Slack"); x.Enabled {
		t.Fatal("Slack should be off")
	}
	h.toggleOnLine("Slack", "Follow main exit")
	if x, _ := h.exception("Slack"); !x.Enabled {
		t.Fatal("Slack should be on again")
	}
}

func TestExceptionsAreMovedCopiedAndDeleted(t *testing.T) {
	h := exceptionsHarness(t)
	// The first exception cannot move up, so the one "Move" entry open is down.
	h.rightClick("intranet.example")
	h.pick("Move")
	st := h.state()
	if st.Exceptions[0].Value != "ads-tracker" || st.Exceptions[1].Value != "intranet.example" {
		t.Fatalf("order after moving down: %s, %s", st.Exceptions[0].Value, st.Exceptions[1].Value)
	}

	h.rightClick("Slack")
	h.pick("Copy")
	if got := h.ui.Clipboard(); got != "Slack" {
		t.Fatalf("copied %q", got)
	}

	h.rightClick("ads-tracker")
	h.pick("Delete")
	if _, ok := h.exception("ads-tracker"); ok {
		t.Fatal("the exception was not deleted")
	}
	h.mustNotHave("ads-tracker")
	if n := len(h.state().Exceptions); n != 2 {
		t.Fatalf("exceptions left: %d", n)
	}
}

func TestExceptionsEmptyCardTeaches(t *testing.T) {
	h := routingHarness(t, harnessOpt{setup: func(be *fixtureBackend, _ *Options) {
		be.arrange(func(st *routing.State) { st.Exceptions = nil })
	}})
	h.mustHave("My exceptions", "Paste a list", "Add")
	h.mustHave("A site going the wrong way? Right-click it in the connections list, or paste its domain here.")
}

func TestExceptionsEmptyCardTeachesInChinese(t *testing.T) {
	h := routingHarness(t, harnessOpt{lang: "zh", setup: func(be *fixtureBackend, _ *Options) {
		be.arrange(func(st *routing.State) { st.Exceptions = nil })
	}})
	h.mustHave("我的例外", "网站走错了？在连接列表里右键，或者在这里粘贴域名。")
}

// ------------------------------------------------------------- pasted lists

// A list as it is found on the web: the Clash syntax with a target on most
// lines, a type that is not supported, a name of an item of the plan and a
// target that is neither.
const pastedList = "DOMAIN-SUFFIX,example.com,DIRECT\n" +
	"IP-CIDR,10.0.0.0/8,REJECT,no-resolve\n" +
	"GEOIP,CN,DIRECT\n" +
	"DOMAIN-KEYWORD,tracker,Streaming\n" +
	"PROCESS-NAME,Updater,Somewhere\n" +
	"plain.example\n"

// streamingInSingapore makes the Streaming item go to a region, so that a
// line that names it can be told from one that takes the destination chosen
// for the list.
func streamingInSingapore(be *fixtureBackend, _ *Options) {
	be.arrange(func(st *routing.State) {
		if err := st.SetItemDest("b-streaming", routing.ToRegion("SG")); err != nil {
			panic(err)
		}
	})
}

func TestBatchPasteShowsWhatItUnderstoodBeforeAddingAnything(t *testing.T) {
	h := routingHarness(t, harnessOpt{setup: streamingInSingapore})
	h.click("My exceptions")
	before := len(h.state().Exceptions)
	h.click("Paste a list")
	h.mustHave("Paste a list", "Several exceptions at once", "Nothing is added until you confirm.")
	h.mustNotHave("entries recognized")

	h.paste(pastedList)
	h.mustHave("5 entries recognized",
		"Domain and subdomains  2", "IP address or range  1", "Domain keyword  1", "Program name  1",
		"“DIRECT”  →  Direct", "“REJECT”  →  Block", "“Streaming”  →  Singapore · auto",
		"“Somewhere”  →  the destination chosen below",
		"1 line was not understood", "Line 3", "GEOIP,CN,DIRECT", "GEOIP rules are not supported here",
		"Add 5 exceptions")

	// Nothing has been kept: the list is only shown until it is confirmed.
	if got := len(h.state().Exceptions); got != before || h.called("edit-routing") != 0 {
		t.Fatalf("the paste was kept before it was confirmed: %d exceptions, calls %v", got, h.be.called())
	}
}

func TestBatchPasteAddsEachLineWithItsOwnDestination(t *testing.T) {
	h := routingHarness(t, harnessOpt{setup: streamingInSingapore})
	h.click("My exceptions")
	before := len(h.state().Exceptions)
	h.click("Paste a list")
	h.paste(pastedList)
	// The destination for the lines that do not say: block them.
	h.chooseInDialog("Follow main exit", "Block")
	h.click("Add 5 exceptions")
	h.settle()
	h.mustNotHave("Several exceptions at once")
	h.mustHave("5 exceptions added")

	st := h.state()
	if len(st.Exceptions) != before+5 {
		t.Fatalf("exceptions: %d, want %d", len(st.Exceptions), before+5)
	}
	want := []struct {
		kind  routing.EntryKind
		value string
		dest  routing.DestKind
		code  string
	}{
		{routing.EntryDomainSuffix, "example.com", routing.DestDirect, ""},   // DIRECT
		{routing.EntryIPCIDR, "10.0.0.0/8", routing.DestReject, ""},          // REJECT
		{routing.EntryDomainKeyword, "tracker", routing.DestRegion, "SG"},    // the name of an item
		{routing.EntryProcessName, "Updater", routing.DestReject, ""},        // a target of no item: the chosen one
		{routing.EntryDomainSuffix, "plain.example", routing.DestReject, ""}, // no target: the chosen one
	}
	got := st.Exceptions[before:]
	for i, w := range want {
		g := got[i]
		if g.Kind != w.kind || g.Value != w.value || g.Dest.Kind != w.dest || g.Dest.Region != w.code || !g.Enabled {
			t.Errorf("added #%d = %+v, want %v %q → %v %s", i, g, w.kind, w.value, w.dest, w.code)
		}
	}
	// The card opens to show what came.
	h.mustHave("example.com", "10.0.0.0/8", "tracker", "Updater", "plain.example")
}

func TestBatchPasteWithNothingToAdd(t *testing.T) {
	h := exceptionsHarness(t)
	h.click("Paste a list")
	h.paste("GEOIP,CN,DIRECT\nMATCH,Proxy")
	h.mustHave("Nothing recognized yet", "2 lines were not understood", "GEOIP rules are not supported here", "MATCH rules are not supported here")
	h.mustNotHave("Add 0 exceptions")
	// The main button has nothing to do: Enter in the box adds nothing.
	h.enter()
	h.settle()
	if h.called("edit-routing") != 0 {
		t.Fatal("something was saved from a list that held nothing")
	}
}

func TestBatchPasteKeepsGoingWhenALineIsABadOne(t *testing.T) {
	h := exceptionsHarness(t)
	h.click("Paste a list")
	var b strings.Builder
	for i := 0; i < 9; i++ {
		b.WriteString("DOMAIN-REGEX,(unclosed\n")
	}
	b.WriteString("ok.example\n")
	h.paste(b.String())
	h.mustHave("1 entry recognized", "9 lines were not understood", "and 3 more")
}

func TestBatchPasteInChinese(t *testing.T) {
	h := routingHarness(t, harnessOpt{lang: "zh"})
	h.click("我的例外")
	h.click("批量粘贴")
	h.paste(pastedList)
	h.mustHave("识别出 5 条", "有 1 行没能识别", "“DIRECT”  →  直连", "“REJECT”  →  拦截", "“Somewhere”  →  下面选择的走向", "添加 5 条例外")
}
