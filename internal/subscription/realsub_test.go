package subscription

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jtsang4/sing-box-ui/internal/testsupport/realsub"
)

func TestRealSubscription(t *testing.T) {
	url := realsub.URL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	f, err := Fetch(ctx, url, FetchOptions{})
	if err != nil {
		t.Fatalf("fetch failed: %s", realsub.Scrub(url, err.Error()))
	}
	res, err := Parse(f.Body)
	if err != nil {
		t.Fatalf("parse failed: %s (skipped %d)", realsub.Scrub(url, err.Error()), len(res.skippedOrNil()))
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
