package demo_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"eggbox/internal/agent/engine"
	"eggbox/internal/agent/engine/demo"
	"eggbox/internal/agentapi"
	"eggbox/internal/configgen"
	"eggbox/internal/subscription"
	"eggbox/internal/testsupport/srs"
)

// plan is a configuration like the application's, with nodes in three
// regions and items whose rule sets are files, as the agent's policy would
// leave them.
func plan(t *testing.T, n int, mutate func(*configgen.Input)) engine.Prepared {
	t.Helper()
	var nodes []configgen.Node
	for i := range n {
		sub := subscription.Node{
			Name: fmt.Sprintf("Demo %02d", i+1), Type: "shadowsocks",
			Server: fmt.Sprintf("node%02d.example.net", i+1), Port: 8388,
			Config: map[string]any{
				"type": "shadowsocks", "server": fmt.Sprintf("node%02d.example.net", i+1), "server_port": 8388,
				"method": "aes-128-gcm", "password": "demo-password",
			},
		}
		nodes = append(nodes, configgen.Node{Node: sub, Region: []string{"HK", "JP", "US"}[i%3]})
	}
	domains := func(id string, exit configgen.Exit, suffixes ...string) configgen.Item {
		data := srs.DomainSuffixes(t, suffixes...)
		return configgen.Item{ID: id, Name: "Item " + id, Exit: exit, Domain: data, DNS: data}
	}
	in := configgen.Input{
		Nodes: nodes, Prefs: configgen.DefaultPrefs(), Main: configgen.Exit{Kind: configgen.ExitAuto},
		Fallback: configgen.Exit{Kind: configgen.ExitMain},
		Items: []configgen.Item{
			domains("ads", configgen.Exit{Kind: configgen.ExitReject}, "ads-tracker.example"),
			domains("ai", configgen.Exit{Kind: configgen.ExitMain}, "openai.com", "anthropic.com"),
			domains("cn", configgen.Exit{Kind: configgen.ExitDirect}, "baidu.com", "bilibili.com", "weibo.com", "alicdn.com"),
			{ID: "tg", Name: "Item tg", Exit: configgen.Exit{Kind: configgen.ExitMain}, IP: srs.CIDRs(t, "203.0.113.96/28")},
		},
	}
	if mutate != nil {
		mutate(&in)
	}
	out, err := configgen.Generate(in)
	if err != nil {
		t.Fatal(err)
	}
	// The agent's policy points every "res:" reference into its resource
	// directory. This one has backslashes in its name: separators on Windows,
	// ordinary characters elsewhere. Either way the path has to be escaped to
	// stand in a JSON string, and a test that never meets such a path passes
	// on the machines where that is forgotten (it failed on Windows alone).
	dir := filepath.Join(t.TempDir(), `Users\runner\Temp`)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, r := range out.Resources {
		if err := os.WriteFile(filepath.Join(dir, r.Name), r.Data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// "res:NAME" becomes "<dir>/NAME", the directory written as JSON writes it
	// (the closing quote of the marshalled text is left off: NAME follows).
	quoted, err := json.Marshal(dir + "/")
	if err != nil {
		t.Fatal(err)
	}
	raw := strings.ReplaceAll(string(out.JSON), `"res:`, string(quoted[:len(quoted)-1]))
	return engine.Prepared{Revision: 1, JSON: []byte(raw), HasTun: true, TunTag: configgen.TagTun}
}

func generated(t *testing.T, n int) engine.Prepared { return plan(t, n, nil) }

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
	if !proxy.Selectable || proxy.Type != "preferred" || auto.Selectable || auto.Type != "urltest" {
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

// A watcher that does not keep up must end up holding the groups as they are
// now: if the newest picture were the one dropped, a choice made while it
// lagged would never reach it, and nothing sends the groups again.
func TestAWatcherOfTheGroupsThatFellBehindEndsUpWithTheGroupsAsTheyAreNow(t *testing.T) {
	// An hour per tick keeps the world still: the only changes are made here.
	e := demo.New(demo.Options{Seed: 7, Tick: time.Hour})
	if err := e.Start(context.Background(), generated(t, 4)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Stop(context.Background()) })

	slow, err := e.WatchGroups(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var members []string
	for _, it := range group(recv(t, slow, "groups"), configgen.TagProxy).Items {
		members = append(members, it.Tag)
	}
	if len(members) < 3 {
		t.Fatalf("the choices are %v", members)
	}

	// Many choices with nobody reading, far more than the watcher can hold,
	// the last one unlike every other.
	const changes = 40
	early, last := members[0], members[len(members)-1]
	for i := range changes {
		pick := early
		if i == changes-1 {
			pick = last
		}
		if err := e.SelectOutbound(configgen.TagProxy, pick); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(slow); n == 0 || n >= changes {
		t.Fatalf("the watcher holds %d pictures: the test does not overflow it", n)
	}

	fresh, err := e.WatchGroups(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := recv(t, fresh, "the groups now")
	var got *agentapi.Groups
	for len(slow) > 0 {
		got = <-slow
	}
	if group(want, configgen.TagProxy).Selected == early {
		t.Fatalf("choosing %s after %s left the same choice: the test cannot tell the pictures apart", last, early)
	}
	if a, b := group(got, configgen.TagProxy).Selected, group(want, configgen.TagProxy).Selected; a != b {
		t.Fatalf("the watcher that fell behind was left with %s selected, the groups have %s", a, b)
	}
	if !proto.Equal(got, want) {
		t.Fatalf("the watcher that fell behind was left with another picture than the groups now:\n%v\nwant\n%v", got, want)
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

// Events are changes, so a watcher that fell behind cannot be left with the
// first ones and miss the rest: it is given the whole list again instead.
func TestAWatcherOfTheConnectionsThatFellBehindIsGivenTheListAsItIsNow(t *testing.T) {
	// An hour per tick keeps the world still: the only connections are opened here.
	e := demo.New(demo.Options{Seed: 7, Tick: time.Hour})
	if err := e.Start(context.Background(), generated(t, 4)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Stop(context.Background()) })

	ch, err := e.WatchConnections(t.Context(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	// Many connections open with nobody reading, far more than the stream holds.
	open := map[string]bool{}
	for range 300 {
		c, err := e.Open("github.com")
		if err != nil || c == nil {
			t.Fatalf("opening a connection: %v %v", c, err)
		}
		open[c.Id] = true
	}

	// What a watcher makes of the messages it reads, as the interface does.
	known := map[string]bool{}
	deadline := time.After(5 * time.Second)
	resets := 0
	for !sameKeys(known, open) {
		select {
		case m, ok := <-ch:
			if !ok {
				t.Fatal("the stream ended")
			}
			if m.Reset_ {
				resets++
				known = map[string]bool{}
			}
			for _, ev := range m.Events {
				switch ev.Type {
				case agentapi.ConnectionEvent_TYPE_NEW:
					known[ev.Id] = true
				case agentapi.ConnectionEvent_TYPE_CLOSED:
					delete(known, ev.Id)
				}
			}
		case <-deadline:
			t.Fatalf("the watcher knows %d of the %d open connections", len(known), len(open))
		}
	}
	if resets < 2 { // the first list is the one every watcher starts with
		t.Fatalf("the watcher was given the list %d time(s): the test does not overflow the stream", resets)
	}
}

func sameKeys(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
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

// open invents connections to destinations of the world and returns them by
// the destination asked for.
func open(t *testing.T, e *demo.Engine, dests ...string) map[string]*agentapi.Connection {
	t.Helper()
	got := map[string]*agentapi.Connection{}
	for _, d := range dests {
		c, err := e.Open(d)
		if err != nil {
			t.Fatal(err)
		}
		got[d] = c
	}
	return got
}

func TestConnectionsFollowTheRulesOfTheConfiguration(t *testing.T) {
	e, _ := start(t, 6)
	got := open(t, e, "api.openai.com", "www.baidu.com", "files.small-shop.example", "192.168.1.20", "203.0.113.101", "dns.alidns.com")
	check := func(key, rule string, leafIs func(string) bool, last string) {
		t.Helper()
		c := got[key]
		if c == nil {
			t.Fatalf("%s: no connection", key)
		}
		if c.Rule != rule {
			t.Errorf("%s: rule %q, want %q", key, c.Rule, rule)
		}
		if len(c.Chain) == 0 || c.Chain[len(c.Chain)-1] != last || c.Chain[0] != c.Outbound || !leafIs(c.Outbound) {
			t.Errorf("%s: outbound %q through %v, want it to end at %s", key, c.Outbound, c.Chain, last)
		}
	}
	node := func(s string) bool { return strings.HasPrefix(s, "Demo ") }
	direct := func(s string) bool { return s == "direct" }
	check("api.openai.com", "rule_set=d-ai => route(item-ai)", node, "item-ai")
	check("www.baidu.com", "rule_set=d-cn => route(item-cn)", direct, "item-cn")
	check("203.0.113.101", "rule_set=p-tg => route(item-tg)", node, "item-tg")
	check("files.small-shop.example", "", node, "item-fallback") // nothing claimed it
	check("192.168.1.20", "ip_is_private=true => route(direct)", direct, "direct")
	check("dns.alidns.com", "protocol=dns => hijack-dns", direct, "direct")
	if c := got["api.openai.com"]; len(c.Chain) < 3 {
		t.Errorf("a connection through groups names every group: %v", c.Chain)
	}
	if _, err := e.Open("nowhere.example"); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("a destination the world does not have: %v", err)
	}
}

func TestRefusedConnectionsAreLoggedNotListed(t *testing.T) {
	e := demo.New(demo.Options{Seed: 5, Tick: time.Hour})
	var mu sync.Mutex
	var lines []string
	e.SetLogSink(func(_, msg string) {
		mu.Lock()
		lines = append(lines, msg)
		mu.Unlock()
	})
	if err := e.Start(context.Background(), generated(t, 3)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Stop(context.Background()) })
	c, err := e.Open("pixel.ads-tracker.example")
	if err != nil || c != nil {
		t.Fatalf("a blocked destination gave %+v, %v", c, err)
	}
	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, l := range lines {
		found = found || strings.Contains(l, "router: reject connection to pixel.ads-tracker.example")
	}
	if !found {
		t.Errorf("the refusal is not in the log: %q", lines)
	}
	ch, _ := e.WatchConnections(t.Context(), time.Millisecond)
	if reset := recv(t, ch, "reset"); len(reset.Events) != 0 {
		t.Errorf("a refused connection is listed: %+v", reset.Events)
	}
}

func TestSelectingAnItemsExitMovesItsNextConnections(t *testing.T) {
	e, _ := start(t, 6)
	if err := e.SelectOutbound("item-ai", "direct"); err != nil {
		t.Fatal(err)
	}
	got := open(t, e, "api.openai.com", "203.0.113.101")
	if c := got["api.openai.com"]; c.Outbound != "direct" || c.Chain[len(c.Chain)-1] != "item-ai" {
		t.Fatalf("after choosing direct: %+v", c)
	}
	if c := got["203.0.113.101"]; c.Outbound == "direct" {
		t.Fatalf("another item moved too: %+v", c)
	}
	if err := e.SelectOutbound("item-ai", "Demo 02"); err != nil {
		t.Fatal(err)
	}
	if c := open(t, e, "api.openai.com")["api.openai.com"]; c.Outbound != "Demo 02" || c.Chain[0] != "Demo 02" {
		t.Errorf("a node was chosen, the traffic went through %q (%v)", c.Outbound, c.Chain)
	}
	// And the global mode overrides every item.
	if err := e.SetMode("Global"); err != nil {
		t.Fatal(err)
	}
	if c := open(t, e, "www.baidu.com")["www.baidu.com"]; c.Rule != "clash_mode=Global => route(Proxy)" || c.Outbound == "direct" {
		t.Errorf("global mode: %+v", c)
	}
}

func TestCheckNeedsTheRuleSets(t *testing.T) {
	p := generated(t, 3)
	// A rule set that is gone, as when the resource directory was cleaned.
	bad := engine.Prepared{JSON: []byte(strings.ReplaceAll(string(p.JSON), "/", "/gone-")), HasTun: true}
	e := demo.New(demo.Options{})
	if err := e.Check(context.Background(), bad); err == nil {
		t.Fatal("a configuration whose rule sets are missing passed the check")
	}
	if err := e.Start(context.Background(), bad); err == nil || e.Running() {
		t.Fatalf("it started: %v", err)
	}
	// A reference the policy did not resolve is a mistake worth saying out loud.
	// The paths are cut as the JSON text they are in, where a Windows path has
	// its backslashes doubled: the path functions of the machine would take them
	// for separators and collapse them, and the text would no longer be found.
	first := firstPath(t, p.JSON)
	dir := first[:strings.LastIndex(first, "/")+1]
	unresolved := engine.Prepared{JSON: []byte(strings.ReplaceAll(string(p.JSON), `"path": "`+dir, `"path": "res:`)), HasTun: true}
	if err := e.Check(context.Background(), unresolved); err == nil || !strings.Contains(err.Error(), "res:") {
		t.Fatalf("an unresolved reference: %v", err)
	}
}

func firstPath(t *testing.T, raw []byte) string {
	t.Helper()
	const key = `"path": "`
	i := strings.Index(string(raw), key)
	if i < 0 {
		t.Fatal("no path in the configuration")
	}
	rest := string(raw)[i+len(key):]
	return rest[:strings.Index(rest, `"`)]
}
