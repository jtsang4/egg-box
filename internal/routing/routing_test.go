package routing

import (
	"errors"
	"slices"
	"testing"
)

func names(v View) []string {
	var out []string
	for _, r := range v.Rows {
		out = append(out, r.Item.Name)
	}
	return out
}

// airport is a plan as a subscription would bring it, with invented names.
func airport(items ...string) *Plan {
	p := &Plan{ID: SubPlanID("s1"), Kind: PlanSubscription, Name: "Airport A", SubID: "s1", Fallback: Follow()}
	for _, n := range items {
		p.Items = append(p.Items, Item{ID: "a-" + NormName(n), Name: n, Dest: Follow(), Enabled: true,
			Sources: []Source{URLSource("https://example.test/"+NormName(n)+".srs", n, "Example")}})
	}
	return p
}

func TestNormName(t *testing.T) {
	for in, want := range map[string]string{
		"💬 AI 服务":     "ai 服务",
		"  AI   服务 ":  "ai 服务",
		"🚀 节点选择":      "节点选择",
		"🇺🇸  Netflix": "netflix",
		"YouTube":     "youtube",
		"—— Google":   "google",
		"":            "",
		"🎬":           "",
	} {
		if got := NormName(in); got != want {
			t.Errorf("NormName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEntryNormalizeAndValid(t *testing.T) {
	cases := []struct {
		in   Entry
		want Entry
		ok   bool
	}{
		{Entry{EntryDomainSuffix, " *.Example.COM "}, Entry{EntryDomainSuffix, "example.com"}, true},
		{Entry{EntryDomainSuffix, "+.a.org"}, Entry{EntryDomainSuffix, "a.org"}, true},
		{Entry{EntryDomain, "WWW.A.org"}, Entry{EntryDomain, "www.a.org"}, true},
		{Entry{EntryIPCIDR, "10.1.2.3"}, Entry{EntryIPCIDR, "10.1.2.3/32"}, true},
		{Entry{EntryIPCIDR, "10.1.2.9/24"}, Entry{EntryIPCIDR, "10.1.2.0/24"}, true},
		{Entry{EntryIPCIDR, "2001:db8::1"}, Entry{EntryIPCIDR, "2001:db8::1/128"}, true},
		{Entry{EntryIPCIDR, "nope"}, Entry{}, false},
		{Entry{EntryPort, "8000-9000"}, Entry{EntryPort, "8000:9000"}, true},
		{Entry{EntryPort, "443"}, Entry{EntryPort, "443"}, true},
		{Entry{EntryPort, "9000-8000"}, Entry{}, false},
		{Entry{EntryPort, "0"}, Entry{}, false},
		{Entry{EntryDomainRegex, `^a\d+\.b$`}, Entry{EntryDomainRegex, `^a\d+\.b$`}, true},
		{Entry{EntryDomainRegex, `([`}, Entry{}, false},
		{Entry{EntryProcessName, "chrome.exe"}, Entry{EntryProcessName, "chrome.exe"}, true},
		{Entry{EntryProcessPath, `C:\Program Files\x.exe`}, Entry{EntryProcessPath, `C:\Program Files\x.exe`}, true},
		{Entry{EntryProcessName, "Google Chrome Helper"}, Entry{EntryProcessName, "Google Chrome Helper"}, true},
		{Entry{EntryProcessName, "dir/x"}, Entry{}, false},
		{Entry{EntryProcessName, `dir\x.exe`}, Entry{}, false},
		{Entry{EntryProcessName, "a\nb"}, Entry{}, false},
		// What is not a name of a host cannot be a domain, whatever else it is.
		{Entry{EntryDomainSuffix, "10.0.0.0/8"}, Entry{}, false},
		{Entry{EntryDomainSuffix, "10.0.0.1"}, Entry{}, false},
		{Entry{EntryDomainSuffix, "8443"}, Entry{}, false},
		{Entry{EntryDomainSuffix, "a..b"}, Entry{}, false},
		{Entry{EntryDomainSuffix, "-a.org"}, Entry{}, false},
		{Entry{EntryDomainSuffix, "a b.org"}, Entry{}, false},
		{Entry{EntryDomainSuffix, "https://a.org"}, Entry{}, false},
		{Entry{EntryDomainSuffix, "a.org:443"}, Entry{}, false},
		{Entry{EntryDomain, "*.a.org"}, Entry{}, false},
		{Entry{EntryDomain, "2001:db8::1"}, Entry{}, false},
		{Entry{EntryDomainSuffix, "cn"}, Entry{EntryDomainSuffix, "cn"}, true},
		{Entry{EntryDomainSuffix, "_dmarc.a.org"}, Entry{EntryDomainSuffix, "_dmarc.a.org"}, true},
		{Entry{EntryDomainSuffix, "a.org."}, Entry{EntryDomainSuffix, "a.org"}, true},
		{Entry{EntryDomainSuffix, "例子.com"}, Entry{EntryDomainSuffix, "xn--fsqu00a.com"}, true},
		{Entry{EntryDomain, "Bücher.example"}, Entry{EntryDomain, "xn--bcher-kva.example"}, true},
		{Entry{EntryDomainKeyword, "Ads-Tracker"}, Entry{EntryDomainKeyword, "ads-tracker"}, true},
		{Entry{EntryDomainKeyword, ".cn."}, Entry{EntryDomainKeyword, ".cn."}, true},
		{Entry{EntryDomainKeyword, "ads tracker"}, Entry{}, false},
		{Entry{EntryDomainKeyword, "ads/tracker"}, Entry{}, false},
		{Entry{EntryDomainKeyword, "例子"}, Entry{}, false},
		{Entry{EntryDomainSuffix, "*."}, Entry{}, false},
		{Entry{"weird", "x"}, Entry{}, false},
		{Entry{EntryDomain, "  "}, Entry{}, false},
	}
	for _, c := range cases {
		err := c.in.Valid()
		if (err == nil) != c.ok {
			t.Errorf("%v: Valid() = %v, want ok=%v", c.in, err, c.ok)
			continue
		}
		if c.ok && c.in.Normalize() != c.want {
			t.Errorf("%v: Normalize() = %v, want %v", c.in, c.in.Normalize(), c.want)
		}
	}
}

func TestDestValidity(t *testing.T) {
	node := ToNode(NodeID{Sub: "s", Name: "n"})
	for _, c := range []struct {
		d    Dest
		main bool
		ok   bool
	}{
		{Follow(), false, true}, {Follow(), true, false},
		{Direct(), false, true}, {Direct(), true, false},
		{Reject(), false, true}, {Reject(), true, false},
		{Auto(), true, true}, {Auto(), false, false},
		{ToRegion("us"), false, true}, {ToRegion("us"), true, true}, {Dest{Kind: DestRegion, Region: "USA"}, false, false},
		{node, false, true}, {node, true, true}, {Dest{Kind: DestNode}, false, false},
		{Dest{Kind: "teleport"}, false, false},
	} {
		if err := c.d.Valid(c.main); (err == nil) != c.ok {
			t.Errorf("%v main=%v: %v, want ok=%v", c.d, c.main, err, c.ok)
		}
	}
	if ToRegion("us").Region != "US" {
		t.Error("regions are upper case")
	}
	a, b := ToNode(NodeID{Sub: "s", Name: "n", Hash: "h1"}), ToNode(NodeID{Sub: "s", Name: "n", Hash: "h2"})
	if !a.Equal(b) {
		t.Error("the same name in the same subscription is the same node, whatever its settings")
	}
	c := ToNode(NodeID{Sub: "s", Name: "renamed", Hash: "h1"})
	if !a.Equal(c) {
		t.Error("the same settings in the same subscription are the same node, whatever its name")
	}
}

func TestBuiltinPlanMatchesTheDesign(t *testing.T) {
	p := Builtin()
	var got []string
	for _, it := range p.Items {
		got = append(got, it.Key)
	}
	want := []string{"ads", "ai", "telegram", "youtube", "streaming", "google", "social", "github", "microsoft", "apple", "cn", "foreign"}
	if !slices.Equal(got, want) {
		t.Fatalf("items = %v, want %v", got, want)
	}
	dest := map[string]DestKind{}
	on := map[string]bool{}
	for _, it := range p.Items {
		dest[it.Key], on[it.Key] = it.Dest.Kind, it.Enabled
		if len(it.Sources) == 0 {
			t.Errorf("%s has no source", it.Key)
		}
		for _, s := range it.Sources {
			if s.Kind != SourceURL || s.URL == "" || s.Project != MetaCubeX {
				t.Errorf("%s: bad source %+v", it.Key, s)
			}
		}
	}
	if dest["ads"] != DestReject || on["ads"] {
		t.Error("ad blocking blocks and is off by default")
	}
	for _, k := range []string{"microsoft", "apple", "cn"} {
		if dest[k] != DestDirect {
			t.Errorf("%s goes direct by default", k)
		}
	}
	for _, k := range []string{"ai", "telegram", "youtube", "streaming", "google", "social", "github", "foreign"} {
		if dest[k] != DestFollow || !on[k] {
			t.Errorf("%s follows the main exit and is on", k)
		}
	}
	if p.Fallback.Kind != DestFollow {
		t.Error("the fallback follows the main exit")
	}
	// narrower comes first
	idx := func(k string) int { return slices.Index(got, k) }
	if idx("youtube") > idx("google") || idx("github") > idx("microsoft") || idx("cn") > idx("foreign") {
		t.Error("the order must put narrow items first")
	}
	// a Chinese plan item and its English twin are the same item
	ai := p.Items[1]
	for _, n := range []string{"AI 服务", "AI services", "💬 AI", "ChatGPT"} {
		if !slices.Contains(ai.Keys(), NormName(n)) {
			t.Errorf("the AI item should answer to %q", n)
		}
	}
	// A service the AI item takes in carries a choice over, but is not its
	// name: an item of its own may have it.
	if slices.Contains(ai.Names(), "chatgpt") || !slices.Contains(ai.Names(), "ai") {
		t.Errorf("the AI item's names: %v", ai.Names())
	}
	st := NewState()
	if _, err := st.AddItem(Item{Name: "Netflix", Dest: Direct(), Enabled: true}, ""); err != nil {
		t.Errorf("Netflix beside the streaming item: %v", err)
	}
	if _, err := st.AddItem(Item{Name: "💬 AI", Dest: Direct(), Enabled: true}, ""); !errors.Is(err, ErrDuplicateName) {
		t.Errorf("another name of the AI item: %v", err)
	}
	if BuiltinName("ai", "zh-CN") != "AI 服务" || BuiltinName("ai", "en") != "AI services" || BuiltinName("nope", "en") != "" {
		t.Error("BuiltinName")
	}
}

// Whether an item's names are looked up by the domestic DNS server belongs to
// what the item is made of: the shipped plan says so for the local services,
// and an item made to go direct is one, whatever is chosen for it later.
func TestDomesticItemsAreMarkedWhenTheyAreMade(t *testing.T) {
	for _, it := range Builtin().Items {
		want := it.Key == "microsoft" || it.Key == "apple" || it.Key == "cn"
		if it.Domestic != want {
			t.Errorf("%s: domestic = %v", it.Key, it.Domestic)
		}
	}
	var st State
	direct, err := st.AddItem(Item{Name: "Bank", Dest: Direct(), Enabled: true}, "")
	if err != nil {
		t.Fatal(err)
	}
	proxied, err := st.AddItem(Item{Name: "Forum", Dest: Follow(), Enabled: true}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !direct.Domestic || proxied.Domestic {
		t.Fatalf("domestic: direct item %v, proxied item %v", direct.Domestic, proxied.Domestic)
	}
	// Changing where it goes does not change what it is.
	if err := st.SetItemDest(direct.ID, Follow()); err != nil {
		t.Fatal(err)
	}
	if err := st.SetItemDest(proxied.ID, Direct()); err != nil {
		t.Fatal(err)
	}
	for _, r := range st.View().Rows {
		switch r.Item.Name {
		case "Bank":
			if !r.Item.Domestic {
				t.Error("an item made to go direct stopped being local after it was proxied")
			}
		case "Forum":
			if r.Item.Domestic {
				t.Error("a proxied item became local after it was sent direct")
			}
		}
	}
}

func TestChoicesOnAFollowedPlanSurviveARefresh(t *testing.T) {
	s := NewState()
	s.SyncPlan(airport("💬 AI", "Netflix", "Others"))
	if err := s.Switch(SubPlanID("s1")); err != nil {
		t.Fatal(err)
	}
	v := s.View()
	ai, _, _ := v.Row("a-ai")
	if err := s.SetItemDest(ai.Item.ID, ToRegion("us")); err != nil {
		t.Fatal(err)
	}
	if err := s.SetItemEnabled("a-netflix", false); err != nil {
		t.Fatal(err)
	}
	v = s.View()
	if r, _, _ := v.Row("a-ai"); r.Item.Dest.Region != "US" || !r.Overridden || r.DefaultDest.Kind != DestFollow {
		t.Errorf("AI row: %+v", r)
	}
	if r, _, _ := v.Row("a-netflix"); r.Item.Enabled || !r.Overridden || !r.DefaultOn {
		t.Errorf("Netflix row: %+v", r)
	}

	// The airport renames the emoji, drops one item and adds another.
	fresh := airport("🤖 AI", "Netflix", "Games")
	change := s.SyncPlan(fresh)
	if !slices.Equal(change.Added, []string{"Games"}) || !slices.Equal(change.Removed, []string{"Others"}) {
		t.Errorf("change = %+v", change)
	}
	v = s.View()
	if r, _, _ := v.Row("a-ai"); r.Item.Dest.Region != "US" {
		t.Errorf("the choice for AI is kept by name, not by emoji: %+v", r)
	}
	if r, _, _ := v.Row("a-netflix"); r.Item.Enabled {
		t.Error("the switch is kept")
	}
	if again := s.SyncPlan(fresh); !again.Empty() {
		t.Errorf("a second look at the same plan finds no change: %+v", again)
	}
}

func TestFirstSyncReportsNothingAsAdded(t *testing.T) {
	s := NewState()
	if c := s.SyncPlan(airport("A", "B")); !c.Empty() {
		t.Errorf("first sync: %+v", c)
	}
}

func TestSettingTheDefaultBackRemovesTheOverride(t *testing.T) {
	s := NewState()
	_ = s.SetItemDest("b-ai", Direct())
	if ov := s.Overlays[BuiltinID]; ov == nil || len(ov.Dest) != 1 {
		t.Fatalf("overlay = %+v", ov)
	}
	_ = s.SetItemDest("b-ai", Follow())
	if ov := s.Overlays[BuiltinID]; len(ov.Dest) != 0 {
		t.Errorf("an override equal to the default is no override: %+v", ov.Dest)
	}
	_ = s.SetItemEnabled("b-ads", true)
	_ = s.SetItemEnabled("b-ads", false)
	if ov := s.Overlays[BuiltinID]; len(ov.Enabled) != 0 {
		t.Errorf("enabled override should be gone: %+v", ov.Enabled)
	}
	if err := s.SetItemDest("nope", Direct()); !errors.Is(err, ErrNoItem) {
		t.Errorf("unknown item: %v", err)
	}
	if err := s.SetItemDest("b-ai", Auto()); err == nil {
		t.Error("an item cannot be automatic")
	}
}

func TestOwnItemsAreAnchoredByName(t *testing.T) {
	s := NewState()
	s.Plans = []*Plan{airport("A", "B", "C")}
	_ = s.Switch(SubPlanID("s1"))

	x, err := s.AddItem(Item{Name: "Work", Dest: Direct(), Enabled: true}, "a-b")
	if err != nil {
		t.Fatal(err)
	}
	if got := names(s.View()); !slices.Equal(got, []string{"A", "Work", "B", "C"}) {
		t.Fatalf("after add before B: %v", got)
	}
	y, _ := s.AddItem(Item{Name: "Home", Dest: Direct(), Enabled: true}, "a-b")
	if got := names(s.View()); !slices.Equal(got, []string{"A", "Work", "Home", "B", "C"}) {
		t.Fatalf("two items before B keep the order they were added in: %v", got)
	}
	// Moving among the plan's own items is not allowed, only around them.
	if err := s.Move("a-a", 1); !errors.Is(err, ErrLocked) {
		t.Errorf("moving a plan item: %v", err)
	}
	if err := s.DeleteItem("a-a"); !errors.Is(err, ErrLocked) {
		t.Errorf("deleting a plan item: %v", err)
	}
	if err := s.Move(y.ID, -1); err != nil {
		t.Fatal(err)
	}
	if got := names(s.View()); !slices.Equal(got, []string{"A", "Home", "Work", "B", "C"}) {
		t.Fatalf("moving Home up: %v", got)
	}
	if err := s.Move(y.ID, -1); err != nil {
		t.Fatal(err)
	}
	if got := names(s.View()); !slices.Equal(got, []string{"Home", "A", "Work", "B", "C"}) {
		t.Fatalf("Home passes A: %v", got)
	}
	if err := s.MoveBefore(x.ID, ""); err != nil {
		t.Fatal(err)
	}
	if got := names(s.View()); !slices.Equal(got, []string{"Home", "A", "B", "C", "Work"}) {
		t.Fatalf("Work to the end: %v", got)
	}
	// The airport drops the anchor: the item goes before the fallback.
	_ = s.MoveBefore(y.ID, "a-c")
	s.SyncPlan(airport("A", "B"))
	if got := names(s.View()); got[len(got)-1] != "Work" && got[len(got)-1] != "Home" {
		t.Errorf("an item whose anchor is gone goes last: %v", got)
	}
	if _, err := s.AddItem(Item{Name: "💬 work", Dest: Direct(), Enabled: true}, ""); !errors.Is(err, ErrDuplicateName) {
		t.Errorf("same name ignoring emoji and case: %v", err)
	}
	if _, err := s.AddItem(Item{Name: "  ", Dest: Direct()}, ""); !errors.Is(err, ErrNoName) {
		t.Errorf("empty name: %v", err)
	}
	if err := s.DeleteItem(x.ID); err != nil {
		t.Fatal(err)
	}
	for _, n := range names(s.View()) {
		if n == "Work" {
			t.Error("Work should be gone")
		}
	}
}

func TestSwitchCarriesChoicesOverByName(t *testing.T) {
	s := NewState()
	s.Plans = []*Plan{airport("🤖 AI", "Streaming", "Others")}
	// choices on the shipped plan: AI to the US, ads on, microsoft via proxy
	_ = s.SetItemDest("b-ai", ToRegion("US"))
	_ = s.SetItemEnabled("b-ads", true)
	_ = s.SetItemDest("b-microsoft", Follow())
	_ = s.SetFallback(Direct())

	if err := s.Switch(SubPlanID("s1")); err != nil {
		t.Fatal(err)
	}
	v := s.View()
	ai, _, _ := v.Row("a-ai")
	if ai.Item.Dest.Region != "US" {
		t.Errorf("AI (🤖 AI = AI 服务) keeps its destination: %+v", ai.Item.Dest)
	}
	if st, _, _ := v.Row("a-streaming"); st.Overridden {
		t.Error("an item the person never touched is not overridden")
	}
	if v.Fallback.Kind != DestDirect || !v.FallbackOverridden {
		t.Errorf("the fallback choice carries over: %+v", v.Fallback)
	}
	// and back: the airport's own plan had no choices, so nothing new there
	_ = s.SetItemDest("a-others", ToRegion("JP"))
	if err := s.Switch(BuiltinID); err != nil {
		t.Fatal(err)
	}
	v = s.View()
	if r, _, _ := v.Row("b-ai"); r.Item.Dest.Region != "US" {
		t.Errorf("shipped plan still has AI in the US: %+v", r.Item.Dest)
	}
	if r, _, _ := v.Row("b-foreign"); r.Item.Dest.Kind != DestFollow {
		t.Error("'Others' has no counterpart in the shipped plan except by alias; foreign is untouched by region JP")
	}
}

func TestSwitchToAndFromAPlanOfMyOwn(t *testing.T) {
	s := NewState()
	_ = s.SetItemDest("b-cn", Follow())
	mine, err := s.SaveAsMine("My plan")
	if err != nil {
		t.Fatal(err)
	}
	if s.ActiveID() != mine.ID || mine.Origin == "" {
		t.Errorf("active=%s origin=%q", s.ActiveID(), mine.Origin)
	}
	v := s.View()
	if len(v.Rows) != len(Builtin().Items) {
		t.Fatalf("rows = %d", len(v.Rows))
	}
	if r, _, _ := v.Row("b-cn"); r.Item.Dest.Kind != DestFollow || r.Locked || r.Origin != OriginMine {
		t.Errorf("cn row in my plan: %+v", r)
	}
	// my plan is free to edit
	if err := s.DeleteItem("b-ads"); err != nil {
		t.Fatal(err)
	}
	if err := s.Move("b-cn", -1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddItem(Item{Name: "Mine only", Dest: Reject(), Enabled: true}, "b-ai"); err != nil {
		t.Fatal(err)
	}
	got := names(s.View())
	if slices.Contains(got, "广告拦截") || got[0] != "Mine only" {
		t.Errorf("my plan after edits: %v", got)
	}
	if err := s.SetItemDest("b-ai", ToRegion("DE")); err != nil {
		t.Fatal(err)
	}
	// leaving and coming back: my plan keeps its own settings
	_ = s.Switch(BuiltinID)
	if r, _, _ := s.View().Row("b-ai"); r.Item.Dest.Region != "DE" {
		t.Errorf("carried over into the shipped plan: %+v", r.Item.Dest)
	}
	_ = s.RestoreDefault()
	_ = s.Switch(mine.ID)
	if r, _, _ := s.View().Row("b-ai"); r.Item.Dest.Region != "DE" {
		t.Errorf("a plan of my own keeps its choices: %+v", r.Item.Dest)
	}
	if err := s.RestoreDefault(); !errors.Is(err, ErrNotMine) {
		t.Errorf("restore default on my plan: %v", err)
	}
	if err := s.RenamePlan(mine.ID, "Renamed"); err != nil || s.PlanByID(mine.ID).Name != "Renamed" {
		t.Errorf("rename: %v", err)
	}
	if err := s.DeletePlan(mine.ID); err != nil {
		t.Fatal(err)
	}
	if s.ActiveID() != BuiltinID || s.PlanByID(mine.ID) != nil {
		t.Errorf("deleting the active plan falls back to the shipped one: %s", s.ActiveID())
	}
	if err := s.DeletePlan(BuiltinID); !errors.Is(err, ErrNoPlan) {
		t.Errorf("the shipped plan cannot be deleted: %v", err)
	}
	if err := s.Switch("p-missing"); !errors.Is(err, ErrNoPlan) {
		t.Errorf("switch to unknown: %v", err)
	}
}

func TestSaveAsMineSnapshotsTheEffectivePlan(t *testing.T) {
	s := NewState()
	s.Plans = []*Plan{airport("A", "B")}
	_ = s.Switch(SubPlanID("s1"))
	_ = s.SetItemDest("a-a", Direct())
	_, _ = s.AddItem(Item{Name: "Own", Dest: Reject(), Enabled: true}, "a-b")
	_ = s.SetFallback(Direct())
	mine, err := s.SaveAsMine("Copy")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range mine.Items {
		got = append(got, it.Name+":"+string(it.Dest.Kind))
	}
	if !slices.Equal(got, []string{"A:direct", "Own:reject", "B:follow"}) || mine.Fallback.Kind != DestDirect {
		t.Errorf("snapshot = %v fallback=%v", got, mine.Fallback)
	}
	// the airport's plan is untouched by edits of the copy
	_ = s.SetItemDest("a-a", Follow())
	if r, _, _ := s.ViewOf(SubPlanID("s1")).Row("a-a"); r.Item.Dest.Kind != DestDirect {
		t.Error("the copy must not change the original's overlay")
	}
}

func TestCopyItemFromAnotherPlan(t *testing.T) {
	s := NewState()
	s.Plans = []*Plan{airport("Work VPN", "Other")}
	it, err := s.CopyItemFrom(SubPlanID("s1"), "a-work vpn", "b-cn")
	if err != nil {
		t.Fatal(err)
	}
	if it.ID == "a-work vpn" || it.ID == "" {
		t.Errorf("a copy has an id of its own: %q", it.ID)
	}
	got := names(s.View())
	if i := slices.Index(got, "Work VPN"); i < 0 || got[i+1] != "国内网站" {
		t.Errorf("copy placed before the item asked: %v", got)
	}
	if _, err := s.CopyItemFrom(SubPlanID("s1"), "a-work vpn", ""); !errors.Is(err, ErrDuplicateName) {
		t.Errorf("copying twice: %v", err)
	}
	if _, err := s.CopyItemFrom("zzz", "x", ""); !errors.Is(err, ErrNoPlan) {
		t.Errorf("unknown plan: %v", err)
	}
	if _, err := s.CopyItemFrom(SubPlanID("s1"), "zzz", ""); !errors.Is(err, ErrNoItem) {
		t.Errorf("unknown item: %v", err)
	}
}

func TestDetachPlanKeepsTheEffectiveContent(t *testing.T) {
	s := NewState()
	s.Plans = []*Plan{airport("A", "B")}
	_ = s.Switch(SubPlanID("s1"))
	_ = s.SetItemDest("a-a", ToRegion("JP"))
	_, _ = s.AddItem(Item{Name: "Own", Dest: Direct(), Enabled: true}, "")
	p, err := s.DetachPlan(SubPlanID("s1"), "from deleted Airport A")
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != PlanMine || p.Origin == "" || s.ActiveID() != p.ID || s.PlanByID(SubPlanID("s1")) != nil {
		t.Errorf("detached: %+v active=%s", p, s.ActiveID())
	}
	if got := names(s.View()); !slices.Equal(got, []string{"A", "B", "Own"}) {
		t.Errorf("content: %v", got)
	}
	if r, _, _ := s.View().Row("a-a"); r.Item.Dest.Region != "JP" {
		t.Error("the person's choice survives")
	}
	if _, ok := s.Overlays[SubPlanID("s1")]; ok {
		t.Error("the overlay of the vanished plan is dropped")
	}
	if _, err := s.DetachPlan(BuiltinID, ""); !errors.Is(err, ErrNoPlan) {
		t.Errorf("the shipped plan has nothing to detach from: %v", err)
	}
}

func TestExceptions(t *testing.T) {
	s := NewState()
	a, err := s.AddException(Exception{Kind: EntryDomainSuffix, Value: " *.Example.com ", Dest: Direct(), Enabled: true})
	if err != nil || a.Value != "example.com" || a.ID == "" {
		t.Fatalf("add: %+v %v", a, err)
	}
	b, _ := s.AddException(Exception{Kind: EntryIPCIDR, Value: "10.0.0.1", Dest: Reject(), Enabled: true})
	if b.Value != "10.0.0.1/32" {
		t.Errorf("cidr normalised: %q", b.Value)
	}
	if _, err := s.AddException(Exception{Kind: EntryPort, Value: "x", Dest: Direct()}); err == nil {
		t.Error("invalid entry")
	}
	if _, err := s.AddException(Exception{Kind: EntryDomain, Value: "a.b", Dest: Dest{Kind: "x"}}); err == nil {
		t.Error("invalid destination")
	}
	// one click on a site that already has an exception changes that one
	up, err := s.UpsertException(Entry{Kind: EntryDomainSuffix, Value: "EXAMPLE.com"}, ToRegion("us"))
	if err != nil || up.ID != a.ID || up.Dest.Region != "US" || len(s.Exceptions) != 2 {
		t.Errorf("upsert existing: %+v %v n=%d", up, err, len(s.Exceptions))
	}
	_ = s.SetExceptionEnabled(a.ID, false)
	up, _ = s.UpsertException(Entry{Kind: EntryDomainSuffix, Value: "example.com"}, Direct())
	if !up.Enabled {
		t.Error("a click on a switched-off exception turns it back on")
	}
	nw, _ := s.UpsertException(Entry{Kind: EntryDomain, Value: "new.test"}, Follow())
	if len(s.Exceptions) != 3 || nw.ID == "" || !nw.Enabled {
		t.Errorf("upsert new: %+v", nw)
	}
	if s.ExceptionIndex(nw.ID) != 3 || s.ExceptionIndex("zz") != 0 {
		t.Error("ExceptionIndex")
	}
	_ = s.MoveException(nw.ID, -1)
	if s.ExceptionIndex(nw.ID) != 2 {
		t.Error("move up")
	}
	_ = s.MoveException(nw.ID, -5) // out of range is a no-op
	_ = s.MoveException(a.ID, -1)
	if s.ExceptionIndex(a.ID) != 1 {
		t.Error("first stays first")
	}
	_ = s.MoveExceptionBefore(a.ID, "") // a, nw, b → nw, b, a
	if s.ExceptionIndex(a.ID) != 3 || s.ExceptionIndex(nw.ID) != 1 {
		t.Error("move to the end")
	}
	_ = s.MoveExceptionBefore(a.ID, nw.ID)
	if s.ExceptionIndex(a.ID) != 1 || s.ExceptionIndex(nw.ID) != 2 || s.ExceptionIndex(b.ID) != 3 {
		t.Error("move before another")
	}
	_ = s.MoveExceptionBefore(nw.ID, nw.ID) // before itself: stays where it is
	if s.ExceptionIndex(nw.ID) != 2 {
		t.Error("move before itself")
	}
	if err := s.UpdateException(b.ID, Exception{Kind: EntryDomain, Value: "Edited.test", Dest: Direct(), Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if x := s.Exceptions[s.ExceptionIndex(b.ID)-1]; x.Value != "edited.test" || x.ID != b.ID {
		t.Errorf("update: %+v", x)
	}
	if err := s.SetExceptionDest(b.ID, Reject()); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteException(b.ID); err != nil || len(s.Exceptions) != 2 {
		t.Errorf("delete: %v", err)
	}
	for _, f := range []func() error{
		func() error { return s.DeleteException("zz") },
		func() error { return s.SetExceptionDest("zz", Direct()) },
		func() error { return s.SetExceptionEnabled("zz", true) },
		func() error { return s.MoveException("zz", 1) },
		func() error { return s.MoveExceptionBefore("zz", "") },
		func() error { return s.UpdateException("zz", Exception{}) },
	} {
		if err := f(); !errors.Is(err, ErrNoException) {
			t.Errorf("unknown exception: %v", err)
		}
	}
}

func TestExceptionsAreCapped(t *testing.T) {
	s := NewState()
	for i := 0; i < MaxExceptions; i++ {
		if _, err := s.AddException(Exception{Kind: EntryPort, Value: "443", Dest: Direct(), Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.AddException(Exception{Kind: EntryPort, Value: "80", Dest: Direct()}); !errors.Is(err, ErrTooManyExceptions) {
		t.Errorf("past the cap: %v", err)
	}
}

func TestResolveNodes(t *testing.T) {
	live := []LiveNode{
		{Sub: "s1", Name: "HK 01", Hash: "h-hk1", Region: "HK"},
		{Sub: "s1", Name: "HK 02 renamed", Hash: "h-hk2", Region: "HK"},
		{Sub: "s2", Name: "JP 01", Hash: "h-jp1", Region: "JP"},
	}
	for _, c := range []struct {
		id   NodeID
		want int
		how  Match
	}{
		{NodeID{Sub: "s1", Name: "HK 01", Hash: "stale-password-changed"}, 0, MatchName},
		{NodeID{Sub: "s1", Name: "HK 02", Hash: "h-hk2"}, 1, MatchHash},
		{NodeID{Sub: "s1", Name: "JP 01", Hash: "h-jp1"}, 2, MatchSettings},
		{NodeID{Sub: "s1", Name: "gone", Hash: "h-gone"}, -1, MatchNone},
		{NodeID{Sub: "s1", Name: "gone"}, -1, MatchNone},
		{NodeID{}, -1, MatchNone},
	} {
		i, how := Resolve(c.id, live)
		if i != c.want || how != c.how {
			t.Errorf("Resolve(%+v) = %d,%v want %d,%v", c.id, i, how, c.want, c.how)
		}
	}
}

func TestRefreshIdentitiesFollowsRenamesAndKeepsVanishedPins(t *testing.T) {
	s := NewState()
	s.Plans = []*Plan{airport("A")}
	_ = s.Switch(SubPlanID("s1"))
	pin := func(name, hash string) Dest { return ToNode(NodeID{Sub: "s1", Name: name, Hash: hash, Region: "HK"}) }
	s.Main = pin("HK 01", "h1")
	_ = s.SetItemDest("a-a", pin("HK 02", "h2"))
	_, _ = s.AddException(Exception{Kind: EntryDomain, Value: "x.test", Dest: pin("HK 03", "h3"), Enabled: true})
	_, _ = s.AddItem(Item{Name: "Own", Dest: pin("HK 04", "h4"), Enabled: true}, "")
	s.SetNodeAuto(NodeID{Sub: "s1", Name: "HK 02", Hash: "h2"}, false)
	s.SetNodeRegion(NodeID{Sub: "s1", Name: "HK 01", Hash: "h1"}, "sg")

	live := []LiveNode{
		{Sub: "s1", Name: "HK 01", Hash: "h1-new-password", Region: "HK"}, // same name, new settings
		{Sub: "s1", Name: "香港 02", Hash: "h2", Region: "HK"},              // renamed
		{Sub: "s1", Name: "HK 04", Hash: "h4", Region: "HK"},
		// HK 03 is gone
	}
	if !s.RefreshIdentities(live) {
		t.Fatal("expected a change")
	}
	if s.Main.Node.Hash != "h1-new-password" || s.Main.Node.Name != "HK 01" {
		t.Errorf("main: %+v", s.Main.Node)
	}
	if r, _, _ := s.View().Row("a-a"); r.Item.Dest.Node.Name != "香港 02" {
		t.Errorf("a pinned item follows the rename: %+v", r.Item.Dest.Node)
	}
	if n := s.Exceptions[0].Dest.Node; n.Name != "HK 03" || n.Hash != "h3" {
		t.Errorf("a vanished pin stays as it was: %+v", n)
	}
	if p, ok := s.Nodes[NodeID{Sub: "s1", Name: "香港 02"}.Key()]; !ok || !p.NoAuto {
		t.Errorf("the node's preferences follow the rename: %+v", s.Nodes)
	}
	if p := s.NodePref(NodeID{Sub: "s1", Name: "HK 01"}); p.Region != "SG" || p.Hash != "h1-new-password" {
		t.Errorf("node pref: %+v", p)
	}
	if s.RefreshIdentities(live) {
		t.Error("a second refresh with the same nodes changes nothing")
	}
	s.SetNodeAuto(NodeID{Sub: "s1", Name: "香港 02"}, true)
	s.SetNodeRegion(NodeID{Sub: "s1", Name: "HK 01"}, "")
	if len(s.Nodes) != 0 {
		t.Errorf("preferences back to default are dropped: %+v", s.Nodes)
	}
}

func TestStateCloneIsDeep(t *testing.T) {
	s := NewState()
	s.Plans = []*Plan{airport("A")}
	_ = s.Switch(SubPlanID("s1"))
	_ = s.SetItemDest("a-a", ToNode(NodeID{Sub: "s", Name: "n"}))
	_, _ = s.AddException(Exception{Kind: EntryDomain, Value: "x.test", Dest: Direct(), Enabled: true})
	c := s.Clone()
	_ = c.SetItemDest("a-a", Direct())
	_ = c.DeleteException(c.Exceptions[0].ID)
	c.Plans[0].Items[0].Name = "changed"
	if r, _, _ := s.View().Row("a-a"); r.Item.Dest.Kind != DestNode || len(s.Exceptions) != 1 || s.Plans[0].Items[0].Name != "A" {
		t.Error("editing a clone must not touch the original")
	}
}

func TestRecentNodesAreTheLastChosenAsMain(t *testing.T) {
	s := NewState()
	node := func(name string) NodeID { return NodeID{Sub: "s1", Name: name, Hash: "h-" + name} }
	for _, n := range []string{"A", "B", "C", "A"} {
		if err := s.SetMain(ToNode(node(n))); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(s.Recent); got != 3 || s.Recent[0].Name != "A" || s.Recent[1].Name != "C" || s.Recent[2].Name != "B" {
		t.Errorf("newest first, each node once: %+v", s.Recent)
	}
	// A region or automatic leaves the memory alone.
	_ = s.SetMain(Auto())
	_ = s.SetMain(ToRegion("jp"))
	if len(s.Recent) != 3 {
		t.Errorf("only nodes are remembered: %+v", s.Recent)
	}
	// The same node under another name (the provider renamed it) is still one entry.
	_ = s.SetMain(ToNode(NodeID{Sub: "s1", Name: "A renamed", Hash: "h-A"}))
	if len(s.Recent) != 3 || s.Recent[0].Name != "A renamed" {
		t.Errorf("a renamed node replaces its old entry: %+v", s.Recent)
	}
	// There is room for MaxRecent and no more.
	for i := 0; i < 2*MaxRecent; i++ {
		_ = s.SetMain(ToNode(node(string(rune('a' + i)))))
	}
	if len(s.Recent) != MaxRecent {
		t.Errorf("kept %d, want %d", len(s.Recent), MaxRecent)
	}
	// A failed choice is not remembered.
	before := s.Recent[0]
	if err := s.SetMain(Dest{Kind: DestFollow}); err == nil {
		t.Fatal("the main exit cannot follow itself")
	}
	if s.Recent[0] != before {
		t.Error("an invalid choice changed the memory")
	}
	// Renames are followed, and a copy shares nothing.
	c := s.Clone()
	live := []LiveNode{{Sub: "s1", Name: "renamed " + before.Name, Hash: before.Hash, Region: "HK"}}
	if !c.RefreshIdentities(live) || c.Recent[0].Name != "renamed "+before.Name {
		t.Errorf("recent nodes follow a rename: %+v", c.Recent[0])
	}
	if s.Recent[0] != before {
		t.Error("refreshing a clone changed the original")
	}
}

func TestNewPlanStartsEmptyOrAsACopyOfAnyPlan(t *testing.T) {
	s := NewState()
	if err := s.SetFallback(Direct()); err != nil {
		t.Fatal(err)
	}
	// From nothing: no items, everything else follows the main exit.
	empty, err := s.NewPlan(" Scratch ", "")
	if err != nil {
		t.Fatal(err)
	}
	if s.ActiveID() != empty.ID || empty.Name != "Scratch" || len(empty.Items) != 0 || empty.Fallback.Kind != DestFollow || empty.Origin != "" {
		t.Fatalf("the empty plan: %+v", empty)
	}
	if _, err := s.AddItem(Item{Name: "Office", Dest: Direct(), Enabled: true, Sources: []Source{InlineSource([]Entry{{Kind: EntryDomainSuffix, Value: "corp.example"}})}}, ""); err != nil {
		t.Fatalf("an item in the empty plan: %v", err)
	}
	// A copy of a plan that is not the one in force takes it as the person
	// left it: the fallback they chose goes with it.
	cp, err := s.NewPlan("Copy", BuiltinID)
	if err != nil {
		t.Fatal(err)
	}
	if s.ActiveID() != cp.ID || len(cp.Items) != len(Builtin().Items) || cp.Fallback.Kind != DestDirect || cp.Origin != Builtin().Name {
		t.Fatalf("the copy: %d items, fallback %s, origin %q", len(cp.Items), cp.Fallback.Kind, cp.Origin)
	}
	if _, err := s.NewPlan("", ""); !errors.Is(err, ErrNoName) {
		t.Errorf("no name: %v", err)
	}
	if _, err := s.NewPlan("X", "p-gone"); !errors.Is(err, ErrNoPlan) {
		t.Errorf("a plan that is not there: %v", err)
	}
}

// A node the person renamed keeps every choice made of it.
func TestRenameNode(t *testing.T) {
	old := NodeID{Sub: "s1", Name: "Gateway", Hash: "h1"}
	other := NodeID{Sub: "s2", Name: "Gateway", Hash: "h9"}
	s := NewState()
	s.Main = ToNode(old)
	s.Recent = []NodeID{old, other}
	s.Exceptions = []Exception{{ID: "x1", Kind: EntryDomainSuffix, Value: "corp.example", Dest: ToNode(old), Enabled: true}}
	if _, err := s.SaveAsMine("Mine"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetItemDest(s.View().Rows[1].Item.ID, ToNode(old)); err != nil {
		t.Fatal(err)
	}
	s.SetNodeAuto(old, false)
	s.SetNodeAuto(other, false)

	next := NodeID{Sub: "s1", Name: "Gateway 2", Hash: "h2"}
	if !s.RenameNode("s1", "Gateway", next) {
		t.Fatal("nothing changed")
	}
	if *s.Main.Node != next || *s.Exceptions[0].Dest.Node != next || *s.View().Rows[1].Item.Dest.Node != next {
		t.Fatalf("destinations: main %v exception %v item %v", s.Main.Node, s.Exceptions[0].Dest.Node, s.View().Rows[1].Item.Dest.Node)
	}
	if s.Recent[0] != next || s.Recent[1] != other {
		t.Fatalf("recent = %v", s.Recent)
	}
	if !s.NodePref(next).NoAuto || s.NodePref(next).Hash != "h2" || !s.NodePref(other).NoAuto {
		t.Fatalf("prefs = %+v", s.Nodes)
	}
	if _, ok := s.Nodes[old.Key()]; ok {
		t.Fatal("the old name's preferences stayed")
	}
	if s.RenameNode("s1", "Gateway", next) {
		t.Fatal("renaming what is no longer there changed something")
	}
	// A name already among the recent ones is one entry.
	s.Recent = []NodeID{next, {Sub: "s1", Name: "Other"}}
	s.RenameNode("s1", "Other", next)
	if len(s.Recent) != 1 || s.Recent[0] != next {
		t.Fatalf("recent = %v", s.Recent)
	}
}
