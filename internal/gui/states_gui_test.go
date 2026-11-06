package gui

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"eggbox/internal/app"
	"eggbox/internal/routing"
)

// The states of the screens that the main tests do not come to: what the
// dashboard says of a main exit that is a region or a node that went away,
// who a node is kept for in the notes of the Nodes page, what the answer of a
// test says of the lookup it needed, how a failed update of an item is told
// from one that went on for days, and the small ones: the lists with nothing
// in them.

// ----------------------------------------------------------------- dashboard

func TestDashboardWithoutAConnectionSaysWhichRegionIsTheMainExit(t *testing.T) {
	h := newHarness(t, harnessOpt{h: 900, setup: func(be *fixtureBackend, _ *Options) {
		be.arrange(func(st *routing.State) {
			if err := st.SetMain(routing.ToRegion("JP")); err != nil {
				panic(err)
			}
		})
	}})
	h.disconnected()
	h.mustHave("Exit node", "Fastest in Japan", "The fastest node of the region is used")
	h.mustNotHave("The fastest node is chosen for you")
}

func TestDashboardWithoutAConnectionSaysWhenTheNodeChosenIsGone(t *testing.T) {
	h := newHarness(t, harnessOpt{h: 900, setup: func(be *fixtureBackend, o *Options) {
		seoulIsChosen(be, o)
		withoutNode(be, "Seoul 01")
	}})
	h.disconnected()
	h.mustHave("Exit node", "Seoul 01", "No longer in any subscription")
	h.mustNotHave("The fastest node is chosen for you")
}

func TestDashboardSaysWhenTheCoreHasNotPickedANodeYet(t *testing.T) {
	h := newHarness(t, harnessOpt{h: 900})
	h.mustNotHave("Waiting for the core to pick a node…")
	s := h.be.connectedSnapshot()
	s.Exits, s.Groups = nil, nil
	h.be.set_(s)
	h.settle()
	h.mustHave("Waiting for the core to pick a node…")
}

func TestDashboardSaysWhenNothingHasBeenReported(t *testing.T) {
	const nothing = "Nothing yet. Events from the service and your subscriptions will appear here."
	h := newHarness(t, harnessOpt{h: 900})
	h.mustNotHave(nothing)
	s := h.be.connectedSnapshot()
	s.Notices = nil
	h.be.set_(s)
	h.settle()
	h.mustHave("Recent activity", nothing)
}

func TestConnectionsWithoutAConnectionSayNothingIsFlowing(t *testing.T) {
	h := newHarness(t, harnessOpt{h: 900})
	h.disconnected()
	h.click("Connections")
	h.settle()
	h.mustHave("Nothing is flowing", "Connect to see every connection that goes through the proxy, live, with its route and traffic.")
}

// --------------------------------------------------------------------- nodes

func TestNodesSearchWithoutAnAnswerCanBeCleared(t *testing.T) {
	h := nodesHarness(t, nil)
	h.click("Search nodes")
	h.typeText("no-such-node-zzz")
	h.settle()
	h.mustHave("No matching nodes", "Try a different search, or clear the filters.", "Clear filters")
	h.mustNotHave("Osaka 01")
	h.click("Clear filters")
	h.settle()
	h.mustNotHave("No matching nodes")
	h.mustHave("Osaka 01")
}

func TestNodePickerSaysWhenNoNodeMatches(t *testing.T) {
	h := routingHarness(t, harnessOpt{})
	h.chooseDest("AI services", "Follow main exit", "A specific node…")
	h.mustHave("Choose a node")
	h.typeText("no-such-node-zzz")
	h.mustHave("No node matches")
}

func TestDestinationMenuSaysWhenThereAreNoRegionsYet(t *testing.T) {
	h := routingHarness(t, harnessOpt{setup: func(be *fixtureBackend, _ *Options) {
		be.nodes = nil
		be.refresh()
	}})
	h.clickOnLine("AI services", "Follow main exit")
	h.mustHave("Fastest in a region")
	h.typeAhead("Fastest")
	h.ui.Key(0, ui.KeyRight)
	h.ui.Advance(300 * time.Millisecond)
	h.mustHave("No regions yet")
	h.closeMenu()
}

