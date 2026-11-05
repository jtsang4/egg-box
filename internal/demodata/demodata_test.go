package demodata_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"eggbox/internal/demodata"
	"eggbox/internal/planimport"
	"eggbox/internal/routing"
	"eggbox/internal/ruledata"
	"eggbox/internal/ruleurl"
	"eggbox/internal/subscription"
)

var now = time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)

func TestProvidersParseWithoutLosses(t *testing.T) {
	c := demodata.Client(func() time.Time { return now })
	want := map[string]int{demodata.AuroraLink: 14, demodata.NimbusLink: 6}
	for link, n := range want {
		got, err := subscription.Fetch(context.Background(), link, subscription.FetchOptions{Client: c})
		if err != nil {
			t.Fatalf("fetch %s: %v", link, err)
		}
		res, err := subscription.Parse(got.Body)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if len(res.Nodes) != n || len(res.Skipped) != 0 {
			t.Fatalf("%s: %d nodes, skipped %v, want %d", link, len(res.Nodes), res.Skipped, n)
		}
		if got.Usage == nil || got.Usage.Total == 0 || got.Usage.Expire.Before(now) {
			t.Fatalf("usage = %+v", got.Usage)
		}
		seen := map[string]bool{}
		for _, node := range res.Nodes {
			if seen[node.Name] {
				t.Errorf("duplicate node %s", node.Name)
			}
			seen[node.Name] = true
			if !strings.HasSuffix(node.Server, ".example.net") {
				t.Errorf("%s: server %q is not a placeholder", node.Name, node.Server)
			}
		}
	}
}

func TestImportedNodesParse(t *testing.T) {
	res, err := subscription.Parse([]byte(demodata.NodesText()))
	if err != nil || len(res.Nodes) != 2 || len(res.Skipped) != 0 {
		t.Fatalf("nodes=%d skipped=%v err=%v", len(res.Nodes), res.Skipped, err)
	}
}

// Every file the built-in plan asks for can be downloaded from the invented
// world and read, and says something.
func TestBuiltinPlanRuleSetsDownloadAndValidate(t *testing.T) {
	m, err := ruledata.New(ruledata.Options{Dir: t.TempDir(), Client: demodata.Client(nil), Mirrors: demodata.Mirrors("auto", "")})
	if err != nil {
		t.Fatal(err)
	}
	var urls []string
	for _, it := range routing.Builtin().Items {
		for _, src := range it.Sources {
			urls = append(urls, src.URL)
		}
	}
	if len(urls) < 20 {
		t.Fatalf("only %d sources", len(urls))
	}
	for _, r := range m.Update(context.Background(), urls, ruledata.UpdateOptions{}) {
		if r.Outcome != ruledata.Updated {
			t.Errorf("%s: %v %v", ruleurl.Display(r.URL), r.Outcome, r.Err)
			continue
		}
		p, err := m.Parsed(r.URL)
		if err != nil || len(p.Rules) == 0 || p.Stats.Domain+p.Stats.IP == 0 {
			t.Errorf("%s: %v %+v", ruleurl.Display(r.URL), err, p)
		}
	}
}

// Aurora Cloud's profile carries a plan, and it is the plan the demonstration
// describes: six items with the destinations the provider chose, and rule
// sets the invented world serves.
func TestAuroraBringsAPlan(t *testing.T) {
	c := demodata.Client(func() time.Time { return now })
	got, err := subscription.Fetch(context.Background(), demodata.AuroraLink, subscription.FetchOptions{Client: c})
	if err != nil {
		t.Fatal(err)
	}
	res, err := planimport.Import(got.Body, planimport.Options{SubID: "aurora", SubName: "Aurora Cloud"})
	if err != nil {
		t.Fatal(err)
	}
	plan := res.Plan
	if len(plan.Items) != 6 {
		var names []string
		for _, it := range plan.Items {
			names = append(names, it.Name)
		}
		t.Fatalf("%d items: %v", len(plan.Items), names)
	}
	dest := map[string]routing.DestKind{}
	for _, it := range plan.Items {
		dest[routing.NormName(it.Name)] = it.Dest.Kind
	}
	for name, want := range map[string]routing.DestKind{
		"ai 服务": routing.DestNode, "telegram": routing.DestRegion, "流媒体": routing.DestFollow,
		"游戏平台": routing.DestFollow, "国内网站": routing.DestDirect, "广告拦截": routing.DestReject,
	} {
		if dest[name] != want {
			t.Errorf("%s goes %q, want %q", name, dest[name], want)
		}
	}
	if plan.Fallback.Kind != routing.DestFollow {
		t.Errorf("the fallback goes %q", plan.Fallback.Kind)
	}

	m, err := ruledata.New(ruledata.Options{Dir: t.TempDir(), Client: c, Mirrors: demodata.Mirrors("auto", "")})
	if err != nil {
		t.Fatal(err)
	}
	var urls []string
	for _, it := range plan.Items {
		for _, src := range it.Sources {
			if src.Kind == routing.SourceURL {
				urls = append(urls, src.URL)
			}
		}
	}
	if len(urls) != 8 {
		t.Fatalf("%d rule set addresses: %v", len(urls), urls)
	}
	for _, r := range m.Update(context.Background(), urls, ruledata.UpdateOptions{}) {
		if r.Outcome != ruledata.Updated {
			t.Errorf("%s: %v %v", ruleurl.Display(r.URL), r.Outcome, r.Err)
		}
	}
}

// A file pasted from any of the well-known projects has something to
// download, in the format its path says; what is not a rule file is not found.
func TestPastedFilesFromWellKnownProjectsAreServed(t *testing.T) {
	m, err := ruledata.New(ruledata.Options{Dir: t.TempDir(), Client: demodata.Client(nil), Mirrors: demodata.Mirrors("auto", "")})
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		"https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/master/rule/Clash/Netflix/Netflix.list",
		"https://github.com/SagerNet/sing-geosite/blob/rule-set/geosite-bilibili.srs",
		"https://raw.githubusercontent.com/someone/rules/main/work.json",
	} {
		f, err := m.Fetch(context.Background(), raw)
		if err != nil {
			t.Errorf("%s: %v", raw, err)
			continue
		}
		if f.Parsed == nil || len(f.Parsed.Rules) == 0 {
			t.Errorf("%s: nothing in it: %+v", raw, f)
		}
	}
	if _, err := m.Fetch(context.Background(), "https://raw.githubusercontent.com/someone/rules/main/readme.md"); err == nil {
		t.Error("a page that is not a rule file was found")
	}
	if _, err := m.Fetch(context.Background(), "https://example.org/rules.list"); err == nil {
		t.Error("a host outside the invented world answered")
	}
}

func TestTheWorldEndsAtItsBorder(t *testing.T) {
	c := demodata.Client(nil)
	for _, u := range []string{"https://github.com/", "http://127.0.0.1:7890/", "https://sub.example.invalid.evil.example/x"} {
		resp, err := c.Get(u)
		if err == nil {
			resp.Body.Close()
			t.Errorf("%s answered", u)
		}
	}
	resp, err := c.Get("https://" + demodata.SubscriptionHost + "/unknown")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d", resp.StatusCode)
	}
}
