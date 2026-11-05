package app_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"eggbox/internal/agent/testkit"
	"eggbox/internal/agentapi"
	"eggbox/internal/agentclient"
	"eggbox/internal/app"
	"eggbox/internal/configgen"
	"eggbox/internal/library"
	"eggbox/internal/routing"
	"eggbox/internal/ruledata"
	"eggbox/internal/rulefmt"
	"eggbox/internal/ruleurl"
	"eggbox/internal/subscription"
	"eggbox/internal/testsupport/srs"
)

// secretToken is part of the provider's link; no output may contain it.
const secretToken = "tk-7f3a91c2d4b8e605"

type provider struct {
	srv  *httptest.Server
	body atomic.Value
	hits atomic.Int32
}

func newProvider(t *testing.T, body string) *provider {
	t.Helper()
	p := &provider{}
	p.body.Store(body)
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.hits.Add(1)
		w.Header().Set("Subscription-Userinfo", "upload=1; download=2; total=100000; expire=1893456000")
		_, _ = w.Write([]byte(p.body.Load().(string)))
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *provider) link() string { return p.srv.URL + "/sub/" + secretToken }

// links renders trojan share links for invented servers.
func links(names ...string) string {
	var b strings.Builder
	for i, n := range names {
		host := fmt.Sprintf("srv%d-%s.example.net", i, strings.ToLower(strings.Fields(n)[0]))
		b.WriteString("trojan://pw" + fmt.Sprint(i) + "@" + host + ":443?sni=" + host + "#" + strings.ReplaceAll(n, " ", "%20") + "\n")
	}
	return b.String()
}

// flaky dials the in-memory agent and can cut every connection and refuse new
// ones, like a service that went away.
type flaky struct {
	agent *testkit.Agent
	mu    sync.Mutex
	down  bool
	conns []net.Conn
}

func (f *flaky) dial(ctx context.Context) (net.Conn, error) {
	f.mu.Lock()
	down := f.down
	f.mu.Unlock()
	if down {
		return nil, errors.New("connection refused")
	}
	c, err := f.agent.Dial(ctx)
	if err == nil {
		f.mu.Lock()
		f.conns = append(f.conns, c)
		f.mu.Unlock()
	}
	return c, err
}

func (f *flaky) cut() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.down = true
	for _, c := range f.conns {
		_ = c.Close()
	}
	f.conns = nil
}

func (f *flaky) restore() {
	f.mu.Lock()
	f.down = false
	f.mu.Unlock()
}

type harness struct {
	t     *testing.T
	svc   *app.Service
	lib   *library.Library
	agent *testkit.Agent
	net   *flaky
	prov  *provider
	rules *ruleServer
	data  *ruledata.Manager
	clock *clock
}

// clock is the time the service and the rule data share in tests: the real
// time, moved ahead on request, so "a day later" costs nothing.
type clock struct {
	mu     sync.Mutex
	offset time.Duration
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Add(c.offset)
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.offset += d
}

// ruleServer serves rule files: for the geosite and geoip names the plans ask
// for, a small binary rule set whose content depends on the name; other paths
// are served from files set by the test. Paths in down answer 404.
type ruleServer struct {
	t     testing.TB
	base  string
	mu    sync.Mutex
	files map[string][]byte
	down  map[string]bool
	hits  map[string]int
}

var safeLabel = regexp.MustCompile(`[^a-z0-9]+`)

// domainOf is the domain the served geosite set of a name matches.
func domainOf(name string) string {
	return strings.Trim(safeLabel.ReplaceAllString(strings.ToLower(name), "-"), "-") + ".example"
}

// cidrOf is the range the served geoip set of a name matches.
func cidrOf(name string) string {
	sum := 0
	for _, c := range name {
		sum = (sum*31 + int(c)) % 200
	}
	return fmt.Sprintf("10.%d.0.0/16", 20+sum)
}

func (rs *ruleServer) set(path string, data []byte) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.files[path] = data
}

func (rs *ruleServer) fail(path string, down bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.down == nil {
		rs.down = map[string]bool{}
	}
	rs.down[path] = down
}

func (rs *ruleServer) hitCount(path string) int {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.hits[path]
}

func (rs *ruleServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rs.mu.Lock()
	if rs.hits == nil {
		rs.hits = map[string]int{}
	}
	rs.hits[r.URL.Path]++
	down := rs.down[r.URL.Path]
	data, ok := rs.files[r.URL.Path]
	rs.mu.Unlock()
	if down {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	if ok {
		_, _ = w.Write(data)
		return
	}
	name := strings.TrimSuffix(r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:], ".srs")
	switch {
	case strings.Contains(r.URL.Path, "/geo/geosite/"):
		_, _ = w.Write(srs.DomainSuffixes(rs.t, domainOf(name)))
	case strings.Contains(r.URL.Path, "/geo/geoip/"):
		_, _ = w.Write(srs.CIDRs(rs.t, cidrOf(name)))
	default:
		http.NotFound(w, r)
	}
}

