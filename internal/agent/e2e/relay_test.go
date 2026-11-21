//go:build eggbox_dev && ((darwin && !ios) || (linux && !android))

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"eggbox/internal/agent/engine"
	"eggbox/internal/agent/engine/singbox"
	"eggbox/internal/agent/transport"
	"eggbox/internal/app"
	"eggbox/internal/boot"
	"eggbox/internal/configgen"
	"eggbox/internal/library"
	"eggbox/internal/routing"
)

// countingProxy sits in front of a loopback server and counts accepted TCP
// connections so tests can prove traffic really traversed the first hop.
type countingProxy struct {
	ln     net.Listener
	target string
	hits   atomic.Int64
	port   int
}

func newCountingProxy(t *testing.T, targetPort int) *countingProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cp := &countingProxy{
		ln:     ln,
		target: fmt.Sprintf("127.0.0.1:%d", targetPort),
		port:   ln.Addr().(*net.TCPAddr).Port,
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			cp.hits.Add(1)
			go func(in net.Conn) {
				defer in.Close()
				out, err := net.DialTimeout("tcp", cp.target, 5*time.Second)
				if err != nil {
					return
				}
				defer out.Close()
				done := make(chan struct{}, 2)
				go func() { _, _ = io.Copy(out, in); done <- struct{}{} }()
				go func() { _, _ = io.Copy(in, out); done <- struct{}{} }()
				<-done
			}(c)
		}
	}()
	return cp
}

// relaySSNode is a Shadowsocks server that forwards connections straight to
// the target requested (so it can act as a real first-hop relay to a second
// Shadowsocks node), while answering /generate_204 probes via its exit server.
type relaySSNode struct {
	t       *testing.T
	name    string
	port    int
	exit    *httptest.Server
	eng     *singbox.Engine
	running bool
	dir     string
}

func newRelaySSNode(t *testing.T, name string, probeHost string) *relaySSNode {
	t.Helper()
	n := &relaySSNode{t: t, name: name, port: free(t), exit: newExit(t, name), dir: t.TempDir()}
	n.start(probeHost)
	t.Cleanup(func() {
		if n.running {
			n.stop()
		}
	})
	return n
}

func (n *relaySSNode) start(probeHost string) {
	n.t.Helper()
	n.eng = singbox.New(singbox.RegistryOptions{ServerInbounds: true})
	cfg := fmt.Sprintf(`{
	  "log": {"level": "warn"},
	  "inbounds": [{"type": "shadowsocks", "tag": "ss-in", "listen": "127.0.0.1", "listen_port": %d,
	                "method": "aes-128-gcm", "password": "e2e-password"}],
	  "outbounds": [{"type": "direct", "tag": "direct"}],
	  "route": {
	    "rules": [
	      {"domain": [%q], "action": "route", "outbound": "direct", "override_address": "127.0.0.1", "override_port": %d}
	    ],
	    "final": "direct"
	  },
	  "experimental": {"cache_file": {"enabled": true, "path": %q}}
	}`, n.port, probeHost, portOf(n.t, n.exit), filepath.Join(n.dir, "cache.db"))
	c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := n.eng.Start(c, engine.Prepared{Revision: 1, JSON: []byte(cfg)}); err != nil {
		n.t.Fatalf("relay node %s: %v", n.name, err)
	}
	n.running = true
}

func (n *relaySSNode) stop() {
	n.t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := n.eng.Stop(c); err != nil {
		n.t.Errorf("stop relay node %s: %v", n.name, err)
	}
	n.running = false
}