// A choice of a node that went away is told, in the notes of the Nodes page,
// for what it was made: an exception by what it matches, the rest of the
// traffic by its name. Choosing again changes that one and no other.
func TestNodesBannerNamesTheExceptionAndTheRestOfTheTrafficAChosenNodeWasChosenFor(t *testing.T) {
	h := nodesHarness(t, func(be *fixtureBackend, _ *Options) {
		n, _ := be.node("Seoul 01")
		be.arrange(func(st *routing.State) {
			if err := st.SetExceptionDest(st.Exceptions[0].ID, routing.ToNode(n.ID())); err != nil {
				panic(err)
			}
			if err := st.SetFallback(routing.ToNode(n.ID())); err != nil {
				panic(err)
			}
		})
		withoutNode(be, "Seoul 01")
	})
	// What stands in is told where the core says so: for the rest of the traffic,
	// not for an exception, which the core has no exit of its own for.
	const ex = "The node “Seoul 01” you chose for “Exception intranet.example” is no longer in any subscription. A nearby node is used meanwhile."
	const rest = "The node “Seoul 01” you chose for “Everything else” is no longer in any subscription. “Tokyo 01” is used meanwhile."
	h.mustHave(ex, rest)
	h.clickOnLine(ex, "Choose again")
	h.mustHave("Choose a node", "Exception intranet.example")
	h.typeText("Osaka")
	h.tabTo("Osaka 01")
	h.enter()
	h.settle()
	x, _ := h.exception("intranet.example")
	if x.Dest.Kind != routing.DestNode || x.Dest.Node == nil || x.Dest.Node.Name != "Osaka 01" {
		t.Fatalf("the exception goes %+v, want Osaka 01", x.Dest)
	}
	if f := h.state().View().Fallback; f.Kind != routing.DestNode || f.Node == nil || f.Node.Name != "Seoul 01" {
		t.Fatalf("the rest of the traffic goes %+v, want what it had", f)
	}
	h.mustNotHave(ex)
	h.mustHave(rest)
}

func TestNodesSaysHowManyMoreItemsGoThroughNodes(t *testing.T) {
	enabled := 0
	h := nodesHarness(t, func(be *fixtureBackend, _ *Options) {
		be.arrange(func(st *routing.State) {
			for _, r := range st.View().Rows {
				if err := st.SetItemDest(r.Item.ID, routing.ToRegion("JP")); err != nil {
					panic(err)
				}
				if r.Item.Enabled {
					enabled++
				}
			}
		})
	})
	if enabled <= 8 {
		t.Fatalf("only %d items are on; the row of what uses the nodes shows 8", enabled)
	}
	h.mustHave("Used by items", "+"+strconv.Itoa(enabled-8)+" more")
}

// ------------------------------------------------------------------- routing

func TestRoutingSaysWhenThePlanHasNoItems(t *testing.T) {
	h := routingHarness(t, harnessOpt{setup: func(be *fixtureBackend, _ *Options) {
		be.arrange(func(st *routing.State) {
			if _, err := st.SaveAsMine("Bare"); err != nil {
				panic(err)
			}
			for _, r := range st.View().Rows {
				if err := st.DeleteItem(r.Item.ID); err != nil {
					panic(err)
				}
			}
		})
	}})
	h.mustHave("No items yet", "Add an item to send one kind of traffic somewhere specific: a service from the catalog, a rule file, or a few rules you write yourself.")
	h.mustNotHave("AI services")
}

func TestRoutingSaysWhereAPlanOfYourOwnCameFrom(t *testing.T) {
	h := routingHarness(t, harnessOpt{setup: func(be *fixtureBackend, _ *Options) {
		be.arrange(func(st *routing.State) {
			p, err := st.SaveAsMine("Bare")
			if err != nil {
				panic(err)
			}
			p.Origin = ""
		})
	}})
	h.mustHave("Your own plan. Only you change it.")
	h.mustNotHave("Your own plan, made from")
}

func TestAnItemSaysWhenItsLastUpdateFailedJustNow(t *testing.T) {
	gh := routing.GeoSite("github")
	h := routingHarness(t, harnessOpt{setup: func(be *fixtureBackend, _ *Options) {
		be.lately[gh] = true
		be.refresh()
	}})
	h.mustHave("Update failed")
	h.mustNotHave("Not updated for")
	h.click("GitHub")
	h.mustHave("The last update failed", "HTTP 503")
}

