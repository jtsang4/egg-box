package gui

import (
	"context"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"eggbox/internal/routing"
)

// chooseResolver opens the resolver menu of the dialog, which shows now, and
// chooses the way whose name starts with first.
func (h *harness) chooseResolver(now, first string) {
	h.t.Helper()
	h.tabTo(now)
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Advance(300 * time.Millisecond)
	h.pick(first)
}

// typeServer puts text in the resolver's server field and presses Return.
func (h *harness) typeServer(text string) {
	h.t.Helper()
	if err := h.ui.Click(h.app.t("resolver.placeholder")); err != nil {
		h.t.Fatalf("the server field: %v; have %s", err, h.digest())
	}
	h.ui.Key(ui.Cmd, ui.KeyA)
	h.typeText(text)
	h.enter()
	h.settle()
}

func TestItemResolverIsChosenInTheItemsDetail(t *testing.T) {
	h := routingHarness(t, harnessOpt{})
	h.app.openItem("b-ai")
	h.settle()
	h.mustHave("Look up names", "Names that go direct use the domestic DNS, all others the remote DNS.")

	h.chooseResolver("Default", "At")
	if got := h.row("b-ai").Item.Resolver; got != "exit" {
		t.Fatalf("resolver = %q, want exit", got)
	}
	h.mustHave("At the exit", h.app.t("resolver.hint.exit"))

	// A server is saved when it is one; until then nothing changes.
	h.chooseResolver("At the exit", "With")
	h.mustHave(h.app.t("resolver.hint.server"))
	h.typeServer("gopher://nope")
	h.mustHave(h.app.t("err.resolver_invalid"))
	if got := h.row("b-ai").Item.Resolver; got != "exit" {
		t.Fatalf("an unusable server was saved: %q", got)
	}
	h.typeServer(" 10.0.0.53 ")
	if got := h.row("b-ai").Item.Resolver; got != "10.0.0.53" {
		t.Fatalf("resolver = %q", got)
	}
	h.mustNotHave(h.app.t("err.resolver_invalid"))

	// Back to the default: the choice laid over the plan goes.
	h.chooseResolver("With a DNS server", "Default")
	if got := h.row("b-ai").Item.Resolver; got != "" {
		t.Fatalf("resolver = %q", got)
	}
}

func TestBlockedItemHasNoResolver(t *testing.T) {
	h := routingHarness(t, harnessOpt{})
	h.app.openItem("b-ads")
	h.settle()
	h.mustNotHave("Look up names")
}

func TestExceptionDialogKeepsAndChangesTheResolver(t *testing.T) {
	h := exceptionsHarness(t)
	x, _ := h.exception("intranet.example")
	h.app.openException(&x)
	h.settle()
	h.mustHave("Look up names")
	h.chooseResolver("Default", "At")
	h.submitDialog("Save")
	if x, _ = h.exception("intranet.example"); x.Resolver != "exit" {
		t.Fatalf("resolver = %q", x.Resolver)
	}

	// Editing it again shows it, and saving something else keeps it.
	h.app.openException(&x)
	h.settle()
	h.mustHave("At the exit")
	h.submitDialog("Save")
	if x, _ = h.exception("intranet.example"); x.Resolver != "exit" {
		t.Fatalf("saving the dialog lost the resolver: %q", x.Resolver)
	}

	// A rule about a program looks nothing up.
	slack, _ := h.exception("Slack")
	h.app.openException(&slack)
	h.settle()
	h.mustNotHave("Look up names")
}

