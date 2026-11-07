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
	h.pick("Copy as rule")
	if got := h.ui.Clipboard(); got != "PROCESS-NAME,Slack" {
		t.Fatalf("copied %q", got)
	}
	h.rightClick("intranet.example")
	h.pick("Copy as rule")
	if got := h.ui.Clipboard(); got != "DOMAIN-SUFFIX,intranet.example,DIRECT" {
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
	h.mustHave("My exceptions", "Edit as text", "Add")
	h.mustHave("A site going the wrong way? Right-click it in the connections list, or paste its domain here.")
}

func TestExceptionsEmptyCardTeachesInChinese(t *testing.T) {
	h := routingHarness(t, harnessOpt{lang: "zh", setup: func(be *fixtureBackend, _ *Options) {
		be.arrange(func(st *routing.State) { st.Exceptions = nil })
	}})
	h.mustHave("我的例外", "网站走错了？在连接列表里右键，或者在这里粘贴域名。")
}

// ------------------------------------------------------- exceptions as text

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
// line that names it can be told from one that names nothing.
func streamingInSingapore(be *fixtureBackend, _ *Options) {
	be.arrange(func(st *routing.State) {
		if err := st.SetItemDest("b-streaming", routing.ToRegion("SG")); err != nil {
			panic(err)
		}
	})
}

// openEditorWith opens the exceptions as text, with text in place of them.
func openEditorWith(a *App, text string) {
	a.openExceptionsEditor()
	a.dlg.(*exceptionsEditor).text = text
}

// editorText is what the exceptions editor that is open holds.
func (h *harness) editorText() string {
	h.t.Helper()
	d, ok := h.app.dlg.(*exceptionsEditor)
	if !ok || !h.app.dlgOpen {
		h.t.Fatal("the exceptions editor is not open")
	}
	return d.text
}

// replaceText puts text in place of everything in the editor's text box.
func (h *harness) replaceText(text string) {
	h.t.Helper()
	if err := h.ui.Click(h.app.t("exedit.placeholder")); err != nil {
		h.t.Fatalf("the text box: %v", err)
	}
	h.ui.Key(ui.Cmd, ui.KeyA)
	h.paste(text)
	h.settle()
}

func TestExceptionsEditorShowsTheListAsText(t *testing.T) {
	h := exceptionsHarness(t)
	h.click("Edit as text")
	h.mustHave("Edit exceptions as text", "One per line; the first that matches wins", "Nothing changed yet", "3 exceptions",
		"Domain and subdomains  1", "“DIRECT”  →  Direct", "“REJECT”  →  Block")
	want := "DOMAIN-SUFFIX,intranet.example,DIRECT\nDOMAIN-KEYWORD,ads-tracker,REJECT\nPROCESS-NAME,Slack\n"
	if got := h.editorText(); got != want {
		t.Fatalf("text %q, want %q", got, want)
	}
	// Nothing to save until something changes.
	h.click("Save")
	h.settle()
	if h.called("edit-routing") != 0 || !h.app.dlgOpen {
		t.Fatal("an unchanged text was saved")
	}
}

func TestExceptionsEditorReplacesTheList(t *testing.T) {
	h := exceptionsHarness(t)
	before := h.state().Exceptions
	h.click("Edit as text")
	h.replaceText("DOMAIN-KEYWORD,ads-tracker,DIRECT\n" +
		"DOMAIN-SUFFIX,intranet.example,DIRECT\n" +
		"DOMAIN-SUFFIX,new.example,REGION:JP\n" +
		"# DOMAIN,off.example,REJECT\n")
	h.mustHave("2 new · 1 removed · 1 changed · order changed", "4 exceptions", "“REGION:JP”  →  Japan",
		"These will be removed:", "PROCESS-NAME,Slack")
	if h.called("edit-routing") != 0 {
		t.Fatal("the text was saved before it was confirmed")
	}
	h.click("Save")
	h.settle()
	h.mustNotHave("Edit exceptions as text")
	h.mustHave("Exceptions saved")

	st := h.state()
	type row struct {
		value string
		dest  string
		on    bool
	}
	var got []row
	for _, x := range st.Exceptions {
		got = append(got, row{x.Value, x.Dest.String(), x.Enabled})
	}
	want := []row{{"ads-tracker", "direct", true}, {"intranet.example", "direct", true}, {"new.example", "region:JP", true}, {"off.example", "reject", false}}
	if len(got) != len(want) {
		t.Fatalf("exceptions %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("exception #%d = %+v, want %+v", i+1, got[i], want[i])
		}
	}
	// What stays keeps its identity.
	if st.Exceptions[0].ID != before[1].ID || st.Exceptions[1].ID != before[0].ID {
		t.Fatal("exceptions that stayed got new ids")
	}
	// The card shows the new list.
	h.mustHave("new.example", "off.example")
	h.mustNotHave("Slack")
}

