package gui

import (
	"context"
	"fmt"
	"math"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"eggbox/internal/agentapi"
	"eggbox/internal/app"
	"eggbox/internal/configgen"
	"eggbox/internal/library"
	"eggbox/internal/plancompile"
	"eggbox/internal/routing"
	"eggbox/internal/rulefmt"
	"eggbox/internal/ruleurl"
	"eggbox/internal/subscription"
)

// fixtureBackend is an in-memory Backend with invented data: it never talks
// to a service or the network, so screenshots and interaction tests are
// deterministic. None of its names, servers or figures belongs to any real
// provider. The routing state is the real one (internal/routing); what the
// service would work out from it (where each item's traffic leaves, what the
// rule files hold) is worked out here in a few lines.
type fixtureBackend struct {
	mu     sync.Mutex
	snap   *app.Snapshot
	subs   []library.Subscription
	nodes  []library.ActiveNode
	set    library.Settings
	prefs  configgen.Prefs
	rt     routing.State
	ver    uint64
	notify func()
	calls  []string
	// failNext makes the next operation fail with this error.
	failNext error
	watching bool
	// down names the nodes that do not answer; missing the rule files that are
	// not downloaded; failing the ones whose last update failed.
	down    map[string]bool
	missing map[string]bool
	failing map[string]bool
	// partial names the rule files some of whose rules the engine cannot use.
	partial map[string]bool
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

// fixtureDelay is what the fixture's speed test found for the i-th node: the
// first Tokyo node is the fastest, the seventh does not answer, the twelfth
// was never measured.
func fixtureDelay(i int, name string) int {
	switch {
	case name == "Tokyo 01":
		return 58
	case i == 6:
		return -1
	case i == 11:
		return 0
	}
	return 62 + (i*37)%300
}

// newFixture builds a connected backend with three subscriptions, the shipped
// routing plan, a plan that came with a subscription and three exceptions.
func newFixture() *fixtureBackend {
	f := &fixtureBackend{set: library.DefaultSettings(), prefs: configgen.DefaultPrefs(), ver: 1,
		down: map[string]bool{}, missing: map[string]bool{}, failing: map[string]bool{}, partial: map[string]bool{}}
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
			f.nodes = append(f.nodes, library.ActiveNode{Node: fixtureNode(i, n), SubID: id, SubName: name, Auto: true})
		}
	}
	mk("sub-aurora", "Aurora Cloud", library.KindRemote, fixtureNodeNames[:9],
		&subscription.Usage{Upload: 6_200_000_000, Download: 32_400_000_000, Total: 200_000_000_000, Expire: fixtureNow.Add(23 * 24 * time.Hour)})
	mk("sub-nimbus", "Nimbus Net", library.KindRemote, fixtureNodeNames[9:14],
		&subscription.Usage{Upload: 20_000_000_000, Download: 101_000_000_000, Total: 150_000_000_000, Expire: fixtureNow.Add(4 * 24 * time.Hour)})
	mk("sub-local", "My nodes", library.KindLocal, fixtureNodeNames[14:], nil)

	st := routing.NewState()
	for _, x := range []routing.Exception{
		{Kind: routing.EntryDomainSuffix, Value: "intranet.example", Dest: routing.Direct(), Enabled: true},
		{Kind: routing.EntryDomainKeyword, Value: "ads-tracker", Dest: routing.Reject(), Enabled: true},
		{Kind: routing.EntryProcessName, Value: "Slack", Dest: routing.Follow(), Enabled: true},
	} {
		if _, err := st.AddException(x); err != nil {
			panic(err)
		}
	}
	// Aurora Cloud brings a plan of its own, which is not in force.
	src := func(name string) routing.Source {
		return routing.URLSource(routing.GeoSite(name), "geosite: "+name, routing.MetaCubeX)
	}
	st.Plans = append(st.Plans, &routing.Plan{
		ID: routing.SubPlanID("sub-aurora"), Kind: routing.PlanSubscription, Name: "Aurora Cloud", SubID: "sub-aurora",
		Fallback: routing.Follow(), UpdatedAt: fixtureNow.Add(-3 * time.Hour),
		Items: []routing.Item{
			{ID: "aur-1", Name: "Streaming", Icon: "film", Sources: []routing.Source{src("netflix")}, Dest: routing.Follow(), Enabled: true},
			{ID: "aur-2", Name: "Games", Icon: "gamepad", Sources: []routing.Source{src("steam")}, Dest: routing.Direct(), Enabled: true},
			{ID: "aur-3", Name: "Local sites", Icon: "home", Sources: []routing.Source{src("cn")}, Dest: routing.Direct(), Enabled: true},
		},
	})
	f.rt = st
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
	s := &app.Snapshot{
		Link:   app.LinkInfo{State: 2, Agent: app.AgentInfo{AgentVersion: "0.1.0", CoreVersion: "1.14.4", Platform: "darwin", Arch: "arm64", Driver: "darwin", Peer: "demo"}},
		Status: status, Phase: app.PhaseConnected,
		AgentSettings: &agentapi.Settings{AutoConnect: true, ResumeAfterCrash: true, ReconnectOnNetworkChange: true, LogLevel: "info"},
		Traffic: app.Traffic{Available: true, Up: last.Up, Down: last.Down, UpTotal: 1_240_000_000, DownTotal: 18_600_000_000,
			Connections: 47, Memory: 96 << 20, History: hist, Peak: peak},
		Modes:   []string{"rule", "global", "direct"},
		Notices: notices,
		Applied: app.AppliedInfo{At: fixtureNow.Add(-102 * time.Minute), Revision: 7, Nodes: len(f.nodes)},
		Logs:    fixtureLogs(),
		Conns:   fixtureConns(),
	}
	f.derive(s)
	return s
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
	item := func(id, name string) app.Why { return app.Why{Kind: app.WhyItem, ID: id, Name: name} }
	exception := func(n int, what string) app.Why { return app.Why{Kind: app.WhyException, Name: what, Index: n} }
	fallback := app.Why{Kind: app.WhyFallback}
	mk := func(id, host, dest, proto, rule string, why app.Why, outbound string, chain []string, proc string, age time.Duration, up, down int64, upr, downr float64) app.Conn {
		return app.Conn{ID: id, Network: "tcp", Source: "198.18.0.1:" + id, Destination: dest, Host: host, Protocol: proto, Rule: rule, Why: why, Outbound: outbound,
			Chain: chain, Process: proc, Created: fixtureNow.Add(-age), Up: up, Down: down, UpRate: upr, DownRate: downr}
	}
	proxy := func(node string) []string { return []string{node, "Auto", "Proxy"} }
	conns := []app.Conn{
		mk("50211", "github.com", "140.82.112.4:443", "tls", "rule_set=geosite-github", item("b-github", "GitHub"), "Tokyo 01", proxy("Tokyo 01"), "Code Helper", 12*time.Minute+8*time.Second, 1_800_000, 24_000_000, 12_000, 640_000),
		mk("50214", "api.openai.com", "104.18.7.192:443", "tls", "rule_set=geosite-category-ai-!cn", item("b-ai", "AI services"), "Tokyo 01", proxy("Tokyo 01"), "Cursor", 4*time.Minute+20*time.Second, 420_000, 3_100_000, 4_000, 96_000),
		mk("50217", "www.baidu.com", "110.242.68.4:443", "tls", "rule_set=geosite-cn", item("b-cn", "China sites"), "direct", []string{"direct"}, "Google Chrome Helper", 9*time.Minute, 220_000, 1_900_000, 0, 0),
		mk("50230", "cdn.jsdelivr.net", "151.101.1.229:443", "tls", "final", fallback, "Tokyo 01", proxy("Tokyo 01"), "Google Chrome Helper", 2*time.Minute+1*time.Second, 36_000, 5_400_000, 800, 1_800_000),
		mk("50233", "slack.com", "44.231.8.76:443", "tls", "process_name=Slack", exception(3, "Slack"), "Singapore 01", proxy("Singapore 01"), "Slack", 31*time.Minute, 2_600_000, 6_800_000, 1_100, 3_200),
		mk("50241", "", "17.253.144.10:443", "", "final", fallback, "Tokyo 01", proxy("Tokyo 01"), "trustd", 3*time.Minute+40*time.Second, 6_000, 18_000, 0, 0),
		mk("50244", "intranet.example", "10.8.0.14:8443", "tls", "domain_suffix=intranet.example", exception(1, "intranet.example"), "direct", []string{"direct"}, "Safari", 18*time.Minute, 90_000, 410_000, 0, 0),
		mk("50252", "registry.npmjs.org", "104.16.1.34:443", "tls", "final", fallback, "Tokyo 01", proxy("Tokyo 01"), "node", 52*time.Second, 14_000, 2_300_000, 0, 0),
		mk("50263", "open.spotify.com", "35.186.224.25:443", "tls", "rule_set=geosite-spotify", item("b-streaming", "Streaming"), "Frankfurt 01", proxy("Frankfurt 01"), "Spotify", 27*time.Minute, 310_000, 82_000_000, 2_000, 410_000),
		mk("50271", "", "192.168.1.20:445", "", "ip_is_private", app.Why{Kind: app.WhyLAN}, "direct", []string{"direct"}, "kernel_task", 44*time.Minute, 4_200_000, 18_400_000, 0, 0),
		mk("50280", "video.example-cdn.net", "93.184.216.34:443", "tls", "final", fallback, "Tokyo 01", proxy("Tokyo 01"), "Google Chrome Helper", 6*time.Minute+30*time.Second, 120_000, 410_000_000, 6_000, 5_400_000),
		mk("50288", "mail.google.com", "142.250.80.37:443", "tls", "rule_set=geosite-google", item("b-google", "Google"), "Tokyo 01", proxy("Tokyo 01"), "Mail", 14*time.Minute, 540_000, 2_200_000, 0, 0),
	}
	closed := mk("50199", "ads-tracker.example", "203.0.113.9:443", "tls", "domain_keyword=ads-tracker", exception(2, "ads-tracker"), "reject", []string{"reject"}, "Google Chrome Helper", 7*time.Minute, 0, 0, 0, 0)
	closed.Closed = fixtureNow.Add(-6*time.Minute - 59*time.Second)
	steam := mk("50203", "store.steampowered.com", "23.55.9.181:443", "tls", "final", fallback, "Singapore 01", proxy("Singapore 01"), "steamwebhelper", 11*time.Minute, 88_000, 1_250_000, 0, 0)
	steam.Closed = fixtureNow.Add(-2 * time.Minute)
	return append(conns, closed, steam)
}

