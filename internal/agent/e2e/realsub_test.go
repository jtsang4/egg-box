//go:build sbui_dev && (darwin || linux)

package e2e

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jtsang4/sing-box-ui/internal/agent/transport"
	"github.com/jtsang4/sing-box-ui/internal/app"
	"github.com/jtsang4/sing-box-ui/internal/boot"
	"github.com/jtsang4/sing-box-ui/internal/configgen"
	"github.com/jtsang4/sing-box-ui/internal/library"
	"github.com/jtsang4/sing-box-ui/internal/testsupport/realsub"
)

// probeURL answers 204 to anyone, from anywhere; reaching it through a node
// proves the node carries a TLS connection to the Internet.
const probeURL = "https://cp.cloudflare.com/generate_204"

// A node of the real subscription, as far as a log may say.
type probed struct {
	index int
	tag   string
	label string // protocol/transport: nothing that identifies the server
}

// TestRealSubscriptionEveryNodeWorksInTheSandbox runs the whole product (the
// application's service, the agent, the real sing-box core) against the
// developer's real subscription, and sends one real request through every
// node.
//
// It cannot touch the host's network: the agent runs with the simulated
// platform, so the TUN is a proxy on a loopback port; the core's only contact
// with the outside is its own connections to the nodes, and the test's request
// goes to that loopback port. Nothing the subscription holds is printed: the
// log has counts, protocol names and failure classes.
//
//	SBUI_TEST_SUBSCRIPTION_FILE=~/.sbui-dev/subscription.url \
//	  go test -tags "sbui_dev,with_gvisor,with_quic,with_dhcp,with_wireguard,with_utls,with_clash_api,badlinkname" \
//	  -run RealSubscription -v ./internal/agent/e2e
func TestRealSubscriptionEveryNodeWorksInTheSandbox(t *testing.T) {
	link := realsub.URL(t)
	scrub := func(err error) string { return realsub.Scrub(link, err.Error()) }

	s := start(t)
	interfacesBefore := interfaces(t)

	rt, err := boot.Start(ctx(t), boot.Config{
		Mode:    boot.Dev,
		DataDir: t.TempDir(),
		Secrets: library.NewMemorySecrets(),
		Dial:    transport.DialDev(s.socket),
		Version: "e2e",
	})
	if err != nil {
		t.Fatalf("assemble the application: %v", err)
	}
	t.Cleanup(rt.Close)
	svc := rt.Service

	until(t, "the service answers", 20*time.Second, func() bool { return svc.Snapshot().Phase == app.PhaseDisconnected })

	// 1. The subscription: downloaded and parsed the way the application does.
	_, res, err := svc.AddSubscription(ctx(t), "e2e", link, library.RemoteOptions{})
	if err != nil {
		t.Fatalf("add the subscription: %s", scrub(err))
	}
	if res.NodeCount == 0 {
		t.Fatal("the subscription gave no nodes")
	}
	t.Logf("subscription: %d nodes, %d entries skipped", res.NodeCount, len(res.Skipped))

	// 2. Connect, in the sandbox, with everything going to the proxy group so
	//    that the rules (and their downloads) do not matter here.
	if err := svc.Connect(ctx(t), app.ConnectOptions{}); err != nil {
		t.Fatalf("connect: %s", scrub(err))
	}
	until(t, "connected", 60*time.Second, func() bool { return svc.Snapshot().Phase == app.PhaseConnected })
	st := svc.Snapshot().Status
	if st.GetSandboxPort() == 0 || st.GetTunInterface() != "" {
		t.Fatalf("the sandbox must run the TUN as a loopback proxy: port=%d interface=%q", st.GetSandboxPort(), st.GetTunInterface())
	}
	port := st.GetSandboxPort()
	t.Logf("connected: core %s, driver %s, %d nodes in the configuration, %d left out",
		st.GetCoreVersion(), svc.Snapshot().Link.Agent.Driver, svc.Snapshot().Applied.Nodes, len(svc.Snapshot().Applied.Dropped))
	for _, d := range svc.Snapshot().Applied.Dropped {
		t.Errorf("a node was left out of the configuration: %s", d.Reason)
	}
	if err := svc.SetMode(ctx(t), "Global"); err != nil {
		t.Fatalf("global mode: %s", scrub(err))
	}
	svc.WatchConnections(true)
	t.Cleanup(func() { svc.WatchConnections(false) })

	// 3. The nodes, as the live groups list them.
	nodes := liveNodes(svc.Snapshot())
	kinds := map[string]int{}
	for _, n := range nodes {
		kinds[n.label]++
	}
	t.Logf("protocols: %v", kinds)
	if len(nodes) != res.NodeCount {
		t.Errorf("the groups list %d nodes, the subscription gave %d", len(nodes), res.NodeCount)
	}

	// 4. The core's own delay test of the whole group.
	started := time.Now()
	if err := svc.TestLatency(ctx(t), configgen.TagProxy); err != nil {
		t.Fatalf("latency test: %s", scrub(err))
	}
	until(t, "every node was measured", 90*time.Second, func() bool {
		for _, n := range liveNodes(svc.Snapshot()) {
			if !n.tested.After(started) {
				return false
			}
		}
		return true
	})
	var slow, unreachable []string
	reachable := 0
	for _, n := range liveNodes(svc.Snapshot()) {
		switch {
		case n.delay > 0:
			reachable++
			if n.delay > 3000 {
				slow = append(slow, fmt.Sprintf("#%d %s %dms", n.index, n.label, n.delay))
			}
		default:
			unreachable = append(unreachable, fmt.Sprintf("#%d %s", n.index, n.label))
		}
	}
	t.Logf("delay test: %d of %d nodes answered; slow: %v; no answer: %v", reachable, len(nodes), slow, unreachable)

	// 5. One real request through each node, chosen one by one.
	var failed []string
	var took []string
	verified := 0
	for _, n := range nodes {
		var lastClass string
		ok := false
		for attempt := 1; attempt <= 2 && !ok; attempt++ {
			if err := svc.Select(ctx(t), n.tag); err != nil {
				lastClass = "select: " + realsub.Class(err)
				continue
			}
			until(t, "the node is selected", 10*time.Second, func() bool { return svc.SelectedTag() == n.tag })
			at := time.Now()
			code, err := viaSandbox(port, probeURL)
			elapsed := time.Since(at)
			switch {
			case err != nil:
				lastClass = realsub.Class(err)
			case code != http.StatusNoContent:
				lastClass = fmt.Sprintf("http %d", code)
			default:
				ok = true
				took = append(took, fmt.Sprintf("#%d %dms", n.index, elapsed.Milliseconds()))
				if chainShows(svc, n.tag, at) {
					verified++
				}
			}
		}
		if !ok {
			failed = append(failed, fmt.Sprintf("#%d %s: %s", n.index, n.label, lastClass))
		}
	}
	t.Logf("requests: %d of %d nodes carried a TLS request to the Internet (%d confirmed in the connection list); time to the answer: %v",
		len(nodes)-len(failed), len(nodes), verified, took)
	for _, f := range failed {
		t.Errorf("no request got through: %s", f)
	}
	if verified == 0 {
		t.Error("no request showed up in the connection list with its node in the chain")
	}

	// The delay test again, now that every path is warm.
	warm := time.Now()
	if err := svc.TestLatency(ctx(t), configgen.TagProxy); err != nil {
		t.Fatalf("second latency test: %s", scrub(err))
	}
	until(t, "every node was measured again", 90*time.Second, func() bool {
		for _, n := range liveNodes(svc.Snapshot()) {
			if !n.tested.After(warm) {
				return false
			}
		}
		return true
	})
	var delays []string
	for _, n := range liveNodes(svc.Snapshot()) {
		delays = append(delays, fmt.Sprintf("#%d %dms", n.index, n.delay))
	}
	t.Logf("delay test, second round: %v", delays)

	// 6. Disconnect: the sandbox port closes and the host is as it was.
	if err := svc.Disconnect(ctx(t)); err != nil {
		t.Fatalf("disconnect: %s", scrub(err))
	}
	until(t, "disconnected", 30*time.Second, func() bool { return svc.Snapshot().Phase == app.PhaseDisconnected })
	if _, err := viaSandbox(port, probeURL); err == nil {
		t.Error("the sandbox proxy still answers after a disconnect")
	}
	if after := interfaces(t); after != interfacesBefore {
		t.Errorf("the host's interfaces changed:\n%s\n---\n%s", interfacesBefore, after)
	}
}

