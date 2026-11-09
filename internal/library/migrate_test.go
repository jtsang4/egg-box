package library_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"eggbox/internal/library"
	"eggbox/internal/routing"
	"eggbox/internal/subscription"
)

// legacy builds the library file an older version wrote, around nodes that
// exist in dir: the subscription entries are taken from a library written by
// this version, and the routing fields of the old format are laid over them.
type legacy struct {
	preset       string
	blockAds     bool
	rules        []map[string]string
	selectedNode string
	// selectNode, when set, selects the node of that name by its key.
	selectNode  string
	updateHours int
}

// writeLegacy creates dir with one local subscription and returns the Keys of its nodes.
func writeLegacy(t *testing.T, dir string, l legacy) (subID string, keys map[string]string) {
	t.Helper()
	lib := openWith(t, dir, library.NewMemorySecrets())
	res, err := subscription.Parse([]byte(linkList("HK 01", "JP 01")))
	if err != nil {
		t.Fatal(err)
	}
	sub, err := lib.AddLocal("Mine", res)
	if err != nil {
		t.Fatal(err)
	}
	keys = map[string]string{}
	for _, n := range res.Nodes {
		keys[n.Name] = n.Key()
	}
	raw, err := os.ReadFile(filepath.Join(dir, "library.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc["version"] = 1
	delete(doc, "routing")
	prefs := doc["prefs"].(map[string]any)
	prefs["preset"], prefs["blockAds"], prefs["regionGroups"] = l.preset, l.blockAds, true
	doc["rules"] = l.rules
	settings := doc["settings"].(map[string]any)
	if l.selectNode != "" {
		l.selectedNode = keys[l.selectNode]
	}
	settings["selectedNode"], settings["ruleUpdateHours"] = l.selectedNode, l.updateHours
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "library.json"), out, 0o600); err != nil {
		t.Fatal(err)
	}
	return sub.ID, keys
}

func rule(kind, value, target string) map[string]string {
	return map[string]string{"kind": kind, "value": value, "target": target}
}

func rowDest(t *testing.T, st routing.State, id string) (routing.Dest, bool) {
	t.Helper()
	for _, r := range st.View().Rows {
		if r.Item.ID == id {
			return r.Item.Dest, r.Item.Enabled
		}
	}
	t.Fatalf("no item %s", id)
	return routing.Dest{}, false
}

