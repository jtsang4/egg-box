package planimport_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"eggbox/internal/planimport"
	"eggbox/internal/routing"
	"eggbox/internal/subscription"
	"eggbox/internal/testsupport/realsub"
)

// TestRealSubscriptionPlans is the acceptance case of the routing design: the
// same real provider's sing-box link and Clash link must give the same plan.
// It only runs when asked to: EGGBOX_TEST_SINGBOX_LINK_FILE and
// EGGBOX_TEST_CLASH_LINK_FILE name private files holding the two links.
// Nothing about the subscription is logged except counts and classes.
func TestRealSubscriptionPlans(t *testing.T) {
	paths := map[string]string{
		"singbox": os.Getenv("EGGBOX_TEST_SINGBOX_LINK_FILE"),
		"clash":   os.Getenv("EGGBOX_TEST_CLASH_LINK_FILE"),
	}
	if paths["singbox"] == "" || paths["clash"] == "" {
		t.Skip("set EGGBOX_TEST_SINGBOX_LINK_FILE and EGGBOX_TEST_CLASH_LINK_FILE to run against a real provider")
	}
	plans := map[string]*planimport.Result{}
	for format, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("cannot read the %s link file", format)
		}
		link := strings.TrimSpace(string(raw))
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		f, err := subscription.Fetch(ctx, link, subscription.FetchOptions{})
		cancel()
		if err != nil {
			t.Fatalf("%s: fetch failed (%s)", format, realsub.Class(err))
		}
		res, err := planimport.Import(f.Body, planimport.Options{SubID: "sLive", SubName: "live"})
		if err != nil {
			t.Fatalf("%s: import failed (%s)", format, realsub.Class(err))
		}
		plans[format] = res
		r := res.Report
		kinds := map[routing.DestKind]int{}
		for _, it := range res.Plan.Items {
			kinds[it.Dest.Kind]++
		}
		t.Logf("%s: format=%s groups=%d rules=%d items=%d sources=%d lan=%v skipped=%v destinations=%v fallback=%s",
			format, r.Format, r.Groups, r.Rules, r.Items, r.Sources, r.LAN, r.Skipped, kinds, res.Plan.Fallback.Kind)
	}
	sb, cl := plans["singbox"].Plan, plans["clash"].Plan
	if len(sb.Items) != 17 || len(cl.Items) != 17 {
		t.Fatalf("items: sing-box %d, clash %d, the design expects 17 in both", len(sb.Items), len(cl.Items))
	}
	mismatch := 0
	for i := range sb.Items {
		if sb.Items[i].Dest.Kind != cl.Items[i].Dest.Kind {
			mismatch++
			t.Errorf("item %d goes %s in the sing-box plan and %s in the Clash plan", i+1, sb.Items[i].Dest.Kind, cl.Items[i].Dest.Kind)
		}
		a, b := len(sb.Items[i].Sources), len(cl.Items[i].Sources)
		if a != b {
			t.Errorf("item %d has %d sources in the sing-box plan and %d in the Clash plan", i+1, a, b)
		}
		if routing.NormName(sb.Items[i].Name) != routing.NormName(cl.Items[i].Name) && sb.Items[i].Key != "ads" {
			mismatch++
		}
	}
	if mismatch > 0 {
		t.Logf("%d items differ in name or destination between the formats", mismatch)
	}
	for format, res := range plans {
		if !res.Report.LAN {
			t.Errorf("%s: the private network rule was not recognised", format)
		}
		if res.Plan.Fallback.Kind != routing.DestFollow {
			t.Errorf("%s: the fallback should follow the main exit", format)
		}
		if res.Plan.Main == nil || res.Plan.Main.Kind != routing.DestAuto {
			t.Errorf("%s: the main exit should start on the automatic group", format)
		}
		direct, reject := 0, 0
		for _, it := range res.Plan.Items {
			switch it.Dest.Kind {
			case routing.DestDirect:
				direct++
			case routing.DestReject:
				reject++
			}
		}
		if direct != 1 || reject != 1 {
			t.Errorf("%s: %d items go straight out and %d are blocked; the design expects 1 and 1", format, direct, reject)
		}
	}
}
