package subscription

import (
	"context"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// realSubscriptionURL returns the developer's real test subscription from the
// environment, or skips the test. The link and the nodes behind it are
// credentials: they live outside the repository (SBUI_TEST_SUBSCRIPTION_FILE
// points at a private file, or SBUI_TEST_SUBSCRIPTION_URL carries the link),
// and no test may print them. Tests log counts and protocol names only.
func realSubscriptionURL(t *testing.T) string {
	t.Helper()
	if u := strings.TrimSpace(os.Getenv("SBUI_TEST_SUBSCRIPTION_URL")); u != "" {
		return u
	}
	if f := os.Getenv("SBUI_TEST_SUBSCRIPTION_FILE"); f != "" {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("cannot read SBUI_TEST_SUBSCRIPTION_FILE: %v", err)
		}
		if u := strings.TrimSpace(string(raw)); u != "" {
			return u
		}
	}
	t.Skip("set SBUI_TEST_SUBSCRIPTION_FILE (or SBUI_TEST_SUBSCRIPTION_URL) to run against a real subscription")
	return ""
}

func TestRealSubscription(t *testing.T) {
	url := realSubscriptionURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	f, err := Fetch(ctx, url, FetchOptions{})
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	res, err := Parse(f.Body)
	if err != nil {
		t.Fatalf("parse failed: %v (skipped %d)", err, len(res.skippedOrNil()))
	}
	protocols := map[string]int{}
	for _, n := range res.Nodes {
		if err := n.Validate(); err != nil {
			t.Errorf("a node does not validate: %v", err)
		}
		protocols[n.Protocol()+"/"+n.Transport()]++
	}
	var summary []string
	for k, v := range protocols {
		summary = append(summary, k+"="+strconv.Itoa(v))
	}
	sort.Strings(summary)
	skipped := map[string]int{}
	for _, s := range res.Skipped {
		skipped[s.Code]++
	}
	t.Logf("format=%s nodes=%d (%s) skipped=%v notices=%d usage=%v", res.Format, len(res.Nodes), strings.Join(summary, ", "), skipped, len(res.Notices), f.Usage != nil)
	if len(res.Nodes) == 0 {
		t.Fatal("the real subscription yielded no nodes")
	}
}

func (r *Result) skippedOrNil() []Skipped {
	if r == nil {
		return nil
	}
	return r.Skipped
}