// nodesOfTheSameName pins one exception to each of two nodes called "Tokyo 01".
func nodesOfTheSameName(be *fixtureBackend, o *Options) {
	twinOfTokyo("")(be, o)
	tokyo, _ := be.node("Tokyo 01")
	var twin routing.NodeID
	for _, n := range be.nodes {
		if n.Node.Name == "Tokyo 01" && n.SubID == "sub-nimbus" {
			twin = n.ID()
		}
	}
	be.arrange(func(st *routing.State) {
		st.Exceptions = nil
		for _, x := range []routing.Exception{
			{Kind: routing.EntryDomainSuffix, Value: "abema.tv", Dest: routing.ToNode(tokyo.ID()), Enabled: true},
			{Kind: routing.EntryDomainSuffix, Value: "tver.jp", Dest: routing.ToNode(twin), Enabled: true},
		} {
			if _, err := st.AddException(x); err != nil {
				panic(err)
			}
		}
	})
}

func TestExceptionsEditorTellsNodesOfTheSameNameApart(t *testing.T) {
	h := routingHarness(t, harnessOpt{setup: nodesOfTheSameName})
	h.click("My exceptions")
	h.click("Edit as text")
	text := h.editorText()
	if want := "DOMAIN-SUFFIX,abema.tv,NODE:Aurora Cloud/Tokyo 01\nDOMAIN-SUFFIX,tver.jp,NODE:Nimbus Net/Tokyo 01\n"; text != want {
		t.Fatalf("text %q, want %q", text, want)
	}

	// A new line that names the node by its name alone cannot be saved.
	h.replaceText(text + "DOMAIN-SUFFIX,nhk.jp,NODE:Tokyo 01\n")
	h.mustHave("1 line needs fixing before you can save", "Line 3",
		"More than one node is called “Tokyo 01”: write NODE:Aurora Cloud/Tokyo 01 or NODE:Nimbus Net/Tokyo 01")
	h.click("Save")
	h.settle()
	if h.called("edit-routing") != 0 {
		t.Fatal("a line with an ambiguous node was saved")
	}

	// Written with its subscription, it is the node of that subscription.
	h.replaceText(text + "DOMAIN-SUFFIX,nhk.jp,NODE:Nimbus Net/Tokyo 01\n")
	h.mustHave("1 new")
	h.click("Save")
	h.settle()
	st := h.state()
	if len(st.Exceptions) != 3 {
		t.Fatalf("exceptions %+v", st.Exceptions)
	}
	for _, w := range []struct{ value, sub string }{{"abema.tv", "sub-aurora"}, {"tver.jp", "sub-nimbus"}, {"nhk.jp", "sub-nimbus"}} {
		x, ok := h.exception(w.value)
		if !ok || x.Dest.Node == nil || x.Dest.Node.Sub != w.sub || x.Dest.Node.Name != "Tokyo 01" {
			t.Errorf("%s goes to %+v, want Tokyo 01 of %s", w.value, x.Dest, w.sub)
		}
	}
}