func TestAnItemOfAnotherProjectIsAddedAsASourceFromALink(t *testing.T) {
	h := routingHarness(t, harnessOpt{})
	h.click("Telegram")
	h.click("Add source")
	h.click("A link")
	h.typeText(ruleLink)
	h.enter()
	h.settle()
	h.mustHave("Recognized: ", "Add as a source")
	h.mustNotHave("Use this file")
	// Reading is not keeping.
	if h.be.took("keep-rule-set") || len(h.row("b-telegram").Item.Sources) != 2 {
		t.Fatalf("the file was kept before it was chosen: %v", h.be.called())
	}
	h.click("Add as a source")
	h.settle()
	h.mustNotHave("Add a source")
	if !h.be.took("keep-rule-set " + ruleLink) {
		t.Fatalf("the file was not kept: %v", h.be.called())
	}
	row := h.row("b-telegram")
	if len(row.Item.Sources) != 3 || len(row.Extra) != 1 || row.Item.Sources[2].URL != ruleLink {
		t.Fatalf("Telegram has %d sources (%d extra), the last %+v", len(row.Item.Sources), len(row.Extra), row.Item.Sources[len(row.Item.Sources)-1])
	}

	// The same file again is refused, and says why.
	h.click("Add source")
	h.click("A link")
	h.typeText(ruleLink)
	h.enter()
	h.settle()
	h.click("Add as a source")
	h.settle()
	h.mustHave("Add a source", "The item already has this source.")
	if got := len(h.row("b-telegram").Item.Sources); got != 3 {
		t.Fatalf("Telegram has %d sources after the same file was added again", got)
	}
}

// ------------------------------------------------------------------ test URL

func TestTestURLSaysWhichAddressesTheNameWasLookedUpAs(t *testing.T) {
	h := routingHarness(t, harnessOpt{})
	h.click("Test a URL")
	h.typeText("https://www.lookedup.example/")
	h.enter()
	h.settle()
	h.mustHave("Looked up: 203.0.113.5, 203.0.113.6, 203.0.113.7, …",
		"2 items are waiting for their rule files and could not take part.")
	h.mustNotHave("Some rules are about addresses, so every name no rule lists is looked up first")
}

// In Global the core sends everything through the main exit and in Direct
// nothing, before it looks at a single item or exception: the answer says so,
// and offers no exception, which would decide nothing until the mode is Rule.
func TestTestURLSaysWhenTheRoutingModeDecides(t *testing.T) {
	const note = "Your items and exceptions decide again when the mode is Rule. You can change it on the Dashboard."
	h := routingHarness(t, harnessOpt{})
	ask := func() {
		h.click("Routing")
		h.click("Test a URL")
		h.typeText("https://github.com/jtsang4")
		h.enter()
		h.settle()
	}
	h.click("Dashboard")
	h.click("Global")
	if got := h.be.Snapshot().Status.GetMode(); got != "Global" {
		t.Fatalf("the mode is %q", got)
	}
	ask()
	h.mustHave("The routing mode (Global or Direct) decides", "Follow main exit", "Right now it leaves through Tokyo 01", note)
	h.mustNotHave("It matches “GitHub”", "From now on, send")
	h.click("Done")

	h.click("Dashboard")
	h.click("Direct")
	ask()
	h.mustHave("The routing mode (Global or Direct) decides", "Direct", note)
	h.mustNotHave("It matches “GitHub”", "From now on, send", "Right now it leaves through")
	h.click("Done")

	h.click("Dashboard")
	h.click("Rule")
	ask()
	h.mustHave("It matches “GitHub”", "From now on, send")
	h.mustNotHave("The routing mode (Global or Direct) decides", note)
}

// While rule files are being downloaded the page says so, where it otherwise
// says in what order the items are tried.
func TestRoutingSaysWhenRulesAreBeingUpdated(t *testing.T) {
	h := routingHarness(t, harnessOpt{})
	h.mustHave("Tried from top to bottom")
	h.mustNotHave("Updating rules…")
	busy := *h.be.Snapshot()
	busy.Plan.Updating = true
	h.be.set_(&busy)
	h.settle()
	h.mustHave("Updating rules…")
	h.mustNotHave("Tried from top to bottom")
	idle := busy
	idle.Plan.Updating = false
	h.be.set_(&idle)
	h.settle()
	h.mustHave("Tried from top to bottom")
	h.mustNotHave("Updating rules…")
}