func newHarness(t *testing.T, mutate func(*app.Config)) *harness {
	t.Helper()
	return newHarnessWith(t, mutate, nil)
}

// newHarnessWith is newHarness with a say in the rule server before the
// service starts looking for rule data.
func newHarnessWith(t *testing.T, mutate func(*app.Config), prepare func(*ruleServer)) *harness {
	t.Helper()
	clk := &clock{}
	agent := testkit.Start(t, testkit.Options{})
	fl := &flaky{agent: agent}
	client := agentclient.New(fl.dial, agentclient.Options{Version: "test-client", Backoff: func(int) time.Duration { return 20 * time.Millisecond }})
	cctx, ccancel := context.WithCancel(context.Background())
	client.Start(cctx)
	t.Cleanup(func() { ccancel(); client.Close() })

	prov := newProvider(t, links("HK 01", "HK 02", "JP 01"))
	lib, err := library.Open(t.TempDir(), library.Options{Secrets: library.NewMemorySecrets(), Now: clk.Now})
	if err != nil {
		t.Fatal(err)
	}
	// A mirror that serves a rule set for every name the built-in plan (and
	// the plans of the invented providers) ask for: the name is the only
	// thing that varies, so tests can tell the sets apart.
	rs := &ruleServer{t: t, files: map[string][]byte{}}
	mirrorSrv := httptest.NewServer(rs)
	t.Cleanup(mirrorSrv.Close)
	rs.base = mirrorSrv.URL
	mirror := ruledata.Mirror{Name: "test", URL: func(canonical string) string {
		if u, err := url.Parse(canonical); err == nil && u.Host == "raw.githubusercontent.com" {
			return mirrorSrv.URL + u.Path
		}
		return canonical
	}}
	rules, err := ruledata.New(ruledata.Options{Dir: t.TempDir(), Mirrors: []ruledata.Mirror{mirror}, Now: clk.Now})
	if err != nil {
		t.Fatal(err)
	}
	// The harness's settings pick a mirror by name; make sure that name is ours.
	if err := lib.UpdateSettings(func(s *library.Settings) { s.RuleMirror = "auto" }); err != nil {
		t.Fatal(err)
	}

	if prepare != nil {
		prepare(rs)
	}
	cfg := app.Config{
		Library: lib, Rules: rules, Agent: client, Version: "test", HTTPClient: prov.srv.Client(), Clock: clk.Now,
		// Never the public mirrors: tests must not reach the Internet.
		Mirrors: func(string, string) []ruledata.Mirror { return []ruledata.Mirror{mirror} },
	}
	if mutate != nil {
		mutate(&cfg)
	}
	svc := app.New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	svc.Run(ctx)
	t.Cleanup(func() { cancel(); svc.Close() })

	h := &harness{t: t, svc: svc, lib: lib, agent: agent, net: fl, prov: prov, rules: rs, data: rules, clock: clk}
	h.waitFor("the link to become ready", func(s *app.Snapshot) bool { return s.Link.Ready() && s.Status != nil })
	return h
}

func (h *harness) waitFor(what string, cond func(*app.Snapshot) bool) *app.Snapshot {
	h.t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if s := h.svc.Snapshot(); cond(s) {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.t.Fatalf("timed out waiting for %s (phase %s)", what, h.svc.Snapshot().Phase)
	return nil
}

func (h *harness) ctx() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	h.t.Cleanup(cancel)
	return ctx
}

func (h *harness) addSubscription() library.Subscription {
	h.t.Helper()
	// The fetcher's own client is replaced in tests by the provider's.
	sub, _, err := h.svc.AddSubscription(h.ctx(), "", h.prov.link(), library.RemoteOptions{})
	if err != nil {
		h.t.Fatal(err)
	}
	return sub
}