func TestExceptionsEditorSaysWhatIsWrongAndHelpsWithUnknownDestinations(t *testing.T) {
	h := routingHarness(t, harnessOpt{setup: streamingInSingapore})
	h.click("My exceptions")
	h.click("Edit as text")
	h.replaceText(pastedList)
	h.mustHave("2 lines need fixing before you can save",
		"Line 3", "GEOIP,CN,DIRECT", "GEOIP rules are not supported here",
		"Line 5", "“Somewhere” is not a destination",
		"“Streaming”  →  Singapore · auto", "These will be removed:")
	h.click("Follow the main exit where the destination is unknown")
	h.settle()
	if got := strings.Split(h.editorText(), "\n")[4]; got != "PROCESS-NAME,Updater" {
		t.Fatalf("line 5 is now %q", got)
	}
	h.mustNotHave("“Somewhere” is not a destination")
	h.mustHave("1 line needs fixing before you can save", "GEOIP rules are not supported here")
	if h.called("edit-routing") != 0 {
		t.Fatal("something was saved")
	}
}

func TestExceptionsEditorCarriesTheTextOverWhenTheListChangedMeanwhile(t *testing.T) {
	h := exceptionsHarness(t)
	h.click("Edit as text")
	h.replaceText(strings.Replace(h.editorText(), "PROCESS-NAME,Slack", "PROCESS-NAME,Slack,DIRECT", 1) + "DOMAIN-SUFFIX,mine.example,DIRECT\n")
	h.mustHave("1 new · 1 changed")
	// Meanwhile an exception comes from the connections list, and another
	// one, whose line was not touched, is switched off.
	h.be.arrange(func(st *routing.State) {
		if _, err := st.UpsertException(routing.Entry{Kind: routing.EntryDomain, Value: "meanwhile.example"}, routing.Direct()); err != nil {
			panic(err)
		}
		if err := st.SetExceptionEnabled(st.Exceptions[0].ID, false); err != nil {
			panic(err)
		}
	})
	h.settle()
	h.click("Save")
	h.settle()
	// Nothing was saved over the list; the text now holds both.
	h.mustHave("Edit exceptions as text", "The exceptions were changed elsewhere while you were editing.", "1 new · 1 changed")
	want := "# DOMAIN-SUFFIX,intranet.example,DIRECT\nDOMAIN-KEYWORD,ads-tracker,REJECT\nPROCESS-NAME,Slack,DIRECT\n" +
		"DOMAIN-SUFFIX,mine.example,DIRECT\nDOMAIN,meanwhile.example,DIRECT\n"
	if got := h.editorText(); got != want {
		t.Fatalf("text %q, want %q", got, want)
	}
	if _, ok := h.exception("mine.example"); ok {
		t.Fatal("the text was saved over a list that had changed")
	}
	// Typing clears the note.
	h.replaceText(want + "DOMAIN-SUFFIX,more.example\n")
	h.mustNotHave("The exceptions were changed elsewhere while you were editing.")
	h.click("Save")
	h.settle()
	h.mustNotHave("Edit exceptions as text")
	for _, v := range []string{"meanwhile.example", "mine.example", "more.example"} {
		if _, ok := h.exception(v); !ok {
			t.Errorf("%s is missing", v)
		}
	}
	if x, _ := h.exception("intranet.example"); x.Enabled {
		t.Error("the change made meanwhile was undone")
	}
	if x, _ := h.exception("Slack"); x.Dest.Kind != routing.DestDirect {
		t.Error("the change made in the text was lost")
	}
}

func TestExceptionsEditorInChinese(t *testing.T) {
	h := routingHarness(t, harnessOpt{lang: "zh"})
	h.click("我的例外")
	h.click("文本编辑")
	h.mustHave("以文本编辑例外", "每行一条，越靠前越先匹配", "还没有改动", "共 3 条例外", "“DIRECT”  →  直连")
	h.replaceText("DOMAIN-SUFFIX,intranet.example,Proxy\nDOMAIN-SUFFIX,intranet.example,DIRECT\n")
	h.mustHave("有 2 行需要处理，处理好才能保存", "不认识的去向“Proxy”", "和第 1 行重复", "将删除：", "不认识的去向改为跟随主出口")
}