func TestMigrateFromTheFirstFormat(t *testing.T) {
	dir := t.TempDir()
	sub, keys := writeLegacy(t, dir, legacy{
		preset: "proxy-all", blockAds: true, updateHours: 72, selectedNode: "",
		rules: []map[string]string{
			rule("domain_suffix", "*.Corp.Example", "direct"),
			rule("domain_keyword", "tracker", "reject"),
			rule("ip_cidr", "203.0.113.0/24", "Proxy"),
			rule("process_name", "curl", "Proxy"),
			rule("port", "22", "direct"),
			rule("ip_cidr", "not-an-ip", "direct"), // cannot be carried over
			rule("bogus", "x", "direct"),           // neither can this
		},
	})
	original, err := os.ReadFile(filepath.Join(dir, "library.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(original), `"selectedNode"`) {
		t.Fatal("the fixture is not a first-format file")
	}
	_ = keys

	l := openWith(t, dir, library.NewMemorySecrets())
	st := l.Routing()

	// proxy-all: the shipped plan with "China sites" following the main exit; ads switched on.
	if d, _ := rowDest(t, st, "b-cn"); d.Kind != routing.DestFollow {
		t.Errorf("cn: %+v", d)
	}
	if d, on := rowDest(t, st, "b-ads"); !on || d.Kind != routing.DestReject {
		t.Errorf("ads: %+v on=%v", d, on)
	}
	if d, on := rowDest(t, st, "b-github"); !on || d.Kind != routing.DestFollow {
		t.Errorf("an item nobody touched keeps the plan's choices: %+v on=%v", d, on)
	}
	if d, _ := rowDest(t, st, "b-apple"); d.Kind != routing.DestDirect {
		t.Errorf("apple: %+v", d)
	}
	if st.View().Fallback.Kind != routing.DestFollow {
		t.Errorf("fallback %+v", st.View().Fallback)
	}
	if st.Active != routing.BuiltinID {
		t.Errorf("active plan %q", st.Active)
	}
	if st.Main.Kind != routing.DestAuto {
		t.Errorf("no selected node means the fastest one: %+v", st.Main)
	}

	// The rules keep their order; the ones that cannot be carried over are counted, not silently lost.
	if len(st.Exceptions) != 5 {
		t.Fatalf("%d exceptions: %+v", len(st.Exceptions), st.Exceptions)
	}
	want := []struct {
		kind routing.EntryKind
		val  string
		dest routing.DestKind
	}{
		{routing.EntryDomainSuffix, "corp.example", routing.DestDirect},
		{routing.EntryDomainKeyword, "tracker", routing.DestReject},
		{routing.EntryIPCIDR, "203.0.113.0/24", routing.DestFollow},
		{routing.EntryProcessName, "curl", routing.DestFollow},
		{routing.EntryPort, "22", routing.DestDirect},
	}
	for i, w := range want {
		x := st.Exceptions[i]
		if x.Kind != w.kind || x.Value != w.val || x.Dest.Kind != w.dest || !x.Enabled || x.ID == "" {
			t.Errorf("exception %d: %+v, want %+v", i, x, w)
		}
	}
	notes := l.PendingNotices()
	if len(notes) != 1 || notes[0].Code != "migrate_rules_dropped" || notes[0].Args["count"] != "2" {
		t.Errorf("notices %+v", notes)
	}

	// The old interval was the old default, so it takes the new one.
	if h := l.Settings().RuleUpdateHours; h != library.DefaultRuleUpdateHours {
		t.Errorf("rule update hours %d", h)
	}
	if _, ok := l.Subscription(sub); !ok {
		t.Error("the subscription is gone")
	}

	// The file is upgraded and the old one kept next to it.
	var doc struct {
		Version int `json:"version"`
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "library.json"))
	if err := json.Unmarshal(raw, &doc); err != nil || doc.Version != 2 {
		t.Fatalf("version %d, %v", doc.Version, err)
	}
	if strings.Contains(string(raw), `"selectedNode"`) || strings.Contains(string(raw), `"preset"`) {
		t.Error("the old fields are still written")
	}
	backup, err := os.ReadFile(filepath.Join(dir, "library.v1.json"))
	if err != nil || string(backup) != string(original) {
		t.Errorf("the old file was not kept as it was: %v", err)
	}

	// Opening it again changes nothing, and neither does opening an upgraded file a third time.
	for i := 0; i < 2; i++ {
		again := openWith(t, dir, library.NewMemorySecrets())
		if !reflect.DeepEqual(again.Routing(), st) {
			t.Fatalf("round %d: the routing state changed on a second open", i)
		}
		if got := again.PendingNotices(); !reflect.DeepEqual(got, notes) {
			t.Fatalf("round %d: notices %+v, want %+v", i, got, notes)
		}
	}
}