func TestConnectGeneratesAppliesAndConnects(t *testing.T) {
	h := newHarness(t, nil)
	if got := h.svc.Snapshot().Phase; got != app.PhaseDisconnected {
		t.Fatalf("initial phase %s", got)
	}
	sub := h.addSubscription()
	if sub.NodeCount != 3 {
		t.Fatalf("nodes: %d", sub.NodeCount)
	}

	if err := h.svc.Connect(h.ctx(), app.ConnectOptions{}); err != nil {
		t.Fatal(err)
	}
	snap := h.waitFor("connected", func(s *app.Snapshot) bool { return s.Phase == app.PhaseConnected })
	if !snap.Status.GetHasConfig() || snap.Applied.Nodes != 3 || snap.Applied.Revision == 0 {
		t.Errorf("applied %+v, status %v", snap.Applied, snap.Status)
	}
	// The rule data the built-in plan needs was fetched on the way, and every
	// item that is on can use it.
	plan := h.waitFor("the plan to be compiled", func(s *app.Snapshot) bool { return s.Plan.Ready && s.Plan.Missing == 0 }).Plan
	rows := h.lib.Routing().View().Rows
	if len(rows) != 12 || len(plan.Items) != 12 || len(plan.Sources) == 0 {
		t.Fatalf("plan: %d rows, %d statuses, %d sources", len(rows), len(plan.Items), len(plan.Sources))
	}
	for _, row := range rows {
		st := plan.Items[row.Item.ID]
		switch {
		case !row.Item.Enabled && st.State != app.ItemOff:
			t.Errorf("%s is off but is %v", row.Item.Key, st.State)
		case row.Item.Enabled && st.State != app.ItemReady:
			t.Errorf("%s is on and %v (missing %d)", row.Item.Key, st.State, st.Missing)
		}
	}
	// Rule data is fetched for what is on; what is off waits until it is turned on.
	needed := map[string]bool{}
	for _, row := range rows {
		if row.Item.Enabled {
			for _, src := range row.Item.Sources {
				needed[src.URL] = true
			}
		}
	}
	for url, src := range plan.Sources {
		switch {
		case needed[url] && (!src.Present || src.Format != "srs" || src.Err != ""):
			t.Errorf("source %s is needed: %+v", url, src)
		case !needed[url] && src.Present:
			t.Errorf("source %s is not needed and was fetched: %+v", url, src)
		}
	}
	// Throughput arrives.
	h.waitFor("traffic samples", func(s *app.Snapshot) bool {
		return s.Traffic.Available && len(s.Traffic.History) > 0 && s.Traffic.Down > 0
	})

	if err := h.svc.Disconnect(h.ctx()); err != nil {
		t.Fatal(err)
	}
	h.waitFor("disconnected", func(s *app.Snapshot) bool { return s.Phase == app.PhaseDisconnected })
	h.waitFor("the throughput to drop to zero", func(s *app.Snapshot) bool { return !s.Traffic.Available && s.Traffic.Down == 0 })

	// Toggle goes both ways.
	if err := h.svc.Toggle(h.ctx()); err != nil {
		t.Fatal(err)
	}
	h.waitFor("connected again", func(s *app.Snapshot) bool { return s.Phase == app.PhaseConnected })
	if err := h.svc.Toggle(h.ctx()); err != nil {
		t.Fatal(err)
	}
	h.waitFor("disconnected again", func(s *app.Snapshot) bool { return s.Phase == app.PhaseDisconnected })
}

func TestApplySkipsWhatDidNotChange(t *testing.T) {
	h := newHarness(t, nil)
	h.addSubscription()

	first, err := h.svc.Apply(h.ctx())
	if err != nil || first.Skipped || first.Revision == 0 {
		t.Fatalf("%+v, %v", first, err)
	}
	second, err := h.svc.Apply(h.ctx())
	if err != nil || !second.Skipped || second.Revision != first.Revision {
		t.Fatalf("an unchanged library must not store a new revision: %+v, %v", second, err)
	}

	// Blocking something changes the configuration: a rule set, a new revision.
	ads := rowByKey(t, h.lib.Routing(), "ads")
	if err := h.svc.EditRouting(h.ctx(), func(st *routing.State) error { return st.SetItemEnabled(ads.Item.ID, true) }); err != nil {
		t.Fatal(err)
	}
	third, err := h.svc.Apply(h.ctx())
	if err != nil || third.Skipped || third.Revision <= first.Revision {
		t.Fatalf("a changed plan must produce a new revision: %+v, %v", third, err)
	}

	// Choosing where traffic goes is not a configuration change: the plan is
	// the same, only what a group has selected differs.
	ai := rowByKey(t, h.lib.Routing(), "ai")
	if err := h.svc.EditRouting(h.ctx(), func(st *routing.State) error { return st.SetItemDest(ai.Item.ID, routing.Direct()) }); err != nil {
		t.Fatal(err)
	}
	nodes, _ := h.lib.ActiveNodes()
	if err := h.svc.SetMainExit(h.ctx(), routing.ToNode(routing.NodeID{Sub: nodes[1].SubID, Name: nodes[1].Node.Name})); err != nil {
		t.Fatal(err)
	}
	fourth, err := h.svc.Apply(h.ctx())
	if err != nil || !fourth.Skipped {
		t.Fatalf("choosing a destination must not look like a configuration change: %+v, %v", fourth, err)
	}

	// The preferences that shape the configuration do.
	prefs := h.lib.Prefs()
	prefs.IPv6 = !prefs.IPv6
	if _, err := h.svc.SavePrefs(h.ctx(), prefs); err != nil {
		t.Fatal(err)
	}
	fifth, err := h.svc.Apply(h.ctx())
	if err != nil || fifth.Skipped || fifth.Revision <= third.Revision {
		t.Fatalf("changed preferences must produce a new revision: %+v, %v", fifth, err)
	}
}