// A rule file that has not come yet is named as such on the page of its item,
// and only that one.
func TestItemDialogSaysWhichFileIsNotDownloadedYet(t *testing.T) {
	h := routingHarness(t, harnessOpt{setup: func(be *fixtureBackend, _ *Options) {
		be.missing[routing.GeoSite("disney")] = true
		be.refresh()
	}})
	h.click("Streaming")
	h.mustHave("geosite: netflix", "geosite: disney", "Not downloaded yet")
	h.click("Done")
	h.click("GitHub")
	h.mustHave("geosite: github")
	h.mustNotHave("Not downloaded yet")
}

// A file that was read and holds nothing the engine can use is not shown as a
// list of zeros.
func TestItemDialogSaysWhenAFileHoldsNoRule(t *testing.T) {
	h := routingHarness(t, harnessOpt{})
	empty := *h.be.Snapshot()
	sources := map[string]app.SourceStatus{}
	for url, ss := range empty.Plan.Sources {
		ss.Domain, ss.IP, ss.Other = 0, 0, 0
		sources[url] = ss
	}
	empty.Plan.Sources = sources
	h.be.set_(&empty)
	h.settle()
	h.click("GitHub")
	h.mustHave("geosite: github", "sing-box rules (binary) · No rules in it")
	h.mustNotHave("0 domains")
}

// A plan whose subscription is no longer there says so. (The service turns such
// a plan into one of the person's own; this is the word for the moment before
// the page knows.)
func TestRoutingSaysWhenTheSubscriptionOfThePlanIsGone(t *testing.T) {
	h := routingHarness(t, harnessOpt{})
	h.click("Recommended")
	h.pick("Aurora Cloud’s plan")
	h.mustHave("Comes with “Aurora Cloud”")
	if err := h.be.RemoveSubscription(context.Background(), "sub-aurora"); err != nil {
		t.Fatal(err)
	}
	h.settle()
	h.mustHave("The subscription it came with was removed")
	h.mustNotHave("Comes with “Aurora Cloud”")
}

// The check of a pasted link takes a while: the dialog says what it is doing.
// (The harness runs every job to its end at once, so the wait is put on by
// hand and the answer shown is the one the dialog gives while it lasts.)
func TestAddItemSaysItIsDownloadingWhileItChecksALink(t *testing.T) {
	h := addItemHarness(t)
	h.click("A link")
	h.typeText("https://rules.example.invalid/work.list")
	d := h.app.dlg.(*addDialog)
	d.pvBusy, d.pvFor = true, d.link
	h.settle()
	h.mustHave("Downloading and reading the file…")
	d.pvBusy, d.pvFor = false, ""
	h.settle()
	h.mustNotHave("Downloading and reading the file…")
}

// A region that was chosen as the main exit is on the page of nodes whether or
// not it has a node right now, and says so, where the others say how many nodes
// they pick from.
func TestNodesPageSaysWhenTheRegionChosenHasNoNodesRightNow(t *testing.T) {
	h := nodesHarness(t, func(be *fixtureBackend, _ *Options) {
		be.arrange(func(st *routing.State) {
			if err := st.SetMain(routing.ToRegion("BR")); err != nil {
				panic(err)
			}
		})
	})
	h.mustHave("Fastest in Brazil", "No nodes right now", "Fastest in Hong Kong", "Fastest of 3 nodes")
	h.mustNotHave("Fastest of 0 nodes")
}

// With no item in the plan there is nothing a node can be used for.
func TestNodeMenuHasNoItemToUseTheNodeForInAnEmptyPlan(t *testing.T) {
	h := nodesHarness(t, func(be *fixtureBackend, _ *Options) {
		be.arrange(func(st *routing.State) {
			if _, err := st.SaveAsMine("Bare"); err != nil {
				panic(err)
			}
			for _, r := range st.View().Rows {
				if err := st.DeleteItem(r.Item.ID); err != nil {
					panic(err)
				}
			}
		})
	})
	h.rightClick("Osaka 01")
	h.mustHave("Use for an item…")
	h.pick("Use#1")
	h.mustHave("No items yet")
	h.mustNotHave("Telegram")
}

