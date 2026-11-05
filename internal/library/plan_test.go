package library_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"eggbox/internal/library"
	"eggbox/internal/routing"
	"eggbox/internal/subscription"
)

// planProfile is an invented Clash profile with one rule set per service name.
func planProfile(services ...string) string {
	var b strings.Builder
	b.WriteString("proxies:\n")
	for i, n := range []string{"HK 01", "JP 01"} {
		fmt.Fprintf(&b, "  - {name: %q, type: trojan, server: n%d.example.net, port: 443, password: x}\n", n, i)
	}
	b.WriteString("proxy-groups:\n")
	b.WriteString("  - {name: \"🚀 Select\", type: select, proxies: [\"⚡ Auto\", \"HK 01\", \"JP 01\", DIRECT]}\n")
	b.WriteString("  - {name: \"⚡ Auto\", type: url-test, url: 'https://www.gstatic.com/generate_204', interval: 300, proxies: [\"HK 01\", \"JP 01\"]}\n")
	b.WriteString("  - {name: \"🐟 Final\", type: select, proxies: [\"🚀 Select\", \"HK 01\", \"JP 01\", DIRECT]}\n")
	for _, s := range services {
		fmt.Fprintf(&b, "  - {name: %q, type: select, proxies: [\"🚀 Select\", \"HK 01\", \"JP 01\", DIRECT]}\n", s)
	}
	b.WriteString("rule-providers:\n")
	for i := range services {
		fmt.Fprintf(&b, "  set%d: {type: http, behavior: domain, format: mrs, url: \"https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/meta/geo/geosite/svc%d.mrs\", path: ./s%d.mrs, interval: 86400}\n", i, i, i)
	}
	b.WriteString("rules:\n")
	for i, s := range services {
		fmt.Fprintf(&b, "  - RULE-SET,set%d,%s\n", i, s)
	}
	b.WriteString("  - MATCH,\"🐟 Final\"\n")
	return b.String()
}

func noticeCodes(l *library.Library) []string {
	var out []string
	for _, n := range l.PendingNotices() {
		out = append(out, n.Code)
	}
	return out
}

