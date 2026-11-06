package gui

import (
	"testing"

	"eggbox/internal/agentapi"
	"eggbox/internal/agentclient"
	"eggbox/internal/routing"
)

// ---------------------------------------------------------------- test a URL

func TestTestURLSaysWhereAnAddressGoesAndWhy(t *testing.T) {
	h := routingHarness(t, harnessOpt{})
	h.click("Test a URL")
	h.mustHave("Test a URL", "See where an address goes, and why", "Type a web address, a domain or an IP address")
	h.typeText("https://github.com/jtsang4")
	h.enter()
	h.settle()
	h.mustHave("https://github.com/jtsang4", "It matches “GitHub”", "Matched by MetaCubeX", "Follow main exit",
		"Right now it leaves through Tokyo 01", "From now on, send", "github.com and subdomains", "Main exit", "Other…")
	h.mustNotHave("Type a web address, a domain or an IP address")
	if !h.be.took("test-url https://github.com/jtsang4") {
		t.Fatalf("the address was not tested as typed: %v", h.be.called())
	}
}

func TestTestURLMakesAnExceptionOfTheResult(t *testing.T) {
	h := routingHarness(t, harnessOpt{})
	n := len(h.state().Exceptions)
	h.click("Test a URL")
	h.typeText("github.com")
	h.enter()
	h.settle()
	h.tabTo("Direct")
	h.enter()
	h.settle()

	st := h.state()
	if len(st.Exceptions) != n+1 {
		t.Fatalf("exceptions: %d, want %d", len(st.Exceptions), n+1)
	}
	x, ok := h.exception("github.com")
	if !ok || x.Kind != routing.EntryDomainSuffix || x.Dest.Kind != routing.DestDirect || !x.Enabled {
		t.Fatalf("the exception made is %+v", x)
	}
	// The test runs again, and now an exception of the person's decides.
	h.mustHave("It matches your exception #4", "Your entry: Domain and subdomains github.com")
	h.mustNotHave("Right now it leaves through")

	// Another choice for the same address changes that exception, it does not add another.
	h.tabTo("Block")
	h.enter()
	h.settle()
	st = h.state()
	if len(st.Exceptions) != n+1 {
		t.Fatalf("a second choice for the same address added an exception: %d", len(st.Exceptions))
	}
	if x, _ := h.exception("github.com"); x.Dest.Kind != routing.DestReject {
		t.Fatalf("the exception is %+v, want it blocked", x)
	}
}

func TestTestURLMoreChoicesAreInTheMenu(t *testing.T) {
	h := routingHarness(t, harnessOpt{})
	h.click("Test a URL")
	h.typeText("api.openai.com")
	h.enter()
	h.settle()
	h.mustHave("It matches “AI services”")
	// The way to a region: the fastest node of one.
	h.tabTo("Other…")
	h.enter()
	h.pick("Fastest", "Japan")
	x, ok := h.exception("openai.com")
	if !ok || x.Dest.Kind != routing.DestRegion || x.Dest.Region != "JP" {
		t.Fatalf("the exception made is %+v, want one that goes to the fastest node in Japan", x)
	}
	h.mustHave("It matches your exception #4", "Japan · auto")
}

func TestTestURLNarrowsTheException(t *testing.T) {
	h := routingHarness(t, harnessOpt{})
	h.click("Test a URL")
	h.typeText("www.github.com")
	h.enter()
	h.settle()
	// The whole domain is what is offered first; the name itself is narrower.
	h.mustHave("github.com and subdomains")
	h.tabTo("github.com and subdomains")
	h.enter()
	h.pick("only")
	h.mustHave("only www.github.com")
	h.tabTo("Block")
	h.enter()
	h.settle()
	if x, ok := h.exception("www.github.com"); !ok || x.Kind != routing.EntryDomain || x.Dest.Kind != routing.DestReject {
		t.Fatalf("the exception made is %+v, want www.github.com blocked", x)
	}
}

func TestTestURLAnIPAddressOffersTheAddress(t *testing.T) {
	h := routingHarness(t, harnessOpt{})
	h.click("Test a URL")
	h.typeText("93.184.216.34")
	h.enter()
	h.settle()
	h.mustHave("No item above matches, so it falls to everything else", "address 93.184.216.34/32")
	h.tabTo("Direct")
	h.enter()
	h.settle()
	if x, ok := h.exception("93.184.216.34/32"); !ok || x.Kind != routing.EntryIPCIDR || x.Dest.Kind != routing.DestDirect {
		t.Fatalf("the exception made is %+v", x)
	}
}

// A name that leads nowhere is turned away where the plan has rules about
// addresses; the answer says so, and why, in either language.
func TestTestURLSaysWhenANameLeadsNowhereAndIsRefused(t *testing.T) {
	for _, tc := range []struct {
		lang  string
		click string
		want  []string
	}{
		{"en", "Test a URL", []string{"The name could not be looked up, so the connection is refused", "Block",
			"Some rules are about addresses, so every name no rule lists is looked up first"}},
		{"zh", "测试网址", []string{"这个域名解析不出地址，连接会被拒绝", "拦截", "有些规则是按 IP 地址匹配的"}},
	} {
		t.Run(tc.lang, func(t *testing.T) {
			h := routingHarness(t, harnessOpt{lang: tc.lang})
			h.click(tc.click)
			h.typeText("gone.nowhere.example")
			h.enter()
			h.settle()
			h.mustHave(append([]string{"gone.nowhere.example"}, tc.want...)...)
			h.mustNotHave("No item above matches", "Looked up:", "解析到：")
			h.leaks("the answer to a refused name (" + tc.lang + ")")
		})
	}
}

