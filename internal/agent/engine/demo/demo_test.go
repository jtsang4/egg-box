package demo_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"eggbox/internal/agent/engine"
	"eggbox/internal/agent/engine/demo"
	"eggbox/internal/agentapi"
	"eggbox/internal/configgen"
	"eggbox/internal/subscription"
)

// generated builds the configuration the application would send, from
// invented nodes.
func generated(t *testing.T, n int) engine.Prepared {
	t.Helper()
	var nodes []subscription.Node
	for i := range n {
		nodes = append(nodes, subscription.Node{
			Name: fmt.Sprintf("Demo %02d", i+1), Type: "shadowsocks",
			Server: fmt.Sprintf("node%02d.example.net", i+1), Port: 8388,
			Config: map[string]any{
				"type": "shadowsocks", "server": fmt.Sprintf("node%02d.example.net", i+1), "server_port": 8388,
				"method": "aes-128-gcm", "password": "demo-password",
			},
		})
	}
	prefs := configgen.DefaultPrefs()
	prefs.BlockAds = false
	prefs.Preset = configgen.PresetProxyAll
	out, err := configgen.Generate(configgen.Input{Nodes: nodes, Prefs: prefs})
	if err != nil {
		t.Fatal(err)
	}
	return engine.Prepared{Revision: 1, JSON: out.JSON, HasTun: true, TunTag: configgen.TagTun}
}

func start(t *testing.T, n int) (*demo.Engine, engine.Prepared) {
	t.Helper()
	e := demo.New(demo.Options{Seed: 7, Tick: 10 * time.Millisecond})
	p := generated(t, n)
	if err := e.Start(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Stop(context.Background()) })
	return e, p
}

func group(g *agentapi.Groups, tag string) *agentapi.Group {
	for _, x := range g.Groups {
		if x.Tag == tag {
			return x
		}
	}
	return nil
}

func recv[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v, ok := <-ch:
		if !ok {
			t.Fatalf("%s: the stream ended", what)
		}
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: nothing arrived", what)
	}
	panic("unreachable")
}

