package catalog_test

import (
	"slices"
	"strings"
	"testing"

	"eggbox/internal/catalog"
	"eggbox/internal/routing"
	"eggbox/internal/ruleurl"
)

func set(names []string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

func TestSnapshotsAreWhatTheyClaim(t *testing.T) {
	if n := len(catalog.GeoSiteNames()); n < 1500 {
		t.Errorf("only %d geosite names", n)
	}
	if n := len(catalog.GeoIPNames()); n < 200 {
		t.Errorf("only %d geoip names", n)
	}
	if n := len(catalog.Blackmatrix7Names()); n < 500 {
		t.Errorf("only %d blackmatrix7 categories", n)
	}
	for _, name := range [][]string{catalog.GeoSiteNames(), catalog.GeoIPNames(), catalog.Blackmatrix7Names()} {
		for _, n := range name {
			if strings.ContainsAny(n, " \t/") {
				t.Fatalf("%q is not a name that can sit in an address", n)
			}
		}
	}
}

func TestEveryEntryIsWellFormedAndPointsAtNamesThatExist(t *testing.T) {
	sites, ips, b7 := set(catalog.GeoSiteNames()), set(catalog.GeoIPNames()), set(catalog.Blackmatrix7Names())
	entries := catalog.Entries()
	if len(entries) < 60 || len(entries) > 100 {
		t.Errorf("%d curated entries; the plan is 60 to 100", len(entries))
	}
	ids := map[string]bool{}
	classes := set(nil)
	for _, c := range catalog.Classes {
		classes[string(c)] = true
	}
	for _, e := range entries {
		if e.ID == "" || ids[e.ID] {
			t.Errorf("entry %q: missing or repeated id", e.ID)
		}
		ids[e.ID] = true
		if e.Zh == "" || e.En == "" || e.Icon == "" {
			t.Errorf("%s: name or icon missing", e.ID)
		}
		if !classes[string(e.Class)] {
			t.Errorf("%s: unknown class %q", e.ID, e.Class)
		}
		switch e.Dest {
		case routing.DestFollow, routing.DestDirect, routing.DestReject:
		default:
			t.Errorf("%s: suggests %q", e.ID, e.Dest)
		}
		if (routing.Dest{Kind: e.Dest}).Valid(false) != nil {
			t.Errorf("%s: destination is not valid", e.ID)
		}
		if len(e.Sites)+len(e.IPs) == 0 {
			t.Errorf("%s: nothing to read rules from", e.ID)
		}
		for _, s := range e.Sites {
			if !sites[s] {
				t.Errorf("%s: MetaCubeX has no geosite %q", e.ID, s)
			}
		}
		for _, s := range e.IPs {
			if !ips[s] {
				t.Errorf("%s: MetaCubeX has no geoip %q", e.ID, s)
			}
		}
		if e.Clash != "" && !b7[e.Clash] {
			t.Errorf("%s: blackmatrix7 has no category %q", e.ID, e.Clash)
		}
		if e.Dest == routing.DestReject && e.Prio != catalog.PrioBlock {
			t.Errorf("%s: blocks but is not placed first", e.ID)
		}
		if e.Desc("zh") == "" || e.Desc("en") == "" {
			t.Errorf("%s: no description in a language", e.ID)
		}
		for _, src := range e.Sources() {
			info, err := ruleurl.Canonical(src.URL)
			if err != nil || info.URL != src.URL || info.Project != catalog.ProjectMetaCubeX {
				t.Errorf("%s: %s is not canonical (%v)", e.ID, src.URL, err)
			}
		}
		for _, alt := range e.Alternatives() {
			if len(alt.Sources) == 0 || alt.Project == "" {
				t.Errorf("%s: an alternative says nothing", e.ID)
			}
			for _, src := range alt.Sources {
				info, err := ruleurl.Canonical(src.URL)
				if err != nil || info.URL != src.URL {
					t.Errorf("%s: %s is not canonical (%v)", e.ID, src.URL, err)
				}
				if info.MRS {
					t.Errorf("%s: %s is a binary file nobody can read", e.ID, src.URL)
				}
			}
		}
	}
	// The shipped plan's items are all in the catalog, with the same rules, so
	// choosing one from the directory gives what the plan has.
	for _, it := range routing.Builtin().Items {
		e, ok := catalog.Get(it.Key)
		if !ok {
			// Some shipped items are made of several entries' rules.
			continue
		}
		if got := e.Sources(); len(got) != len(it.Sources) {
			t.Errorf("%s: the catalog has %d sources, the plan %d", it.Key, len(got), len(it.Sources))
			continue
		}
		for i, s := range e.Sources() {
			if s.URL != it.Sources[i].URL {
				t.Errorf("%s: source %d differs from the shipped plan's", it.Key, i)
			}
		}
		if e.Dest != it.Dest.Kind {
			t.Errorf("%s: catalog suggests %q, the plan %q", it.Key, e.Dest, it.Dest.Kind)
		}
	}
}

func TestSearchFindsWhatPeopleTypeInEitherLanguage(t *testing.T) {
	first := func(q string) string {
		t.Helper()
		r := catalog.Search(q, "zh", 30)
		if len(r.Curated) == 0 {
			t.Fatalf("%q found nothing curated", q)
		}
		return r.Curated[0].ID
	}
	for q, want := range map[string]string{
		"netflix": "netflix", "奈飞": "netflix", "网飞": "netflix", "Netflix": "netflix",
		"电报": "telegram", "tg": "telegram", "油管": "youtube", "广告": "ads", "adblock": "ads",
		"推特": "twitter", "谷歌": "google", "ai": "ai", "chatgpt": "openai", "steam": "steam",
	} {
		if got := first(q); got != want {
			t.Errorf("%q finds %q first, want %q", q, got, want)
		}
	}
	// Words may come in any order and all of them count.
	if r := catalog.Search("steam 国内", "zh", 10); len(r.Curated) == 0 || r.Curated[0].ID != "steam-cn" {
		t.Errorf("two words: %+v", r.Curated)
	}
	// A class name finds every entry of the class.
	games := catalog.Search("游戏", "zh", 10).Curated
	for _, id := range []string{"steam", "epic", "nintendo", "games-cn"} {
		if !slices.ContainsFunc(games, func(e catalog.Entry) bool { return e.ID == id }) {
			t.Errorf("游戏 does not find %s", id)
		}
	}
	if r := catalog.Search("zzzzqqqq", "en", 10); len(r.Curated)+len(r.GeoSite)+len(r.Other) != 0 {
		t.Errorf("nonsense found %+v", r)
	}
	// Empty: everything curated, nothing else.
	r := catalog.Search("  ", "en", 10)
	if len(r.Curated) != len(catalog.Entries()) || len(r.GeoSite) != 0 || len(r.Other) != 0 {
		t.Errorf("empty query: %d curated, %d geosite, %d other", len(r.Curated), len(r.GeoSite), len(r.Other))
	}
}

func TestSearchListsTheThreePartsInOrderWithoutRepeats(t *testing.T) {
	r := catalog.Search("bili", "en", 5)
	if len(r.Curated) == 0 || r.Curated[0].ID != "bilibili" {
		t.Fatalf("curated: %+v", r.Curated)
	}
	for _, h := range r.GeoSite {
		if h.Name == "bilibili" {
			t.Error("the geosite the curated entry already uses is listed again")
		}
		if h.Source.URL != ruleurl.GeoSite(h.Name) || h.Source.Project != catalog.ProjectMetaCubeX {
			t.Errorf("geosite hit source: %+v", h.Source)
		}
		if !strings.Contains(h.Name, "bili") {
			t.Errorf("%q does not match", h.Name)
		}
	}
	if len(r.GeoSite) > 5 || r.MoreGeoSite < 0 {
		t.Errorf("the limit was not applied: %d, more %d", len(r.GeoSite), r.MoreGeoSite)
	}
	// A name only the other project has comes with its own address.
	r = catalog.Search("3type", "en", 5)
	if len(r.Other) != 1 || r.Other[0].Name != "3Type" || r.Other[0].Project != catalog.ProjectBlackmatrix7 ||
		r.Other[0].Source.URL != "https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/master/rule/Clash/3Type/3Type.list" {
		t.Errorf("other: %+v", r.Other)
	}
	// Exact names come before longer ones.
	r = catalog.Search("tiktok", "en", 30)
	for i, h := range r.GeoSite {
		if i > 0 && len(h.Name) < len(r.GeoSite[i-1].Name) && !strings.HasPrefix(h.Name, "tiktok") {
			t.Errorf("order: %v", r.GeoSite)
			break
		}
	}
}

func TestAnEntryBecomesAnItemTheWayThePlanExpects(t *testing.T) {
	e, _ := catalog.Get("netflix")
	it := e.Item("zh", -1)
	if it.Name != "Netflix" || it.Catalog != "netflix" || !it.Enabled || it.Dest.Kind != routing.DestFollow || it.Domestic {
		t.Errorf("%+v", it)
	}
	if len(it.Sources) != 2 || it.Sources[0].URL != ruleurl.GeoSite("netflix") || it.Sources[1].URL != ruleurl.GeoIP("netflix") {
		t.Errorf("sources: %+v", it.Sources)
	}
	cn, _ := catalog.Get("cn")
	it = cn.Item("en", -1)
	if it.Name != "China sites" || !it.Domestic || it.Dest.Kind != routing.DestDirect || !slices.Contains(it.Aliases, "国内网站") {
		t.Errorf("%+v", it)
	}
	// An alternative replaces the recommended rules.
	ads, _ := catalog.Get("ads")
	alts := ads.Alternatives()
	if len(alts) != 5 {
		t.Fatalf("%d alternatives for ads", len(alts))
	}
	got := ads.Item("zh", 1)
	if len(got.Sources) != 1 || got.Sources[0].Project != "anti-AD" && got.Sources[0].Project != "217heidai" {
		t.Errorf("alternative item: %+v", got.Sources)
	}
	if ads.Item("zh", 99).Sources[0].Project != catalog.ProjectMetaCubeX {
		t.Error("an alternative that does not exist should fall back to the recommended rules")
	}
	// What the catalog makes can be added to a plan, where the entry belongs.
	st := routing.NewState()
	steam, _ := catalog.Get("steam")
	added, err := st.AddItem(steam.Item("en", -1), catalog.InsertBefore(routing.Builtin().Items, steam))
	if err != nil {
		t.Fatal(err)
	}
	rows := st.View().Rows
	for i, r := range rows {
		if r.Item.ID == added.ID && rows[i+1].Item.Key != "cn" {
			t.Errorf("steam went before %q", rows[i+1].Item.Name)
		}
	}
}

func TestWhereAnEntryGoes(t *testing.T) {
	plan := routing.Builtin()
	id := func(key string) string {
		for _, it := range plan.Items {
			if it.Key == key {
				return it.ID
			}
		}
		t.Fatalf("no %s", key)
		return ""
	}
	pick := func(id string) catalog.Entry { e, _ := catalog.Get(id); return e }

	// A blocking entry goes before the first item that does not block: the
	// shipped plan starts with the ad item, which blocks, so after it.
	if got := catalog.InsertBefore(plan.Items, pick("win-spy")); got != id("ai") {
		t.Errorf("a blocking entry goes before %q", got)
	}
	// A service goes before the broad categories.
	if got := catalog.InsertBefore(plan.Items, pick("steam")); got != id("cn") {
		t.Errorf("a service goes before %q", got)
	}
	// A broad category goes last.
	if got := catalog.InsertBefore(plan.Items, pick("cn")); got != "" {
		t.Errorf("a broad category goes before %q", got)
	}
	// The same works for a plan that came from elsewhere, where only the names say
	// what is broad.
	other := []routing.Item{
		{ID: "1", Name: "🚀 Telegram"}, {ID: "2", Name: "🔒 国内服务"}, {ID: "3", Name: "🐟 其他"},
	}
	if got := catalog.InsertBefore(other, pick("steam")); got != "2" {
		t.Errorf("by name: before %q", got)
	}
	if got := catalog.InsertBefore(nil, pick("steam")); got != "" {
		t.Errorf("an empty plan: %q", got)
	}
}

func TestAnEntryTheplanAlreadyHasIsRecognized(t *testing.T) {
	plan := routing.Builtin()
	for id, want := range map[string]string{"telegram": "Telegram", "netflix": "流媒体", "github": "GitHub"} {
		e, _ := catalog.Get(id)
		got, ok := catalog.Covers(plan.Items, e)
		if !ok || got != want {
			t.Errorf("%s is covered by %q (%v), want %q", id, got, ok, want)
		}
	}
	for _, id := range []string{"gitlab", "steam", "twitch"} {
		e, _ := catalog.Get(id)
		if got, ok := catalog.Covers(plan.Items, e); ok {
			t.Errorf("%s is said to be covered by %q", id, got)
		}
	}
}

func TestProjectsAreTheOnesTheDocumentNames(t *testing.T) {
	want := []string{"MetaCubeX", "SagerNet", "Loyalsoldier", "DustinWin", "blackmatrix7", "ACL4SSR", "SukkaW", "VPSDance", "anti-AD", "217heidai", "AWAvenue", "hagezi"}
	have := map[string]bool{}
	for _, p := range catalog.Projects() {
		have[p.Name] = true
		info, err := ruleurl.Canonical(p.Example)
		if err != nil || info.URL != p.Example {
			t.Errorf("%s: %s is not canonical (%v)", p.Name, p.Example, err)
		}
		if info.Project != p.Name {
			t.Errorf("%s: its example is attributed to %q", p.Name, info.Project)
		}
		if info.MRS {
			t.Errorf("%s: its example is unreadable", p.Name)
		}
	}
	for _, n := range want {
		if !have[n] {
			t.Errorf("no example for %s", n)
		}
	}
}