func TestExceptionsEditorReadsDNSOptions(t *testing.T) {
	h := exceptionsHarness(t)
	h.click("Edit as text")
	h.replaceText("DOMAIN-SUFFIX,intranet.example,DIRECT,dns=10.0.0.53\nDOMAIN-KEYWORD,ads-tracker,REJECT\nPROCESS-NAME,Slack,dns=exit\n")
	h.mustHave("1 line needs fixing before you can save", "PROCESS-NAME rules do not look anything up, so dns= does nothing here")
	h.click("Save")
	h.settle()
	if h.called("edit-routing") != 0 {
		t.Fatal("a line with a problem was saved")
	}
	h.replaceText("DOMAIN-SUFFIX,intranet.example,DIRECT,dns=10.0.0.53\nDOMAIN-KEYWORD,ads-tracker,REJECT\nPROCESS-NAME,Slack\nDOMAIN-SUFFIX,corp.example,dns=exit\n")
	h.mustHave("1 new · 1 changed", "“dns=10.0.0.53”  →  With 10.0.0.53", "“dns=exit”  →  At the exit")
	h.click("Save")
	h.settle()
	x, _ := h.exception("intranet.example")
	y, _ := h.exception("corp.example")
	if x.Resolver != "10.0.0.53" || y.Resolver != "exit" || y.Dest.Kind != routing.DestFollow {
		t.Fatalf("saved %+v and %+v", x, y)
	}
	// The text written out again says it.
	h.click("Edit as text")
	if got := h.editorText(); got != "DOMAIN-SUFFIX,intranet.example,DIRECT,dns=10.0.0.53\nDOMAIN-KEYWORD,ads-tracker,REJECT\nPROCESS-NAME,Slack\nDOMAIN-SUFFIX,corp.example,dns=exit\n" {
		t.Fatalf("text %q", got)
	}
}

// nodeIntranet adds an item of the person's own whose entries the fixture's
// exception about intranet.example already decides, in part.
func nodeIntranet(be *fixtureBackend, _ *Options) {
	be.arrange(func(st *routing.State) {
		if _, err := st.AddItem(routing.Item{Name: "Intranet, through a node", Dest: routing.Follow(), Enabled: true, Resolver: "exit",
			Sources: []routing.Source{routing.InlineSource([]routing.Entry{
				{Kind: routing.EntryDomainSuffix, Value: "wiki.intranet.example"},
				{Kind: routing.EntryIPCIDR, Value: "10.0.0.0/8"},
			})}}, ""); err != nil {
			panic(err)
		}
	})
}

func TestItemSaysWhatExceptionsDecideForIt(t *testing.T) {
	h := routingHarness(t, harnessOpt{setup: nodeIntranet})
	row, ok := h.itemNamed("Intranet, through a node")
	if !ok {
		t.Fatal("no item")
	}
	h.app.openItem(row.Item.ID)
	h.settle()
	h.mustHave("1 hand-written entry is decided by an exception, which says where it goes and how it is looked up:",
		"DOMAIN-SUFFIX,wiki.intranet.example (exception #1)", "At the exit")
	h.mustNotHave("IP-CIDR,10.0.0.0/8 (exception")
}

// internalDNS has the AI item's names looked up by an internal DNS server.
func internalDNS(be *fixtureBackend, _ *Options) {
	be.arrange(func(st *routing.State) {
		if err := st.SetItemResolver("b-ai", "10.0.0.53"); err != nil {
			panic(err)
		}
	})
}

// Adding an exception for a match that already has one changes where it goes
// and leaves how its names are looked up alone, unless the person changed that
// too.
func TestAddingAnExistingExceptionKeepsItsResolver(t *testing.T) {
	h := exceptionsHarness(t)
	_ = h.be.EditRouting(context.Background(), func(st *routing.State) error {
		x, _ := h.exception("intranet.example")
		return st.SetExceptionResolver(x.ID, "10.0.0.53")
	})
	h.settle()
	h.app.openException(nil)
	h.settle()
	h.typeText("intranet.example")
	h.chooseInDialog("Follow main exit", "Block")
	h.submitDialog("Add")
	x, _ := h.exception("intranet.example")
	if x.Dest.Kind != routing.DestReject || x.Resolver != "10.0.0.53" {
		t.Fatalf("exception = %+v", x)
	}
}

// An item of hand-written addresses only looks nothing up, so it offers no
// choice of how.
func TestAddressOnlyItemOffersNoResolver(t *testing.T) {
	h := routingHarness(t, harnessOpt{setup: func(be *fixtureBackend, _ *Options) {
		be.arrange(func(st *routing.State) {
			if _, err := st.AddItem(routing.Item{Name: "Direct range", Dest: routing.Direct(), Enabled: true,
				Sources: []routing.Source{routing.InlineSource([]routing.Entry{{Kind: routing.EntryIPCIDR, Value: "10.99.0.0/24"}})}}, ""); err != nil {
				panic(err)
			}
		})
	}})
	h.app.openItem(h.lastItem().ID)
	h.settle()
	h.mustHave("Direct range")
	h.mustNotHave("Look up names")
}

