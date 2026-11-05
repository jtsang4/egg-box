//go:build sbui_dev && (darwin || linux)

// Package e2e runs the whole agent stack the way the development agent does:
// the real controller, policy and sing-box core, a simulated operating system,
// and a client reaching it over a Unix socket. It is safe on any machine: the
// platform is an in-memory simulation, a TUN configuration runs as a proxy on a
// loopback port, and only loopback sockets are used.
package e2e

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jtsang4/sing-box-ui/internal/agent/daemon"
	"github.com/jtsang4/sing-box-ui/internal/agent/engine/singbox"
	"github.com/jtsang4/sing-box-ui/internal/agent/platform/sim"
	"github.com/jtsang4/sing-box-ui/internal/agent/policy"
	"github.com/jtsang4/sing-box-ui/internal/agent/server"
	"github.com/jtsang4/sing-box-ui/internal/agent/transport"
	"github.com/jtsang4/sing-box-ui/internal/agentapi"
	"github.com/jtsang4/sing-box-ui/internal/agentclient"
)

type stack struct {
	t      *testing.T
	d      *daemon.Daemon
	client *agentclient.Client
	origin *httptest.Server
	socket string
}

// start boots a development agent on a Unix socket in a short temporary path
// (socket paths are limited to about a hundred bytes).
func start(t *testing.T) *stack {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("the development agent never runs as root")
	}
	dir, err := os.MkdirTemp("/tmp", "sbui")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	s := &stack{t: t, socket: filepath.Join(dir, "a.sock")}
	s.origin = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/big" {
			_, _ = w.Write([]byte(strings.Repeat("x", 512<<10)))
			return
		}
		_, _ = io.WriteString(w, "hello from the origin")
	}))
	t.Cleanup(s.origin.Close)

	reg := singbox.RegistryOptions{}
	s.d, err = daemon.Build(daemon.Config{
		DataDir: filepath.Join(dir, "data"),
		Driver:  sim.New(sim.Options{Sandbox: true}),
		Engine:  singbox.New(reg),
		Policy:  policy.New(singbox.PolicyOptions(reg)),
		Version: "e2e", AuthorizedUser: "tester", DevMode: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	l, verify, err := transport.ListenDev(s.socket)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.d.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- s.d.Serve(l, server.NewCredentials(verify)) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := s.d.Shutdown(ctx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
		select {
		case <-served:
		case <-time.After(5 * time.Second):
			t.Error("the server did not stop")
		}
	})

	s.client = agentclient.New(transport.DialDev(s.socket), agentclient.Options{Version: "e2e"})
	bg, cancel := context.WithCancel(context.Background())
	s.client.Start(bg)
	t.Cleanup(func() { cancel(); s.client.Close() })
	wctx, wcancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer wcancel()
	if err := s.client.WaitReady(wctx); err != nil {
		t.Fatalf("client: %v (%s)", err, s.client.Link().Describe())
	}
	return s
}

func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return c
}

func (s *stack) waitStatus(what string, pred func(*agentapi.Status) bool) *agentapi.Status {
	s.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if st, err := s.client.Status(ctx(s.t)); err == nil && pred(st) {
			return st
		}
		time.Sleep(20 * time.Millisecond)
	}
	s.t.Fatalf("timed out waiting for %s", what)
	return nil
}

func (s *stack) get(proxyPort uint32, path string) (string, error) {
	proxy, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", proxyPort))
	c := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}}
	resp, err := c.Get(s.origin.URL + path)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