// node is a node as the live groups show it.
type node struct {
	probed
	delay  int
	tested time.Time
}

// liveNodes lists the nodes of the live groups once each, in the order the
// groups give them.
func liveNodes(snap *app.Snapshot) []node {
	var out []node
	seen := map[string]bool{}
	for _, g := range snap.Groups {
		for _, it := range g.Items {
			if it.Node == nil || seen[it.Tag] {
				continue
			}
			seen[it.Tag] = true
			out = append(out, node{
				probed: probed{index: len(out) + 1, tag: it.Tag, label: strings.ToLower(it.Node.Protocol + "/" + it.Node.Transport)},
				delay:  it.DelayMS, tested: it.Tested,
			})
		}
	}
	return out
}

// viaSandbox fetches a URL through the sandbox's loopback proxy.
func viaSandbox(port uint32, target string) (int, error) {
	proxy, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	c := &http.Client{
		Timeout:   25 * time.Second,
		Transport: &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true},
		// A redirect would be somebody else's page; the probe never has one.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := c.Get(target)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

// chainShows reports whether the connection list has the probe's connection,
// opened since at, with the node in its chain: proof that the request went
// through the node and not around it.
func chainShows(svc *app.Service, tag string, at time.Time) bool {
	host, _, _ := strings.Cut(strings.TrimPrefix(probeURL, "https://"), "/")
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		for _, c := range svc.Snapshot().Conns {
			if c.Host == host && !c.Created.Before(at.Add(-time.Second)) && slices.Contains(c.Chain, tag) {
				return true
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false
}

// until polls a condition and fails the test, without printing anything the
// condition looked at, when it does not hold in time.
func until(t *testing.T, what string, within time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting: %s", what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
