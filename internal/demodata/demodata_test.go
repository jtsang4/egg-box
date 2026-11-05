package demodata_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"eggbox/internal/demodata"
	"eggbox/internal/ruledata"
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

func TestRuleSetsDownloadAndValidate(t *testing.T) {
	m, err := ruledata.New(ruledata.Options{Dir: t.TempDir(), Client: demodata.Client(nil), Mirrors: demodata.Mirrors("auto")})
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{"geosite-cn", "geoip-cn", "geosite-category-ads-all"}
	for _, r := range m.Update(context.Background(), ids, ruledata.UpdateOptions{}) {
		if r.Outcome != ruledata.Updated {
			t.Errorf("%s: %v %v", r.ID, r.Outcome, r.Err)
		}
	}
	sets, missing := m.Load(ids...)
	if len(missing) != 0 || len(sets) != 3 {
		t.Fatalf("sets=%d missing=%v", len(sets), missing)
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