// rowByKey finds a built-in item.
func rowByKey(t *testing.T, st routing.State, key string) routing.Row {
	t.Helper()
	for _, r := range st.View().Rows {
		if r.Item.Key == key {
			return r
		}
	}
	t.Fatalf("no item %q in the plan", key)
	return routing.Row{}
}

func TestNodesTheServiceRejectsAreDroppedNotFatal(t *testing.T) {
	h := newHarness(t, nil)
	// The in-memory service has no uTLS, so REALITY nodes are refused by its policy.
	pbk := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32))
	text := links("HK 01", "JP 01") + "vless://11111111-2222-3333-4444-555555555555@rl.example.net:443?encryption=none&security=reality&sni=www.example.org&fp=chrome&pbk=" + pbk + "&sid=ab12#Reality%20One\n"
	if _, err := h.svc.ImportNodes(h.ctx(), "mixed", text); err != nil {
		t.Fatal(err)
	}
	rep, err := h.svc.Apply(h.ctx())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Dropped) != 1 || rep.Dropped[0].Name != "Reality One" || rep.Dropped[0].Reason == "" {
		t.Fatalf("dropped %+v", rep.Dropped)
	}
	if rep.Nodes != 3 {
		t.Errorf("nodes = %d (the report counts what was offered)", rep.Nodes)
	}
	if got := h.svc.Snapshot().Applied; got.Nodes != 2 || len(got.Dropped) != 1 {
		t.Errorf("applied %+v: the connection must carry the two good nodes", got)
	}
	if err := h.svc.Connect(h.ctx(), app.ConnectOptions{}); err != nil {
		t.Fatalf("one bad node must not stop the others: %v", err)
	}

	// With nothing but rejected nodes there is nothing to connect through.
	h2 := newHarness(t, nil)
	if _, err := h2.svc.ImportNodes(h2.ctx(), "only", "vless://11111111-2222-3333-4444-555555555555@rl.example.net:443?encryption=none&security=reality&sni=www.example.org&fp=chrome&pbk="+pbk+"&sid=ab12#R\n"); err != nil {
		t.Fatal(err)
	}
	_, err = h2.svc.Apply(h2.ctx())
	if !errors.Is(err, app.ErrNoNodes) {
		t.Fatalf("got %v, want ErrNoNodes", err)
	}
}

func TestConnectWithoutNodesExplainsWhy(t *testing.T) {
	h := newHarness(t, nil)
	err := h.svc.Connect(h.ctx(), app.ConnectOptions{})
	if !errors.Is(err, app.ErrNoNodes) {
		t.Fatalf("got %v", err)
	}
	if ex := app.Explain(err); ex.Code != "no_nodes" {
		t.Errorf("%+v", ex)
	}
	snap := h.svc.Snapshot()
	if len(snap.Notices) == 0 || snap.Notices[len(snap.Notices)-1].Level != app.LevelError {
		t.Errorf("an error notice was expected: %+v", snap.Notices)
	}
	if snap.Busy.Any() {
		t.Errorf("busy flags leaked: %+v", snap.Busy)
	}
}