func TestMigratePresetsAndSelections(t *testing.T) {
	cases := []struct {
		name  string
		in    legacy
		check func(t *testing.T, l *library.Library, sub string, keys map[string]string)
	}{
		{"china is the shipped plan as it is", legacy{preset: "china", updateHours: 12},
			func(t *testing.T, l *library.Library, _ string, _ map[string]string) {
				st := l.Routing()
				if d, _ := rowDest(t, st, "b-cn"); d.Kind != routing.DestDirect {
					t.Errorf("cn %+v", d)
				}
				if _, on := rowDest(t, st, "b-ads"); on {
					t.Error("ads must stay off")
				}
				if len(st.Exceptions) != 0 || len(l.PendingNotices()) != 0 {
					t.Errorf("%+v %+v", st.Exceptions, l.PendingNotices())
				}
				if h := l.Settings().RuleUpdateHours; h != 12 {
					t.Errorf("an interval the person chose is kept: %d", h)
				}
			}},
		{"foreign-only sends the rest straight out", legacy{preset: "foreign-only", updateHours: 72},
			func(t *testing.T, l *library.Library, _ string, _ map[string]string) {
				if f := l.Routing().View().Fallback; f.Kind != routing.DestDirect {
					t.Errorf("fallback %+v", f)
				}
				if d, _ := rowDest(t, l.Routing(), "b-cn"); d.Kind != routing.DestDirect {
					t.Errorf("cn %+v", d)
				}
			}},
		{"a region becomes the main exit", legacy{preset: "china", selectedNode: "region-JP", updateHours: 72},
			func(t *testing.T, l *library.Library, _ string, _ map[string]string) {
				if m := l.Routing().Main; m.Kind != routing.DestRegion || m.Region != "JP" {
					t.Errorf("main %+v", m)
				}
			}},
		{"a node is found by its hash", legacy{preset: "china", selectNode: "JP 01", updateHours: 72},
			func(t *testing.T, l *library.Library, sub string, keys map[string]string) {
				m := l.Routing().Main
				if m.Kind != routing.DestNode || m.Node == nil || m.Node.Sub != sub || m.Node.Name != "JP 01" || m.Node.Hash != keys["JP 01"] || m.Node.Region != "JP" {
					t.Fatalf("main %+v node %+v", m, m.Node)
				}
				if len(l.PendingNotices()) != 0 {
					t.Errorf("notices %+v", l.PendingNotices())
				}
			}},
		{"a node that is gone is reported", legacy{preset: "china", selectedNode: "0123456789abcdef", updateHours: 72},
			func(t *testing.T, l *library.Library, _ string, _ map[string]string) {
				if m := l.Routing().Main; m.Kind != routing.DestAuto {
					t.Errorf("main %+v", m)
				}
				n := l.PendingNotices()
				if len(n) != 1 || n[0].Code != "migrate_node_lost" {
					t.Errorf("notices %+v", n)
				}
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			sub, keys := writeLegacy(t, dir, c.in)
			c.check(t, openWith(t, dir, library.NewMemorySecrets()), sub, keys)
		})
	}
}

func TestNotices(t *testing.T) {
	l := open(t, t.TempDir(), nil)
	if err := l.AddNotice("plan_adopted", map[string]string{"name": "A"}); err != nil {
		t.Fatal(err)
	}
	// The same notice is not queued twice while it waits.
	_ = l.AddNotice("plan_adopted", map[string]string{"name": "A"})
	_ = l.AddNotice("plan_adopted", map[string]string{"name": "B"})
	n := l.PendingNotices()
	if len(n) != 2 || n[0].Args["name"] != "A" || n[1].Args["name"] != "B" || n[0].ID == n[1].ID {
		t.Fatalf("%+v", n)
	}
	if err := l.DismissNotice(n[0].ID); err != nil {
		t.Fatal(err)
	}
	if got := l.PendingNotices(); len(got) != 1 || got[0].ID != n[1].ID {
		t.Fatalf("%+v", got)
	}
	if err := l.DismissNotice(n[0].ID); err == nil {
		t.Error("dismissing a notice twice must say so")
	}
	// A queue that nobody reads is bounded.
	for i := 0; i < 200; i++ {
		_ = l.AddNotice("x", map[string]string{"i": string(rune('a'+i%26)) + string(rune('a'+i/26))})
	}
	if got := len(l.PendingNotices()); got > 50 {
		t.Errorf("%d notices are waiting", got)
	}
}

func TestNoticesSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	secrets := library.NewMemorySecrets()
	l := openWith(t, dir, secrets)
	_ = l.AddNotice("plan_adopted", map[string]string{"name": "A"})
	l2 := openWith(t, dir, secrets)
	if got := l2.PendingNotices(); len(got) != 1 || got[0].Code != "plan_adopted" || got[0].Args["name"] != "A" {
		t.Fatalf("%+v", got)
	}
}

