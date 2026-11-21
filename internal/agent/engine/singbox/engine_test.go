package singbox_test

// These tests run the real sing-box core. They are safe to run on a developer
// machine: only loopback sockets are used, no TUN device is created, no route,
// DNS or proxy setting is touched, and every configuration goes through the
// same policy the agent applies in production.

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"eggbox/internal/agent/engine"
	"eggbox/internal/agent/engine/singbox"
	"eggbox/internal/agent/policy"
	"eggbox/internal/agentapi"
)

// ---------------------------------------------------------------- fixtures

// freePort returns a port that is free for both TCP and UDP: the proxy inbounds
// listen on both, and Windows can exclude contiguous ranges for one protocol
// while the other hands out sequential ephemeral ports inside that range.
func freePort(t *testing.T) int {
	t.Helper()
	var lastErr error
	for i := 0; i < 200; i++ {
		switch {
		case i < 20 && i%2 == 0:
			pc, err := net.ListenPacket("udp", "127.0.0.1:0")
			if err != nil {
				lastErr = err
				continue
			}
			port := pc.LocalAddr().(*net.UDPAddr).Port
			l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
			_ = pc.Close()
			if err != nil {
				lastErr = err
				continue
			}
			_ = l.Close()
			return port
		case i < 20:
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				lastErr = err
				continue
			}
			port := l.Addr().(*net.TCPAddr).Port
			pc, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", port))
			_ = l.Close()
			if err != nil {
				lastErr = err
				continue
			}
			_ = pc.Close()
			return port
		default:
			port := 20000 + rand.IntN(40000)
			l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
			if err != nil {
				lastErr = err
				continue
			}
			pc, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", port))
			_ = l.Close()
			if err != nil {
				lastErr = err
				continue
			}
			_ = pc.Close()
			return port
		}
	}
	t.Fatalf("no port that is free for both TCP and UDP (last error: %v)", lastErr)
	return 0
}

// rig is a complete loopback network: an HTTP origin, a Shadowsocks upstream
// run by a second core, and the client core under test.
type rig struct {
	t            *testing.T
	dir          string
	origin       *httptest.Server
	originPort   int
	upstreamPort int
	clientPort   int
	upstream     *singbox.Engine
	client       *singbox.Engine
	prepared     engine.Prepared
	pol          *policy.Policy
	env          policy.Env
	withAuto     bool // add a url-test group (see engine_upstream_race_test.go)
}

func newRig(t *testing.T) *rig {
	t.Helper()
	return newRigWith(t, false)
}

func newRigWith(t *testing.T, withAuto bool) *rig {
	t.Helper()
	r := &rig{t: t, dir: t.TempDir(), withAuto: withAuto}

	r.origin = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/generate_204":
			w.WriteHeader(http.StatusNoContent)
		case "/big":
			_, _ = w.Write([]byte(strings.Repeat("x", 256<<10)))
		default:
			_, _ = io.WriteString(w, "hello from origin")
		}
	}))
	t.Cleanup(r.origin.Close)
	u, _ := url.Parse(r.origin.URL)
	fmt.Sscan(u.Port(), &r.originPort)

	r.upstreamPort = freePort(t)
	r.clientPort = freePort(t)

	// The upstream is a test fixture: it needs a server inbound, which the
	// production registry deliberately does not contain.
	r.upstream = singbox.New(singbox.RegistryOptions{ServerInbounds: true})
	upCfg := fmt.Sprintf(`{
	  "log": {"level": "warn"},
	  "inbounds": [{"type": "shadowsocks", "tag": "ss-in", "listen": "127.0.0.1", "listen_port": %d,
	                "method": "aes-128-gcm", "password": "test-password"}],
	  "outbounds": [{"type": "direct", "tag": "direct"}],
	  "experimental": {"cache_file": {"enabled": true, "path": %q}}
	}`, r.upstreamPort, filepath.Join(r.dir, "upstream-cache.db"))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := r.upstream.Start(ctx, engine.Prepared{Revision: 1, JSON: []byte(upCfg)}); err != nil {
		t.Fatalf("upstream: %v", err)
	}
	t.Cleanup(func() { stop(t, r.upstream) })

	r.client = singbox.New(singbox.RegistryOptions{})
	t.Cleanup(func() { stop(t, r.client) })
	r.pol = policy.New(singbox.PolicyOptions(singbox.RegistryOptions{}))
	r.env = policy.Env{
		ResourceDir: r.dir, Resources: map[string]bool{},
		CachePath: filepath.Join(r.dir, "cache.db"), LogLevel: "info",
	}
	r.prepared = r.prepare(r.clientConfig(r.clientPort))
	return r
}