// ------------------------------------------------------ what the service works out

// nodeRef is how the core's reports name a node.
func (f *fixtureBackend) nodeRef(st routing.State, n library.ActiveNode) app.NodeRef {
	names := 0
	for _, o := range f.nodes {
		if o.Node.Name == n.Node.Name {
			names++
		}
	}
	return app.NodeRef{Key: n.Key(), ID: n.ID(), Name: n.Node.Name, Protocol: n.Node.Protocol(), Transport: n.Node.Transport(),
		Region: app.RegionOf(st, n), Server: n.Node.Server, Port: n.Node.Port, SubID: n.SubID, SubName: n.SubName,
		SubAlias: n.SubAlias, Ambiguous: names > 1}
}

func (f *fixtureBackend) delayOf(n library.ActiveNode) int {
	for i, o := range f.nodes {
		if o.Key() == n.Key() {
			if f.down[o.Node.Name] {
				return -1
			}
			return fixtureDelay(i, o.Node.Name)
		}
	}
	return 0
}

// fastest is the node an automatic choice ends on: the fastest that answers,
// among those the automatic choice may use, in the region when one is given.
func (f *fixtureBackend) fastest(st routing.State, region string) (*app.NodeRef, int) {
	var best *library.ActiveNode
	bestDelay := 0
	for i := range f.nodes {
		n := f.nodes[i]
		d := f.delayOf(n)
		if d <= 0 || !n.Auto || st.NodePref(n.ID()).NoAuto {
			continue
		}
		if region != "" && !strings.EqualFold(app.RegionOf(st, n), region) {
			continue
		}
		if best == nil || d < bestDelay {
			best, bestDelay = &f.nodes[i], d
		}
	}
	if best == nil {
		return nil, 0
	}
	ref := f.nodeRef(st, *best)
	return &ref, bestDelay
}