func interfaces(t *testing.T) string {
	t.Helper()
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

// A GUI-style TUN configuration, as the generator will produce it.
const tunConfig = `{
  "inbounds": [{"type": "tun", "tag": "tun-in"}],
  "outbounds": [
    {"type": "direct", "tag": "direct"},
    {"type": "selector", "tag": "proxy", "outbounds": ["direct"], "default": "direct"}
  ],
  "route": {"rules": [{"clash_mode": "Direct", "outbound": "direct"}], "final": "proxy"}
}`

func TestTunConfigurationRunsAsALoopbackProxyAndLeavesTheHostAlone(t *testing.T) {
	s := start(t)
	before := interfaces(t)

	hello := s.client.Link().Hello
	if hello.Driver != "sim" || !hello.DevMode || !strings.HasPrefix(hello.Peer, "uid=") {
		t.Fatalf("hello = %v", hello)
	}

	if _, err := s.client.ApplyConfig(ctx(t), 0, &agentapi.ConfigBundle{ConfigJson: []byte(tunConfig), ProfileName: "e2e"}, agentapi.ApplyMode_APPLY_MODE_STORE_ONLY); err != nil {
		t.Fatal(err)
	}
	if _, err := s.client.Connect(ctx(t), agentclient.ConnectOptions{}); err != nil {
		t.Fatal(err)
	}
	st := s.waitStatus("connected", func(x *agentapi.Status) bool {
		return x.Connection == agentapi.ConnectionState_CONNECTION_STATE_CONNECTED
	})
	if st.SandboxPort == 0 || st.TunInterface != "" {
		t.Fatalf("in the sandbox a TUN runs as a loopback proxy and creates no interface: %v", st)
	}
	if st.CoreVersion == "" || st.CoreVersion == "unknown" || st.ActiveRevision != 1 {
		t.Fatalf("status = %v", st)
	}

	// Traffic really flows through the real core.
	if body, err := s.get(st.SandboxPort, "/hello"); err != nil || body != "hello from the origin" {
		t.Fatalf("GET through the sandbox proxy: %q %v", body, err)
	}
	if body, err := s.get(st.SandboxPort, "/big"); err != nil || len(body) != 512<<10 {
		t.Fatalf("big GET: %d bytes, %v", len(body), err)
	}

	bg, cancel := context.WithCancel(context.Background())
	defer cancel()
	traffic := make(chan *agentapi.Traffic, 32)
	go s.client.WatchTraffic(bg, 250*time.Millisecond, func(x *agentapi.Traffic) { traffic <- x })
	deadline := time.After(10 * time.Second)
	for seen := false; !seen; {
		select {
		case x := <-traffic:
			seen = x.Available && x.DownlinkTotal >= 512<<10
		case <-deadline:
			t.Fatal("the traffic stream never reported the downloaded bytes")
		}
	}

	// A held-open connection shows up in the connection list and can be closed.
	conns := make(chan *agentapi.ConnectionEvents, 64)
	go s.client.WatchConnections(bg, 250*time.Millisecond, func(x *agentapi.ConnectionEvents) { conns <- x })
	held, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", st.SandboxPort))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	_, _ = held.Write([]byte{5, 1, 0})
	reply := make([]byte, 2)
	if _, err := io.ReadFull(held, reply); err != nil {
		t.Fatal(err)
	}
	host, port, _ := net.SplitHostPort(s.origin.Listener.Addr().String())
	var p uint16
	fmt.Sscan(port, &p)
	req := append([]byte{5, 1, 0, 1}, net.ParseIP(host).To4()...)
	req = append(req, byte(p>>8), byte(p))
	_, _ = held.Write(req)
	if _, err := io.ReadFull(held, make([]byte, 10)); err != nil {
		t.Fatal(err)
	}
	var id string
	deadline = time.After(10 * time.Second)
	for id == "" {
		select {
		case e := <-conns:
			for _, ev := range e.Events {
				if ev.Type == agentapi.ConnectionEvent_TYPE_NEW && ev.Connection != nil && ev.Connection.Destination == s.origin.Listener.Addr().String() {
					id = ev.Id
				}
			}
		case <-deadline:
			t.Fatal("the held connection never appeared in the connection stream")
		}
	}
	if _, err := s.client.CloseConnection(ctx(t), id); err != nil {
		t.Fatal(err)
	}
	_ = held.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := held.Read(make([]byte, 1)); err == nil {
		t.Fatal("the connection should have been closed by the agent")
	}

	// Switching the mode and the selected node goes through.
	if _, err := s.client.SetMode(ctx(t), "Direct"); err != nil {
		t.Fatalf("set mode: %v", err)
	}
	if _, err := s.client.SelectOutbound(ctx(t), "proxy", "direct"); err != nil {
		t.Fatalf("select outbound: %v", err)
	}
	if _, err := s.client.SelectOutbound(ctx(t), "proxy", "nowhere"); agentclient.CodeOf(err) == agentapi.ErrorCode_ERROR_CODE_UNSPECIFIED {
		t.Fatalf("selecting an unknown outbound must be refused with a code, got %v", err)
	}

	// Disconnect: the proxy port is released and the host is as it was.
	if _, err := s.client.Disconnect(ctx(t)); err != nil {
		t.Fatal(err)
	}
	s.waitStatus("disconnected", func(x *agentapi.Status) bool {
		return x.Connection == agentapi.ConnectionState_CONNECTION_STATE_DISCONNECTED
	})
	if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", st.SandboxPort), time.Second); err == nil {
		c.Close()
		t.Fatal("the sandbox proxy port is still open after a disconnect")
	}
	if after := interfaces(t); after != before {
		t.Fatalf("the host's interfaces changed:\n%s\n---\n%s", before, after)
	}
}

