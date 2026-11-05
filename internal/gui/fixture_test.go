package gui

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"eggbox/internal/agentapi"
	"eggbox/internal/app"
	"eggbox/internal/configgen"
	"eggbox/internal/library"
	"eggbox/internal/subscription"
)

// fixtureBackend is an in-memory Backend with invented data: it never talks
// to a service or the network, so screenshots and interaction tests are
// deterministic. None of its names, servers or figures belongs to any real
// provider.
type fixtureBackend struct {
	mu     sync.Mutex
	snap   *app.Snapshot
	subs   []library.Subscription
	nodes  []library.ActiveNode
	set    library.Settings
	prefs  configgen.Prefs
	rules  []configgen.CustomRule
	data   []app.RuleData
	notify func()
	calls  []string
	// failNext makes the next operation fail with this error.
	failNext error
	watching bool
}

// fixtureNow is the fixed "now" of the fixtures.
var fixtureNow = time.Date(2026, 11, 5, 14, 30, 0, 0, time.UTC)

func (f *fixtureBackend) record(s string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf(s, args...))
}

func (f *fixtureBackend) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fixtureBackend) took(prefix string) bool {
	for _, c := range f.called() {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func (f *fixtureBackend) fail() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	err := f.failNext
	f.failNext = nil
	return err
}

var fixtureNodeNames = []string{
	"Tokyo 01", "Tokyo 02", "Osaka 01", "Singapore 01", "Singapore 02", "Hong Kong 01",
	"Hong Kong 02", "Hong Kong 03", "US Los Angeles 01", "US San Jose 01", "Seoul 01",
	"Frankfurt 01", "London 01", "Taipei 01", "Sydney 01", "Amsterdam 01",
}

var fixtureProtocols = []string{"vless", "vless", "trojan", "hysteria2", "vmess", "shadowsocks"}

func fixtureNode(i int, name string) subscription.Node {
	typ := fixtureProtocols[i%len(fixtureProtocols)]
	cfg := map[string]any{
		"type": typ, "tag": name, "server": fmt.Sprintf("node%02d.example.net", i), "server_port": 443 + i,
	}
	if typ == "vless" && i%2 == 0 {
		cfg["tls"] = map[string]any{"enabled": true, "reality": map[string]any{"enabled": true}}
	}
	if i%5 == 3 {
		cfg["transport"] = map[string]any{"type": "ws"}
	}
	return subscription.Node{Name: name, Type: typ, Server: cfg["server"].(string), Port: 443 + i, Config: cfg}
}

// newFixture builds a connected backend with three subscriptions.
func newFixture() *fixtureBackend {
	f := &fixtureBackend{set: library.DefaultSettings(), prefs: configgen.DefaultPrefs()}
	f.set.Theme = "dark"
	f.set.Language = "en"
	f.set.OnboardingDone = true

	mk := func(id, name string, kind library.Kind, nodes []string, usage *subscription.Usage) {
		s := library.Subscription{
			ID: id, Name: name, Kind: kind, Enabled: true, Host: strings.ToLower(strings.ReplaceAll(name, " ", "")) + ".example",
			IntervalHours: 24, CreatedAt: fixtureNow.Add(-90 * 24 * time.Hour), UpdatedAt: fixtureNow.Add(-3 * time.Hour),
			CheckedAt: fixtureNow.Add(-3 * time.Hour), NodeCount: len(nodes), Format: "sing-box", Usage: usage,
		}
		if kind == library.KindLocal {
			s.Host = ""
			s.IntervalHours = 0
		}
		f.subs = append(f.subs, s)
		for _, n := range nodes {
			i := len(f.nodes)
			f.nodes = append(f.nodes, library.ActiveNode{Node: fixtureNode(i, n), SubID: id, SubName: name})
		}
	}
	mk("sub-aurora", "Aurora Cloud", library.KindRemote, fixtureNodeNames[:9],
		&subscription.Usage{Upload: 6_200_000_000, Download: 32_400_000_000, Total: 200_000_000_000, Expire: fixtureNow.Add(23 * 24 * time.Hour)})
	mk("sub-nimbus", "Nimbus Net", library.KindRemote, fixtureNodeNames[9:14],
		&subscription.Usage{Upload: 20_000_000_000, Download: 101_000_000_000, Total: 150_000_000_000, Expire: fixtureNow.Add(4 * 24 * time.Hour)})
	mk("sub-local", "My nodes", library.KindLocal, fixtureNodeNames[14:], nil)

	f.rules = []configgen.CustomRule{
		{Kind: "domain_suffix", Value: "intranet.example", Target: "direct"},
		{Kind: "domain_keyword", Value: "ads-tracker", Target: "reject"},
		{Kind: "process_name", Value: "Slack", Target: "Proxy"},
	}
	f.data = []app.RuleData{
		{ID: configgen.RuleSetCN, Present: true, Needed: true, Updated: fixtureNow.Add(-26 * time.Hour), Checked: fixtureNow.Add(-2 * time.Hour), Size: 412_000, Source: "github"},
		{ID: configgen.RuleSetCNIP, Present: true, Needed: true, Updated: fixtureNow.Add(-26 * time.Hour), Checked: fixtureNow.Add(-2 * time.Hour), Size: 96_000, Source: "github"},
		{ID: configgen.RuleSetAds, Present: false, Needed: false},
		{ID: configgen.RuleSetForeign, Present: false, Needed: false},
	}
	f.snap = f.connectedSnapshot()
	return f
}

// history makes a plausible throughput curve.
func fixtureHistory(n int) []app.Sample {
	out := make([]app.Sample, n)
	for i := range out {
		x := float64(i)
		down := 2.4e6 + 1.9e6*math.Sin(x/7) + 1.2e6*math.Sin(x/2.3+1) + 0.6e6*math.Sin(x*1.7)
		if i > 55 && i < 72 {
			down += 5.5e6 * math.Sin(float64(i-55)/17*math.Pi)
		}
		up := 0.28e6 + 0.18e6*math.Sin(x/5+2) + 0.1e6*math.Sin(x*1.3)
		if down < 40e3 {
			down = 40e3
		}
		if up < 8e3 {
			up = 8e3
		}
		out[i] = app.Sample{Down: down, Up: up}
	}
	return out
}

func (f *fixtureBackend) connectedSnapshot() *app.Snapshot {
	hist := fixtureHistory(app.HistoryLen)
	var peak float64
	for _, h := range hist {
		peak = math.Max(peak, math.Max(h.Down, h.Up))
	}
	last := hist[len(hist)-1]
	groups := f.groups("Tokyo 01", 58)
	status := &agentapi.Status{
		Service: agentapi.ServiceState_SERVICE_STATE_READY, Connection: agentapi.ConnectionState_CONNECTION_STATE_CONNECTED,
		Mode: "Rule", ConnectedSinceUnix: fixtureNow.Add(-(1*time.Hour + 42*time.Minute + 8*time.Second)).Unix(),
		ActiveRevision: 7, StoredRevision: 7, IntentConnected: true, HasConfig: true, TunInterface: "EggBox",
		CoreVersion: "1.14.4", AgentVersion: "0.1.0", AuthorizedUser: "demo",
	}
	notices := []app.Notice{
		{ID: 1, Time: fixtureNow.Add(-3 * time.Hour), Level: app.LevelSuccess, Code: "subscription_updated", Args: map[string]string{"name": "Aurora Cloud", "nodes": "9"}},
		{ID: 2, Time: fixtureNow.Add(-102 * time.Minute), Level: app.LevelSuccess, Code: "connected"},
		{ID: 3, Time: fixtureNow.Add(-61 * time.Minute), Level: app.LevelInfo, Code: "subscription_current", Args: map[string]string{"name": "Nimbus Net"}},
		{ID: 4, Time: fixtureNow.Add(-18 * time.Minute), Level: app.LevelWarning, Code: "nodes_dropped", Args: map[string]string{"count": "2"}},
	}
	return &app.Snapshot{
		Link:   app.LinkInfo{State: 2, Agent: app.AgentInfo{AgentVersion: "0.1.0", CoreVersion: "1.14.4", Platform: "darwin", Arch: "arm64", Driver: "darwin", Peer: "demo"}},
		Status: status, Phase: app.PhaseConnected,
		AgentSettings: &agentapi.Settings{AutoConnect: true, ResumeAfterCrash: true, ReconnectOnNetworkChange: true, LogLevel: "info"},
		Groups:        groups,
		Traffic: app.Traffic{Available: true, Up: last.Up, Down: last.Down, UpTotal: 1_240_000_000, DownTotal: 18_600_000_000,
			Connections: 47, Memory: 96 << 20, History: hist, Peak: peak},
		Modes:   []string{"rule", "global", "direct"},
		Notices: notices,
		Applied: app.AppliedInfo{At: fixtureNow.Add(-102 * time.Minute), Revision: 7, Nodes: len(f.nodes)},
		Logs:    fixtureLogs(),
		Conns:   fixtureConns(),
	}
}

// fixtureLogs is a believable stretch of the core's log.
func fixtureLogs() []app.LogLine {
	type l struct {
		ago             time.Duration
		level, src, msg string
	}
	raw := []l{
		{412 * time.Second, "info", "core", "sing-box 1.14.4 started"},
		{411 * time.Second, "info", "tun", "EggBox: mtu 1500, stack mixed, auto_route"},
		{410 * time.Second, "info", "dns", "initialized, 2 servers, strategy prefer_ipv4"},
		{402 * time.Second, "info", "router", "match rule[domain_suffix] => direct"},
		{371 * time.Second, "info", "outbound/vless[Tokyo 01]", "outbound connection to api.openai.com:443"},
		{358 * time.Second, "info", "router", "match rule[geosite-cn] => direct"},
		{340 * time.Second, "debug", "dns", "exchange www.baidu.com. A via local, 11ms"},
		{311 * time.Second, "info", "outbound/vless[Tokyo 01]", "outbound connection to github.com:443"},
		{288 * time.Second, "info", "router", "match rule[process_name=Slack] => Proxy"},
		{262 * time.Second, "warn", "dns", "upstream slow: 812ms (https://1.1.1.1/dns-query)"},
		{240 * time.Second, "info", "outbound/vless[Tokyo 01]", "outbound connection to cdn.jsdelivr.net:443"},
		{221 * time.Second, "info", "router", "match rule[domain_keyword=ads-tracker] => reject"},
		{198 * time.Second, "error", "outbound/hysteria2[Seoul 01]", "dial udp: i/o timeout"},
		{181 * time.Second, "info", "urltest[Auto]", "selected Tokyo 01 (58 ms)"},
		{150 * time.Second, "info", "router", "match rule[geoip-cn] => direct"},
		{129 * time.Second, "info", "outbound/vless[Tokyo 01]", "outbound connection to registry.npmjs.org:443"},
		{97 * time.Second, "warn", "router", "connection from 192.168.1.20:51312 lingered 30s without data"},
		{82 * time.Second, "info", "outbound/trojan[Frankfurt 01]", "outbound connection to open.spotify.com:443"},
		{64 * time.Second, "info", "router", "match rule[ip_is_private] => direct"},
		{40 * time.Second, "info", "router", "match rule[domain_suffix] => direct"},
		{31 * time.Second, "info", "outbound/vless[Tokyo 01]", "outbound connection to cdn.example.org:443"},
		{20 * time.Second, "warn", "dns", "upstream slow: 640ms"},
		{9 * time.Second, "info", "router", "match rule[geosite-cn] => direct"},
	}
	out := make([]app.LogLine, len(raw))
	for i, r := range raw {
		out[i] = app.LogLine{Time: fixtureNow.Add(-r.ago), Level: r.level, Source: r.src, Message: r.msg}
	}
	return out
}

// fixtureConns is a believable table of connections (public hostnames, invented nodes).
func fixtureConns() []app.Conn {
	mk := func(id, host, dest, proto, rule, outbound string, chain []string, proc string, age time.Duration, up, down int64, upr, downr float64) app.Conn {
		return app.Conn{ID: id, Network: "tcp", Source: "198.18.0.1:" + id, Destination: dest, Host: host, Protocol: proto, Rule: rule, Outbound: outbound,
			Chain: chain, Process: proc, Created: fixtureNow.Add(-age), Up: up, Down: down, UpRate: upr, DownRate: downr}
	}
	proxy := func(node string) []string { return []string{node, "Auto", "Proxy"} }
	conns := []app.Conn{
		mk("50211", "github.com", "140.82.112.4:443", "tls", "domain_suffix=github.com", "Tokyo 01", proxy("Tokyo 01"), "Code Helper", 12*time.Minute+8*time.Second, 1_800_000, 24_000_000, 12_000, 640_000),
		mk("50214", "api.openai.com", "104.18.7.192:443", "tls", "domain_suffix=openai.com", "Tokyo 01", proxy("Tokyo 01"), "Cursor", 4*time.Minute+20*time.Second, 420_000, 3_100_000, 4_000, 96_000),
		mk("50217", "www.baidu.com", "110.242.68.4:443", "tls", "geosite-cn", "direct", []string{"direct"}, "Google Chrome Helper", 9*time.Minute, 220_000, 1_900_000, 0, 0),
		mk("50230", "cdn.jsdelivr.net", "151.101.1.229:443", "tls", "final", "Tokyo 01", proxy("Tokyo 01"), "Google Chrome Helper", 2*time.Minute+1*time.Second, 36_000, 5_400_000, 800, 1_800_000),
		mk("50233", "slack.com", "44.231.8.76:443", "tls", "process_name=Slack", "Singapore 01", proxy("Singapore 01"), "Slack", 31*time.Minute, 2_600_000, 6_800_000, 1_100, 3_200),
		mk("50241", "", "17.253.144.10:443", "", "final", "Tokyo 01", proxy("Tokyo 01"), "trustd", 3*time.Minute+40*time.Second, 6_000, 18_000, 0, 0),
		mk("50244", "intranet.example", "10.8.0.14:8443", "tls", "domain_suffix=intranet.example", "direct", []string{"direct"}, "Safari", 18*time.Minute, 90_000, 410_000, 0, 0),
		mk("50252", "registry.npmjs.org", "104.16.1.34:443", "tls", "final", "Tokyo 01", proxy("Tokyo 01"), "node", 52*time.Second, 14_000, 2_300_000, 0, 0),
		mk("50263", "open.spotify.com", "35.186.224.25:443", "tls", "final", "Frankfurt 01", proxy("Frankfurt 01"), "Spotify", 27*time.Minute, 310_000, 82_000_000, 2_000, 410_000),
		mk("50271", "", "192.168.1.20:445", "", "ip_is_private", "direct", []string{"direct"}, "kernel_task", 44*time.Minute, 4_200_000, 18_400_000, 0, 0),
		mk("50280", "video.example-cdn.net", "93.184.216.34:443", "tls", "final", "Tokyo 01", proxy("Tokyo 01"), "Google Chrome Helper", 6*time.Minute+30*time.Second, 120_000, 410_000_000, 6_000, 5_400_000),
		mk("50288", "mail.google.com", "142.250.80.37:443", "tls", "domain_suffix=google.com", "Tokyo 01", proxy("Tokyo 01"), "Mail", 14*time.Minute, 540_000, 2_200_000, 0, 0),
	}
	closed := mk("50199", "ads-tracker.example", "203.0.113.9:443", "tls", "domain_keyword=ads-tracker", "reject", []string{"reject"}, "Google Chrome Helper", 7*time.Minute, 0, 0, 0, 0)
	closed.Closed = fixtureNow.Add(-6*time.Minute - 59*time.Second)
	steam := mk("50203", "store.steampowered.com", "23.55.9.181:443", "tls", "final", "Singapore 01", proxy("Singapore 01"), "steamwebhelper", 11*time.Minute, 88_000, 1_250_000, 0, 0)
	steam.Closed = fixtureNow.Add(-2 * time.Minute)
	return append(conns, closed, steam)
}

// groups builds the group tree of a running configuration.
func (f *fixtureBackend) groups(exit string, exitDelay int) []app.Group {
	var items []app.GroupItem
	byRegion := map[string][]string{}
	for i, n := range f.nodes {
		d := 40 + (i*37)%320
		if n.Node.Name == exit {
			d = exitDelay
		}
		if i == 6 {
			d = -1
		}
		if i == 11 {
			d = 0
		}
		ref := &app.NodeRef{Key: n.Key(), Name: n.Node.Name, Protocol: n.Node.Protocol(), Transport: n.Node.Transport(),
			Region: subscription.Region(n.Node.Name), Server: n.Node.Server, Port: n.Node.Port, SubID: n.SubID, SubName: n.SubName}
		items = append(items, app.GroupItem{Tag: n.Node.Name, Type: n.Node.Type, DelayMS: d, Tested: fixtureNow.Add(-4 * time.Minute), Node: ref})
		if ref.Region != "" {
			byRegion[ref.Region] = append(byRegion[ref.Region], n.Node.Name)
		}
	}
	proxyItems := []app.GroupItem{{Tag: "Auto", Type: "urltest"}}
	groups := []app.Group{}
	for _, code := range []string{"HK", "JP", "SG", "US"} {
		if len(byRegion[code]) >= 2 {
			proxyItems = append(proxyItems, app.GroupItem{Tag: "region-" + code, Type: "urltest"})
			var mem []app.GroupItem
			for _, it := range items {
				for _, t := range byRegion[code] {
					if it.Tag == t {
						mem = append(mem, it)
					}
				}
			}
			groups = append(groups, app.Group{Tag: "region-" + code, Type: "urltest", Items: mem, Selected: mem[0].Tag})
		}
	}
	proxyItems = append(proxyItems, items...)
	groups = append([]app.Group{
		{Tag: "Proxy", Type: "selector", Selectable: true, Selected: exit, Items: proxyItems},
		{Tag: "Auto", Type: "urltest", Items: items, Selected: exit},
	}, groups...)
	return groups
}

// disconnected returns the snapshot of an idle application.
func (f *fixtureBackend) disconnectedSnapshot() *app.Snapshot {
	s := f.connectedSnapshot()
	s.Phase = app.PhaseDisconnected
	s.Status = &agentapi.Status{Service: agentapi.ServiceState_SERVICE_STATE_READY, Connection: agentapi.ConnectionState_CONNECTION_STATE_DISCONNECTED, HasConfig: true, Mode: "Rule"}
	s.Groups = nil
	s.Traffic = app.Traffic{}
	s.Applied = app.AppliedInfo{At: fixtureNow.Add(-26 * time.Hour)}
	return s
}

// -------------------------------------------------------------- Backend

func (f *fixtureBackend) Snapshot() *app.Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snap
}