// find resolves a choice of a node the way the configuration does.
func (f *fixtureBackend) find(st routing.State, id routing.NodeID) (library.ActiveNode, bool) {
	live := make([]routing.LiveNode, len(f.nodes))
	for i, n := range f.nodes {
		live[i] = routing.LiveNode{Sub: n.SubID, Name: n.Node.Name, Hash: n.Node.Key(), Region: app.RegionOf(st, n)}
	}
	i, m := routing.Resolve(id, live)
	if m == routing.MatchNone || i < 0 || i >= len(f.nodes) {
		return library.ActiveNode{}, false
	}
	return f.nodes[i], true
}

// exitFor says where traffic with a destination leaves.
func (f *fixtureBackend) exitFor(st routing.State, d routing.Dest, group string) app.ExitInfo {
	info := app.ExitInfo{Group: group}
	switch d.Kind {
	case routing.DestFollow:
		inner := f.exitFor(st, st.Main, configgen.TagProxy)
		inner.Group, inner.Member, inner.Wanted, inner.StandIn = group, configgen.TagProxy, nil, false
		return inner
	case routing.DestDirect:
		info.Member = "direct"
	case routing.DestReject:
		return app.ExitInfo{}
	case routing.DestAuto:
		info.Member = configgen.TagAuto
		info.Node, info.DelayMS = f.fastest(st, "")
	case routing.DestRegion:
		info.Member = configgen.RegionPrefix + d.Region
		info.Node, info.DelayMS = f.fastest(st, d.Region)
	case routing.DestNode:
		if d.Node == nil {
			return info
		}
		id := *d.Node
		if n, ok := f.find(st, id); ok && !f.down[n.Node.Name] {
			ref := f.nodeRef(st, n)
			info.Node, info.Member, info.DelayMS = &ref, n.Node.Name, f.delayOf(n)
			return info
		}
		// The chosen node does not answer or is gone: a node of its region stands in.
		info.Member = "pin"
		info.Node, info.DelayMS = f.fastest(st, id.Region)
		if info.Node == nil {
			info.Node, info.DelayMS = f.fastest(st, "")
		}
		info.Wanted, info.StandIn = &id, true
	}
	return info
}

