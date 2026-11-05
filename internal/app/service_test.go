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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jtsang4/sing-box-ui/internal/agent/testkit"
	"github.com/jtsang4/sing-box-ui/internal/agentapi"
	"github.com/jtsang4/sing-box-ui/internal/agentclient"
	"github.com/jtsang4/sing-box-ui/internal/app"
	"github.com/jtsang4/sing-box-ui/internal/configgen"
	"github.com/jtsang4/sing-box-ui/internal/library"
	"github.com/jtsang4/sing-box-ui/internal/ruledata"
	"github.com/jtsang4/sing-box-ui/internal/subscription"
	"github.com/jtsang4/sing-box-ui/internal/testsupport/srs"
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
}

func newHarness(t *testing.T, mutate func(*app.Config)) *harness {
	t.Helper()
	agent := testkit.Start(t, testkit.Options{})
	fl := &flaky{agent: agent}
	client := agentclient.New(fl.dial, agentclient.Options{Version: "test-client", Backoff: func(int) time.Duration { return 20 * time.Millisecond }})
	cctx, ccancel := context.WithCancel(context.Background())
	client.Start(cctx)
	t.Cleanup(func() { ccancel(); client.Close() })

	prov := newProvider(t, links("HK 01", "HK 02", "JP 01"))
	lib, err := library.Open(t.TempDir(), library.Options{Secrets: library.NewMemorySecrets()})
	if err != nil {
		t.Fatal(err)
	}
	// A mirror that serves the rule sets the default preset needs.
	files := map[string][]byte{
		"sing-geosite/geosite-cn.srs": srs.DomainSuffixes(t, "example.cn"),
		"sing-geoip/geoip-cn.srs":     srs.CIDRs(t, "10.0.0.0/8"),
	}
	mirrorSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if data, ok := files[strings.TrimPrefix(r.URL.Path, "/")]; ok {
			_, _ = w.Write(data)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(mirrorSrv.Close)
	mirror := ruledata.Mirror{Name: "test", URL: func(repo, file string) string { return mirrorSrv.URL + "/" + repo + "/" + file }}
	rules, err := ruledata.New(ruledata.Options{Dir: t.TempDir(), Mirrors: []ruledata.Mirror{mirror}})
	if err != nil {
		t.Fatal(err)
	}
	// The harness's settings pick a mirror by name; make sure that name is ours.
	if err := lib.UpdateSettings(func(s *library.Settings) { s.RuleMirror = "auto" }); err != nil {
		t.Fatal(err)
	}

	cfg := app.Config{
		Library: lib, Rules: rules, Agent: client, Version: "test", HTTPClient: prov.srv.Client(),
		// Never the public mirrors: tests must not reach the Internet.
		Mirrors: func(string) []ruledata.Mirror { return []ruledata.Mirror{mirror} },
	}
	if mutate != nil {
		mutate(&cfg)
	}
	svc := app.New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	svc.Run(ctx)
	t.Cleanup(func() { cancel(); svc.Close() })

	h := &harness{t: t, svc: svc, lib: lib, agent: agent, net: fl, prov: prov}
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
	// The rule data the default preset needs was fetched on the way.
	for _, r := range h.svc.RuleDataStatus() {
		if r.Needed && !r.Present {
			t.Errorf("rule set %s is needed and missing", r.ID)
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

	prefs := h.lib.Prefs()
	prefs.Preset = configgen.PresetProxyAll
	if _, err := h.svc.SavePrefs(h.ctx(), prefs); err != nil {
		t.Fatal(err)
	}
	third, err := h.svc.Apply(h.ctx())
	if err != nil || third.Skipped || third.Revision <= first.Revision {
		t.Fatalf("changed preferences must produce a new revision: %+v, %v", third, err)
	}

	// Choosing a node is not a configuration change.
	snapNodes, _ := h.lib.ActiveNodes()
	_ = h.svc.Select(h.ctx(), snapNodes[1].Node.Name)
	fourth, err := h.svc.Apply(h.ctx())
	if err != nil || !fourth.Skipped {
		t.Fatalf("selecting a node must not look like a configuration change: %+v, %v", fourth, err)
	}
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

func TestSelectIsRememberedAndPassedToTheCore(t *testing.T) {
	h := newHarness(t, nil)
	h.addSubscription()
	nodes, _ := h.lib.ActiveNodes()
	jp := nodes[2]

	// Offline: remembered for the next connection, nothing sent to the core.
	if err := h.svc.SelectKey(h.ctx(), jp.Key()); err != nil {
		t.Fatal(err)
	}
	if got := h.lib.Settings().SelectedNode; got != jp.Key() {
		t.Fatalf("selected %q, want the node's key %q", got, jp.Key())
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
	// Connecting put the Proxy group back on the remembered node.
	waitCall(t, h.agent, "select Proxy="+jp.Node.Name)

	// Online: the choice goes to the core at once.
	if err := h.svc.Select(h.ctx(), "Auto"); err != nil {
		t.Fatal(err)
	}
	waitCall(t, h.agent, "select Proxy=Auto")
	if got := h.lib.Settings().SelectedNode; got != "" {
		t.Errorf("Auto is remembered as no selection, got %q", got)
	}
	if err := h.svc.TestLatency(h.ctx(), "Auto"); err != nil {
		t.Fatal(err)
	}
	waitCall(t, h.agent, "urltest Auto")
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
	snap := h.waitFor("the new revision", func(s *app.Snapshot) bool { return s.Applied.Revision > rev && s.Applied.Nodes == 4 })
	if snap.Phase != app.PhaseConnected {
		t.Errorf("phase %s", snap.Phase)
	}
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