func TestHotApplySwitchesTheRunningConfiguration(t *testing.T) {
	s := start(t)
	if _, err := s.client.ApplyConfig(ctx(t), 0, &agentapi.ConfigBundle{ConfigJson: []byte(tunConfig)}, agentapi.ApplyMode_APPLY_MODE_STORE_ONLY); err != nil {
		t.Fatal(err)
	}
	if _, err := s.client.Connect(ctx(t), agentclient.ConnectOptions{}); err != nil {
		t.Fatal(err)
	}
	s.waitStatus("connected", func(x *agentapi.Status) bool {
		return x.Connection == agentapi.ConnectionState_CONNECTION_STATE_CONNECTED
	})

	// A broken revision is rolled back; the first one keeps running.
	broken := strings.Replace(tunConfig, `"final": "proxy"`, `"final": "ghost"`, 1)
	_, err := s.client.ApplyConfig(ctx(t), 1, &agentapi.ConfigBundle{ConfigJson: []byte(broken)}, agentapi.ApplyMode_APPLY_MODE_AUTO)
	if err == nil {
		t.Fatal("a configuration pointing at a missing outbound must be refused")
	}
	st := s.waitStatus("still connected on revision 1", func(x *agentapi.Status) bool {
		return x.Connection == agentapi.ConnectionState_CONNECTION_STATE_CONNECTED && x.ActiveRevision == 1
	})
	if body, err := s.get(st.SandboxPort, "/hello"); err != nil || body != "hello from the origin" {
		t.Fatalf("the running configuration must keep serving: %q %v", body, err)
	}

	// A good one replaces it.
	good := strings.Replace(tunConfig, `"final": "proxy"`, `"final": "direct"`, 1)
	if _, err := s.client.ApplyConfig(ctx(t), 1, &agentapi.ConfigBundle{ConfigJson: []byte(good)}, agentapi.ApplyMode_APPLY_MODE_AUTO); err != nil {
		t.Fatal(err)
	}
	st = s.waitStatus("revision 2 active", func(x *agentapi.Status) bool {
		return x.Connection == agentapi.ConnectionState_CONNECTION_STATE_CONNECTED && x.ActiveRevision == 2
	})
	if body, err := s.get(st.SandboxPort, "/hello"); err != nil || body != "hello from the origin" {
		t.Fatalf("after the hot apply: %q %v", body, err)
	}
}

func TestStateSurvivesAnAgentRestart(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "sbui")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "a.sock")
	reg := singbox.RegistryOptions{}

	boot := func() (*daemon.Daemon, *agentclient.Client, func()) {
		d, err := daemon.Build(daemon.Config{
			DataDir: filepath.Join(dir, "data"), Driver: sim.New(sim.Options{Sandbox: true}),
			Engine: singbox.New(reg), Policy: policy.New(singbox.PolicyOptions(reg)),
			Version: "e2e", DevMode: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		l, verify, err := transport.ListenDev(socket)
		if err != nil {
			t.Fatal(err)
		}
		if err := d.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		go func() { _ = d.Serve(l, server.NewCredentials(verify)) }()
		c := agentclient.New(transport.DialDev(socket), agentclient.Options{Backoff: func(int) time.Duration { return 20 * time.Millisecond }})
		bg, cancel := context.WithCancel(context.Background())
		c.Start(bg)
		wctx, wcancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer wcancel()
		if err := c.WaitReady(wctx); err != nil {
			t.Fatal(err)
		}
		return d, c, func() {
			sctx, scancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer scancel()
			_ = d.Shutdown(sctx)
			cancel()
			c.Close()
		}
	}

	d1, c1, stop1 := boot()
	_ = d1
	if _, err := c1.ApplyConfig(ctx(t), 0, &agentapi.ConfigBundle{ConfigJson: []byte(tunConfig), ProfileName: "kept"}, agentapi.ApplyMode_APPLY_MODE_STORE_ONLY); err != nil {
		t.Fatal(err)
	}
	on := true
	if _, err := c1.UpdateSettings(ctx(t), &agentapi.UpdateSettingsRequest{AutoConnect: &on}); err != nil {
		t.Fatal(err)
	}
	first := c1.Link().Hello.InstanceId
	stop1()

	_, c2, stop2 := boot()
	defer stop2()
	if got := c2.Link().Hello.InstanceId; got == first {
		t.Fatal("a restarted agent must report a new instance id")
	}
	st, err := c2.Status(ctx(t))
	if err != nil || !st.HasConfig || st.StoredRevision != 1 {
		t.Fatalf("the stored configuration must survive a restart: %v %v", st, err)
	}
	s, err := c2.Settings(ctx(t))
	if err != nil || !s.AutoConnect {
		t.Fatalf("settings must survive a restart: %v %v", s, err)
	}
}