// exits works out where the main exit, the fallback and every item leave.
func (f *fixtureBackend) exits(st routing.State) map[string]app.ExitInfo {
	out := map[string]app.ExitInfo{"main": f.exitFor(st, st.Main, configgen.TagProxy)}
	view := st.View()
	out["fallback"] = f.exitFor(st, view.Fallback, "fallback")
	for _, r := range view.Rows {
		if r.Item.Enabled && r.Item.Dest.Kind != routing.DestReject {
			out[r.Item.ID] = f.exitFor(st, r.Item.Dest, configgen.SelectorPrefix+r.Item.ID)
		}
	}
	return out
}

// planInfo says how the plan in force came out when it was compiled.
func (f *fixtureBackend) planInfo(st routing.State) app.PlanInfo {
	info := app.PlanInfo{Seq: f.ver, Ready: true, Items: map[string]app.ItemStatus{}, Sources: map[string]app.SourceStatus{}}
	needed := map[string]bool{}
	view := st.View()
	for _, r := range view.Rows {
		is := app.ItemStatus{State: app.ItemReady, Sources: len(r.Item.Sources)}
		if !r.Item.Enabled {
			is.State = app.ItemOff
		}
		missing := 0
		for _, src := range r.Item.Sources {
			if src.Kind != routing.SourceURL {
				continue
			}
			ss, ok := info.Sources[src.URL]
			if !ok {
				ss = app.SourceStatus{URL: src.URL, Present: !f.missing[src.URL], Format: string(rulefmt.FormatSRS)}
				if ss.Present {
					ss.Size, ss.Updated, ss.Checked = 40_000, fixtureNow.Add(-26*time.Hour), fixtureNow.Add(-2*time.Hour)
					ss.Domain, ss.IP = 1200+len(src.URL)%700, 30+len(src.URL)%40
				}
				if f.failing[src.URL] {
					ss.Err = "the server answered with an error (HTTP 503)"
					ss.Updated, ss.Checked = fixtureNow.Add(-4*24*time.Hour), fixtureNow.Add(-4*24*time.Hour)
				}
				if f.partial[src.URL] && ss.Present {
					ss.Unsupported = map[string]int{"process_name": 2}
				}
				info.Sources[src.URL] = ss
			}
			if !ss.Present {
				missing++
				// Like the service, only what an item that is on needs counts.
				if r.Item.Enabled {
					needed[src.URL] = true
				}
			}
			if ss.Err != "" {
				is.Failing = true
			}
			if ss.Present && (is.Oldest.IsZero() || ss.Checked.Before(is.Oldest)) {
				is.Oldest = ss.Checked
			}
			is.Domain += ss.Domain
			is.IP += ss.IP
			is.Unsupported += unsupportedCount(ss)
		}
		if r.Item.Enabled && missing > 0 {
			is.Missing = missing
			is.State = app.ItemPartial
			if missing == len(r.Item.Sources) {
				is.State = app.ItemUnavailable
			}
		}
		info.Items[r.Item.ID] = is
	}
	info.Missing = len(needed)
	// Chosen nodes that no subscription offers.
	pin := func(owner plancompile.OwnerKind, id, name string, d routing.Dest) {
		if d.Kind == routing.DestNode && d.Node != nil {
			if _, ok := f.find(st, *d.Node); !ok {
				info.Gone = append(info.Gone, app.Pin{Owner: owner, ID: id, Name: name, Node: *d.Node})
			}
		}
	}
	pin(plancompile.OwnerMain, "", "", st.Main)
	for _, r := range view.Rows {
		if r.Item.Enabled {
			pin(plancompile.OwnerItem, r.Item.ID, r.Item.Name, r.Item.Dest)
		}
	}
	return info
}

