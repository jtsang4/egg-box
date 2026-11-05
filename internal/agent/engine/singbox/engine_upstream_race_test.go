//go:build !race

package singbox_test

// These tests cover the parts of the core's daemon API that contain data races
// inside sing-box v1.14.4 itself, which the race detector reports no matter
// how the caller uses them:
//
//   - clashmode.Manager.SetMode writes the mode string while the router reads it;
//   - group.URLTest.Selected reads the selected outbound while a URL test replaces it.
//
// The functionality is still verified here; everything else in this package
// (lifecycle, check, connections, selectors, traffic, logs) runs under -race.

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"eggbox/internal/agent/engine"
	"eggbox/internal/agentapi"
)

func TestURLTestGroupIsListedButNotSelectable(t *testing.T) {
	r := newRigWith(t, true)
	r.startClient()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	groups, err := r.client.WatchGroups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	g := recvUntil(t, groups, 5*time.Second, "the groups", func(g *agentapi.Groups) bool { return len(g.Groups) >= 2 })
	var auto *agentapi.Group
	for _, x := range g.Groups {
		if x.Tag == "auto" {
			auto = x
		}
	}
	if auto == nil || auto.Selectable || auto.Type != "urltest" || len(auto.Items) != 2 {
		t.Fatalf("auto group = %+v", auto)
	}
	if err := r.client.SelectOutbound("auto", "direct"); !errors.Is(err, engine.ErrInvalid) {
		t.Fatalf("a url-test group is not selectable: %v", err)
	}
}

func TestURLTestFillsInLatency(t *testing.T) {
	r := newRigWith(t, true)
	r.startClient()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	groups, err := r.client.WatchGroups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.client.URLTest(ctx, "auto"); err != nil {
		t.Fatal(err)
	}
	if err := r.client.URLTest(ctx, "missing"); !errors.Is(err, engine.ErrNotFound) {
		t.Fatalf("unknown tag: %v", err)
	}
	g := recvUntil(t, groups, 10*time.Second, "latency results", func(g *agentapi.Groups) bool {
		for _, x := range g.Groups {
			if x.Tag != "auto" {
				continue
			}
			n := 0
			for _, it := range x.Items {
				if it.DelayMs > 0 || it.TestedUnix > 0 {
					n++
				}
			}
			return n == 2
		}
		return false
	})
	_ = g
}

func TestModesSwitchRouting(t *testing.T) {
	r := newRig(t)
	r.startClient()
	cur, all, err := r.client.Mode()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) < 3 || !hasTag(all, "Direct") || !hasTag(all, "Global") {
		t.Fatalf("mode = %q of %v", cur, all)
	}
	if err := r.client.SetMode("bogus"); !errors.Is(err, engine.ErrInvalid) {
		t.Fatalf("unknown mode: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conns, err := r.client.WatchConnections(ctx, 250*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	dest := fmt.Sprintf("127.0.0.1:%d", r.originPort)
	chainOf := func(path string, known map[string]bool) []string {
		_ = r.get(path)
		var chain []string
		recvUntil(t, conns, 5*time.Second, "a connection for "+path, func(ev *agentapi.ConnectionEvents) bool {
			for _, e := range ev.Events {
				if e.Type == agentapi.ConnectionEvent_TYPE_NEW && e.Connection != nil && e.Connection.Destination == dest && !known[e.Id] {
					known[e.Id] = true
					chain = e.Connection.Chain
					return true
				}
			}
			return false
		})
		return chain
	}
	known := map[string]bool{}
	if err := r.client.SetMode("Direct"); err != nil {
		t.Fatal(err)
	}
	if m, _, _ := r.client.Mode(); m != "Direct" {
		t.Fatalf("mode = %q", m)
	}
	if c := chainOf("/hello?d", known); hasTag(c, "ss-up") || !hasTag(c, "direct") {
		t.Fatalf("Direct mode chain = %v", c)
	}
	if err := r.client.SetMode("Global"); err != nil {
		t.Fatal(err)
	}
	if c := chainOf("/hello?g", known); !hasTag(c, "ss-up") {
		t.Fatalf("Global mode chain = %v", c)
	}
}
