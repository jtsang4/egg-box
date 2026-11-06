//go:build eggbox_dev && (darwin || linux)

package e2e

// The routing plan, from the application's service down to the real core,
// with nothing but loopback in between.
//
// The world is invented and lives on 127.0.0.1: three Shadowsocks nodes whose
// exits are three little web servers (so the body of an answer says which node
// carried the request), one more web server for what goes direct, a name
// server that knows a handful of invented names, a mirror that serves the rule
// files the shipped plan asks for, and a provider that hands out the nodes.
// The application runs as it does installed (service, library, rule data,
// client of the agent) against the development agent, whose TUN is a proxy on
// a loopback port, so the host's network is never touched.
//
// Every request goes through that proxy to a name or an address the plan knows
// something about, and what the core really did (the rule it matched, the
// chain it went through) is held against what the test-URL dialog said it
// would do.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing-box/option"
	"go.yaml.in/yaml/v3"
	"golang.org/x/net/dns/dnsmessage"

	"eggbox/internal/agent/engine"
	"eggbox/internal/agent/engine/singbox"
	"eggbox/internal/agent/transport"
	"eggbox/internal/app"
	"eggbox/internal/boot"
	"eggbox/internal/configgen"
	"eggbox/internal/library"
	"eggbox/internal/routing"
	"eggbox/internal/subscription"
	"eggbox/internal/testsupport/realsub"
	"eggbox/internal/testsupport/srs"
)

// ------------------------------------------------------------------- the world

// A name server that knows the names a test registers and answers everything
// else with "no such name".
type nameServer struct {
	pc    net.PacketConn
	mu    sync.Mutex
	names map[string][]netip.Addr
	asked map[string]int
}

func newNameServer(t *testing.T) *nameServer {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ns := &nameServer{pc: pc, names: map[string][]netip.Addr{}, asked: map[string]int{}}
	t.Cleanup(func() { _ = pc.Close() })
	go ns.serve()
	return ns
}

func (ns *nameServer) port() int { return ns.pc.LocalAddr().(*net.UDPAddr).Port }

// know registers what a name leads to.
func (ns *nameServer) know(name string, addrs ...string) {
	ns.mu.Lock()
	defer ns.mu.Unlock()
	for _, a := range addrs {
		ns.names[name] = append(ns.names[name], netip.MustParseAddr(a))
	}
}

func (ns *nameServer) lookup(name string) []netip.Addr {
	ns.mu.Lock()
	defer ns.mu.Unlock()
	ns.asked[name]++
	return append([]netip.Addr(nil), ns.names[name]...)
}

func (ns *nameServer) timesAsked(name string) int {
	ns.mu.Lock()
	defer ns.mu.Unlock()
	return ns.asked[name]
}

func (ns *nameServer) serve() {
	buf := make([]byte, 1500)
	for {
		n, from, err := ns.pc.ReadFrom(buf)
		if err != nil {
			return
		}
		var p dnsmessage.Parser
		hdr, err := p.Start(buf[:n])
		if err != nil {
			continue
		}
		q, err := p.Question()
		if err != nil {
			continue
		}
		name := strings.TrimSuffix(strings.ToLower(q.Name.String()), ".")
		addrs := ns.lookup(name)
		resp := dnsmessage.Message{
			Header: dnsmessage.Header{
				ID: hdr.ID, Response: true, Authoritative: true, RecursionDesired: hdr.RecursionDesired, RecursionAvailable: true,
			},
			Questions: []dnsmessage.Question{q},
		}
		if len(addrs) == 0 {
			resp.Header.RCode = dnsmessage.RCodeNameError
		}
		for _, a := range addrs {
			if q.Type == dnsmessage.TypeA && a.Is4() {
				resp.Answers = append(resp.Answers, dnsmessage.Resource{
					Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 1},
					Body:   &dnsmessage.AResource{A: a.As4()},
				})
			}
		}
		if b, err := resp.Pack(); err == nil {
			_, _ = ns.pc.WriteTo(b, from)
		}
	}
}

// free finds a port that is free for both TCP and UDP.
func free(t *testing.T) int {
	t.Helper()
	for i := 0; i < 50; i++ {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := l.Addr().(*net.TCPAddr).Port
		pc, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", port))
		_ = l.Close()
		if err != nil {
			continue
		}
		_ = pc.Close()
		return port
	}
	t.Fatal("no free port")
	return 0
}

// An exit is a web server that says who it is, and answers the latency probes
// of the automatic groups.
func newExit(t *testing.T, name string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/generate_204") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = io.WriteString(w, "exit-"+name)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func portOf(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	return srv.Listener.Addr().(*net.TCPAddr).Port
}

// An ssNode: a Shadowsocks server that sends everything it is asked to reach to
// its own exit, except the name server's port, which it reaches for real (the
// proxied name lookups need it). It can be stopped and started again.
type ssNode struct {
	t        *testing.T
	name     string
	port     int
	exit     *httptest.Server
	dnsPort  int
	eng      *singbox.Engine
	running  bool
	instance int
	dir      string
}

func newSSNode(t *testing.T, name string, dnsPort int) *ssNode {
	t.Helper()
	n := &ssNode{t: t, name: name, port: free(t), exit: newExit(t, name), dnsPort: dnsPort, dir: t.TempDir()}
	n.start()
	t.Cleanup(func() {
		if n.running {
			n.stop()
		}
	})
	return n
}

func (n *ssNode) start() {
	n.t.Helper()
	n.instance++
	n.eng = singbox.New(singbox.RegistryOptions{ServerInbounds: true})
	cfg := fmt.Sprintf(`{
	  "log": {"level": "warn"},
	  "inbounds": [{"type": "shadowsocks", "tag": "ss-in", "listen": "127.0.0.1", "listen_port": %d,
	                "method": "aes-128-gcm", "password": "e2e-password"}],
	  "outbounds": [{"type": "direct", "tag": "direct"}],
	  "route": {
	    "rules": [
	      {"port": [%d], "action": "route", "outbound": "direct"},
	      {"network": "tcp", "action": "route", "outbound": "direct", "override_address": "127.0.0.1", "override_port": %d}
	    ],
	    "final": "direct"
	  },
	  "experimental": {"cache_file": {"enabled": true, "path": %q}}
	}`, n.port, n.dnsPort, portOf(n.t, n.exit), filepath.Join(n.dir, "cache.db"))
	c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := n.eng.Start(c, engine.Prepared{Revision: 1, JSON: []byte(cfg)}); err != nil {
		n.t.Fatalf("node %s: %v", n.name, err)
	}
	n.running = true
}

func (n *ssNode) stop() {
	n.t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := n.eng.Stop(c); err != nil {
		n.t.Errorf("stop node %s: %v", n.name, err)
	}
	n.running = false
}

// The rule files the shipped plan asks for. A set of names is the name of the
// service with ".example" after it; a set of addresses comes from a table, so
// that a test can send a request to an address the plan has an opinion about.
var addressSets = map[string][]string{
	"telegram": {"198.51.100.0/24"},
	"netflix":  {"198.18.0.0/16"},
	"google":   {"192.0.2.0/24"},
	"twitter":  {"203.0.113.0/26"},
	"facebook": {"203.0.113.64/26"},
	// What is "in China" for this test is the one address every test can
	// reach without leaving the machine.
	"cn": {"127.0.0.1/32"},
}