// groups builds the group tree of a running configuration.
func (f *fixtureBackend) groups(st routing.State, exits map[string]app.ExitInfo) []app.Group {
	var items []app.GroupItem
	byRegion := map[string][]string{}
	for _, n := range f.nodes {
		ref := f.nodeRef(st, n)
		items = append(items, app.GroupItem{Tag: n.Node.Name, Type: n.Node.Type, DelayMS: f.delayOf(n), Tested: fixtureNow.Add(-4 * time.Minute), Node: &ref})
		if ref.Region != "" {
			byRegion[ref.Region] = append(byRegion[ref.Region], n.Node.Name)
		}
	}
	best, _ := f.fastest(st, "")
	autoSel := ""
	if best != nil {
		autoSel = best.Name
	}
	proxyItems := []app.GroupItem{{Tag: configgen.TagAuto, Type: "urltest"}}
	var regions []app.Group
	codes := make([]string, 0, len(byRegion))
	for code := range byRegion {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	for _, code := range codes {
		if len(byRegion[code]) < 2 {
			continue
		}
		proxyItems = append(proxyItems, app.GroupItem{Tag: configgen.RegionPrefix + code, Type: "urltest"})
		var mem []app.GroupItem
		for _, it := range items {
			for _, t := range byRegion[code] {
				if it.Tag == t {
					mem = append(mem, it)
				}
			}
		}
		sel := mem[0].Tag
		if ref, _ := f.fastest(st, code); ref != nil {
			sel = ref.Name
		}
		regions = append(regions, app.Group{Tag: configgen.RegionPrefix + code, Type: "urltest", Items: mem, Selected: sel})
	}
	proxyItems = append(proxyItems, items...)
	main := exits["main"]
	return append([]app.Group{
		{Tag: configgen.TagProxy, Type: "selector", Selectable: true, Selected: main.Member, Items: proxyItems},
		{Tag: configgen.TagAuto, Type: "urltest", Items: items, Selected: autoSel},
	}, regions...)
}

// derive fills in what follows from the routing state: the plan's status, where
// traffic leaves and the groups. It changes s.
func (f *fixtureBackend) derive(s *app.Snapshot) {
	st := f.rt
	s.Plan = f.planInfo(st)
	if s.Phase.Online() {
		s.Exits = f.exits(st)
		s.Groups = f.groups(st, s.Exits)
	} else {
		s.Exits, s.Groups = nil, nil
	}
}

// disconnected returns the snapshot of an idle application.
func (f *fixtureBackend) disconnectedSnapshot() *app.Snapshot {
	s := f.connectedSnapshot()
	s.Phase = app.PhaseDisconnected
	s.Status = &agentapi.Status{Service: agentapi.ServiceState_SERVICE_STATE_READY, Connection: agentapi.ConnectionState_CONNECTION_STATE_DISCONNECTED, HasConfig: true, Mode: "Rule"}
	s.Traffic = app.Traffic{}
	s.Applied = app.AppliedInfo{At: fixtureNow.Add(-26 * time.Hour)}
	f.derive(s)
	return s
}

// commit stores a new routing state and brings the snapshot in line with it.
func (f *fixtureBackend) commit(st routing.State) {
	f.mu.Lock()
	f.rt = st
	f.ver++
	cp := *f.snap
	f.derive(&cp)
	f.snap = &cp
	n := f.notify
	f.mu.Unlock()
	if n != nil {
		n()
	}
}

// node finds a node of the fixture by its name.
func (f *fixtureBackend) node(name string) (library.ActiveNode, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, n := range f.nodes {
		if n.Node.Name == name {
			return n, true
		}
	}
	return library.ActiveNode{}, false
}

// arrange changes the routing state before (or between) the steps of a test,
// the way a person's earlier work would have, and brings the snapshot in line.
func (f *fixtureBackend) arrange(fn func(*routing.State)) {
	st := f.Routing()
	fn(&st)
	f.commit(st)
}

// refresh recomputes the snapshot after the fixture's own state (nodes that are
// down, rule files that are missing) was changed by a test.
func (f *fixtureBackend) refresh() {
	f.mu.Lock()
	f.ver++
	cp := *f.snap
	f.derive(&cp)
	f.snap = &cp
	n := f.notify
	f.mu.Unlock()
	if n != nil {
		n()
	}
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

func (f *fixtureBackend) Routing() routing.State {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rt.Clone()
}

func (f *fixtureBackend) LibraryVersion() uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ver
}

func (f *fixtureBackend) EditRouting(ctx context.Context, fn func(*routing.State) error) error {
	f.record("edit-routing")
	if err := f.fail(); err != nil {
		return err
	}
	st := f.Routing()
	if err := fn(&st); err != nil {
		return err
	}
	f.commit(st)
	return nil
}

// describe words a destination for the log of calls.
func describe(d routing.Dest) string {
	switch d.Kind {
	case routing.DestRegion:
		return "region:" + d.Region
	case routing.DestNode:
		if d.Node != nil {
			return "node:" + d.Node.Name
		}
	}
	return string(d.Kind)
}

func (f *fixtureBackend) SetMainExit(ctx context.Context, d routing.Dest) error {
	f.record("main %s", describe(d))
	if err := f.fail(); err != nil {
		return err
	}
	st := f.Routing()
	if err := st.SetMain(d); err != nil {
		return err
	}
	f.commit(st)
	return nil
}

// fixtureHosts are the sites of the shipped items, for TestURL.
var fixtureHosts = map[string][]string{
	"ai":        {"openai.com", "anthropic.com"},
	"telegram":  {"telegram.org", "t.me"},
	"youtube":   {"youtube.com"},
	"streaming": {"netflix.com", "spotify.com"},
	"google":    {"google.com"},
	"github":    {"github.com"},
	"microsoft": {"microsoft.com"},
	"apple":     {"apple.com"},
	"cn":        {"baidu.com", "qq.com"},
}

// hostOf reads the host out of what was typed.
func hostOf(raw string) string {
	s := strings.TrimSpace(raw)
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, ":"); i >= 0 && !strings.Contains(s[i:], "]") && strings.Count(s, ":") == 1 {
		s = s[:i]
	}
	if s == "" || strings.ContainsAny(s, " \t") {
		return ""
	}
	return strings.ToLower(s)
}