func TestMainExitIsRememberedAndPassedToTheCore(t *testing.T) {
	h := newHarness(t, nil)
	h.addSubscription()
	nodes, _ := h.lib.ActiveNodes()
	jp := nodes[2]
	id := routing.NodeID{Sub: jp.SubID, Name: jp.Node.Name}

	// Offline: remembered for the next connection, nothing sent to the core.
	if err := h.svc.SetMainExit(h.ctx(), routing.ToNode(id)); err != nil {
		t.Fatal(err)
	}
	if main := h.lib.Routing().Main; main.Kind != routing.DestNode || main.Node == nil || main.Node.Name != jp.Node.Name || main.Node.Sub != jp.SubID {
		t.Fatalf("the main exit is %+v", main)
	}
	if calls := h.agent.Engine.Calls(); len(calls) != 0 {
		t.Fatalf("the core was told about an offline choice: %v", calls)
	}

	// The fake core's groups mirror the generated ones.
	h.agent.Engine.SetGroups(&agentapi.Groups{Groups: []*agentapi.Group{{
		Tag: "Proxy", Type: "selector", Selectable: true, Selected: "Auto",
		Items: []*agentapi.GroupItem{{Tag: "Auto", Type: "urltest"}, {Tag: jp.Node.Name, Type: "trojan", DelayMs: 123}},
	}}})
	if err := h.svc.Connect(h.ctx(), app.ConnectOptions{}); err != nil {
		t.Fatal(err)
	}
	snap := h.waitFor("groups with node details", func(s *app.Snapshot) bool {
		return len(s.Groups) == 1 && len(s.Groups[0].Items) == 2 && s.Groups[0].Items[1].Node != nil
	})
	item := snap.Groups[0].Items[1]
	if item.DelayMS != 123 || item.Node.Key != jp.Key() || item.Node.Protocol != "Trojan" || item.Node.Region != "JP" || item.Node.SubName == "" {
		t.Errorf("%+v / %+v", item, item.Node)
	}
	if got := item.Node.ID; got.Sub != jp.SubID || got.Name != jp.Node.Name || got.Hash != jp.Key() || got.Region != "JP" {
		t.Errorf("identity %+v", got)
	}
	// Connecting put the Proxy group on the remembered node.
	waitCall(t, h.agent, "select Proxy="+jp.Node.Name)

	// Online: the choice goes to the core at once, and the core is not restarted.
	starts := len(h.agent.Engine.Started())
	if err := h.svc.Select(h.ctx(), "Auto"); err != nil {
		t.Fatal(err)
	}
	waitCall(t, h.agent, "select Proxy=Auto")
	if got := h.lib.Routing().Main; got.Kind != routing.DestAuto {
		t.Errorf("Auto is the main exit, got %+v", got)
	}
	if n := len(h.agent.Engine.Started()); n != starts {
		t.Errorf("choosing the main exit restarted the core (%d starts, was %d)", n, starts)
	}
	if err := h.svc.Select(h.ctx(), "region-jp"); err != nil {
		t.Fatal(err)
	}
	if got := h.lib.Routing().Main; got.Kind != routing.DestRegion || got.Region != "JP" {
		t.Errorf("a region group is the main exit, got %+v", got)
	}
	if err := h.svc.Select(h.ctx(), "no-such-group"); err == nil {
		t.Error("an unknown group cannot be the main exit")
	}
	if err := h.svc.TestLatency(h.ctx(), "Auto"); err != nil {
		t.Fatal(err)
	}
	waitCall(t, h.agent, "urltest Auto")
}

func TestChangingWhereAnItemGoesSwitchesWithoutRestartingTheCore(t *testing.T) {
	h := newHarness(t, nil)
	h.addSubscription()
	nodes, _ := h.lib.ActiveNodes()
	ai := rowByKey(t, h.lib.Routing(), "ai")
	tag := "item-" + ai.Item.ID
	h.agent.Engine.SetGroups(&agentapi.Groups{Groups: []*agentapi.Group{{
		Tag: tag, Type: "selector", Selectable: true, Selected: "Proxy",
		Items: []*agentapi.GroupItem{{Tag: "Proxy", Type: "selector"}, {Tag: "direct", Type: "direct"}, {Tag: nodes[0].Node.Name, Type: "trojan"}},
	}}})
	if err := h.svc.Connect(h.ctx(), app.ConnectOptions{}); err != nil {
		t.Fatal(err)
	}
	h.waitFor("connected", func(s *app.Snapshot) bool { return s.Phase == app.PhaseConnected })
	waitCall(t, h.agent, "select "+tag+"=Proxy")
	starts := len(h.agent.Engine.Started())
	rev := h.svc.Snapshot().Applied.Revision

	// Direct, a region, a node: all of them are selections.
	for want, d := range map[string]routing.Dest{
		"direct":           routing.Direct(),
		nodes[0].Node.Name: routing.ToNode(routing.NodeID{Sub: nodes[0].SubID, Name: nodes[0].Node.Name}),
	} {
		if err := h.svc.EditRouting(h.ctx(), func(st *routing.State) error { return st.SetItemDest(ai.Item.ID, d) }); err != nil {
			t.Fatal(err)
		}
		waitCall(t, h.agent, "select "+tag+"="+want)
	}
	if n := len(h.agent.Engine.Started()); n != starts {
		t.Fatalf("switching an item's destination restarted the core (%d starts, was %d)", n, starts)
	}
	if got := h.svc.Snapshot().Applied.Revision; got != rev {
		t.Errorf("switching an item's destination stored a revision (%d, was %d)", got, rev)
	}

	// Blocking is a different rule set, not a selection: the core is restarted.
	if err := h.svc.EditRouting(h.ctx(), func(st *routing.State) error { return st.SetItemDest(ai.Item.ID, routing.Reject()) }); err != nil {
		t.Fatal(err)
	}
	h.waitFor("the core restarted with the new configuration", func(s *app.Snapshot) bool {
		return s.Applied.Revision > rev && s.Phase == app.PhaseConnected
	})
	if n := len(h.agent.Engine.Started()); n <= starts {
		t.Error("blocking an item must restart the core with a new configuration")
	}
}