// -------------------------------------------------------------------- words

// Times are worded from now, in the unit that fits, with one form for one and
// one for more, in either language.
func TestTimesAreWordedFromNow(t *testing.T) {
	en := newHarness(t, harnessOpt{}).app
	zh := newHarness(t, harnessOpt{lang: "zh"}).app
	base := time.Date(2026, 11, 5, 14, 30, 0, 0, time.UTC)
	en.now, zh.now = base, base
	cases := []struct {
		d     time.Duration
		ago   [2]string // en, zh
		until [2]string
	}{
		{10 * time.Second, [2]string{"Just now", "刚刚"}, [2]string{"Ends in 1 minute", "1 分钟后到期"}},
		{44 * time.Second, [2]string{"Just now", "刚刚"}, [2]string{"Ends in 1 minute", "1 分钟后到期"}},
		{45 * time.Second, [2]string{"1 minute ago", "1 分钟前"}, [2]string{"Ends in 1 minute", "1 分钟后到期"}},
		{5 * time.Minute, [2]string{"5 minutes ago", "5 分钟前"}, [2]string{"Ends in 5 minutes", "5 分钟后到期"}},
		{89 * time.Minute, [2]string{"89 minutes ago", "89 分钟前"}, [2]string{"Ends in 89 minutes", "89 分钟后到期"}},
		{90 * time.Minute, [2]string{"2 hours ago", "2 小时前"}, [2]string{"Ends in 2 hours", "2 小时后到期"}},
		{35 * time.Hour, [2]string{"35 hours ago", "35 小时前"}, [2]string{"Ends in 35 hours", "35 小时后到期"}},
		{36 * time.Hour, [2]string{"2 days ago", "2 天前"}, [2]string{"Ends in 2 days", "2 天后到期"}},
		{4 * 24 * time.Hour, [2]string{"4 days ago", "4 天前"}, [2]string{"Ends in 4 days", "4 天后到期"}},
	}
	for _, c := range cases {
		if got := en.ago(base.Add(-c.d)); got != c.ago[0] {
			t.Errorf("en: %v ago is worded %q, want %q", c.d, got, c.ago[0])
		}
		if got := zh.ago(base.Add(-c.d)); got != c.ago[1] {
			t.Errorf("zh: %v ago is worded %q, want %q", c.d, got, c.ago[1])
		}
		if got := en.until(base.Add(c.d)); got != c.until[0] {
			t.Errorf("en: in %v is worded %q, want %q", c.d, got, c.until[0])
		}
		if got := zh.until(base.Add(c.d)); got != c.until[1] {
			t.Errorf("zh: in %v is worded %q, want %q", c.d, got, c.until[1])
		}
	}
	if got := en.ago(time.Time{}); got != "Never" {
		t.Errorf("a time that never was is worded %q", got)
	}
	if got, zhGot := en.until(base.Add(-time.Second)), zh.until(base.Add(-time.Second)); got != "Expired" || zhGot != "已到期" {
		t.Errorf("a time that has passed is worded %q and %q", got, zhGot)
	}
}

// Every format of a rule file that can be read has a name for the preview of
// an address, in both languages: the names are found by the format's own, so
// a format that is added without one would show as its key.
func TestEveryRuleFormatHasAName(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "../rulefmt/types.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var formats []string
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		if id, ok := spec.Type.(*ast.Ident); !ok || id.Name != "Format" {
			return true
		}
		for _, v := range spec.Values {
			if lit, ok := v.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if s, err := strconv.Unquote(lit.Value); err == nil {
					formats = append(formats, s)
				}
			}
		}
		return true
	})
	if len(formats) < 9 {
		t.Fatalf("found only %d formats (%v); have they moved?", len(formats), formats)
	}
	for _, f := range formats {
		for lang, cat := range map[string]map[string]string{"en": catalogEN, "zh": catalogZH} {
			if text, ok := cat["fmt."+f]; !ok || text == "" {
				t.Errorf("[%s] the format %q has no name", lang, f)
			}
		}
	}
}