func TestFirstSubscriptionsPlanIsAdopted(t *testing.T) {
	l := open(t, t.TempDir(), nil)
	p := newProvider(t, planProfile("💬 AI", "📺 Video"))
	sub, err := l.AddRemote("Aurora", p.link(), library.RemoteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := l.Refresh(context.Background(), sub.ID, library.RefreshOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Plan.Stored || !res.Plan.Adopted || res.Plan.Items != 2 {
		t.Fatalf("plan sync: %+v", res.Plan)
	}
	st := l.Routing()
	if st.ActiveID() != routing.SubPlanID(sub.ID) {
		t.Fatalf("the plan in force is %q", st.ActiveID())
	}
	plan := st.PlanByID(routing.SubPlanID(sub.ID))
	if plan == nil || plan.Name != "Aurora" || plan.SubID != sub.ID || plan.Kind != routing.PlanSubscription {
		t.Fatalf("plan: %+v", plan)
	}
	if st.Main.Kind != routing.DestAuto {
		t.Fatalf("main exit %v", st.Main)
	}
	if got := noticeCodes(l); len(got) != 1 || got[0] != "plan_adopted" {
		t.Fatalf("notices %v", got)
	}
	if n := l.PendingNotices()[0]; n.Args["name"] != "Aurora" {
		t.Fatalf("notice args %v", n.Args)
	}
}

func TestLaterSubscriptionsPlanIsOnlyStored(t *testing.T) {
	l := open(t, t.TempDir(), nil)
	first, _ := l.AddRemote("A", newProvider(t, planProfile("💬 AI")).link(), library.RemoteOptions{})
	if _, err := l.Refresh(context.Background(), first.ID, library.RefreshOptions{}); err != nil {
		t.Fatal(err)
	}
	second, _ := l.AddRemote("B", newProvider(t, planProfile("📺 Video", "🎮 Games")).link(), library.RemoteOptions{})
	res, err := l.Refresh(context.Background(), second.ID, library.RefreshOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Plan.Stored || res.Plan.Adopted {
		t.Fatalf("plan sync: %+v", res.Plan)
	}
	st := l.Routing()
	if st.ActiveID() != routing.SubPlanID(first.ID) || st.PlanByID(routing.SubPlanID(second.ID)) == nil {
		t.Fatalf("active %q, plans %d", st.ActiveID(), len(st.Plans))
	}
}

func TestAPlanIsNotAdoptedOverTheUsersOwnChoices(t *testing.T) {
	l := open(t, t.TempDir(), nil)
	if err := l.UpdateRouting(func(s *routing.State) error {
		_, err := s.UpsertException(routing.Entry{Kind: routing.EntryDomainSuffix, Value: "mine.example"}, routing.Dest{Kind: routing.DestDirect})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	sub, _ := l.AddRemote("A", newProvider(t, planProfile("💬 AI")).link(), library.RemoteOptions{})
	res, err := l.Refresh(context.Background(), sub.ID, library.RefreshOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Plan.Stored || res.Plan.Adopted || l.Routing().ActiveID() != routing.BuiltinID {
		t.Fatalf("plan sync %+v, active %q", res.Plan, l.Routing().ActiveID())
	}
	if len(noticeCodes(l)) != 0 {
		t.Fatalf("notices %v", noticeCodes(l))
	}
}

func TestARefreshReportsWhatTheProviderChanged(t *testing.T) {
	l := open(t, t.TempDir(), nil)
	p := newProvider(t, planProfile("💬 AI", "📺 Video"))
	sub, _ := l.AddRemote("A", p.link(), library.RemoteOptions{})
	if _, err := l.Refresh(context.Background(), sub.ID, library.RefreshOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, n := range l.PendingNotices() {
		_ = l.DismissNotice(n.ID)
	}
	// The person turns one item off; the provider drops Video and adds Games.
	if err := l.UpdateRouting(func(s *routing.State) error {
		return s.SetItemEnabled(s.View().Rows[0].Item.ID, false)
	}); err != nil {
		t.Fatal(err)
	}
	p.body.Store(planProfile("💬 AI", "🎮 Games"))
	res, err := l.Refresh(context.Background(), sub.ID, library.RefreshOptions{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Plan.Change.Added) != 1 || len(res.Plan.Change.Removed) != 1 || res.Plan.Adopted {
		t.Fatalf("plan change %+v", res.Plan)
	}
	got := noticeCodes(l)
	if len(got) != 2 || got[0] != "plan_items_added" || got[1] != "plan_items_removed" {
		t.Fatalf("notices %v", got)
	}
	for _, n := range l.PendingNotices() {
		if n.Args["count"] != "1" || n.Args["name"] != "A" {
			t.Fatalf("notice %+v", n)
		}
		for _, v := range n.Args {
			if strings.Contains(v, "Video") || strings.Contains(v, "Games") {
				t.Fatalf("a notice carries a provider's group name: %+v", n)
			}
		}
	}
	// The person's switch survived the refresh.
	row := l.Routing().View().Rows[0]
	if row.Item.Enabled {
		t.Fatalf("the refresh turned the item back on: %+v", row)
	}
}

func TestAProviderThatStopsSendingRulesKeepsItsPlan(t *testing.T) {
	l := open(t, t.TempDir(), nil)
	p := newProvider(t, planProfile("💬 AI"))
	sub, _ := l.AddRemote("A", p.link(), library.RemoteOptions{})
	if _, err := l.Refresh(context.Background(), sub.ID, library.RefreshOptions{}); err != nil {
		t.Fatal(err)
	}
	p.body.Store(linkList("HK 01", "JP 01"))
	res, err := l.Refresh(context.Background(), sub.ID, library.RefreshOptions{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Plan.Stored {
		t.Fatalf("nothing should have been stored: %+v", res.Plan)
	}
	if l.Routing().PlanByID(routing.SubPlanID(sub.ID)) == nil || len(l.Routing().View().Rows) != 1 {
		t.Fatal("the plan was lost")
	}
}

func TestRemovingTheSubscriptionKeepsItsRoutingAsTheUsersOwn(t *testing.T) {
	l := open(t, t.TempDir(), nil)
	sub, _ := l.AddRemote("Aurora", newProvider(t, planProfile("💬 AI", "📺 Video")).link(), library.RemoteOptions{})
	if _, err := l.Refresh(context.Background(), sub.ID, library.RefreshOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := l.Remove(sub.ID); err != nil {
		t.Fatal(err)
	}
	st := l.Routing()
	active := st.PlanByID(st.ActiveID())
	if active == nil || active.Kind != routing.PlanMine || len(active.Items) != 2 {
		t.Fatalf("active plan after removal: %+v", active)
	}
	if st.PlanByID(routing.SubPlanID(sub.ID)) != nil {
		t.Fatal("the subscription's plan is still there")
	}
}

func TestAPastedProfileBringsItsPlanToo(t *testing.T) {
	l := open(t, t.TempDir(), nil)
	body := planProfile("💬 AI", "📺 Video")
	res, err := subscription.Parse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	sub, err := l.AddLocal("Pasted", res)
	if err != nil {
		t.Fatal(err)
	}
	imp, err := planimportOf(body, sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	ps, err := l.SyncPlan(sub.ID, imp)
	if err != nil || !ps.Adopted || ps.Items != 2 {
		t.Fatalf("plan sync %+v, %v", ps, err)
	}
}