func waitCall(t *testing.T, a *testkit.Agent, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, c := range a.Engine.Calls() {
			if c == want {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the core never saw %q; calls: %v", want, a.Engine.Calls())
}

func TestRefreshWhileConnectedReappliesChangedNodes(t *testing.T) {
	h := newHarness(t, nil)
	sub := h.addSubscription()
	if err := h.svc.Connect(h.ctx(), app.ConnectOptions{}); err != nil {
		t.Fatal(err)
	}
	h.waitFor("connected", func(s *app.Snapshot) bool { return s.Phase == app.PhaseConnected })
	starts := len(h.agent.Engine.Started())
	rev := h.svc.Snapshot().Applied.Revision

	// Nothing changed: no new revision.
	if _, err := h.svc.RefreshSubscription(h.ctx(), sub.ID); err != nil {
		t.Fatal(err)
	}
	if got := h.svc.Snapshot().Applied.Revision; got != rev {
		t.Errorf("revision moved from %d to %d without a change", rev, got)
	}

	// The provider adds a node: the running connection picks it up.
	h.prov.body.Store(links("HK 01", "HK 02", "JP 01", "SG 01"))
	res, err := h.svc.RefreshSubscription(h.ctx(), sub.ID)
	if err != nil || !res.Changed || res.Added != 1 {
		t.Fatalf("%+v, %v", res, err)
	}
	// The revision moves while the core restarts; the connection is back a
	// moment later, and that is what is asserted.
	h.waitFor("the new revision, connected again", func(s *app.Snapshot) bool {
		return s.Applied.Revision > rev && s.Applied.Nodes == 4 && s.Phase == app.PhaseConnected
	})
	if len(h.agent.Engine.Started()) <= starts {
		t.Error("the core was not restarted with the new configuration")
	}

	// With the setting off the connection is left alone.
	if err := h.lib.UpdateSettings(func(s *library.Settings) { s.ReapplyAfterRefresh = false }); err != nil {
		t.Fatal(err)
	}
	rev = h.svc.Snapshot().Applied.Revision
	h.prov.body.Store(links("HK 01", "HK 02", "JP 01"))
	if _, err := h.svc.RefreshSubscription(h.ctx(), sub.ID); err != nil {
		t.Fatal(err)
	}
	if got := h.svc.Snapshot().Applied.Revision; got != rev {
		t.Errorf("the configuration changed although re-applying is off")
	}
}

func TestAddSubscriptionWithABrokenLinkLeavesNothingBehind(t *testing.T) {
	h := newHarness(t, nil)
	h.prov.body.Store("<html>not a subscription</html>")
	_, _, err := h.svc.AddSubscription(h.ctx(), "bad", h.prov.link(), library.RemoteOptions{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if ex := app.Explain(err); ex.Code != "bad_subscription" {
		t.Errorf("%+v", ex)
	}
	if subs := h.lib.Subscriptions(); len(subs) != 0 {
		t.Errorf("the failed subscription was kept: %+v", subs)
	}
	if strings.Contains(err.Error(), secretToken) {
		t.Errorf("the error contains the link: %v", err)
	}
	if _, _, err := h.svc.AddSubscription(h.ctx(), "bad", "not a url", library.RemoteOptions{}); app.Explain(err).Code != "fetch_bad_url" {
		t.Errorf("%v", err)
	}
}

func TestSchedulerRefreshesDueSubscriptions(t *testing.T) {
	h := newHarness(t, func(c *app.Config) {
		c.ScheduleDelay = 10 * time.Millisecond
		c.ScheduleEvery = 30 * time.Millisecond
	})
	// A subscription that was never refreshed is due at once.
	sub, err := h.lib.AddRemote("late", h.prov.link(), library.RemoteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if got, _ := h.lib.Subscription(sub.ID); got.NodeCount == 3 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the scheduler never refreshed the subscription")
}

func TestDiagnosticsLeakNothing(t *testing.T) {
	h := newHarness(t, nil)
	h.addSubscription()
	if err := h.svc.Connect(h.ctx(), app.ConnectOptions{}); err != nil {
		t.Fatal(err)
	}
	h.agent.Engine.Log("info", "dial tcp srv0-hk.example.net:443 password=pw0 token="+secretToken)
	h.waitFor("the log line", func(s *app.Snapshot) bool {
		for _, l := range s.Logs {
			if strings.Contains(l.Message, "srv0-hk") {
				return true
			}
		}
		return false
	})
	name, data, err := h.svc.ExportDiagnostics(h.ctx())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(name, ".json") {
		t.Errorf("filename %q", name)
	}
	text := string(data)
	for _, secret := range []string{secretToken, "srv0-hk", "srv1-hk", "srv2-jp", "pw0", "pw1", "127.0.0.1:" + strings.Split(h.prov.srv.URL, ":")[2]} {
		if strings.Contains(text, secret) {
			t.Errorf("the report contains %q", secret)
		}
	}
	for _, want := range []string{`"app"`, `"agent"`, `"subscriptions"`, `"phase"`} {
		if !strings.Contains(text, want) {
			t.Errorf("the report lacks %s", want)
		}
	}
}

func TestServiceDiagnosticsExistWithoutAnAgentAndAreBounded(t *testing.T) {
	s := app.New(app.Config{})
	s.RecordServiceLog("error", "SMAppServiceErrorDomain: registration failed (code 42)")
	before := s.Snapshot()
	if len(before.Logs) != 1 || before.Logs[0].Level != "error" || before.Logs[0].Source != "service" ||
		!strings.Contains(before.Logs[0].Message, "code 42") {
		t.Fatalf("missing setup diagnostic: %+v", before.Logs)
	}
	for i := 0; i < 700; i++ {
		s.RecordServiceLog("info", fmt.Sprintf("Attempt %d", i))
	}
	if len(s.Snapshot().Logs) != 600 || !strings.Contains(before.Logs[0].Message, "code 42") {
		t.Fatal("the log must be bounded and previous snapshots must stay immutable")
	}
}

func TestServiceDiagnosticsSurviveAgentLogReplay(t *testing.T) {
	h := newHarness(t, nil)
	h.svc.RecordServiceLog("error", "Registration failed (code 42)")
	generation := h.svc.Snapshot().Link.Generation
	h.net.cut()
	h.waitFor("the lost agent", func(s *app.Snapshot) bool { return !s.Link.Ready() })
	h.net.restore()
	h.waitFor("the new agent connection", func(s *app.Snapshot) bool { return s.Link.Generation > generation && s.Link.Ready() })
	h.agent.Engine.Log("info", "log after reconnect")
	snap := h.waitFor("replayed agent logs", func(s *app.Snapshot) bool {
		for _, line := range s.Logs {
			if line.Message == "log after reconnect" {
				return true
			}
		}
		return false
	})
	count := 0
	for _, line := range snap.Logs {
		if line.Message == "Registration failed (code 42)" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("setup error appears %d times after agent replay, want 1", count)
	}
}

func TestExplainClassifiesErrors(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{nil, ""},
		{app.ErrNoNodes, "no_nodes"},
		{configgen.ErrNoNodes, "no_nodes"},
		{subscription.ErrNoNodes, "no_nodes"},
		{subscription.ErrUnknownFormat, "bad_subscription"},
		{&subscription.FetchError{Kind: subscription.FetchTimeout}, "fetch_timeout"},
		{&subscription.FetchError{Kind: subscription.FetchStatus, Status: 403}, "fetch_http_status"},
		{agentclient.ErrNotReady, "agent_unreachable"},
		{context.DeadlineExceeded, "timeout"},
		{library.ErrNotFound, "not_found"},
		{&agentclient.OpError{Code: agentapi.ErrorCode_ERROR_CODE_ROUTE_CONFLICT}, "route_conflict"},
		{&agentclient.OpError{Code: agentapi.ErrorCode_ERROR_CODE_ROUTE_CONFLICT, Details: map[string]string{"kind": "no_free_subnet"}}, "no_free_subnet"},
		{&agentclient.OpError{Code: agentapi.ErrorCode_ERROR_CODE_CORE_START_FAILED, RolledBack: true}, "rolled_back"},
		{&app.ValidationError{Issues: []app.Issue{{Path: "$.x", Message: "bad"}}}, "invalid_config"},
		{errors.New("something odd"), "unknown"},
	}
	for _, c := range cases {
		if got := app.Explain(c.err).Code; got != c.want {
			t.Errorf("Explain(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}

// What can go wrong with a person's routing choices is told in words of
// their own, whichever way the error arrives: bare, or wrapped by the layers
// it passed.
func TestExplainClassifiesRoutingErrors(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{routing.ErrDuplicateName, "item_duplicate_name"},
		{routing.ErrNoName, "item_no_name"},
		{routing.ErrLocked, "plan_locked"},
		{routing.ErrNotMine, "plan_not_mine"},
		{routing.ErrTooManyExceptions, "too_many_exceptions"},
		{routing.ErrNoItem, "not_found"},
		{routing.ErrNoException, "not_found"},
		{routing.ErrNoPlan, "not_found"},
		{routing.ErrNoSource, "not_found"},
		{routing.ErrDuplicateSource, "source_duplicate"},
		{routing.ErrLastSource, "source_last"},
		{routing.ErrNotInline, "source_not_inline"},
		{routing.ErrNoEntries, "entries_empty"},
		{routing.ErrTooManyEntries, "entries_too_many"},
		{app.ErrBadTarget, "bad_target"},
		{app.ErrLooksLikeSubscription, "looks_like_subscription"},
		{ruleurl.ErrAddress, "rule_bad_address"},
		{ruledata.ErrTooLarge, "rule_too_large"},
		{rulefmt.ErrEmpty, "rule_empty"},
		{rulefmt.ErrUnrecognized, "rule_unreadable"},
		{rulefmt.ErrMalformed, "rule_unreadable"},
		{&ruledata.ParseError{Err: rulefmt.ErrUnrecognized, Body: []byte("x")}, "rule_unreadable"},
		{ruledata.StatusError(404), "rule_http_status"},
		// An entry that cannot be used, whatever the reason (the reason is for logs).
		{routing.Entry{Kind: routing.EntryDomainSuffix, Value: "10.0.0.0/8"}.Valid(), "entry_invalid"},
		{routing.Entry{Kind: routing.EntryPort, Value: "0"}.Valid(), "entry_invalid"},
		{routing.Entry{Kind: routing.EntryIPCIDR, Value: "nope"}.Valid(), "entry_invalid"},
		{routing.Entry{Kind: routing.EntryDomainRegex, Value: "(["}.Valid(), "entry_invalid"},
		{routing.Entry{Kind: "weird", Value: "x"}.Valid(), "entry_invalid"},
		{routing.Entry{Kind: routing.EntryDomain}.Valid(), "entry_invalid"},
	}
	for _, c := range cases {
		if c.err == nil {
			t.Errorf("a case for %q has no error", c.want)
			continue
		}
		if got := app.Explain(c.err).Code; got != c.want {
			t.Errorf("Explain(%v) = %q, want %q", c.err, got, c.want)
		}
		wrapped := fmt.Errorf("saving the plan: %w", fmt.Errorf("editing: %w", c.err))
		if got := app.Explain(wrapped).Code; got != c.want {
			t.Errorf("Explain(wrapped %v) = %q, want %q", c.err, got, c.want)
		}
	}
	// The reason stays in the detail, for the logs; the interface shows words of its own.
	if ex := app.Explain(routing.Entry{Kind: routing.EntryDomainSuffix, Value: "10.0.0.0/8"}.Valid()); ex.Code != "entry_invalid" || ex.Detail == "" {
		t.Errorf("Explain(bad entry) = %+v", ex)
	}
}

func TestLinkLossIsReflectedAndRecovers(t *testing.T) {
	h := newHarness(t, nil)
	h.addSubscription()
	if err := h.svc.Connect(h.ctx(), app.ConnectOptions{}); err != nil {
		t.Fatal(err)
	}
	h.waitFor("connected", func(s *app.Snapshot) bool { return s.Phase == app.PhaseConnected })

	// The service goes away: the interface must say so, and forget the stale
	// status instead of showing "connected".
	h.net.cut()
	snap := h.waitFor("the link to drop", func(s *app.Snapshot) bool { return !s.Link.Ready() })
	if snap.Phase != app.PhaseNoAgent {
		t.Errorf("phase %s", snap.Phase)
	}
	if snap.Traffic.Available || snap.Traffic.Down != 0 {
		t.Errorf("traffic from a service that is gone: %+v", snap.Traffic)
	}
	if err := h.svc.Connect(h.ctx(), app.ConnectOptions{}); err == nil || app.Explain(err).Code != "agent_unreachable" {
		t.Errorf("connecting without a service: %v", err)
	}

	h.net.restore()
	h.waitFor("recovery", func(s *app.Snapshot) bool { return s.Link.Ready() && s.Phase == app.PhaseConnected })
}