func (f *fixtureBackend) set_(s *app.Snapshot) {
	f.mu.Lock()
	f.snap = s
	n := f.notify
	f.mu.Unlock()
	if n != nil {
		n()
	}
}

func (f *fixtureBackend) SetNotify(fn func()) { f.mu.Lock(); f.notify = fn; f.mu.Unlock() }
func (f *fixtureBackend) Subscriptions() []library.Subscription {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]library.Subscription(nil), f.subs...)
}
func (f *fixtureBackend) ActiveNodes() ([]library.ActiveNode, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]library.ActiveNode(nil), f.nodes...), nil
}
func (f *fixtureBackend) Settings() library.Settings {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.set
}
func (f *fixtureBackend) UpdateSettings(fn func(*library.Settings)) error {
	f.record("settings")
	f.mu.Lock()
	fn(&f.set)
	f.mu.Unlock()
	return nil
}
func (f *fixtureBackend) Prefs() configgen.Prefs {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.prefs
}
func (f *fixtureBackend) Rules() []configgen.CustomRule {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]configgen.CustomRule(nil), f.rules...)
}
func (f *fixtureBackend) RuleDataStatus() []app.RuleData { return f.data }

func (f *fixtureBackend) Connect(ctx context.Context, o app.ConnectOptions) error {
	f.record("connect ignore=%v", o.IgnoreRouteConflicts)
	if err := f.fail(); err != nil {
		return err
	}
	f.set_(f.connectedSnapshot())
	return nil
}
func (f *fixtureBackend) Disconnect(ctx context.Context) error {
	f.record("disconnect")
	f.set_(f.disconnectedSnapshot())
	return nil
}
func (f *fixtureBackend) SelectKey(ctx context.Context, key string) error {
	f.record("select %s", key)
	f.mu.Lock()
	f.set.SelectedNode = key
	f.mu.Unlock()
	return f.fail()
}
func (f *fixtureBackend) Select(ctx context.Context, tag string) error {
	f.record("select-tag %s", tag)
	return nil
}
func (f *fixtureBackend) TestLatency(ctx context.Context, tag string) error {
	f.record("test %s", tag)
	return f.fail()
}
func (f *fixtureBackend) SetMode(ctx context.Context, mode string) error {
	f.record("mode %s", mode)
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *f.snap
	st := proto.Clone(f.snap.Status).(*agentapi.Status)
	st.Mode = mode
	cp.Status = st
	f.snap = &cp
	return nil
}
func (f *fixtureBackend) CloseConnection(ctx context.Context, id string) error {
	f.record("close %s", id)
	return nil
}
func (f *fixtureBackend) CloseAllConnections(ctx context.Context) error {
	f.record("close-all")
	return nil
}
func (f *fixtureBackend) WatchConnections(on bool) {
	f.record("watch %v", on)
	f.mu.Lock()
	f.watching = on
	f.mu.Unlock()
}
func (f *fixtureBackend) RetryCleanup(ctx context.Context) error {
	f.record("retry-cleanup")
	return nil
}
func (f *fixtureBackend) SetAgentSettings(ctx context.Context, fn func(*agentapi.UpdateSettingsRequest)) error {
	f.record("agent-settings")
	return nil
}
func (f *fixtureBackend) AddSubscription(ctx context.Context, name, link string, o library.RemoteOptions) (library.Subscription, *library.RefreshResult, error) {
	f.record("add-sub %s", name)
	if err := f.fail(); err != nil {
		return library.Subscription{}, nil, err
	}
	s := library.Subscription{ID: "sub-new", Name: name, Kind: library.KindRemote, Enabled: true, NodeCount: 4, UpdatedAt: fixtureNow}
	f.mu.Lock()
	f.subs = append(f.subs, s)
	f.mu.Unlock()
	return s, &library.RefreshResult{NodeCount: 4, Added: 4, Changed: true}, nil
}
func (f *fixtureBackend) ImportNodes(ctx context.Context, name, text string) (library.Subscription, error) {
	f.record("import %s", name)
	if err := f.fail(); err != nil {
		return library.Subscription{}, err
	}
	s := library.Subscription{ID: "sub-import", Name: name, Kind: library.KindLocal, Enabled: true, NodeCount: 2, UpdatedAt: fixtureNow}
	f.mu.Lock()
	f.subs = append(f.subs, s)
	f.mu.Unlock()
	return s, nil
}
func (f *fixtureBackend) RefreshSubscription(ctx context.Context, id string) (*library.RefreshResult, error) {
	f.record("refresh %s", id)
	return &library.RefreshResult{NotModified: true}, f.fail()
}
func (f *fixtureBackend) RefreshAll(ctx context.Context) (int, error) {
	f.record("refresh-all")
	return 2, f.fail()
}
func (f *fixtureBackend) EditSubscription(ctx context.Context, id string, fn func(*library.Subscription)) error {
	f.record("edit %s", id)
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.subs {
		if f.subs[i].ID == id {
			fn(&f.subs[i])
		}
	}
	return nil
}
func (f *fixtureBackend) RemoveSubscription(ctx context.Context, id string) error {
	f.record("remove %s", id)
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.subs {
		if f.subs[i].ID == id {
			f.subs = append(f.subs[:i], f.subs[i+1:]...)
			break
		}
	}
	return nil
}
func (f *fixtureBackend) MoveSubscription(id string, index int) error {
	f.record("move %s %d", id, index)
	return nil
}
func (f *fixtureBackend) Link(id string) (string, error) {
	f.record("link %s", id)
	return "https://sub.example.invalid/" + id + "?token=demo", nil
}
func (f *fixtureBackend) SetLink(id, link string) error {
	f.record("set-link %s", id)
	return f.fail()
}
func (f *fixtureBackend) SavePrefs(ctx context.Context, p configgen.Prefs) ([]string, error) {
	f.record("save-prefs")
	f.mu.Lock()
	f.prefs = p
	f.mu.Unlock()
	return nil, f.fail()
}
func (f *fixtureBackend) SaveRules(ctx context.Context, r []configgen.CustomRule) error {
	f.record("save-rules %d", len(r))
	f.mu.Lock()
	f.rules = r
	f.mu.Unlock()
	return f.fail()
}
func (f *fixtureBackend) UpdateRuleData(ctx context.Context, force bool) error {
	f.record("update-rule-data %v", force)
	return f.fail()
}
func (f *fixtureBackend) ExportDiagnostics(ctx context.Context) (string, []byte, error) {
	f.record("diagnostics")
	return "egg-box-diagnostics.json", []byte(`{"ok":true}`), f.fail()
}
