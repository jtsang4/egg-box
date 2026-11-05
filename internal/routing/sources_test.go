package routing

import (
	"errors"
	"slices"
	"testing"
)

func sourceNames(it Item) []string {
	var out []string
	for _, s := range it.Sources {
		out = append(out, s.Name)
	}
	return out
}

func rowOf(t *testing.T, st State, name string) Row {
	t.Helper()
	for _, r := range st.View().Rows {
		if NormName(r.Item.Name) == NormName(name) {
			return r
		}
	}
	t.Fatalf("no item %q in %v", name, names(st.View()))
	return Row{}
}

func inline(entries ...Entry) Source { return InlineSource(entries) }

// An item the user owns can be given more rules and lose some, but not its last.
func TestSourcesOfAnItemTheUserOwns(t *testing.T) {
	st := NewState()
	it, err := st.AddItem(Item{Name: "Work", Dest: Direct(), Enabled: true, Sources: []Source{
		inline(Entry{EntryDomainSuffix, "corp.example"}),
	}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddSource(it.ID, URLSource("https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/sing/geo/geosite/slack.srs", "geosite: slack", "MetaCubeX")); err != nil {
		t.Fatal(err)
	}
	if err := st.AddSource(it.ID, URLSource("https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/sing/geo/geosite/slack.srs", "again", "")); !errors.Is(err, ErrDuplicateSource) {
		t.Fatalf("the same file twice: %v", err)
	}
	if err := st.AddSource(it.ID, Source{Kind: SourceURL, URL: "not a url"}); err == nil {
		t.Fatal("an address that is not one was accepted")
	}
	// A page of a file on GitHub is stored as the file.
	if err := st.AddSource(it.ID, Source{Kind: SourceURL, URL: "https://github.com/blackmatrix7/ios_rule_script/blob/master/rule/Clash/Zoom/Zoom.list"}); err != nil {
		t.Fatal(err)
	}
	row := rowOf(t, st, "Work")
	if len(row.Item.Sources) != 3 || row.Item.Sources[2].URL != "https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/master/rule/Clash/Zoom/Zoom.list" {
		t.Fatalf("%+v", row.Item.Sources)
	}
	if row.Item.Sources[2].Project != "blackmatrix7" || row.Item.Sources[2].Name != "Zoom" {
		t.Errorf("the source is not described: %+v", row.Item.Sources[2])
	}

	// Edit the hand-written one.
	var inl Source
	for _, s := range row.Item.Sources {
		if s.Kind == SourceInline {
			inl = s
		}
	}
	if err := st.SetEntries(it.ID, inl.ID, []Entry{{EntryDomainSuffix, "*.Corp.Example"}, {EntryDomainSuffix, "corp.example"}, {EntryIPCIDR, "10.1.2.3"}}); err != nil {
		t.Fatal(err)
	}
	got := rowOf(t, st, "Work").Item.Sources[0].Entries
	if !slices.Equal(got, []Entry{{EntryDomainSuffix, "corp.example"}, {EntryIPCIDR, "10.1.2.3/32"}}) {
		t.Errorf("entries were not normalized and deduplicated: %v", got)
	}
	if err := st.SetEntries(it.ID, inl.ID, nil); !errors.Is(err, ErrNoEntries) {
		t.Errorf("emptying: %v", err)
	}
	if err := st.SetEntries(it.ID, inl.ID, []Entry{{EntryIPCIDR, "bad"}}); err == nil {
		t.Error("an invalid entry was accepted")
	}
	if err := st.SetEntries(it.ID, row.Item.Sources[1].ID, []Entry{{EntryDomain, "a.b"}}); !errors.Is(err, ErrNotInline) {
		t.Errorf("editing a file: %v", err)
	}

	// Remove until one is left.
	for _, s := range row.Item.Sources[1:] {
		if err := st.RemoveSource(it.ID, s.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.RemoveSource(it.ID, inl.ID); !errors.Is(err, ErrLastSource) {
		t.Errorf("removing the last: %v", err)
	}
	if err := st.RemoveSource(it.ID, "nope"); !errors.Is(err, ErrNoSource) {
		t.Errorf("removing nothing: %v", err)
	}
}

func TestEntryLimitsAreEnforced(t *testing.T) {
	st := NewState()
	var many []Entry
	for i := 0; i <= MaxEntries; i++ {
		many = append(many, Entry{EntryPort, "1"})
	}
	// Repeats count as written, so a paste of a million lines is refused before
	// anything is done with it.
	if _, err := st.AddItem(Item{Name: "Many", Dest: Follow(), Enabled: true, Sources: []Source{inline(many...)}}, ""); err != nil {
		// AddItem does not look at sources; the source operations do.
		t.Logf("AddItem: %v", err)
	}
	it, _ := st.AddItem(Item{Name: "One", Dest: Follow(), Enabled: true, Sources: []Source{inline(Entry{EntryPort, "1"})}}, "")
	if err := st.AddSource(it.ID, inline(many...)); !errors.Is(err, ErrTooManyEntries) {
		t.Errorf("too many: %v", err)
	}
}

// What a person adds to an item a plan owns outlives the plan's refresh, can be
// taken away again, and is not confused with the plan's own sources.
func TestSourcesAddedToAnItemThePlanOwns(t *testing.T) {
	st := NewState()
	st.Plans = append(st.Plans, airport("AI", "Streaming"))
	if err := st.Switch(SubPlanID("s1")); err != nil {
		t.Fatal(err)
	}
	ai := rowOf(t, st, "AI")
	if len(ai.Item.Sources) != 1 || len(ai.Extra) != 0 {
		t.Fatalf("%+v", ai)
	}
	if err := st.AddSource(ai.Item.ID, inline(Entry{EntryDomainSuffix, "ai-startup.example"})); err != nil {
		t.Fatal(err)
	}
	if err := st.AddSource(ai.Item.ID, URLSource("https://example.test/more.srs", "more", "")); err != nil {
		t.Fatal(err)
	}
	ai = rowOf(t, st, "AI")
	if len(ai.Item.Sources) != 3 || len(ai.Extra) != 2 || !ai.Overridden {
		t.Fatalf("%+v", ai)
	}
	if ai.Item.Sources[0].URL != "https://example.test/ai.srs" || !slices.Contains(ai.Extra, ai.Item.Sources[1].ID) || slices.Contains(ai.Extra, ai.Item.Sources[0].ID) {
		t.Errorf("extras are not told apart: %+v", ai)
	}
	// The plan's own source cannot go; the same file cannot be added twice.
	if err := st.RemoveSource(ai.Item.ID, ai.Item.Sources[0].ID); !errors.Is(err, ErrLocked) {
		t.Errorf("removing the plan's source: %v", err)
	}
	if err := st.AddSource(ai.Item.ID, URLSource("https://example.test/ai.srs", "", "")); !errors.Is(err, ErrDuplicateSource) {
		t.Errorf("a source the plan has: %v", err)
	}
	if err := st.AddSource(ai.Item.ID, URLSource("https://example.test/more.srs", "", "")); !errors.Is(err, ErrDuplicateSource) {
		t.Errorf("an extra twice: %v", err)
	}

	// A refresh brings new content; the person's sources stay.
	fresh := airport("AI", "Streaming", "Games")
	fresh.Items[0].Sources = []Source{URLSource("https://example.test/ai-v2.srs", "AI", "Example")}
	st.SyncPlan(fresh)
	ai = rowOf(t, st, "AI")
	if len(ai.Item.Sources) != 3 || ai.Item.Sources[0].URL != "https://example.test/ai-v2.srs" || len(ai.Extra) != 2 {
		t.Errorf("after the refresh: %+v", ai.Item.Sources)
	}

	// Edit and remove what they added.
	inl := ai.Item.Sources[1]
	if err := st.SetEntries(ai.Item.ID, inl.ID, []Entry{{EntryDomainSuffix, "other.example"}}); err != nil {
		t.Fatal(err)
	}
	if got := rowOf(t, st, "AI").Item.Sources[1].Entries; len(got) != 1 || got[0].Value != "other.example" {
		t.Errorf("edited: %v", got)
	}
	if err := st.SetEntries(ai.Item.ID, ai.Item.Sources[0].ID, []Entry{{EntryDomain, "a.b"}}); !errors.Is(err, ErrLocked) && !errors.Is(err, ErrNotInline) {
		t.Errorf("editing the plan's source: %v", err)
	}
	if err := st.RemoveSource(ai.Item.ID, inl.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.RemoveSource(ai.Item.ID, ai.Item.Sources[2].ID); err != nil {
		t.Fatal(err)
	}
	ai = rowOf(t, st, "AI")
	if len(ai.Item.Sources) != 1 || ai.Overridden || len(ai.Extra) != 0 {
		t.Errorf("everything removed, still %+v", ai)
	}
	if len(st.Overlays[SubPlanID("s1")].Extra) != 0 {
		t.Error("an empty list of extras was kept")
	}

	// The plan's own items cannot be renamed or re-iconed.
	if err := st.RenameItem(ai.Item.ID, "Robots"); !errors.Is(err, ErrLocked) {
		t.Errorf("rename: %v", err)
	}
	if err := st.SetItemIcon(ai.Item.ID, "globe"); !errors.Is(err, ErrLocked) {
		t.Errorf("icon: %v", err)
	}
}

// Switching to another plan takes the person's additions to the item of the
// same name along.
func TestAdditionsFollowTheItemToAnotherPlan(t *testing.T) {
	st := NewState()
	st.Plans = append(st.Plans, airport("AI", "Streaming"))
	// Start on the shipped plan, whose AI item is called "AI 服务" (alias: ai).
	ai := rowOf(t, st, "AI 服务")
	if err := st.AddSource(ai.Item.ID, inline(Entry{EntryDomainSuffix, "ai-startup.example"})); err != nil {
		t.Fatal(err)
	}
	if err := st.Switch(SubPlanID("s1")); err != nil {
		t.Fatal(err)
	}
	got := rowOf(t, st, "AI")
	if len(got.Extra) != 1 || got.Item.Sources[1].Entries[0].Value != "ai-startup.example" {
		t.Fatalf("the addition did not follow: %+v", got.Item.Sources)
	}
	// Switching back and forth does not pile them up.
	if err := st.Switch(BuiltinID); err != nil {
		t.Fatal(err)
	}
	if err := st.Switch(SubPlanID("s1")); err != nil {
		t.Fatal(err)
	}
	if got := rowOf(t, st, "AI"); len(got.Extra) != 1 {
		t.Errorf("%d additions after switching back and forth", len(got.Extra))
	}
	// A copy of the effective plan keeps them as the item's own sources.
	mine, err := st.SaveAsMine("Mine")
	if err != nil {
		t.Fatal(err)
	}
	if got := rowOf(t, st, "AI"); got.Origin != OriginMine || len(got.Item.Sources) != 2 || len(got.Extra) != 0 {
		t.Errorf("saved as mine: %+v (plan %s)", got, mine.ID)
	}
}

func TestRenameAndIconOfOwnItems(t *testing.T) {
	st := NewState()
	a, _ := st.AddItem(Item{Name: "Work", Dest: Direct(), Enabled: true, Sources: []Source{inline(Entry{EntryDomain, "a.b"})}}, "")
	b, _ := st.AddItem(Item{Name: "Home", Dest: Direct(), Enabled: true, Sources: []Source{inline(Entry{EntryDomain, "c.d"})}}, "")
	if err := st.RenameItem(a.ID, "  Office "); err != nil {
		t.Fatal(err)
	}
	if rowOf(t, st, "Office").Item.ID != a.ID {
		t.Error("not renamed")
	}
	if err := st.RenameItem(b.ID, "🏢 office"); !errors.Is(err, ErrDuplicateName) {
		t.Errorf("a name another item has: %v", err)
	}
	if err := st.RenameItem(b.ID, "AI 服务"); !errors.Is(err, ErrDuplicateName) {
		t.Errorf("a name the plan's item has: %v", err)
	}
	if err := st.RenameItem(b.ID, " "); !errors.Is(err, ErrNoName) {
		t.Errorf("no name: %v", err)
	}
	if err := st.RenameItem("nope", "X"); !errors.Is(err, ErrNoItem) {
		t.Errorf("no item: %v", err)
	}
	if err := st.SetItemIcon(b.ID, "home"); err != nil {
		t.Fatal(err)
	}
	if rowOf(t, st, "Home").Item.Icon != "home" {
		t.Error("icon not set")
	}
	// Renaming it keeps where it sits and what it holds.
	if got := rowOf(t, st, "Office").Item; len(got.Sources) != 1 || got.Dest.Kind != DestDirect {
		t.Errorf("%+v", got)
	}
}

func TestCopyingAnItemToAPlanOfMyOwn(t *testing.T) {
	st := NewState()
	st.Plans = append(st.Plans, airport("AI"))
	// A plan of my own to copy to.
	mine, err := st.SaveAsMine("Mine")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Switch(SubPlanID("s1")); err != nil {
		t.Fatal(err)
	}
	if err := st.SetItemDest("a-ai", ToRegion("US")); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CopyItemToMine("a-ai", mine.ID); !errors.Is(err, ErrDuplicateName) {
		// "AI" is already among the shipped items "Mine" was made from.
		t.Fatalf("a name the plan has: %v", err)
	}
	mine.Items = nil // an empty plan of my own
	it, err := st.CopyItemToMine("a-ai", mine.ID)
	if err != nil {
		t.Fatal(err)
	}
	if it.Dest.Kind != DestRegion || it.Dest.Region != "US" || it.Key != "" || it.ID == "a-ai" || len(it.Sources) != 1 {
		t.Errorf("the copy: %+v", it)
	}
	if got := st.PlanByID(mine.ID); len(got.Items) != 1 || got.Items[0].Name != "AI" {
		t.Errorf("the plan: %+v", got.Items)
	}
	// Not into the plan in force, and not into one that is not mine.
	if _, err := st.CopyItemToMine("a-ai", SubPlanID("s1")); !errors.Is(err, ErrNotMine) {
		t.Errorf("to a followed plan: %v", err)
	}
	if err := st.Switch(mine.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CopyItemToMine(it.ID, mine.ID); !errors.Is(err, ErrNotMine) {
		t.Errorf("to itself: %v", err)
	}
}