func (r *rig) clientConfig(port int) string {
	members := `"ss-up", "direct"`
	auto := ""
	if r.withAuto {
		auto = fmt.Sprintf(`,
	    {"type": "urltest", "tag": "auto", "outbounds": [%s],
	     "url": "http://127.0.0.1:%d/generate_204", "interval": "10m"}`, members, r.originPort)
	}
	return fmt.Sprintf(`{
	  "inbounds": [{"type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": %d}],
	  "outbounds": [
	    {"type": "shadowsocks", "tag": "ss-up", "server": "127.0.0.1", "server_port": %d,
	     "method": "aes-128-gcm", "password": "test-password"},
	    {"type": "direct", "tag": "direct"},
	    {"type": "selector", "tag": "proxy", "outbounds": [%s], "default": "ss-up"}%s
	  ],
	  "route": {
	    "final": "proxy",
	    "rules": [
	      {"clash_mode": "Direct", "outbound": "direct"},
	      {"clash_mode": "Global", "outbound": "proxy"}
	    ]
	  }
	}`, port, r.upstreamPort, members, auto)
}

func (r *rig) prepare(raw string) engine.Prepared {
	r.t.Helper()
	res := r.pol.Normalize([]byte(raw), r.env)
	if res.HasErrors() {
		r.t.Fatalf("policy rejected the test configuration: %v", res.Errors())
	}
	if strings.Contains(string(res.JSON), "set_system_proxy") {
		r.t.Fatal("a configuration that touches the system proxy must never reach the core")
	}
	return engine.Prepared{Revision: 1, JSON: res.JSON, HasTun: res.HasTun, TunTag: res.TunTag}
}

func (r *rig) startClient() {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := r.client.Start(ctx, r.prepared); err != nil {
		r.t.Fatalf("client start: %v", err)
	}
}

func stop(t *testing.T, e *singbox.Engine) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := e.Stop(ctx); err != nil {
		t.Errorf("stop: %v", err)
	}
}