// siteOf is the domain suffix the geosite set of a service holds.
func siteOf(name string) string {
	return strings.Trim(nonAlnum.ReplaceAllString(strings.ToLower(name), "-"), "-") + ".example"
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// A mirror of the public rule data, and the provider's page.
type mirror struct {
	t     *testing.T
	mu    sync.Mutex
	sub   string
	token string
	files map[string][]byte // what a provider's profile points at
	hits  map[string]int
}

func (m *mirror) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	if m.hits == nil {
		m.hits = map[string]int{}
	}
	m.hits[r.URL.Path]++
	body := m.sub
	file, isFile := m.files[r.URL.Path]
	m.mu.Unlock()
	name := strings.TrimSuffix(r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:], ".srs")
	switch {
	case isFile:
		_, _ = w.Write(file)
	case r.URL.Path == "/sub/"+m.token:
		w.Header().Set("Subscription-Userinfo", "upload=1; download=2; total=100000; expire=1893456000")
		_, _ = io.WriteString(w, body)
	case strings.Contains(r.URL.Path, "/geo/geosite/"):
		_, _ = w.Write(srs.DomainSuffixes(m.t, siteOf(name)))
	case strings.Contains(r.URL.Path, "/geo/geoip/"):
		ranges, ok := addressSets[name]
		if !ok {
			ranges = []string{"240.0.0.0/8"} // nothing anyone reaches
		}
		_, _ = w.Write(srs.CIDRs(m.t, ranges...))
	default:
		http.NotFound(w, r)
	}
}

// A client that asks the mirror for everything that is not on this machine:
// the rule data's addresses are the Internet's, and nothing may go there.
type toMirror struct {
	to   *url.URL
	base http.RoundTripper
}

func (m toMirror) RoundTrip(req *http.Request) (*http.Response, error) {
	if h := req.URL.Hostname(); h != "127.0.0.1" && h != "localhost" {
		req = req.Clone(req.Context())
		req.URL.Scheme, req.URL.Host, req.Host = m.to.Scheme, m.to.Host, m.to.Host
	}
	return m.base.RoundTrip(req)
}

// world is everything a test needs.
type world struct {
	t      *testing.T
	stack  *stack
	rt     *boot.Runtime
	svc    *app.Service
	dns    *nameServer
	nodes  map[string]*ssNode // by the name the subscription gives it
	direct *httptest.Server
	sub    library.Subscription
	items  int
	// dataWait is how long the plan's rule files may take.
	dataWait time.Duration
}

// The nodes of the invented subscription, in the order it lists them.
var nodeNames = []string{"Hong Kong 01", "Japan 01", "Japan 02"}

// worldSpec says what the provider hands out.
type worldSpec struct {
	// profile makes the subscription and the files its rules point at (by
	// path on the mirror) from the ports the nodes listen on.
	profile func(ports map[string]int) (body string, files map[string][]byte)
	// items is how many items the plan has once it has been adopted.
	items int
	// realData downloads the public rule data instead of serving invented
	// sets (it needs the network, for the rule files only).
	realData bool
	// nodes is how many nodes the subscription has (the three of this
	// machine unless a real provider's profile brings its own).
	nodes int
}

// nodesOnly is a subscription of the three nodes and nothing else: the
// shipped plan stays in force.
func nodesOnly(ports map[string]int) (string, map[string][]byte) {
	var outbounds []map[string]any
	for _, name := range nodeNames {
		outbounds = append(outbounds, map[string]any{
			"type": "shadowsocks", "tag": name, "server": "127.0.0.1", "server_port": ports[name],
			"method": "aes-128-gcm", "password": "e2e-password",
		})
	}
	body, err := json.Marshal(map[string]any{"outbounds": outbounds})
	if err != nil {
		panic(err)
	}
	return string(body), nil
}

func newWorld(t *testing.T, opts ...func(*worldSpec)) *world {
	t.Helper()
	spec := worldSpec{profile: nodesOnly, items: 12}
	for _, o := range opts {
		o(&spec)
	}
	w := &world{t: t, nodes: map[string]*ssNode{}, items: spec.items, dataWait: 30 * time.Second}
	w.stack = start(t)
	before := interfaces(t)
	t.Cleanup(func() {
		if after := interfaces(t); after != before {
			t.Errorf("the host's interfaces changed:\n%s\n---\n%s", before, after)
		}
	})
	w.dns = newNameServer(t)
	w.direct = newExit(t, "direct")

	ports := map[string]int{}
	for _, name := range nodeNames {
		n := newSSNode(t, name, w.dns.port())
		w.nodes[name] = n
		ports[name] = n.port
	}
	body, files := spec.profile(ports)
	m := &mirror{t: t, sub: body, token: "e2e-token", files: files}
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	to, _ := url.Parse(srv.URL)

	httpc := &http.Client{Timeout: 20 * time.Second, Transport: toMirror{to: to, base: http.DefaultTransport}}
	if spec.realData {
		httpc = &http.Client{Timeout: 90 * time.Second}
		w.dataWait = 5 * time.Minute
	}
	rt, err := boot.Start(ctx(t), boot.Config{
		Mode:       boot.Dev,
		DataDir:    t.TempDir(),
		Secrets:    library.NewMemorySecrets(),
		Dial:       transport.DialDev(w.stack.socket),
		HTTPClient: httpc,
		Resolve: func(c context.Context, host string) ([]netip.Addr, error) {
			if a := w.dns.lookup(host); len(a) > 0 {
				return a, nil
			}
			return nil, fmt.Errorf("no such host")
		},
		Version: "e2e",
	})
	if err != nil {
		t.Fatalf("assemble the application: %v", err)
	}
	t.Cleanup(rt.Close)
	w.rt, w.svc = rt, rt.Service
	until(t, "the service answers", 20*time.Second, func() bool { return w.svc.Snapshot().Phase == app.PhaseDisconnected })

	// Names and latency probes stay on this machine.
	prefs := configgen.DefaultPrefs()
	prefs.DomesticDNS = fmt.Sprintf("udp://127.0.0.1:%d", w.dns.port())
	prefs.RemoteDNS = fmt.Sprintf("udp://127.0.0.1:%d", w.dns.port())
	prefs.TestURL = "http://latency.e2e.example/generate_204"
	if _, err := w.svc.SavePrefs(ctx(t), prefs); err != nil {
		t.Fatalf("preferences: %v", err)
	}
	w.sub, _, err = w.svc.AddSubscription(ctx(t), "", srv.URL+"/sub/"+m.token, library.RemoteOptions{})
	if err != nil {
		t.Fatalf("add the subscription: %v", err)
	}
	if spec.nodes == 0 {
		spec.nodes = len(nodeNames)
	}
	if spec.nodes > 0 && w.sub.NodeCount != spec.nodes {
		t.Fatalf("the subscription gave %d nodes, want %d", w.sub.NodeCount, spec.nodes)
	}
	if w.items == 0 {
		w.items = len(w.svc.Routing().View().Rows)
	}
	return w
}

// connect brings the proxy up in the sandbox and waits for the plan's rule
// files, which must all have come before the core is asked to use them.
func (w *world) connect() {
	w.t.Helper()
	until(w.t, "the plan's rule files", w.dataWait, func() bool {
		p := w.svc.Snapshot().Plan
		return p.Ready && len(p.Items) == w.items && p.Missing == 0 && !p.Updating
	})
	if err := w.svc.Connect(ctx(w.t), app.ConnectOptions{}); err != nil {
		w.t.Fatalf("connect: %v", err)
	}
	until(w.t, "connected", 60*time.Second, func() bool { return w.svc.Snapshot().Phase == app.PhaseConnected })
	st := w.svc.Snapshot().Status
	if st.GetSandboxPort() == 0 || st.GetTunInterface() != "" {
		w.t.Fatalf("the sandbox must run the TUN as a loopback proxy: port=%d interface=%q", st.GetSandboxPort(), st.GetTunInterface())
	}
	w.svc.WatchConnections(true)
	w.t.Cleanup(func() { w.svc.WatchConnections(false) })
}

func (w *world) nodeID(name string) routing.NodeID { return routing.NodeID{Sub: w.sub.ID, Name: name} }

func (w *world) edit(fn func(*routing.State) error) {
	w.t.Helper()
	if err := w.svc.EditRouting(ctx(w.t), fn); err != nil {
		w.t.Fatalf("edit the routing: %v", err)
	}
}

func (w *world) revision() uint64 { return w.svc.Snapshot().Applied.Revision }

// ---------------------------------------------------------------- the requests