func hostMatches(e routing.Entry, host string) bool {
	switch e.Kind {
	case routing.EntryDomain:
		return host == e.Value
	case routing.EntryDomainSuffix:
		return host == e.Value || strings.HasSuffix(host, "."+e.Value)
	case routing.EntryDomainKeyword:
		return strings.Contains(host, e.Value)
	}
	return false
}

func (f *fixtureBackend) TestURL(ctx context.Context, raw string) (*app.TestURLResult, error) {
	f.record("test-url %s", raw)
	if err := f.fail(); err != nil {
		return nil, err
	}
	host := hostOf(raw)
	if host == "" {
		return nil, app.ErrBadTarget
	}
	st := f.Routing()
	res := &app.TestURLResult{Host: host, Port: 443}
	if ip, err := netip.ParseAddr(host); err == nil {
		res.Addrs = []netip.Addr{ip}
	}
	f.mu.Lock()
	online := f.snap.Phase.Online()
	f.mu.Unlock()
	finish := func(d routing.Dest, group string) {
		res.Dest = d
		if online && d.Kind != routing.DestDirect && d.Kind != routing.DestReject {
			ex := f.exitFor(st, d, group)
			if ex.Node != nil {
				res.Exit = &ex
			}
		}
	}
	for i, x := range st.Exceptions {
		if x.Enabled && hostMatches(x.Entry(), host) {
			res.Why = app.Why{Kind: app.WhyException, ID: x.ID, Name: x.Value, Index: i + 1}
			e := x.Entry()
			res.Source = &app.TestSource{Entry: &e}
			finish(x.Dest, "exception")
			return res, nil
		}
	}
	view := st.View()
	for _, r := range view.Rows {
		if !r.Item.Enabled {
			continue
		}
		for _, suffix := range fixtureHosts[r.Item.Key] {
			if hostMatches(routing.Entry{Kind: routing.EntryDomainSuffix, Value: suffix}, host) {
				res.Why = app.Why{Kind: app.WhyItem, ID: r.Item.ID, Name: r.Item.Name}
				if len(r.Item.Sources) > 0 {
					res.Source = &app.TestSource{Name: r.Item.Sources[0].Name, Project: r.Item.Sources[0].Project, URL: r.Item.Sources[0].URL}
				}
				finish(r.Item.Dest, configgen.SelectorPrefix+r.Item.ID)
				return res, nil
			}
		}
	}
	// A name nobody can look up: the plan has rules about addresses, so the
	// core turns it away (the fixture's one such name is under nowhere.example).
	if strings.HasSuffix(host, ".nowhere.example") {
		res.Resolved, res.ResolveErr = true, "no answer"
		res.Why = app.Why{Kind: app.WhyUnresolved}
		finish(routing.Reject(), "")
		return res, nil
	}
	res.Why = app.Why{Kind: app.WhyFallback}
	finish(view.Fallback, "fallback")
	return res, nil
}