func TestRemovingASubscriptionKeepsItsPlanAsTheUsersOwn(t *testing.T) {
	l := open(t, t.TempDir(), nil)
	res, _ := subscription.Parse([]byte(linkList("HK 01")))
	sub, err := l.AddLocal("Aurora", res)
	if err != nil {
		t.Fatal(err)
	}
	planID := routing.SubPlanID(sub.ID)
	if err := l.UpdateRouting(func(st *routing.State) error {
		st.SyncPlan(&routing.Plan{
			ID: planID, Kind: routing.PlanSubscription, Name: "Aurora", SubID: sub.ID, Fallback: routing.Follow(),
			Items: []routing.Item{{ID: "p-ai", Name: "AI", Enabled: true, Dest: routing.Follow(),
				Sources: []routing.Source{routing.InlineSource([]routing.Entry{{Kind: routing.EntryDomainSuffix, Value: "ai.example"}})}}},
		})
		return st.Switch(planID)
	}); err != nil {
		t.Fatal(err)
	}
	if err := l.UpdateRouting(func(st *routing.State) error { return st.SetItemDest("p-ai", routing.ToRegion("US")) }); err != nil {
		t.Fatal(err)
	}
	if err := l.Remove(sub.ID); err != nil {
		t.Fatal(err)
	}
	st := l.Routing()
	p := st.ActivePlan()
	if p == nil || p.Kind != routing.PlanMine || p.SubID != "" || !strings.Contains(p.Origin, "Aurora") {
		t.Fatalf("the plan after its subscription is gone: %+v", p)
	}
	// What the person chose for its items is part of the plan now.
	rows := st.View().Rows
	if len(rows) != 1 || rows[0].Item.Name != "AI" || rows[0].Item.Dest.Kind != routing.DestRegion || rows[0].Item.Dest.Region != "US" {
		t.Fatalf("%+v", rows)
	}
}

// A routing state of the first format gets its hand-written sources marked
// as the person's, so the private ranges they name claim what they meant them
// to; the entries a provider's plan brought stay unmarked. The library format
// does not change, so an older application still opens the file.
func TestUpgradeMarksWrittenSources(t *testing.T) {
	dir := t.TempDir()
	l := openWith(t, dir, library.NewMemorySecrets())
	if err := l.UpdateRouting(func(st *routing.State) error {
		if _, err := st.SaveAsMine("Mine"); err != nil {
			return err
		}
		_, err := st.AddItem(routing.Item{Name: "Own", Dest: routing.Direct(), Enabled: true, Sources: []routing.Source{
			routing.InlineSource([]routing.Entry{{Kind: routing.EntryIPCIDR, Value: "10.0.0.0/8"}}),
			{ID: "nzz9q8w7e6r5t4", Kind: routing.SourceInline, Entries: []routing.Entry{{Kind: routing.EntryIPCIDR, Value: "192.168.0.0/16"}}},
		}}, "")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// What the previous version wrote: routing state 1, no marks.
	path := filepath.Join(dir, "library.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	rt := doc["routing"].(map[string]any)
	if rt["version"] != float64(routing.FormatVersion) {
		t.Fatalf("routing version %v", rt["version"])
	}
	rt["version"] = 1
	var unmark func(v any)
	unmark = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			delete(x, "written")
			for _, c := range x {
				unmark(c)
			}
		case []any:
			for _, c := range x {
				unmark(c)
			}
		}
	}
	unmark(rt)
	old, _ := json.Marshal(doc)
	if err := os.WriteFile(path, old, 0o600); err != nil {
		t.Fatal(err)
	}

	again := openWith(t, dir, library.NewMemorySecrets())
	var row routing.Row
	for _, r := range again.Routing().View().Rows {
		if r.Item.Name == "Own" {
			row = r
		}
	}
	if got := row.Item.Claims(); len(got) != 1 || got[0] != "10.0.0.0/8" {
		t.Fatalf("claims after the upgrade = %v", got)
	}
	if v := again.Routing().Version; v != routing.FormatVersion {
		t.Fatalf("routing version after the upgrade = %d", v)
	}
	raw, _ = os.ReadFile(path)
	var head struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(raw, &head); err != nil || head.Version != 2 {
		t.Fatalf("the library format changed: %d, %v", head.Version, err)
	}
}