// get fetches path from the origin through the client's HTTP proxy.
func (r *rig) get(path string) string {
	r.t.Helper()
	proxy, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", r.clientPort))
	c := &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true},
	}
	resp, err := c.Get(r.origin.URL + path)
	if err != nil {
		r.t.Fatalf("GET %s through the core: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// socksDial opens a TCP connection to target through the client's SOCKS5
// endpoint and returns it still open.
func (r *rig) socksDial(target string) net.Conn {
	r.t.Helper()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", r.clientPort), 5*time.Second)
	if err != nil {
		r.t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	must := func(err error) {
		if err != nil {
			conn.Close()
			r.t.Fatalf("socks handshake: %v", err)
		}
	}
	_, err = conn.Write([]byte{5, 1, 0})
	must(err)
	reply := make([]byte, 2)
	_, err = io.ReadFull(conn, reply)
	must(err)
	host, portStr, _ := net.SplitHostPort(target)
	var port uint16
	fmt.Sscan(portStr, &port)
	req := []byte{5, 1, 0, 1}
	req = append(req, net.ParseIP(host).To4()...)
	req = binary.BigEndian.AppendUint16(req, port)
	_, err = conn.Write(req)
	must(err)
	resp := make([]byte, 10)
	_, err = io.ReadFull(conn, resp)
	must(err)
	if resp[1] != 0 {
		conn.Close()
		r.t.Fatalf("socks connect refused: %v", resp)
	}
	_ = conn.SetDeadline(time.Time{})
	return conn
}

// recvUntil reads from ch until pred accepts a message.
func recvUntil[T any](t *testing.T, ch <-chan T, d time.Duration, what string, pred func(T) bool) T {
	t.Helper()
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case v, ok := <-ch:
			if !ok {
				t.Fatalf("stream ended while waiting for %s", what)
			}
			if pred(v) {
				return v
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func hasTag(chain []string, tag string) bool {
	for _, c := range chain {
		if c == tag {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- traffic

func TestTrafficFollowsTheSelectedOutbound(t *testing.T) {
	r := newRig(t)
	r.startClient()
	if !r.client.Running() {
		t.Fatal("Running must be true after Start")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conns, err := r.client.WatchConnections(ctx, 250*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	dest := fmt.Sprintf("127.0.0.1:%d", r.originPort)
	isOrigin := func(ev *agentapi.ConnectionEvents) *agentapi.Connection {
		for _, e := range ev.Events {
			if e.Type == agentapi.ConnectionEvent_TYPE_NEW && e.Connection != nil && e.Connection.Destination == dest {
				return e.Connection
			}
		}
		return nil
	}

	if body := r.get("/hello"); body != "hello from origin" {
		t.Fatalf("body = %q", body)
	}
	var viaProxy *agentapi.Connection
	recvUntil(t, conns, 5*time.Second, "the first connection", func(ev *agentapi.ConnectionEvents) bool {
		viaProxy = isOrigin(ev)
		return viaProxy != nil
	})
	if !hasTag(viaProxy.Chain, "ss-up") || !hasTag(viaProxy.Chain, "proxy") || viaProxy.Inbound != "mixed-in" {
		t.Fatalf("expected the connection to leave through the Shadowsocks outbound: %+v", viaProxy)
	}

	// Traffic counters move.
	traffic, err := r.client.WatchTraffic(ctx, 250*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	_ = r.get("/big")
	tr := recvUntil(t, traffic, 5*time.Second, "traffic totals", func(m *agentapi.Traffic) bool { return m.DownlinkTotal > 100<<10 })
	if !tr.Available || tr.Goroutines == 0 || tr.TimeUnixMs == 0 {
		t.Fatalf("traffic = %+v", tr)
	}

	// Groups describe the selector, and switching it reroutes new connections.
	groups, err := r.client.WatchGroups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	find := func(g *agentapi.Groups, tag string) *agentapi.Group {
		for _, x := range g.Groups {
			if x.Tag == tag {
				return x
			}
		}
		return nil
	}
	first := recvUntil(t, groups, 5*time.Second, "the groups", func(g *agentapi.Groups) bool { return find(g, "proxy") != nil })
	if p := find(first, "proxy"); !p.Selectable || p.Selected != "ss-up" || len(p.Items) != 2 || p.Type != "selector" {
		t.Fatalf("proxy group = %+v", p)
	}

	if err := r.client.SelectOutbound("proxy", "direct"); err != nil {
		t.Fatal(err)
	}
	recvUntil(t, groups, 5*time.Second, "the new selection", func(g *agentapi.Groups) bool {
		p := find(g, "proxy")
		return p != nil && p.Selected == "direct"
	})
	switched := time.Now().Add(-50 * time.Millisecond).UnixMilli()
	_ = r.get("/hello?again")
	var direct *agentapi.Connection
	recvUntil(t, conns, 5*time.Second, "the rerouted connection", func(ev *agentapi.ConnectionEvents) bool {
		for _, e := range ev.Events {
			if e.Type == agentapi.ConnectionEvent_TYPE_NEW && e.Connection != nil && e.Connection.Destination == dest && e.Connection.CreatedUnixMs >= switched {
				direct = e.Connection
				return true
			}
		}
		return false
	})
	if hasTag(direct.Chain, "ss-up") || !hasTag(direct.Chain, "direct") {
		t.Fatalf("after switching to direct the chain should not include the upstream: %+v", direct)
	}
}

func TestSelectOutboundErrors(t *testing.T) {
	r := newRig(t)
	r.startClient()
	if err := r.client.SelectOutbound("nope", "direct"); !errors.Is(err, engine.ErrNotFound) {
		t.Fatalf("unknown group: %v", err)
	}
	if err := r.client.SelectOutbound("proxy", "nope"); !errors.Is(err, engine.ErrNotFound) {
		t.Fatalf("unknown outbound: %v", err)
	}
	if err := r.client.SelectOutbound("direct", "direct"); !errors.Is(err, engine.ErrInvalid) {
		t.Fatalf("an outbound that is not a selector: %v", err)
	}
}

func TestClosingConnections(t *testing.T) {
	r := newRig(t)
	r.startClient()

	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	target := echo.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conns, err := r.client.WatchConnections(ctx, 250*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}

	open := func() (net.Conn, string) {
		c := r.socksDial(target)
		var id string
		recvUntil(t, conns, 5*time.Second, "the tunnelled connection", func(ev *agentapi.ConnectionEvents) bool {
			for _, e := range ev.Events {
				if e.Type == agentapi.ConnectionEvent_TYPE_NEW && e.Connection != nil && e.Connection.Destination == target && e.Connection.ClosedUnixMs == 0 {
					id = e.Id
					return true
				}
			}
			return false
		})
		return c, id
	}
	alive := func(c net.Conn) bool {
		_ = c.SetDeadline(time.Now().Add(1500 * time.Millisecond))
		if _, err := c.Write([]byte("ping")); err != nil {
			return false
		}
		buf := make([]byte, 4)
		_, err := io.ReadFull(c, buf)
		return err == nil && string(buf) == "ping"
	}

	c1, id1 := open()
	defer c1.Close()
	if !alive(c1) {
		t.Fatal("the tunnelled connection should echo")
	}
	if err := r.client.CloseConnection(id1); err != nil {
		t.Fatal(err)
	}
	recvUntil(t, conns, 5*time.Second, "the close event", func(ev *agentapi.ConnectionEvents) bool {
		for _, e := range ev.Events {
			if e.Type == agentapi.ConnectionEvent_TYPE_CLOSED && e.Id == id1 {
				return true
			}
		}
		return false
	})
	if alive(c1) {
		t.Fatal("the connection must be dead after CloseConnection")
	}

	c2, _ := open()
	defer c2.Close()
	c3, _ := open()
	defer c3.Close()
	if err := r.client.CloseAllConnections(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if alive(c2) || alive(c3) {
		t.Fatal("CloseAllConnections must drop everything")
	}
}

// ---------------------------------------------------------------- lifecycle

func TestLogLinesReachTheSink(t *testing.T) {
	r := newRig(t)
	var mu sync.Mutex
	var lines []string
	r.client.SetLogSink(func(level, msg string) {
		mu.Lock()
		lines = append(lines, level+"|"+msg)
		mu.Unlock()
	})
	r.startClient()
	_ = r.get("/hello")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(lines)
		var sawStarted, sawInbound bool
		for _, l := range lines {
			sawStarted = sawStarted || strings.Contains(l, "started")
			sawInbound = sawInbound || strings.Contains(l, "inbound/mixed")
			if strings.Contains(l, "\x1b[") {
				mu.Unlock()
				t.Fatalf("colour codes must be stripped: %q", l)
			}
		}
		mu.Unlock()
		if n > 0 && sawStarted && sawInbound {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	t.Fatalf("expected the start-up lines in the sink, got %q", lines)
}

func TestStopReleasesPortsAndEndsStreams(t *testing.T) {
	r := newRig(t)
	r.startClient()
	ctx := context.Background()
	traffic, err := r.client.WatchTraffic(ctx, 250*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	groups, err := r.client.WatchGroups(ctx)
	if err != nil {
		t.Fatal(err)
	}

	stop(t, r.client)
	if r.client.Running() {
		t.Fatal("Running must be false after Stop")
	}
	// The listener is gone: the port can be taken again at once.
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", r.clientPort))
	if err != nil {
		t.Fatalf("Stop must release the listening port: %v", err)
	}
	l.Close()

	for name, drained := range map[string]func() bool{
		"traffic": func() bool {
			for range traffic {
			}
			return true
		},
		"groups": func() bool {
			for range groups {
			}
			return true
		},
	} {
		done := make(chan bool, 1)
		go func(f func() bool) { done <- f() }(drained)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("the %s stream must end when the core stops", name)
		}
	}

	stop(t, r.client) // stopping twice is fine
	if _, err := r.client.WatchTraffic(ctx, time.Second); !errors.Is(err, engine.ErrNotRunning) {
		t.Fatalf("observing a stopped core: %v", err)
	}
	select {
	case ev := <-r.client.Exited():
		t.Fatalf("a requested stop must not look like a crash: %v", ev.Err)
	default:
	}
}

func TestEngineCanBeRestarted(t *testing.T) {
	r := newRig(t)
	for i := 0; i < 3; i++ {
		r.startClient()
		if got := r.get("/hello"); got != "hello from origin" {
			t.Fatalf("round %d: body = %q", i, got)
		}
		stop(t, r.client)
	}
}

func TestStartFailureLeavesNothingRunning(t *testing.T) {
	r := newRig(t)
	// Something else already owns the listener port.
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", r.clientPort))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err = r.client.Start(ctx, r.prepared)
	if err == nil {
		t.Fatal("starting on a busy port must fail")
	}
	if !strings.Contains(err.Error(), "address already in use") && !strings.Contains(err.Error(), "bind") {
		t.Logf("start error: %v", err)
	}
	if r.client.Running() || r.client.TunInterface() != "" {
		t.Fatal("a failed start must leave nothing behind")
	}
	l.Close()
	// The engine is reusable after a failure.
	r.startClient()
	if got := r.get("/hello"); got != "hello from origin" {
		t.Fatalf("body = %q", got)
	}
}

func TestCancelledStartAbortsCleanly(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.client.Start(ctx, r.prepared); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if r.client.Running() {
		t.Fatal("nothing should be running")
	}
	// A start cancelled while in progress also unwinds.
	for i := 0; i < 5; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(i)*5*time.Millisecond)
		err := r.client.Start(ctx, r.prepared)
		cancel()
		if err == nil {
			stop(t, r.client)
			continue
		}
		if r.client.Running() {
			t.Fatalf("attempt %d: a failed start must not leave the core running (%v)", i, err)
		}
	}
	r.startClient()
	if got := r.get("/hello"); got != "hello from origin" {
		t.Fatalf("the engine must still work: %q", got)
	}
}

// The core would otherwise create "cache.db" in the working directory, which for
// a Windows service is the system directory.
func TestStartNeedsAnAbsoluteCachePath(t *testing.T) {
	r := newRig(t)
	stray := filepath.Join(mustGetwd(t), "cache.db")
	_, existed := os.Stat(stray)
	for name, cfg := range map[string]string{
		"no experimental section": `{"outbounds":[{"type":"direct","tag":"direct"}]}`,
		"relative path":           `{"outbounds":[{"type":"direct","tag":"direct"}],"experimental":{"cache_file":{"enabled":true,"path":"cache.db"}}}`,
		"default path":            `{"outbounds":[{"type":"direct","tag":"direct"}],"experimental":{"cache_file":{"enabled":true}}}`,
	} {
		err := r.client.Start(context.Background(), engine.Prepared{Revision: 1, JSON: []byte(cfg)})
		if err == nil || !strings.Contains(err.Error(), "cache_file.path") {
			t.Fatalf("%s: err = %v", name, err)
		}
		if r.client.Running() {
			t.Fatalf("%s: a refused start left a core running", name)
		}
	}
	if _, now := os.Stat(stray); (now == nil) != (existed == nil) {
		t.Fatalf("a refused start created or removed %s", stray)
	}
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return wd
}

func TestStartRefusesToRunTwoCores(t *testing.T) {
	r := newRig(t)
	r.startClient()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.client.Start(ctx, r.prepared); err == nil {
		t.Fatal("a second Start while running must fail")
	}
	if got := r.get("/hello"); got != "hello from origin" {
		t.Fatalf("the running core must be undisturbed: %q", got)
	}
}

func TestObserversNeedARunningCore(t *testing.T) {
	e := singbox.New(singbox.RegistryOptions{})
	ctx := context.Background()
	if _, err := e.WatchGroups(ctx); !errors.Is(err, engine.ErrNotRunning) {
		t.Fatalf("WatchGroups: %v", err)
	}
	if _, err := e.WatchConnections(ctx, time.Second); !errors.Is(err, engine.ErrNotRunning) {
		t.Fatalf("WatchConnections: %v", err)
	}
	if err := e.SelectOutbound("a", "b"); !errors.Is(err, engine.ErrNotRunning) {
		t.Fatalf("SelectOutbound: %v", err)
	}
	if _, _, err := e.Mode(); !errors.Is(err, engine.ErrNotRunning) {
		t.Fatalf("Mode: %v", err)
	}
	if err := e.SetMode("Direct"); !errors.Is(err, engine.ErrNotRunning) {
		t.Fatalf("SetMode: %v", err)
	}
	if err := e.CloseAllConnections(); !errors.Is(err, engine.ErrNotRunning) {
		t.Fatalf("CloseAllConnections: %v", err)
	}
	if err := e.URLTest(ctx, "x"); !errors.Is(err, engine.ErrNotRunning) {
		t.Fatalf("URLTest: %v", err)
	}
	if e.Running() || e.TunInterface() != "" {
		t.Fatal("an idle engine is not running")
	}
	if err := e.Stop(ctx); err != nil {
		t.Fatalf("stopping an idle engine is not an error: %v", err)
	}
}

// ---------------------------------------------------------------- check

func TestCheckValidatesWithoutStartingAnything(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()

	if err := r.client.Check(ctx, r.prepared); err != nil {
		t.Fatalf("a valid configuration: %v", err)
	}
	if r.client.Running() {
		t.Fatal("Check must not start the core")
	}
	// No listener was opened: the port is still free.
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", r.clientPort))
	if err != nil {
		t.Fatalf("Check must not bind ports: %v", err)
	}
	l.Close()

	bad := engine.Prepared{JSON: []byte(`{"outbounds":[{"type":"selector","tag":"s","outbounds":["missing"]}]}`)}
	if err := r.client.Check(ctx, bad); err == nil {
		t.Fatal("a selector pointing to a missing outbound must be rejected")
	}
	broken := engine.Prepared{JSON: []byte(`{"outbounds": [`)}
	if err := r.client.Check(ctx, broken); err == nil {
		t.Fatal("invalid JSON must be rejected")
	}
	unknown := engine.Prepared{JSON: []byte(`{"outbounds":[{"type":"definitely-not-a-protocol","tag":"x"}]}`)}
	if err := r.client.Check(ctx, unknown); err == nil {
		t.Fatal("an unregistered protocol must be rejected")
	}
	// Server inbounds are not in the production registry, whatever the policy says.
	server := engine.Prepared{JSON: []byte(`{"inbounds":[{"type":"shadowsocks","tag":"s","listen":"127.0.0.1","listen_port":1,"method":"aes-128-gcm","password":"x"}]}`)}
	if err := r.client.Check(ctx, server); err == nil {
		t.Fatal("the production core must not be able to run a server inbound")
	}
}

func TestCheckDoesNotDisturbARunningCore(t *testing.T) {
	r := newRig(t)
	r.startClient()
	// The same listener port, checked while the core is using it. Like the
	// controller's dry runs, the check gets its own cache database: the running
	// core holds a lock on the real one.
	r.env.CachePath = filepath.Join(r.dir, "check-cache.db")
	dry := r.prepare(r.clientConfig(r.clientPort))
	for i := 0; i < 3; i++ {
		if err := r.client.Check(context.Background(), dry); err != nil {
			t.Fatalf("check %d: %v", i, err)
		}
	}
	if got := r.get("/hello"); got != "hello from origin" {
		t.Fatalf("the running core must be undisturbed by Check: %q", got)
	}
}

func TestCheckResolvesReferencesAndRuleSets(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(r.dir, "ads.json"), []byte(`{"version":2,"rules":[{"domain_suffix":["ads.example"]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r.env.Resources = map[string]bool{"ads.json": true}
	cfg := `{
	  "outbounds": [{"type": "direct", "tag": "direct"}, {"type": "block", "tag": "block"}],
	  "route": {
	    "rule_set": [{"type": "local", "tag": "ads", "format": "source", "path": "res:ads.json"}],
	    "rules": [{"rule_set": ["ads"], "outbound": "block"}],
	    "final": "direct"
	  }
	}`
	if err := r.client.Check(ctx, r.prepare(cfg)); err != nil {
		t.Fatalf("a configuration with a local rule-set: %v", err)
	}

	// A rule-set file that is gone is caught before anything runs.
	if err := os.Remove(filepath.Join(r.dir, "ads.json")); err != nil {
		t.Fatal(err)
	}
	if err := r.client.Check(ctx, r.prepare(cfg)); err == nil {
		t.Fatal("a missing rule-set file must be rejected by Check")
	}
}

// What the policy accepts as a well-formed, fully wired configuration, the core
// must accept too: DNS servers and rules, groups, rule-sets, resolvers.
func TestCheckAcceptsAFullyWiredConfig(t *testing.T) {
	r := newRig(t)
	cfg := `{
	  "dns": {
	    "servers": [
	      {"type": "udp", "tag": "dns-direct", "server": "223.5.5.5"},
	      {"type": "https", "tag": "dns-remote", "server": "1.1.1.1", "detour": "proxy"}
	    ],
	    "rules": [{"rule_set": ["ads"], "server": "dns-direct"}],
	    "final": "dns-remote"
	  },
	  "inbounds": [{"type": "mixed", "tag": "mixed-in", "listen_port": 7890}],
	  "outbounds": [
	    {"type": "shadowsocks", "tag": "node-a", "server": "203.0.113.5", "server_port": 8388, "method": "aes-128-gcm", "password": "x", "domain_resolver": "dns-direct"},
	    {"type": "direct", "tag": "direct"},
	    {"type": "selector", "tag": "proxy", "outbounds": ["node-a", "direct"], "default": "node-a"},
	    {"type": "urltest", "tag": "auto", "outbounds": ["node-a"], "url": "https://www.gstatic.com/generate_204"}
	  ],
	  "route": {
	    "rule_set": [{"type": "inline", "tag": "ads", "rules": [{"domain_suffix": ["ads.example"]}]}],
	    "rules": [
	      {"action": "sniff"},
	      {"rule_set": ["ads"], "action": "reject"},
	      {"clash_mode": "Direct", "outbound": "direct"},
	      {"domain_suffix": ["lan"], "action": "route", "outbound": "direct"},
	      {"type": "logical", "mode": "or", "rules": [{"domain": ["a.example"]}, {"domain": ["b.example"]}], "outbound": "auto"}
	    ],
	    "final": "proxy",
	    "default_domain_resolver": {"server": "dns-direct", "strategy": "prefer_ipv4"}
	  }
	}`
	if err := r.client.Check(context.Background(), r.prepare(cfg)); err != nil {
		t.Fatalf("the core rejects a configuration the policy accepts: %v", err)
	}
}

// A check of a TUN configuration must never create the device or touch routes
// or DNS: the check phase builds the inbound but does not start it. Opening a
// TUN device needs root, so an unprivileged run succeeding is the proof; the
// test refuses to run as root so it can never be the thing that changes the
// host's network.
func TestCheckNeverTouchesTheHostNetwork(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("never run network-affecting configurations as root")
	}
	r := newRig(t)
	snapshot := func() string {
		ifaces, err := net.Interfaces()
		if err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		for _, ifc := range ifaces {
			addrs, _ := ifc.Addrs()
			fmt.Fprintf(&b, "%s %v %d %v\n", ifc.Name, ifc.Flags, ifc.MTU, addrs)
		}
		return b.String()
	}
	before := snapshot()
	err := r.client.Check(context.Background(), r.prepare(`{
	  "inbounds": [{"type": "tun", "tag": "tun-in", "address": ["172.19.0.1/30"], "auto_route": true, "stack": "system", "mtu": 1500}],
	  "outbounds": [{"type": "direct", "tag": "direct"}],
	  "route": {"auto_detect_interface": true, "final": "direct"}
	}`))
	if err != nil {
		t.Fatalf("checking a TUN configuration needs no privileges: %v", err)
	}
	if after := snapshot(); after != before {
		t.Fatalf("the interface table changed during a check:\n%s\n---\n%s", before, after)
	}
}

func TestCheckAcceptsConfirmedVPNTakeoverWithoutStartingIt(t *testing.T) {
	r := newRig(t)
	r.env.Tun.TakeoverInterface = "en0"
	prepared := r.prepare(`{"inbounds":[{"type":"tun","tag":"tun-in"}],"outbounds":[{"type":"direct","tag":"direct"}],"route":{"final":"direct"}}`)
	if err := r.client.Check(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	if r.client.Running() {
		t.Fatal("checking takeover settings started the core")
	}
}

func TestCheckDoesNotLeakGoroutines(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(r.dir, "ads.json"), []byte(`{"version":2,"rules":[{"domain_suffix":["ads.example"]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r.env.Resources = map[string]bool{"ads.json": true}
	prepared := r.prepare(`{
	  "inbounds": [{"type": "mixed", "tag": "m", "listen": "127.0.0.1", "listen_port": 1}],
	  "outbounds": [{"type": "direct", "tag": "direct"}],
	  "route": {"rule_set": [{"type": "local", "tag": "ads", "format": "source", "path": "res:ads.json"}], "final": "direct"}
	}`)
	// Warm up lazily created singletons, then measure.
	for i := 0; i < 3; i++ {
		if err := r.client.Check(ctx, prepared); err != nil {
			t.Fatal(err)
		}
	}
	settle := func() int {
		time.Sleep(300 * time.Millisecond)
		return runtime.NumGoroutine()
	}
	before := settle()
	for i := 0; i < 25; i++ {
		if err := r.client.Check(ctx, prepared); err != nil {
			t.Fatal(err)
		}
	}
	after := settle()
	// A leak would add at least one goroutine per check (25+); allow a little noise.
	if after > before+8 {
		buf := make([]byte, 1<<16)
		n := runtime.Stack(buf, true)
		t.Fatalf("goroutines grew from %d to %d over 25 checks:\n%s", before, after, buf[:n])
	}
}

func TestVersionIsPinned(t *testing.T) {
	if v := singbox.New(singbox.RegistryOptions{}).Version(); v != "1.14.4" {
		t.Fatalf("version = %q", v)
	}
}