// The dialog opened for a match that has an exception shows that exception's
// resolver; changing the match makes a new exception, with what the dialog
// showed.
func TestExceptionForANewMatchGetsTheResolverShown(t *testing.T) {
	h := exceptionsHarness(t)
	_ = h.be.EditRouting(context.Background(), func(st *routing.State) error {
		x, _ := h.exception("intranet.example")
		return st.SetExceptionResolver(x.ID, "exit")
	})
	h.settle()
	h.app.openExceptionFor(routing.Entry{Kind: routing.EntryDomainSuffix, Value: "intranet.example"}, routing.Direct())
	h.settle()
	h.mustHave("At the exit")
	// The value field has the keyboard when the dialog opens.
	h.ui.Key(ui.Cmd, ui.KeyA)
	h.typeText("wiki.example")
	h.submitDialog("Add")
	y, ok := h.exception("wiki.example")
	if !ok || y.Resolver != "exit" {
		t.Fatalf("new exception = %+v (%v)", y, ok)
	}
	if x, _ := h.exception("intranet.example"); x.Resolver != "exit" {
		t.Fatalf("the other exception changed: %+v", x)
	}
}

// A resolver laid over an item of the plan is a choice the item's own
// "restore" takes back, like its destination.
func TestRestoringAnItemTakesBackItsResolver(t *testing.T) {
	h := routingHarness(t, harnessOpt{setup: internalDNS})
	if r := h.row("b-ai"); r.Item.Resolver != "10.0.0.53" || !r.Overridden {
		t.Fatalf("row = %+v", r)
	}
	h.rightClick("AI services")
	h.mustHave("Restore the plan’s choice")
	h.pick("Restore the plan’s choice")
	if r := h.row("b-ai"); r.Item.Resolver != "" || r.Overridden {
		t.Fatalf("after restoring: resolver %q, overridden %v", r.Item.Resolver, r.Overridden)
	}
}

// Blocking an exception for a while does not lose how its names are looked
// up once it goes somewhere again.
func TestBlockingAnExceptionKeepsItsResolver(t *testing.T) {
	h := exceptionsHarness(t)
	_ = h.be.EditRouting(context.Background(), func(st *routing.State) error {
		x, _ := h.exception("intranet.example")
		return st.SetExceptionResolver(x.ID, "exit")
	})
	h.settle()
	x, _ := h.exception("intranet.example")
	h.app.openException(&x)
	h.settle()
	h.chooseInDialog("Direct", "Block")
	h.mustNotHave("Look up names")
	h.submitDialog("Save")
	if x, _ = h.exception("intranet.example"); x.Dest.Kind != routing.DestReject || x.Resolver != "exit" {
		t.Fatalf("exception = %+v", x)
	}
}

// A server half typed and then hidden by blocking the exception is not
// usable; what the exception had stays.
func TestBlockingWithAnUnusableServerKeepsTheResolver(t *testing.T) {
	h := exceptionsHarness(t)
	_ = h.be.EditRouting(context.Background(), func(st *routing.State) error {
		x, _ := h.exception("intranet.example")
		return st.SetExceptionResolver(x.ID, "exit")
	})
	h.settle()
	h.app.openException(nil)
	h.settle()
	h.typeText("intranet.example")
	h.chooseResolver("Default", "With")
	if err := h.ui.Click(h.app.t("resolver.placeholder")); err != nil {
		t.Fatal(err)
	}
	h.typeText("x")
	h.chooseInDialog("Follow main exit", "Block")
	h.submitDialog("Add")
	if x, _ := h.exception("intranet.example"); x.Dest.Kind != routing.DestReject || x.Resolver != "exit" {
		t.Fatalf("exception = %+v", x)
	}
}