func (f *fixtureBackend) PreviewRuleSet(ctx context.Context, raw string) (*app.RuleSetPreview, error) {
	f.record("preview-rule-set %s", raw)
	if err := f.fail(); err != nil {
		return nil, err
	}
	switch {
	case strings.Contains(raw, "subscription"):
		return nil, app.ErrLooksLikeSubscription
	case !strings.HasPrefix(raw, "https://"):
		return nil, ruleurl.ErrAddress
	}
	return &app.RuleSetPreview{
		URL: raw, Display: raw, Name: "my-list", Format: rulefmt.FormatSingBox, Project: "",
		Stats: rulefmt.Stats{Domain: 128, IP: 4, Other: 1, Unsupported: map[string]int{"process_name": 2}},
	}, nil
}

func (f *fixtureBackend) KeepRuleSet(ctx context.Context, canonical string) error {
	f.record("keep-rule-set %s", canonical)
	return f.fail()
}

func (f *fixtureBackend) UpdateRuleData(ctx context.Context, force bool) error {
	f.record("update-rule-data %v", force)
	if err := f.fail(); err != nil {
		return err
	}
	f.mu.Lock()
	f.missing = map[string]bool{}
	f.failing = map[string]bool{}
	f.mu.Unlock()
	f.refresh()
	return nil
}

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
	f.ver++
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
	f.ver++
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
	f.ver++
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
	f.ver++
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
func (f *fixtureBackend) ExportDiagnostics(ctx context.Context) (string, []byte, error) {
	f.record("diagnostics")
	return "egg-box-diagnostics.json", []byte(`{"ok":true}`), f.fail()
}