func TestTestURLSaysWhenWhatWasTypedIsNoAddress(t *testing.T) {
	h := routingHarness(t, harnessOpt{})
	h.click("Test a URL")
	h.typeText("not an address!")
	h.enter()
	h.settle()
	h.mustHave("That is not a web address, a domain or an IP address.")
	h.mustNotHave("From now on, send")
}

func TestTestURLSaysWhenTheServiceFails(t *testing.T) {
	h := routingHarness(t, harnessOpt{})
	h.click("Test a URL")
	h.typeText("github.com")
	h.be.failNext = &agentclient.OpError{Code: agentapi.ErrorCode_ERROR_CODE_BUSY}
	h.enter()
	h.settle()
	h.mustNotHave("It matches")
	h.mustHave("Test a URL", "The service is busy with another operation.")
}

func TestTestURLWithoutAConnectionSaysWhichNodeNeedsOne(t *testing.T) {
	h := routingHarness(t, harnessOpt{})
	h.disconnected()
	h.click("Test a URL")
	h.typeText("github.com")
	h.enter()
	h.settle()
	h.mustHave("It matches “GitHub”", "Connect to see which node it would use.")
	h.mustNotHave("Right now it leaves through")
}

func TestTestURLInChinese(t *testing.T) {
	h := routingHarness(t, harnessOpt{lang: "zh"})
	h.click("测试网址")
	h.mustHave("测试网址", "看看这个地址会走哪里，以及为什么")
	h.typeText("github.com")
	h.enter()
	h.settle()
	h.mustHave("命中了“GitHub”", "以后让", "直连", "走主出口", "拦截", "其他…")
}

// ------------------------------------------------- from the connections list

func connectionsHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, harnessOpt{h: 1100})
	h.click("Connections")
	h.settle()
	h.mustHave("registry.npmjs.org", "slack.com")
	return h
}

func TestConnectionMenuMakesAnExceptionForTheDomain(t *testing.T) {
	h := connectionsHarness(t)
	n := len(h.state().Exceptions)
	h.rightClick("registry.npmjs.org")
	h.mustHave("Copy host", "Copy address",
		"From now on, send only registry.npmjs.org to…",
		"From now on, send npmjs.org and subdomains to…",
		"From now on, send address 104.16.1.34/32 to…",
		"From now on, send everything from node to…")
	h.pick("From#1", "Direct")

	h.mustHave("Exception added: npmjs.org → Direct")
	st := h.state()
	if len(st.Exceptions) != n+1 {
		t.Fatalf("exceptions: %d, want %d", len(st.Exceptions), n+1)
	}
	x, ok := h.exception("npmjs.org")
	if !ok || x.Kind != routing.EntryDomainSuffix || x.Dest.Kind != routing.DestDirect {
		t.Fatalf("the exception made is %+v", x)
	}
}

func TestConnectionMenuOffersEachWayToSayIt(t *testing.T) {
	h := connectionsHarness(t)
	// Only this name, followed to the main exit.
	h.rightClick("registry.npmjs.org")
	h.pick("From#0", "Follow")
	if x, ok := h.exception("registry.npmjs.org"); !ok || x.Kind != routing.EntryDomain || x.Dest.Kind != routing.DestFollow {
		t.Fatalf("the exception for the name is %+v", x)
	}
	// The address, blocked.
	h.rightClick("registry.npmjs.org")
	h.pick("From#2", "Block")
	if x, ok := h.exception("104.16.1.34/32"); !ok || x.Kind != routing.EntryIPCIDR || x.Dest.Kind != routing.DestReject {
		t.Fatalf("the exception for the address is %+v", x)
	}
	// The program, direct.
	h.rightClick("registry.npmjs.org")
	h.pick("From#3", "Direct")
	if x, ok := h.exception("node"); !ok || x.Kind != routing.EntryProcessPath || x.Dest.Kind != routing.DestDirect {
		t.Fatalf("the exception for the program is %+v", x)
	}
}

func TestConnectionMenuOtherOpensTheDialogFilledIn(t *testing.T) {
	h := connectionsHarness(t)
	n := len(h.state().Exceptions)
	h.rightClick("registry.npmjs.org")
	h.pick("From#1", "Other")
	h.mustHave("Add an exception", "Matches", "Goes to")
	if len(h.state().Exceptions) != n {
		t.Fatal("an exception was made before the dialog was answered")
	}
	h.chooseInDialog("Follow main exit", "Direct")
	h.submitDialog("Add")
	if x, ok := h.exception("npmjs.org"); !ok || x.Dest.Kind != routing.DestDirect {
		t.Fatalf("the exception made is %+v", x)
	}
}

func TestConnectionMenuForAConnectionWithoutAName(t *testing.T) {
	h := connectionsHarness(t)
	// An address and a program are all there is to say it with.
	h.rightClick("17.253.144.10:443")
	h.mustHave("From now on, send address 17.253.144.10/32 to…", "From now on, send everything from trustd to…")
	h.mustNotHave("subdomains to…")
	h.closeMenu()
}

func TestConnectionMenuCopies(t *testing.T) {
	h := connectionsHarness(t)
	h.rightClick("registry.npmjs.org")
	h.pick("Copy")
	if got := h.ui.Clipboard(); got != "registry.npmjs.org" {
		t.Fatalf("copied %q", got)
	}
	h.rightClick("registry.npmjs.org")
	h.pick("Copy#1")
	if got := h.ui.Clipboard(); got != "104.16.1.34:443" {
		t.Fatalf("copied %q", got)
	}
}

func TestConnectionsTellWhyEachWentWhereItDid(t *testing.T) {
	h := connectionsHarness(t)
	// The name of the item or the number of the exception, never the core's own rule text.
	h.mustHave("AI services", "China sites", "Everything else", "Exception #3", "Exception #1", "Local network")
	h.mustNotHave("rule_set=", "process_name=", "domain_suffix=")
}
