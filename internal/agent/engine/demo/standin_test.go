package demo

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"eggbox/internal/agent/engine"
	"eggbox/internal/agentapi"
)

// The demonstration core makes some nodes never answer (by their name, so it
// repeats). A group that prefers one of them must send traffic to the fastest
// node of the region instead, and back to the node when it is chosen again.
func TestPreferredGroupStandsInForANodeThatDoesNotAnswer(t *testing.T) {
	var down string
	var up []string
	for i := 0; len(up) < 2 || down == ""; i++ {
		name := fmt.Sprintf("Probe %d", i)
		switch {
		case unreachable(name) && down == "":
			down = name
		case !unreachable(name) && len(up) < 2:
			up = append(up, name)
		}
	}
	node := func(tag string) map[string]any {
		return map[string]any{"type": "shadowsocks", "tag": tag, "server": "x.example.net", "server_port": 8388}
	}
	members := []string{down, up[0], up[1]}
	cfg, err := json.Marshal(map[string]any{
		"outbounds": []any{
			map[string]any{"type": "direct", "tag": "direct"},
			node(down), node(up[0]), node(up[1]),
			map[string]any{"type": "urltest", "tag": "region-JP", "outbounds": members},
			map[string]any{"type": "urltest", "tag": "Auto", "outbounds": members},
			map[string]any{"type": "preferred", "tag": "Proxy", "outbounds": append([]string{"Auto", "region-JP"}, members...), "default": down, "stand_ins": []string{"region-JP"}},
		},
		"route": map[string]any{"final": "Proxy"},
	})
	if err != nil {
		t.Fatal(err)
	}
	e := New(Options{Seed: 11, Tick: 12 * time.Millisecond})
	if err := e.Start(context.Background(), engine.Prepared{JSON: cfg}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Stop(context.Background()) })
	if err := e.URLTest(context.Background(), "Proxy"); err != nil {
		t.Fatal(err)
	}

	ch, err := e.WatchGroups(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var snap *agentapi.Groups
	select {
	case snap = <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("no groups")
	}
	groups := map[string]*agentapi.Group{}
	for _, g := range snap.Groups {
		groups[g.Tag] = g
	}
	region := groups["region-JP"]
	best, bestDelay := "", int32(1<<30)
	for _, it := range region.Items {
		if it.Tag != down && it.DelayMs > 0 && it.DelayMs < bestDelay {
			best, bestDelay = it.Tag, it.DelayMs
		}
	}
	for _, it := range region.Items {
		if it.Tag == down && it.DelayMs != 0 {
			t.Fatalf("the unreachable node answered: %+v", it)
		}
	}
	if got := groups["Proxy"].Selected; got != best {
		t.Fatalf("the group stands in %q for %q, the fastest of the region is %q", got, down, best)
	}

	c, err := e.Open("files.small-shop.example")
	if err != nil || c == nil {
		t.Fatalf("%v %v", c, err)
	}
	if c.Outbound != best || len(c.Chain) != 2 || c.Chain[1] != "Proxy" {
		t.Errorf("traffic went through %q (%v), want %q", c.Outbound, c.Chain, best)
	}

	// Choosing a node that answers uses that node.
	if err := e.SelectOutbound("Proxy", up[1]); err != nil {
		t.Fatal(err)
	}
	if c, _ := e.Open("files.small-shop.example"); c == nil || c.Outbound != up[1] {
		t.Errorf("a working node chosen: %+v", c)
	}
	// And a group without stand-ins keeps the node it was told to use, answering or not.
	e.mu.Lock()
	e.byTag["Proxy"].standIns = nil
	e.byTag["Proxy"].selected = down
	got := e.effectiveLocked(e.byTag["Proxy"])
	e.mu.Unlock()
	if got != down {
		t.Errorf("no stand-ins: %q", got)
	}
}