// get asks for a page through the sandbox's proxy, to host:port.
func (w *world) get(host string, port int) (string, error) {
	// A new configuration may come up on a new loopback port.
	proxy, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", w.svc.Snapshot().Status.GetSandboxPort()))
	c := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}}
	resp, err := c.Get("http://" + net.JoinHostPort(host, fmt.Sprint(port)) + "/page")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return string(b), fmt.Errorf("status %d", resp.StatusCode)
	}
	return string(b), nil
}

// seen is the connection the core opened for a request to host:port, as the
// connection list shows it.
func (w *world) seen(host string, port int, since time.Time, wait time.Duration) (app.Conn, bool) {
	want := net.JoinHostPort(host, fmt.Sprint(port))
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		var found app.Conn
		ok := false
		for _, c := range w.svc.Snapshot().Conns {
			if (c.Host == host || c.Destination == want) && !c.Created.Before(since.Add(-time.Second)) {
				if !ok || c.Created.After(found.Created) {
					found, ok = c, true
				}
			}
		}
		if ok {
			return found, true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return app.Conn{}, false
}

// outcome is what happened to one request.
type outcome struct {
	body    string
	err     error
	conn    app.Conn
	tracked bool
}

// probe sends a request to host:port and collects what the core did with it.
func (w *world) probe(host string, port int) outcome {
	w.t.Helper()
	at := time.Now()
	body, err := w.get(host, port)
	o := outcome{body: body, err: err}
	// A request that was refused leaves nothing in the list; do not wait for it.
	wait := 6 * time.Second
	if err != nil {
		wait = 1500 * time.Millisecond
	}
	o.conn, o.tracked = w.seen(host, port, at, wait)
	return o
}

// predicted is what the test-URL dialog says about host:port.
func (w *world) predicted(host string, port int) *app.TestURLResult {
	w.t.Helper()
	res, err := w.svc.TestURL(ctx(w.t), "http://"+net.JoinHostPort(host, fmt.Sprint(port))+"/")
	if err != nil {
		w.t.Fatalf("TestURL(%s): %v", host, err)
	}
	return res
}

// what is expected of a request.
type expect struct {
	why  app.WhyKind
	id   string   // the item or exception; empty for the rest
	body []string // acceptable answers; empty when the request must not get through
	note string
}

// check sends a request to host:port and holds the core's doing against the
// dialog's word and against what the person was promised.
func (w *world) check(host string, port int, want expect) outcome {
	w.t.Helper()
	label := fmt.Sprintf("%s:%d (%s)", host, port, want.note)
	pred := w.predicted(host, port)
	if pred.Why.Kind != want.why || pred.Why.ID != want.id {
		w.t.Errorf("%s: the dialog says %+v, want %v %q", label, pred.Why, want.why, want.id)
	}
	o := w.probe(host, port)
	if len(want.body) == 0 {
		if o.err == nil {
			w.t.Errorf("%s: a request that must be refused was answered: %q", label, o.body)
		}
		return o
	}
	if o.err != nil {
		w.t.Errorf("%s: the request failed: %v", label, o.err)
		return o
	}
	ok := false
	for _, b := range want.body {
		ok = ok || o.body == b
	}
	if !ok {
		w.t.Errorf("%s: answered by %q, want one of %v", label, o.body, want.body)
	}
	if !o.tracked {
		w.t.Errorf("%s: the connection is not in the connection list", label)
		return o
	}
	if o.conn.Why.Kind != want.why || o.conn.Why.ID != want.id {
		w.t.Errorf("%s: the core matched %+v (its own words: %q), want %v %q", label, o.conn.Why, o.conn.Rule, want.why, want.id)
	}
	if o.conn.Why != pred.Why {
		w.t.Errorf("%s: the dialog said %+v, the core did %+v", label, pred.Why, o.conn.Why)
	}
	return o
}

// ------------------------------------------------------------------- the tests

func TestTheCoreFollowsThePlanTheWayTheTestURLSaysItWill(t *testing.T) {
	w := newWorld(t)
	directPort := portOf(t, w.direct)

	// The names this world knows.
	w.dns.know("www.cn.example", "127.0.0.1")              // a domestic name
	w.dns.know("www.google.example", "127.0.0.1")          // a service sent direct by choice
	w.dns.know("chat.category-ai-cn.example", "127.0.0.1") // for when the AI item goes direct
	w.dns.know("app.private.category-ai-cn.example", "127.0.0.1")
	w.dns.know("cdn.unlisted.example", "198.18.0.5")     // leads into the streaming addresses
	w.dns.know("cdn2.unlisted.example", "198.51.100.77") // leads into the Telegram addresses
	w.dns.know("site.unlisted-cn.example", "127.0.0.1")  // no list has it; the address is domestic
	w.dns.know("wiki.corp.example", "203.0.113.151")     // leads nowhere either
	w.dns.know("t.telegram.example", "203.0.113.152")    // (a range exception makes the core look every name up first)
	w.dns.know("x.unlisted.example", "203.0.113.150")    // leads nowhere the plan knows
	w.connect()

	// Where the plan sends things: Japan 01 is the main exit, Telegram is
	// pinned to Hong Kong, streaming to the fastest in Japan, Google goes
	// direct (the shipped plan sends only the domestic services that way).
	w.edit(func(st *routing.State) error {
		for _, f := range []func() error{
			func() error { return st.SetMain(routing.ToNode(w.nodeID("Japan 01"))) },
			func() error { return st.SetItemDest("b-telegram", routing.ToNode(w.nodeID("Hong Kong 01"))) },
			func() error { return st.SetItemDest("b-streaming", routing.ToRegion("JP")) },
			func() error { return st.SetItemDest("b-google", routing.Direct()) },
		} {
			if err := f(); err != nil {
				return err
			}
		}
		return nil
	})
	jp1, hk1, direct := "exit-Japan 01", "exit-Hong Kong 01", "exit-direct"
	until(t, "the main exit to be Japan 01", 20*time.Second, func() bool {
		return w.probe("203.0.113.200", directPort).body == jp1
	})

	t.Run("what the plan lists and what it publishes", func(t *testing.T) {
		w.check("chat.category-ai-cn.example", directPort, expect{app.WhyItem, "b-ai", []string{jp1}, "a name of an item that follows the main exit"})
		w.check("t.telegram.example", directPort, expect{app.WhyItem, "b-telegram", []string{hk1}, "a name of an item pinned to a node"})
		w.check("198.51.100.10", directPort, expect{app.WhyItem, "b-telegram", []string{hk1}, "an address in a published range"})
		w.check("cdn.unlisted.example", directPort, expect{app.WhyItem, "b-streaming", []string{"exit-Japan 01", "exit-Japan 02"}, "no list has the name; the address is in a range"})
		w.check("cdn2.unlisted.example", directPort, expect{app.WhyItem, "b-telegram", []string{hk1}, "a name that leads into another range"})
		w.check("www.cn.example", directPort, expect{app.WhyItem, "b-cn", []string{direct}, "a domestic name"})
		w.check("site.unlisted-cn.example", directPort, expect{app.WhyItem, "b-cn", []string{direct}, "no list has the name; the address is domestic"})
		w.check("www.google.example", directPort, expect{app.WhyItem, "b-google", []string{direct}, "a service sent direct by choice"})
		w.check("127.0.0.1", directPort, expect{app.WhyLAN, "", []string{direct}, "this machine"})
		w.check("203.0.113.200", directPort, expect{app.WhyFallback, "", []string{jp1}, "an address nobody claims"})
		w.check("x.unlisted.example", directPort, expect{app.WhyFallback, "", []string{jp1}, "a name that leads nowhere the plan knows"})
	})

	t.Run("exceptions come first, in order", func(t *testing.T) {
		var ids []string
		w.edit(func(st *routing.State) error {
			for _, x := range []routing.Exception{
				{Kind: routing.EntryDomainSuffix, Value: "corp.example", Dest: routing.ToNode(w.nodeID("Japan 02")), Enabled: true},
				{Kind: routing.EntryDomainSuffix, Value: "private.category-ai-cn.example", Dest: routing.Direct(), Enabled: true},
				{Kind: routing.EntryPort, Value: "8443", Dest: routing.ToNode(w.nodeID("Hong Kong 01")), Enabled: true},
				{Kind: routing.EntryIPCIDR, Value: "198.51.100.0/25", Dest: routing.ToNode(w.nodeID("Japan 02")), Enabled: true},
			} {
				added, err := st.AddException(x)
				if err != nil {
					return err
				}
				ids = append(ids, added.ID)
			}
			return nil
		})
		jp2 := "exit-Japan 02"
		until(t, "the exceptions to be in force", 20*time.Second, func() bool {
			o := w.probe("wiki.corp.example", directPort)
			return o.body == jp2
		})

		w.check("wiki.corp.example", directPort, expect{app.WhyException, ids[0], []string{jp2}, "a suffix of the person's own"})
		w.check("app.private.category-ai-cn.example", directPort, expect{app.WhyException, ids[1], []string{direct}, "over the item that lists the same names"})
		w.check("anything.unlisted.example", 8443, expect{app.WhyException, ids[2], []string{hk1}, "a port, whatever the name"})
		w.check("198.51.100.10", directPort, expect{app.WhyException, ids[3], []string{jp2}, "a range, over the item that publishes it"})
		w.check("cdn2.unlisted.example", directPort, expect{app.WhyException, ids[3], []string{jp2}, "a name that leads into the range (looked up before the names of the items)"})
		// What no exception claims is where it was.
		w.check("198.51.100.200", directPort, expect{app.WhyItem, "b-telegram", []string{hk1}, "beyond the range of the exception"})
		w.check("t.telegram.example", directPort, expect{app.WhyItem, "b-telegram", []string{hk1}, "a name of the item"})

		// An exception that is switched off no longer counts, and one
		// that is deleted is gone.
		w.edit(func(st *routing.State) error { return st.SetExceptionEnabled(ids[3], false) })
		until(t, "the range exception to be off", 20*time.Second, func() bool { return w.probe("198.51.100.10", directPort).body == hk1 })
		w.check("198.51.100.10", directPort, expect{app.WhyItem, "b-telegram", []string{hk1}, "the exception is off"})
		w.edit(func(st *routing.State) error { return st.DeleteException(ids[0]) })
		until(t, "the suffix exception to be gone", 20*time.Second, func() bool { return w.probe("wiki.corp.example", directPort).body == jp1 })
		w.check("wiki.corp.example", directPort, expect{app.WhyFallback, "", []string{jp1}, "the exception is deleted"})
	})
}

// Where the plan asks about addresses the core looks a name up first, and
// turns the connection away when it cannot: the dialog has to say so, and has
// to say the opposite where the plan has no such rule (nothing is looked up
// then, and a name that leads nowhere goes where its host name says).
func TestTheCoreTurnsAwayANameThatLeadsNowhereOnlyWhereThePlanAsksAboutAddresses(t *testing.T) {
	w := newWorld(t)
	directPort := portOf(t, w.direct)
	w.dns.know("x.unlisted.example", "203.0.113.150") // leads nowhere the plan knows
	// "gone.unlisted.example" and "named.unlisted.example" are not known to the name server.
	w.connect()
	jp1, hk1 := "exit-Japan 01", "exit-Hong Kong 01"
	w.edit(func(st *routing.State) error { return st.SetMain(routing.ToNode(w.nodeID("Japan 01"))) })
	until(t, "the main exit to be Japan 01", 20*time.Second, func() bool {
		return w.probe("203.0.113.200", directPort).body == jp1
	})

	// The shipped plan publishes address ranges, so every name is looked up.
	w.check("x.unlisted.example", directPort, expect{app.WhyFallback, "", []string{jp1}, "a name that leads nowhere the plan knows"})
	w.check("gone.unlisted.example", directPort, expect{app.WhyUnresolved, "", nil, "a name nobody can look up"})
	if w.dns.timesAsked("gone.unlisted.example") == 0 {
		t.Error("the core turned a name away without asking the name server about it")
	}

	// No item on: no rule about addresses, so no lookup.
	w.edit(func(st *routing.State) error {
		for _, row := range st.View().Rows {
			if err := st.SetItemEnabled(row.Item.ID, false); err != nil {
				return err
			}
		}
		return nil
	})
	until(t, "the plan to ask about no address", 20*time.Second, func() bool {
		return w.probe("gone.unlisted.example", directPort).body == jp1
	})
	asked := w.dns.timesAsked("gone.unlisted.example")
	w.check("gone.unlisted.example", directPort, expect{app.WhyFallback, "", []string{jp1}, "nothing asks where a name leads, so none is looked up"})
	if got := w.dns.timesAsked("gone.unlisted.example"); got != asked {
		t.Errorf("the name server was asked about the name %d times where no rule is about addresses", got-asked)
	}

	// An exception about a range asks again, after the exceptions before it.
	var host, rng routing.Exception
	w.edit(func(st *routing.State) (err error) {
		if host, err = st.AddException(routing.Exception{Kind: routing.EntryDomain, Value: "named.unlisted.example", Dest: routing.ToNode(w.nodeID("Hong Kong 01")), Enabled: true}); err != nil {
			return err
		}
		rng, err = st.AddException(routing.Exception{Kind: routing.EntryIPCIDR, Value: "192.0.2.0/24", Dest: routing.ToNode(w.nodeID("Japan 02")), Enabled: true})
		return err
	})
	until(t, "the exception about a range to be in force", 20*time.Second, func() bool {
		return w.probe("gone.unlisted.example", directPort).err != nil
	})
	w.check("gone.unlisted.example", directPort, expect{app.WhyUnresolved, "", nil, "the exception about a range needs the name looked up"})
	w.check("named.unlisted.example", directPort, expect{app.WhyException, host.ID, []string{hk1}, "an exception about the name itself comes before the lookup"})
	w.check("x.unlisted.example", directPort, expect{app.WhyFallback, "", []string{jp1}, "a name that leads out of the range goes on"})

	w.edit(func(st *routing.State) error { return st.DeleteException(rng.ID) })
	until(t, "the exception about a range to be gone", 20*time.Second, func() bool {
		return w.probe("gone.unlisted.example", directPort).body == jp1
	})
	w.check("gone.unlisted.example", directPort, expect{app.WhyFallback, "", []string{jp1}, "the lookup is gone with the exception that needed it"})
}

// selected is what a group of the running core has chosen right now.
func selected(svc *app.Service, group string) string {
	for _, g := range svc.Snapshot().Groups {
		if g.Tag == group {
			return g.Selected
		}
	}
	return ""
}

// Choosing where an item goes is a switch inside the running core: no new
// configuration, no restart, and the next connection already follows it. Only
// blocking an item, or turning one off, changes the rules themselves.
func TestChoosingWhereAnItemGoesIsASwitchInTheRunningCore(t *testing.T) {
	w := newWorld(t)
	directPort := portOf(t, w.direct)
	w.dns.know("chat.category-ai-cn.example", "127.0.0.1") // so that "direct" has somewhere to go
	w.dns.know("api.github.example", "203.0.113.153")
	w.connect()
	hk, jp1, jp2 := w.nodeID("Hong Kong 01"), w.nodeID("Japan 01"), w.nodeID("Japan 02")
	w.edit(func(st *routing.State) error { return st.SetMain(routing.ToNode(jp1)) })
	until(t, "Japan 01 to be the main exit", 20*time.Second, func() bool {
		return w.probe("203.0.113.200", directPort).body == "exit-Japan 01"
	})
	revision, port := w.revision(), w.svc.Snapshot().Status.GetSandboxPort()

	ai := func(want expect) { t.Helper(); w.check("chat.category-ai-cn.example", directPort, want) }
	toAI := func(d routing.Dest) {
		t.Helper()
		w.edit(func(st *routing.State) error { return st.SetItemDest("b-ai", d) })
	}
	settled := func(body string) {
		t.Helper()
		until(t, "the traffic to follow the choice ("+body+")", 20*time.Second, func() bool {
			return w.probe("chat.category-ai-cn.example", directPort).body == body
		})
	}

	// The item follows the main exit.
	ai(expect{app.WhyItem, "b-ai", []string{"exit-Japan 01"}, "follows the main exit"})

	steps := []struct {
		name string
		dest routing.Dest
		body []string
	}{
		{"one node", routing.ToNode(jp2), []string{"exit-Japan 02"}},
		{"another node", routing.ToNode(hk), []string{"exit-Hong Kong 01"}},
		{"direct", routing.Direct(), []string{"exit-direct"}},
		{"the fastest of a region", routing.ToRegion("JP"), []string{"exit-Japan 01", "exit-Japan 02"}},
		{"the main exit again", routing.Follow(), []string{"exit-Japan 01"}},
	}
	for _, step := range steps {
		toAI(step.dest)
		settled(step.body[0])
		if len(step.body) > 1 {
			until(t, step.name, 20*time.Second, func() bool {
				b := w.probe("chat.category-ai-cn.example", directPort).body
				for _, ok := range step.body {
					if b == ok {
						return true
					}
				}
				return false
			})
		}
		ai(expect{app.WhyItem, "b-ai", step.body, step.name})
		if got := w.revision(); got != revision {
			t.Errorf("%s: a new configuration was applied (revision %d, was %d)", step.name, got, revision)
		}
		if got := w.svc.Snapshot().Status.GetSandboxPort(); got != port {
			t.Errorf("%s: the core was restarted (proxy port %d, was %d)", step.name, got, port)
		}
	}

	// Moving the main exit moves everything that follows it, the fallback too.
	w.edit(func(st *routing.State) error { return st.SetMain(routing.ToNode(hk)) })
	settled("exit-Hong Kong 01")
	ai(expect{app.WhyItem, "b-ai", []string{"exit-Hong Kong 01"}, "the main exit moved"})
	w.check("203.0.113.200", directPort, expect{app.WhyFallback, "", []string{"exit-Hong Kong 01"}, "the fallback follows the main exit too"})
	if got := w.revision(); got != revision {
		t.Errorf("moving the main exit applied a new configuration (revision %d, was %d)", got, revision)
	}

	// Blocking an item changes the rules; a blocked item gets no connection at all.
	toAI(routing.Reject())
	until(t, "the item to be blocked", 20*time.Second, func() bool {
		_, err := w.get("chat.category-ai-cn.example", directPort)
		return err != nil
	})
	if w.revision() <= revision {
		t.Errorf("blocking an item did not change the configuration")
	}
	pred := w.predicted("chat.category-ai-cn.example", directPort)
	if pred.Why.ID != "b-ai" || pred.Dest.Kind != routing.DestReject {
		t.Errorf("the dialog says %+v -> %v of a blocked item", pred.Why, pred.Dest.Kind)
	}
	if o := w.probe("chat.category-ai-cn.example", directPort); o.err == nil || o.tracked {
		t.Errorf("a blocked item let a request through: %+v", o)
	}
	// Everything else still works.
	w.check("203.0.113.200", directPort, expect{app.WhyFallback, "", []string{"exit-Hong Kong 01"}, "the rest of the plan, while an item is blocked"})
	toAI(routing.Follow())
	settled("exit-Hong Kong 01")

	// Turning an item off takes its rules away: its names go where nothing claims them.
	w.check("api.github.example", directPort, expect{app.WhyItem, "b-github", []string{"exit-Hong Kong 01"}, "an item that is on"})
	before := w.revision()
	w.edit(func(st *routing.State) error { return st.SetItemEnabled("b-github", false) })
	until(t, "the item to be off", 20*time.Second, func() bool { return w.predicted("api.github.example", directPort).Why.Kind == app.WhyFallback })
	w.check("api.github.example", directPort, expect{app.WhyFallback, "", []string{"exit-Hong Kong 01"}, "an item that is off"})
	if w.revision() <= before {
		t.Errorf("turning an item off did not change the configuration")
	}
}

// The main exit is a node somebody chose. When it stops answering, the traffic
// moves to the fastest node of the same region by itself, and comes back when
// the node does.
func TestTheMainExitFailsOverToTheSameRegionAndComesBack(t *testing.T) {
	w := newWorld(t)
	directPort := portOf(t, w.direct)
	w.connect()
	w.edit(func(st *routing.State) error { return st.SetMain(routing.ToNode(w.nodeID("Japan 01"))) })
	until(t, "Japan 01 to be the main exit", 20*time.Second, func() bool {
		return w.probe("203.0.113.200", directPort).body == "exit-Japan 01"
	})
	revision := w.revision()

	w.nodes["Japan 01"].stop()
	failed := 0
	until(t, "the traffic to move to Japan 02", 30*time.Second, func() bool {
		body, err := w.get("203.0.113.200", directPort)
		if err != nil {
			failed++
			return false
		}
		return body == "exit-Japan 02"
	})
	if failed > 3 {
		t.Errorf("%d requests failed before the stand-in took over", failed)
	}
	t.Logf("failover: %d request(s) failed before the same-region node took over", failed)
	if w.revision() != revision {
		t.Errorf("the failover needed a new configuration")
	}
	// Hong Kong is not an answer to a Japanese node's failure.
	for i := 0; i < 5; i++ {
		if body, err := w.get("203.0.113.200", directPort); err == nil && body == "exit-Hong Kong 01" {
			t.Errorf("traffic went to another region")
		}
	}

	// A node is measured once a minute while traffic flows and it has a
	// stand-in, so coming back takes up to that long.
	if testing.Short() {
		t.Skip("the recovery takes a minute or two")
	}
	w.nodes["Japan 01"].start()
	until(t, "the chosen node to carry the traffic again", 150*time.Second, func() bool {
		body, err := w.get("203.0.113.200", directPort)
		return err == nil && body == "exit-Japan 01"
	})
	if w.revision() != revision {
		t.Errorf("the recovery needed a new configuration")
	}
}

// ------------------------------------------------- a plan that comes with a provider

const (
	tagMain = "🚀 节点选择"
	tagAuto = "♻️ 自动选择"
	tagAI   = "🤖 AI 服务"
	tagCN   = "🇨🇳 国内"
)

// providerFiles are the rule files a provider's profile points at, by path on
// the mirror, and the address they are given in the profile.
func providerFiles(t *testing.T) (files map[string][]byte, urls map[string]string) {
	t.Helper()
	files = map[string][]byte{
		"/rules/ai.srs":   srs.DomainSuffixes(t, "ai-e2e.example"),
		"/rules/ads.srs":  srs.DomainSuffixes(t, "ads-e2e.example"),
		"/rules/cnip.srs": srs.CIDRs(t, "127.0.0.1/32"),
		"/rules/ai.txt":   []byte("+.ai-e2e.example\n"),
		"/rules/ads.txt":  []byte("+.ads-e2e.example\n"),
		"/rules/cnip.txt": []byte("127.0.0.1/32\n"),
	}
	urls = map[string]string{}
	for path := range files {
		urls[path] = "https://rules.e2e.test" + path
	}
	return files, urls
}

// singboxProfile is a provider's sing-box configuration: nodes, groups, and
// rules that name rule sets by address.
func singboxProfile(t *testing.T) func(map[string]int) (string, map[string][]byte) {
	return func(ports map[string]int) (string, map[string][]byte) {
		files, urls := providerFiles(t)
		var nodesOut []any
		var names []any
		for _, name := range nodeNames {
			names = append(names, name)
			nodesOut = append(nodesOut, map[string]any{
				"type": "shadowsocks", "tag": name, "server": "127.0.0.1", "server_port": ports[name],
				"method": "aes-128-gcm", "password": "e2e-password",
			})
		}
		outbounds := append(nodesOut,
			map[string]any{"type": "selector", "tag": tagMain, "outbounds": append(append([]any{tagAuto}, names...), "direct")},
			map[string]any{"type": "urltest", "tag": tagAuto, "outbounds": names, "url": "http://latency.e2e.example/generate_204"},
			map[string]any{"type": "selector", "tag": tagAI, "outbounds": append([]any{"Japan 01", tagMain}, "direct")},
			map[string]any{"type": "selector", "tag": tagCN, "outbounds": []any{"direct", tagMain}},
			map[string]any{"type": "direct", "tag": "direct"},
		)
		remote := func(tag, path string) map[string]any {
			return map[string]any{"tag": tag, "type": "remote", "format": "binary", "url": urls[path]}
		}
		body, err := json.Marshal(map[string]any{
			"outbounds": outbounds,
			"route": map[string]any{
				"rule_set": []any{remote("ai", "/rules/ai.srs"), remote("ads", "/rules/ads.srs"), remote("cnip", "/rules/cnip.srs")},
				"rules": []any{
					map[string]any{"rule_set": []any{"ads"}, "action": "reject"},
					map[string]any{"rule_set": []any{"ai"}, "action": "route", "outbound": tagAI},
					map[string]any{"domain_suffix": []any{"corp.example"}, "action": "route", "outbound": "direct"},
					map[string]any{"domain_suffix": []any{"cn.example"}, "action": "route", "outbound": tagCN},
					map[string]any{"rule_set": []any{"cnip"}, "action": "route", "outbound": tagCN},
				},
				"final": tagMain,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return string(body), files
	}
}

// clashProfile is the same provider's Clash profile.
func clashProfile(t *testing.T) func(map[string]int) (string, map[string][]byte) {
	return func(ports map[string]int) (string, map[string][]byte) {
		files, urls := providerFiles(t)
		var b strings.Builder
		b.WriteString("proxies:\n")
		for _, name := range nodeNames {
			fmt.Fprintf(&b, "  - {name: %q, type: ss, server: 127.0.0.1, port: %d, cipher: aes-128-gcm, password: e2e-password}\n", name, ports[name])
		}
		list := `"` + strings.Join(nodeNames, `", "`) + `"`
		b.WriteString("proxy-groups:\n")
		fmt.Fprintf(&b, "  - {name: %q, type: select, proxies: [%q, %s, DIRECT]}\n", tagMain, tagAuto, list)
		fmt.Fprintf(&b, "  - {name: %q, type: url-test, proxies: [%s], url: http://latency.e2e.example/generate_204, interval: 300}\n", tagAuto, list)
		fmt.Fprintf(&b, "  - {name: %q, type: select, proxies: [\"Japan 01\", %q, DIRECT]}\n", tagAI, tagMain)
		fmt.Fprintf(&b, "  - {name: %q, type: select, proxies: [DIRECT, %q]}\n", tagCN, tagMain)
		b.WriteString("rule-providers:\n")
		for _, p := range []struct{ name, behavior, path string }{
			{"ai", "domain", "/rules/ai.txt"}, {"ads", "domain", "/rules/ads.txt"}, {"cnip", "ipcidr", "/rules/cnip.txt"},
		} {
			fmt.Fprintf(&b, "  %s: {type: http, behavior: %s, format: text, url: %q, path: ./%s.txt, interval: 86400}\n", p.name, p.behavior, urls[p.path], p.name)
		}
		b.WriteString("rules:\n")
		fmt.Fprintf(&b, "  - RULE-SET,ads,REJECT\n")
		fmt.Fprintf(&b, "  - RULE-SET,ai,%s\n", tagAI)
		fmt.Fprintf(&b, "  - DOMAIN-SUFFIX,corp.example,DIRECT\n")
		fmt.Fprintf(&b, "  - DOMAIN-SUFFIX,cn.example,%s\n", tagCN)
		fmt.Fprintf(&b, "  - RULE-SET,cnip,%s,no-resolve\n", tagCN)
		fmt.Fprintf(&b, "  - MATCH,%s\n", tagMain)
		return b.String(), files
	}
}

// What a provider's profile turns into is the same in either format, and the
// real core carries it out the way the dialog says.
func TestAPlanThatComesWithAProviderRunsInTheRealCore(t *testing.T) {
	for _, format := range []struct {
		name    string
		profile func(*testing.T) func(map[string]int) (string, map[string][]byte)
	}{{"sing-box", singboxProfile}, {"Clash", clashProfile}} {
		t.Run(format.name, func(t *testing.T) {
			w := newWorld(t, func(s *worldSpec) { s.profile = format.profile(t); s.items = 4 })
			directPort := portOf(t, w.direct)
			w.dns.know("chat.ai-e2e.example", "203.0.113.160")
			w.dns.know("x.ads-e2e.example", "203.0.113.161")
			w.dns.know("wiki.corp.example", "127.0.0.1")
			w.dns.know("shop.cn.example", "127.0.0.1")
			w.dns.know("site.unlisted-cn.example", "127.0.0.1")
			w.dns.know("chat.category-ai-cn.example", "203.0.113.162")
			w.dns.know("t.telegram.example", "203.0.113.163")

			// What the provider's profile became: its groups, in its order,
			// each with the destination its group starts on.
			st := w.svc.Routing()
			view := st.View()
			if st.ActiveID() != routing.SubPlanID(w.sub.ID) || len(view.Rows) != 4 {
				t.Fatalf("the provider's plan is not in force: %s, %d items", st.ActiveID(), len(view.Rows))
			}
			id := map[string]string{}
			want := []struct {
				name string
				dest routing.Dest
			}{
				{"广告拦截", routing.Reject()},
				{tagAI, routing.ToNode(w.nodeID("Japan 01"))},
				{"直连", routing.Direct()},
				{tagCN, routing.Direct()},
			}
			for i, row := range view.Rows {
				id[row.Item.Name] = row.Item.ID
				if row.Item.Name != want[i].name || !row.Item.Dest.Equal(want[i].dest) || !row.Item.Enabled {
					t.Errorf("item %d is %q -> %+v, want %q -> %+v", i+1, row.Item.Name, row.Item.Dest, want[i].name, want[i].dest)
				}
			}
			if st.Main.Kind != routing.DestAuto || view.Fallback.Kind != routing.DestFollow {
				t.Errorf("main %+v, fallback %+v", st.Main, view.Fallback)
			}
			w.connect()

			anyNode := []string{"exit-Hong Kong 01", "exit-Japan 01", "exit-Japan 02"}
			w.check("x.ads-e2e.example", directPort, expect{app.WhyItem, id["广告拦截"], nil, "a rule set the provider blocks"})
			w.check("chat.ai-e2e.example", directPort, expect{app.WhyItem, id[tagAI], []string{"exit-Japan 01"}, "a rule set the provider sends to one node"})
			w.check("wiki.corp.example", directPort, expect{app.WhyItem, id["直连"], []string{"exit-direct"}, "a rule that names no group"})
			w.check("shop.cn.example", directPort, expect{app.WhyItem, id[tagCN], []string{"exit-direct"}, "a suffix of a group"})
			w.check("site.unlisted-cn.example", directPort, expect{app.WhyItem, id[tagCN], []string{"exit-direct"}, "an address of the same group"})
			w.check("203.0.113.200", directPort, expect{app.WhyFallback, "", anyNode, "what no rule claims goes to the provider's own default (the fastest node)"})

			// The person chooses: Japan 01 for the main exit, Japan 02 for AI
			// (the provider's own choice for it was Japan 01).
			w.edit(func(st *routing.State) error {
				if err := st.SetMain(routing.ToNode(w.nodeID("Japan 01"))); err != nil {
					return err
				}
				return st.SetItemDest(id[tagAI], routing.ToNode(w.nodeID("Japan 02")))
			})
			until(t, "the choices to be in force", 20*time.Second, func() bool {
				return w.probe("chat.ai-e2e.example", directPort).body == "exit-Japan 02"
			})
			w.check("203.0.113.200", directPort, expect{app.WhyFallback, "", []string{"exit-Japan 01"}, "the provider's default follows the person's main exit"})

			// Switching to the shipped plan while connected: what the person
			// chose for the group of the same name comes along; what the
			// provider chose does not.
			w.edit(func(st *routing.State) error { return st.Switch(routing.BuiltinID) })
			until(t, "the shipped plan to be in force", 30*time.Second, func() bool {
				return w.predicted("chat.category-ai-cn.example", directPort).Why.ID == "b-ai"
			})
			w.check("chat.category-ai-cn.example", directPort, expect{app.WhyItem, "b-ai", []string{"exit-Japan 02"}, "the shipped plan's AI item took the node the person chose for the AI group"})
			w.check("t.telegram.example", directPort, expect{app.WhyItem, "b-telegram", []string{"exit-Japan 01"}, "an item nobody chose for follows the main exit"})
			w.check("x.ads-e2e.example", directPort, expect{app.WhyFallback, "", []string{"exit-Japan 01"}, "the provider's rules are no longer in force"})

			// And back: the provider's plan is as it was left.
			w.edit(func(st *routing.State) error { return st.Switch(routing.SubPlanID(w.sub.ID)) })
			until(t, "the provider's plan to be in force again", 30*time.Second, func() bool {
				return w.predicted("x.ads-e2e.example", directPort).Why.ID == id["广告拦截"]
			})
			w.check("chat.ai-e2e.example", directPort, expect{app.WhyItem, id[tagAI], []string{"exit-Japan 02"}, "back on the provider's plan, with the person's choice"})
			w.check("x.ads-e2e.example", directPort, expect{app.WhyItem, id["广告拦截"], nil, "blocked again"})
		})
	}
}

// -------------------------------------------- the dialog and the core, on a sample

var hostName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)

// pick takes up to n entries spread evenly over the list.
func pick(list []string, n int) []string {
	if len(list) <= n {
		return list
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, list[i*len(list)/n])
	}
	return out
}

// A request to host:port, for one entry of a rule set.
type sampled struct {
	host string
	from string // what it was made from, for the report
	item string // the item whose rule set it was taken from; empty for the rest
}

// samplesOf makes requests for the entries of a rule set: a name inside a
// suffix, an exact name, a name that holds a keyword, an address inside a
// range.
func samplesOf(rules []option.HeadlessRule, perKind int) []sampled {
	var domains, suffixes, keywords, ranges []string
	for _, r := range rules {
		o := r.DefaultOptions
		domains = append(domains, o.Domain...)
		suffixes = append(suffixes, o.DomainSuffix...)
		keywords = append(keywords, o.DomainKeyword...)
		ranges = append(ranges, o.IPCIDR...)
	}
	var out []sampled
	add := func(host, from string) {
		host = strings.ToLower(host)
		if hostName.MatchString(host) {
			out = append(out, sampled{host: host, from: from})
		}
	}
	for _, d := range pick(domains, perKind) {
		add(d, "domain")
	}
	for _, s := range pick(suffixes, perKind) {
		add("www."+strings.TrimPrefix(s, "."), "suffix")
	}
	for _, k := range pick(keywords, perKind/4+1) {
		add("x"+k+"x.sample.example", "keyword")
	}
	var v4 []string
	for _, c := range ranges {
		if p, err := netip.ParsePrefix(c); err == nil && p.Addr().Is4() {
			v4 = append(v4, c)
		}
	}
	for _, c := range pick(v4, perKind) {
		p := netip.MustParsePrefix(c)
		a := p.Addr()
		if next := a.Next(); p.Contains(next) {
			a = next
		}
		out = append(out, sampled{host: a.String(), from: "address"})
	}
	return out
}

// checkSample sends one request and reports where the dialog and the core
// differ, in words that carry nothing but public names. wantBody says which
// exit the dialog's answer leads to (empty: any node of this machine).
//
// The second result is the item the dialog gave the request to.
func (w *world) checkSample(sm sampled, port int, wantBody func(*app.TestURLResult) string) (string, string) {
	pred, err := w.svc.TestURL(ctx(w.t), "http://"+net.JoinHostPort(sm.host, fmt.Sprint(port))+"/")
	if err != nil {
		return fmt.Sprintf("%s (%s): the dialog refused it: %v", sm.host, sm.from, err), ""
	}
	o := w.probe(sm.host, port)
	if pred.Dest.Kind == routing.DestReject {
		if o.err == nil {
			return fmt.Sprintf("%s (%s): the dialog says it is blocked (%v), the request was answered", sm.host, sm.from, pred.Why.Kind), pred.Why.ID
		}
		return "", pred.Why.ID
	}
	want := "exit-"
	exact := wantBody != nil && wantBody(pred) != ""
	if exact {
		want = "exit-" + wantBody(pred)
	}
	switch {
	case o.err != nil:
		return fmt.Sprintf("%s (%s): the request failed (the dialog says %v %q): %v", sm.host, sm.from, pred.Why.Kind, pred.Why.ID, o.err), pred.Why.ID
	case !o.tracked:
		return fmt.Sprintf("%s (%s): not in the connection list", sm.host, sm.from), pred.Why.ID
	case o.conn.Why.Kind != pred.Why.Kind || o.conn.Why.ID != pred.Why.ID:
		return fmt.Sprintf("%s (%s): the dialog says %v %q, the core did %v %q (rule %q)", sm.host, sm.from, pred.Why.Kind, pred.Why.ID, o.conn.Why.Kind, o.conn.Why.ID, o.conn.Rule), pred.Why.ID
	case exact && o.body != want, !exact && !strings.HasPrefix(o.body, want):
		return fmt.Sprintf("%s (%s): the dialog says %v, the request came out at %q", sm.host, sm.from, pred.Dest.Kind, o.body), pred.Why.ID
	}
	return "", pred.Why.ID
}

// The dialog works out where an address goes from the rule files; the core
// does it from the same files, compiled. Wherever the two could disagree
// (a suffix, an exact name, a keyword, a range) they are asked about entries
// of the real public rule data, and must say the same.
//
// It needs the network for the rule files, and nothing else: every item is
// sent to a node of this machine, so no request leaves it.
//
//	EGGBOX_E2E_NETWORK=1 go test -tags "..." -run RealRuleData -v ./internal/agent/e2e
func TestTheCoreAndTheDialogAgreeOnTheRealRuleData(t *testing.T) {
	if os.Getenv("EGGBOX_E2E_NETWORK") == "" {
		t.Skip("downloads the public rule data: set EGGBOX_E2E_NETWORK=1")
	}
	w := newWorld(t, func(s *worldSpec) { s.realData = true })
	directPort := portOf(t, w.direct)

	// Every item goes to one of the nodes of this machine, taking turns, so
	// that the answer says where the dialog sent it.
	w.edit(func(st *routing.State) error {
		if err := st.SetMain(routing.ToNode(w.nodeID("Japan 01"))); err != nil {
			return err
		}
		for i, row := range st.View().Rows {
			if !row.Item.Enabled {
				continue
			}
			if err := st.SetItemDest(row.Item.ID, routing.ToNode(w.nodeID(nodeNames[i%len(nodeNames)]))); err != nil {
				return err
			}
		}
		return nil
	})
	began := time.Now()
	w.connect()
	t.Logf("the real rule data downloaded and loaded by the core in %v", time.Since(began).Round(time.Second))
	nodeOf := func(pred *app.TestURLResult) string {
		if pred.Dest.Kind == routing.DestNode {
			return pred.Dest.Node.Name
		}
		return "Japan 01" // follows the main exit
	}

	var samples []sampled
	var perItem []string
	counts := map[string]int{}
	for _, row := range w.svc.Routing().View().Rows {
		if !row.Item.Enabled {
			continue
		}
		before := len(samples)
		for _, src := range row.Item.Sources {
			parsed, err := w.rt.Rules.Parsed(src.URL)
			if err != nil {
				t.Fatalf("the downloaded rule file of %q does not parse: %v", row.Item.Key, err)
			}
			for _, sm := range samplesOf(parsed.Rules, 24) {
				sm.item = row.Item.ID
				samples = append(samples, sm)
			}
		}
		perItem = append(perItem, row.Item.Key)
		counts[row.Item.ID] = len(samples) - before
	}
	// Names and addresses nothing lists: the dialog and the core look the
	// name up (here) and decide on the address.
	for i := 0; i < 20; i++ {
		name := fmt.Sprintf("nothing%d.sample.example", i)
		w.dns.know(name, fmt.Sprintf("203.0.113.%d", 10+i))
		samples = append(samples, sampled{host: name, from: "unlisted name"})
		samples = append(samples, sampled{host: fmt.Sprintf("203.0.113.%d", 100+i), from: "unlisted address"})
	}
	var bad []string
	own := map[string]int{}
	for _, sm := range samples {
		msg, got := w.checkSample(sm, directPort, nodeOf)
		if msg != "" {
			bad = append(bad, msg)
		}
		if sm.item != "" && got == sm.item {
			own[sm.item]++
		}
	}
	// An item that is earlier in the plan may take names its rule set shares
	// with a later one; but most of what an item lists is its own.
	var report []string
	for _, key := range perItem {
		id := "b-" + key
		report = append(report, fmt.Sprintf("%s %d/%d", key, own[id], counts[id]))
		if counts[id] > 0 && own[id]*2 < counts[id] {
			t.Errorf("only %d of %d requests taken from the rule files of %q went to it", own[id], counts[id], key)
		}
	}
	t.Logf("%d of %d requests: the dialog and the core said the same", len(samples)-len(bad), len(samples))
	t.Logf("requests that went to the item whose rule files they were taken from: %s", strings.Join(report, ", "))
	for i, msg := range bad {
		if i == 15 {
			t.Errorf("... and %d more", len(bad)-i)
			break
		}
		t.Error(msg)
	}
}

// realProfile is a real provider's profile (the link is in a private file)
// with everything kept as it is except the nodes: each becomes one of the
// nodes of this machine under the same name. The plan, its groups, its rule
// files are the provider's; nothing a request does leaves this machine.
func realProfile(t *testing.T, linkFile string) func(map[string]int) (string, map[string][]byte) {
	return func(ports map[string]int) (string, map[string][]byte) {
		raw, err := os.ReadFile(linkFile)
		if err != nil {
			t.Fatal("cannot read the link file")
		}
		f, err := subscription.Fetch(ctx(t), strings.TrimSpace(string(raw)), subscription.FetchOptions{})
		if err != nil {
			t.Fatalf("fetch the real profile (%s)", realsub.Class(err))
		}
		local := []int{ports[nodeNames[0]], ports[nodeNames[1]], ports[nodeNames[2]]}
		if body := bytes.TrimSpace(f.Body); len(body) > 0 && body[0] == '{' {
			var root map[string]any
			if err := json.Unmarshal(body, &root); err != nil {
				t.Fatal("the real profile is not JSON")
			}
			outs, _ := root["outbounds"].([]any)
			n := 0
			for i, o := range outs {
				ob, _ := o.(map[string]any)
				switch ob["type"] {
				case "selector", "urltest", "direct", "block", "dns", "fallback", "":
					continue
				}
				outs[i] = map[string]any{"type": "shadowsocks", "tag": ob["tag"], "server": "127.0.0.1", "server_port": local[n%len(local)],
					"method": "aes-128-gcm", "password": "e2e-password"}
				n++
			}
			out, err := json.Marshal(root)
			if err != nil {
				t.Fatal(err)
			}
			return string(out), nil
		}
		var root map[string]any
		if err := yaml.Unmarshal(f.Body, &root); err != nil {
			t.Fatal("the real profile is neither JSON nor YAML")
		}
		proxies, _ := root["proxies"].([]any)
		for i, p := range proxies {
			pm, _ := p.(map[string]any)
			proxies[i] = map[string]any{"name": pm["name"], "type": "ss", "server": "127.0.0.1", "port": local[i%len(local)],
				"cipher": "aes-128-gcm", "password": "e2e-password"}
		}
		root["proxies"] = proxies
		out, err := yaml.Marshal(root)
		if err != nil {
			t.Fatal(err)
		}
		return string(out), nil
	}
}

// The plan of a real provider, in either of the formats it publishes, with
// its own rule files and the public ones it points at, carried out by the
// real core: wherever the dialog could differ from the core, it is asked about
// entries of those files.
//
//	EGGBOX_E2E_NETWORK=1 EGGBOX_TEST_SINGBOX_LINK_FILE=... EGGBOX_TEST_CLASH_LINK_FILE=... \
//	  go test -tags "..." -run RealProvidersPlan -v ./internal/agent/e2e
func TestTheRealProvidersPlanRunsInTheRealCoreAsTheDialogSays(t *testing.T) {
	if os.Getenv("EGGBOX_E2E_NETWORK") == "" {
		t.Skip("downloads the provider's profile and rule files: set EGGBOX_E2E_NETWORK=1")
	}
	for _, format := range []struct{ name, env string }{
		{"sing-box", "EGGBOX_TEST_SINGBOX_LINK_FILE"}, {"Clash", "EGGBOX_TEST_CLASH_LINK_FILE"},
	} {
		file := os.Getenv(format.env)
		if file == "" {
			continue
		}
		t.Run(format.name, func(t *testing.T) {
			// How many nodes the provider has does not matter here: they all
			// become the nodes of this machine.
			w := newWorld(t, func(s *worldSpec) {
				s.profile, s.realData, s.items, s.nodes = realProfile(t, file), true, 0, -1
			})
			directPort := portOf(t, w.direct)
			rows := w.svc.Routing().View().Rows
			t.Logf("the provider's plan: %d items", len(rows))

			// Nothing goes direct: that would leave this machine.
			w.edit(func(st *routing.State) error {
				for _, row := range st.View().Rows {
					if row.Item.Dest.Kind == routing.DestDirect {
						if err := st.SetItemDest(row.Item.ID, routing.Follow()); err != nil {
							return err
						}
					}
				}
				if st.View().Fallback.Kind == routing.DestDirect {
					return st.SetFallback(routing.Follow())
				}
				return nil
			})
			began := time.Now()
			w.connect()
			t.Logf("rule files fetched and loaded by the core in %v", time.Since(began).Round(time.Second))

			var samples []sampled
			unreadable, counts := 0, map[string]int{}
			for _, row := range w.svc.Routing().View().Rows {
				if !row.Item.Enabled {
					continue
				}
				before := len(samples)
				for _, src := range row.Item.Sources {
					parsed, err := w.rt.Rules.Parsed(src.URL)
					if err != nil {
						unreadable++
						continue
					}
					for _, sm := range samplesOf(parsed.Rules, 16) {
						sm.item = row.Item.ID
						samples = append(samples, sm)
					}
				}
				counts[row.Item.ID] = len(samples) - before
			}
			for i := 0; i < 10; i++ {
				name := fmt.Sprintf("nothing%d.sample.example", i)
				w.dns.know(name, fmt.Sprintf("203.0.113.%d", 10+i))
				samples = append(samples, sampled{host: name, from: "unlisted name"})
				samples = append(samples, sampled{host: fmt.Sprintf("203.0.113.%d", 100+i), from: "unlisted address"})
			}
			t.Logf("%d requests; %d rule files could not be read", len(samples), unreadable)
			if waiting := w.svc.Snapshot().Plan.Missing; waiting > 0 {
				t.Errorf("%d rule files are still missing", waiting)
			}

			var bad []string
			byKind := map[string]int{}
			for _, sm := range samples {
				msg, _ := w.checkSample(sm, directPort, nil)
				if msg != "" {
					bad = append(bad, msg)
				}
				byKind[sm.from]++
			}
			t.Logf("%d of %d requests: the dialog and the core said the same (%v)", len(samples)-len(bad), len(samples), byKind)
			for i, msg := range bad {
				if i == 15 {
					t.Errorf("... and %d more", len(bad)-i)
					break
				}
				t.Error(msg)
			}
		})
	}
}