func TestReadsTheGeneratedConfiguration(t *testing.T) {
	e, p := start(t, 6)
	if !e.Running() || e.TunInterface() != "utun-demo" {
		t.Fatalf("running=%v tun=%q", e.Running(), e.TunInterface())
	}
	if err := e.Check(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	ch, err := e.WatchGroups(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	g := recv(t, ch, "groups")
	proxy, auto := group(g, configgen.TagProxy), group(g, configgen.TagAuto)
	if proxy == nil || auto == nil {
		t.Fatalf("groups = %v", g.Groups)
	}
	if !proxy.Selectable || proxy.Type != "selector" || auto.Selectable || auto.Type != "urltest" {
		t.Fatalf("proxy=%+v auto=%+v", proxy, auto)
	}
	if proxy.Selected == "" || len(proxy.Items) < 6 {
		t.Fatalf("proxy = %+v", proxy)
	}
}

func TestCheckRejectsNonsense(t *testing.T) {
	e := demo.New(demo.Options{})
	for _, raw := range []string{"", "not json", "{}", `{"outbounds":[]}`} {
		if err := e.Check(context.Background(), engine.Prepared{JSON: []byte(raw)}); err == nil {
			t.Errorf("Check(%q) passed", raw)
		}
		if err := e.Start(context.Background(), engine.Prepared{JSON: []byte(raw)}); err == nil {
			t.Errorf("Start(%q) passed", raw)
			_ = e.Stop(context.Background())
		}
	}
	if e.Running() {
		t.Fatal("running after bad configurations")
	}
}

func TestStartTwiceIsAnError(t *testing.T) {
	e, p := start(t, 3)
	if err := e.Start(context.Background(), p); err == nil {
		t.Fatal("second start passed")
	}
}

func TestNodesGetTestedAndAutoPicksTheFastest(t *testing.T) {
	e, _ := start(t, 8)
	ch, _ := e.WatchGroups(t.Context())
	var tested *agentapi.Group
	deadline := time.After(5 * time.Second)
	for tested == nil {
		select {
		case g := <-ch:
			if a := group(g, configgen.TagAuto); a != nil {
				n := 0
				for _, it := range a.Items {
					if it.DelayMs > 0 {
						n++
					}
				}
				if n >= 6 {
					tested = a
				}
			}
		case <-deadline:
			t.Fatal("the nodes were never tested")
		}
	}
	best := int32(1 << 30)
	for _, it := range tested.Items {
		if it.DelayMs > 0 && it.DelayMs < best {
			best = it.DelayMs
		}
	}
	for _, it := range tested.Items {
		if it.Tag == tested.Selected && it.DelayMs != best {
			t.Fatalf("auto picked %s (%dms), the fastest is %dms", it.Tag, it.DelayMs, best)
		}
	}
}

func TestSelectingANode(t *testing.T) {
	e, _ := start(t, 4)
	ch, _ := e.WatchGroups(t.Context())
	g := recv(t, ch, "groups")
	proxy := group(g, configgen.TagProxy)
	target := proxy.Items[len(proxy.Items)-1].Tag
	if err := e.SelectOutbound(configgen.TagProxy, target); err != nil {
		t.Fatal(err)
	}
	for {
		g = recv(t, ch, "update")
		if group(g, configgen.TagProxy).Selected == target {
			break
		}
	}

	for _, c := range []struct {
		group, out string
		is         error
	}{
		{"nope", target, engine.ErrNotFound},
		{configgen.TagProxy, "nope", engine.ErrNotFound},
		{configgen.TagAuto, target, engine.ErrInvalid},
	} {
		if err := e.SelectOutbound(c.group, c.out); !errors.Is(err, c.is) {
			t.Errorf("SelectOutbound(%q,%q) = %v, want %v", c.group, c.out, err, c.is)
		}
	}
}

func TestURLTestFillsDelays(t *testing.T) {
	e, _ := start(t, 5)
	if err := e.URLTest(context.Background(), configgen.TagProxy); err != nil {
		t.Fatal(err)
	}
	ch, _ := e.WatchGroups(t.Context())
	g := recv(t, ch, "groups")
	for _, it := range group(g, configgen.TagAuto).Items {
		if it.TestedUnix == 0 {
			t.Errorf("%s was not tested", it.Tag)
		}
	}
	if err := e.URLTest(context.Background(), "nope"); !errors.Is(err, engine.ErrNotFound) {
		t.Fatalf("unknown tag: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.URLTest(ctx, configgen.TagProxy); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled test: %v", err)
	}
}

func TestModes(t *testing.T) {
	e, _ := start(t, 3)
	m, all, err := e.Mode()
	if err != nil || m != "Rule" || len(all) != 3 {
		t.Fatalf("mode = %q %v %v", m, all, err)
	}
	if err := e.SetMode("Global"); err != nil {
		t.Fatal(err)
	}
	if m, _, _ := e.Mode(); m != "Global" {
		t.Fatalf("mode = %q", m)
	}
	if err := e.SetMode("Chaos"); !errors.Is(err, engine.ErrInvalid) {
		t.Fatalf("unknown mode: %v", err)
	}
}

func TestTrafficGrows(t *testing.T) {
	e, _ := start(t, 3)
	ch, err := e.WatchTraffic(t.Context(), 15*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	first := recv(t, ch, "traffic")
	if !first.Available {
		t.Fatal("not available")
	}
	var last *agentapi.Traffic
	for range 12 {
		last = recv(t, ch, "traffic")
	}
	if last.DownlinkTotal <= first.DownlinkTotal || last.UplinkTotal <= first.UplinkTotal {
		t.Fatalf("totals did not grow: %d -> %d down, %d -> %d up", first.DownlinkTotal, last.DownlinkTotal, first.UplinkTotal, last.UplinkTotal)
	}
	if last.Downlink <= 0 || last.Connections <= 0 || last.Memory == 0 {
		t.Fatalf("traffic = %+v", last)
	}
}

func TestConnectionsComeAndGo(t *testing.T) {
	e, _ := start(t, 3)
	ch, err := e.WatchConnections(t.Context(), 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if first := recv(t, ch, "reset"); !first.Reset_ {
		t.Fatal("the first message is not a reset")
	}
	var id string
	var updates int
	for id == "" || updates == 0 {
		for _, ev := range recv(t, ch, "events").Events {
			switch ev.Type {
			case agentapi.ConnectionEvent_TYPE_NEW:
				c := ev.Connection
				if c.Id == "" || c.Destination == "" || c.Domain == "" || len(c.Chain) == 0 || c.Process == nil || c.Inbound != "tun-in" {
					t.Fatalf("incomplete connection %+v", c)
				}
				if strings.Contains(c.Source, "example.net") {
					t.Fatalf("source %q", c.Source)
				}
				if id == "" {
					id = c.Id
				}
			case agentapi.ConnectionEvent_TYPE_UPDATE:
				updates++
			}
		}
	}
	if err := e.CloseConnection(id); err != nil {
		t.Fatal(err)
	}
	for closed := false; !closed; {
		for _, ev := range recv(t, ch, "events").Events {
			if ev.Type == agentapi.ConnectionEvent_TYPE_CLOSED && ev.Id == id {
				closed = true
				if ev.Connection.ClosedUnixMs == 0 {
					t.Fatal("closed without a time")
				}
			}
		}
	}
	if err := e.CloseConnection(id); !errors.Is(err, engine.ErrNotFound) {
		t.Fatalf("closing twice: %v", err)
	}
	if err := e.CloseAllConnections(); err != nil {
		t.Fatal(err)
	}
}

func TestModeShapesTheInventedRoutes(t *testing.T) {
	e, _ := start(t, 3)
	if err := e.SetMode("Direct"); err != nil {
		t.Fatal(err)
	}
	ch, _ := e.WatchConnections(t.Context(), 20*time.Millisecond)
	seen := 0
	for seen < 5 {
		for _, ev := range recv(t, ch, "events").Events {
			if ev.Type != agentapi.ConnectionEvent_TYPE_NEW || ev.Connection == nil {
				continue
			}
			seen++
			c := ev.Connection
			if c.Domain != "" && c.Outbound != "direct" && !strings.Contains(c.Rule, "hijack-dns") {
				t.Fatalf("direct mode sent %s through %s", c.Domain, c.Outbound)
			}
		}
	}
}

func TestStopEndsEveryStream(t *testing.T) {
	e, _ := start(t, 3)
	g, _ := e.WatchGroups(context.Background())
	tr, _ := e.WatchTraffic(context.Background(), 10*time.Millisecond)
	cn, _ := e.WatchConnections(context.Background(), 10*time.Millisecond)
	if err := e.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := e.Stop(context.Background()); err != nil {
		t.Fatalf("second stop: %v", err)
	}
	drained := func(name string, done func() bool) {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for !done() {
			select {
			case <-deadline:
				t.Fatalf("%s did not end", name)
			case <-time.After(time.Millisecond):
			}
		}
	}
	drained("groups", func() bool { _, ok := <-g; return !ok })
	drained("traffic", func() bool { _, ok := <-tr; return !ok })
	drained("connections", func() bool { _, ok := <-cn; return !ok })

	if e.Running() || e.TunInterface() != "" {
		t.Fatal("still running")
	}
	if _, err := e.WatchGroups(context.Background()); !errors.Is(err, engine.ErrNotRunning) {
		t.Fatalf("WatchGroups after stop: %v", err)
	}
	if err := e.SelectOutbound("x", "y"); !errors.Is(err, engine.ErrNotRunning) {
		t.Fatalf("SelectOutbound after stop: %v", err)
	}
	select {
	case ev := <-e.Exited():
		t.Fatalf("a stop produced an exit event %v", ev)
	default:
	}
}

func TestLogLinesAreOrderedAndNeverMentionRealHosts(t *testing.T) {
	e := demo.New(demo.Options{Seed: 3, Tick: 5 * time.Millisecond})
	var mu sync.Mutex
	var lines []string
	e.SetLogSink(func(level, msg string) {
		mu.Lock()
		lines = append(lines, level+" "+msg)
		mu.Unlock()
	})
	if err := e.Start(context.Background(), generated(t, 3)); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	for {
		mu.Lock()
		n := len(lines)
		mu.Unlock()
		if n > 25 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("only %d lines", n)
		case <-time.After(5 * time.Millisecond):
		}
	}
	_ = e.Stop(context.Background())
	mu.Lock()
	defer mu.Unlock()
	if !strings.HasPrefix(lines[0], "info router: loaded") {
		t.Fatalf("first line %q", lines[0])
	}
	if !strings.Contains(lines[len(lines)-1], "stopped") {
		t.Fatalf("last line %q", lines[len(lines)-1])
	}
	for _, l := range lines {
		if strings.Contains(l, "node0") && !strings.Contains(l, "Demo ") {
			t.Errorf("line leaks a server name: %q", l)
		}
	}
}