func TestChainedProxyEndToEnd(t *testing.T) {
	const probeHost = "latency.e2e.example"
	st := start(t)
	before := interfaces(t)
	t.Cleanup(func() {
		if after := interfaces(t); after != before {
			t.Errorf("the host's interfaces changed")
		}
	})

	dns := newNameServer(t)
	hk1 := newRelaySSNode(t, "Hong Kong 01", probeHost)
	hk2 := newRelaySSNode(t, "Hong Kong 02", probeHost)
	hk1Proxy := newCountingProxy(t, hk1.port)
	hk2Proxy := newCountingProxy(t, hk2.port)

	landing := newSSNode(t, "US Home 01", dns.port())
	landingProxy := newCountingProxy(t, landing.port)

	// Build a sing-box subscription that includes:
	// - Hong Kong 01 & Hong Kong 02 (plain first-hop relays)
	// - US Home 01 (plain node that the user configures to relay)
	// - US Builtin Relay (provider-declared 2-hop relay through Hong Kong 01)
	profileBytes, err := json.Marshal(map[string]any{
		"outbounds": []map[string]any{
			{"type": "shadowsocks", "tag": "Hong Kong 01", "server": "127.0.0.1", "server_port": hk1Proxy.port, "method": "aes-128-gcm", "password": "e2e-password"},
			{"type": "shadowsocks", "tag": "Hong Kong 02", "server": "127.0.0.1", "server_port": hk2Proxy.port, "method": "aes-128-gcm", "password": "e2e-password"},
			{"type": "shadowsocks", "tag": "US Home 01", "server": "127.0.0.1", "server_port": landingProxy.port, "method": "aes-128-gcm", "password": "e2e-password"},
			{"type": "shadowsocks", "tag": "US Builtin", "server": "127.0.0.1", "server_port": landing.port, "method": "aes-128-gcm", "password": "e2e-password", "detour": "Hong Kong 01"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := &mirror{t: t, sub: string(profileBytes), token: "relay-tok"}
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	to, _ := url.Parse(srv.URL)
	httpc := &http.Client{Timeout: 20 * time.Second, Transport: toMirror{to: to, base: http.DefaultTransport}}

	rt, err := boot.Start(ctx(t), boot.Config{
		Mode:       boot.Dev,
		DataDir:    t.TempDir(),
		Secrets:    library.NewMemorySecrets(),
		Dial:       transport.DialDev(st.socket),
		HTTPClient: httpc,
		Version:    "e2e",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rt.Close)
	svc := rt.Service
	until(t, "service ready", 20*time.Second, func() bool { return svc.Snapshot().Phase == app.PhaseDisconnected })

	prefs := configgen.DefaultPrefs()
	prefs.DomesticDNS = fmt.Sprintf("udp://127.0.0.1:%d", dns.port())
	prefs.RemoteDNS = fmt.Sprintf("udp://127.0.0.1:%d", dns.port())
	prefs.TestURL = "http://" + probeHost + "/generate_204"
	if _, err := svc.SavePrefs(ctx(t), prefs); err != nil {
		t.Fatal(err)
	}
	sub, _, err := svc.AddSubscription(ctx(t), "RelayTest", srv.URL+"/sub/"+m.token, library.RemoteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if sub.NodeCount != 4 {
		t.Fatalf("NodeCount = %d, want 4", sub.NodeCount)
	}

	active, err := svc.Library().ActiveNodes()
	if err != nil {
		t.Fatal(err)
	}
	keyByName := map[string]string{}
	idByName := map[string]routing.NodeID{}
	for _, a := range active {
		keyByName[a.Node.Name] = a.Key()
		idByName[a.Node.Name] = a.ID()
	}

	// Configure US Home 01 to relay via Hong Kong 01, and set main exit to US Home 01.
	if err := svc.EditRouting(ctx(t), func(rs *routing.State) error {
		r := routing.RelayNodeVal(idByName["Hong Kong 01"])
		if err := rs.SetNodeRelay(idByName["US Home 01"], &r); err != nil {
			return err
		}
		rs.SetNodeAuto(idByName["US Home 01"], false)
		return rs.SetMain(routing.ToNode(idByName["US Home 01"]))
	}); err != nil {
		t.Fatal(err)
	}

	until(t, "plan rule files ready", 30*time.Second, func() bool {
		p := svc.Snapshot().Plan
		return p.Ready && p.Missing == 0 && !p.Updating
	})
	if err := svc.Connect(ctx(t), app.ConnectOptions{}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	until(t, "connected", 30*time.Second, func() bool { return svc.Snapshot().Phase == app.PhaseConnected })

	get := func() (string, error) {
		proxy, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", svc.Snapshot().Status.GetSandboxPort()))
		c := &http.Client{Timeout: 8 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}}
		resp, err := c.Get("http://203.0.113.200:80/page")
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", err
		}
		if resp.StatusCode != http.StatusOK {
			return string(b), fmt.Errorf("http %d", resp.StatusCode)
		}
		return string(b), nil
	}

	// 1. Traffic reaches US Home 01 through Hong Kong 01.
	beforeHK1 := hk1Proxy.hits.Load()
	body, err := get()
	if err != nil || body != "exit-US Home 01" {
		t.Fatalf("chained request: body=%q err=%v", body, err)
	}
	if hk1Proxy.hits.Load() <= beforeHK1 {
		t.Fatalf("expected Hong Kong 01 proxy to carry the first hop (hits=%d)", hk1Proxy.hits.Load())
	}
	until(t, "Snapshot.Relays shows Hong Kong 01", 10*time.Second, func() bool {
		ri, ok := svc.Snapshot().Relays[keyByName["US Home 01"]]
		return ok && ri.Node != nil && ri.Node.Name == "Hong Kong 01" && !ri.StandIn && !ri.Unavailable
	})

	// 2. Provider-declared 2-hop node ("US Builtin") also traverses Hong Kong 01!
	if err := svc.SetMainExit(ctx(t), routing.ToNode(idByName["US Builtin"])); err != nil {
		t.Fatal(err)
	}
	beforeHK1 = hk1Proxy.hits.Load()
	until(t, "US Builtin answers through HK 01", 15*time.Second, func() bool {
		b, e := get()
		return e == nil && b == "exit-US Home 01" && hk1Proxy.hits.Load() > beforeHK1
	})

	// 3. Switch US Home 01 to Region Auto (HK) and make sure it works through HK.
	if err := svc.EditRouting(ctx(t), func(rs *routing.State) error {
		r := routing.RelayRegionVal("HK")
		if err := rs.SetNodeRelay(idByName["US Home 01"], &r); err != nil {
			return err
		}
		return rs.SetMain(routing.ToNode(idByName["US Home 01"]))
	}); err != nil {
		t.Fatal(err)
	}
	beforeBoth := hk1Proxy.hits.Load() + hk2Proxy.hits.Load()
	until(t, "region relay answers", 20*time.Second, func() bool {
		b, e := get()
		return e == nil && b == "exit-US Home 01" && (hk1Proxy.hits.Load()+hk2Proxy.hits.Load()) > beforeBoth
	})

	// 4. Switch US Home 01 to an unavailable region ("SG" has no plain nodes):
	// traffic MUST FAIL and MUST NEVER fall back to dialling US Home 01 directly!
	if err := svc.EditRouting(ctx(t), func(rs *routing.State) error {
		r := routing.RelayRegionVal("SG")
		return rs.SetNodeRelay(idByName["US Home 01"], &r)
	}); err != nil {
		t.Fatal(err)
	}
	until(t, "relay marked unavailable", 15*time.Second, func() bool {
		ri, ok := svc.Snapshot().Relays[keyByName["US Home 01"]]
		return ok && ri.Unavailable
	})
	beforeLanding := landingProxy.hits.Load()
	if b, e := get(); e == nil {
		t.Fatalf("unavailable relay must NOT fall back to direct, got body=%q", b)
	}
	if landingProxy.hits.Load() != beforeLanding {
		t.Fatalf("landing node was dialled directly while relay was unavailable (hits %d -> %d)", beforeLanding, landingProxy.hits.Load())
	}

	// 5. Pinned relay failover: pin US Home 01 to Hong Kong 01, stop Hong Kong 01 ->
	// relay-pin-1 stands in with Hong Kong 02 automatically!
	if err := svc.EditRouting(ctx(t), func(rs *routing.State) error {
		r := routing.RelayNodeVal(idByName["Hong Kong 01"])
		return rs.SetNodeRelay(idByName["US Home 01"], &r)
	}); err != nil {
		t.Fatal(err)
	}
	until(t, "pinned relay works before stop", 20*time.Second, func() bool {
		b, e := get()
		ri := svc.Snapshot().Relays[keyByName["US Home 01"]]
		return e == nil && b == "exit-US Home 01" && ri.Node != nil && ri.Node.Name == "Hong Kong 01"
	})
	// Stop Hong Kong 01 and its proxy listener so connections fail immediately.
	hk1.stop()
	_ = hk1Proxy.ln.Close()
	beforeHK2 := hk2Proxy.hits.Load()
	until(t, "relay stands in with Hong Kong 02", 25*time.Second, func() bool {
		b, e := get()
		ri := svc.Snapshot().Relays[keyByName["US Home 01"]]
		return e == nil && b == "exit-US Home 01" && hk2Proxy.hits.Load() > beforeHK2 && ri.StandIn && ri.Node != nil && ri.Node.Name == "Hong Kong 02"
	})
}
