package singbox_test

// The preferred group runs inside the real core against loopback upstreams: a
// node that stops answering is replaced by the fastest node of its region and
// comes back by itself, with no help from the application.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"eggbox/internal/agent/engine"
	"eggbox/internal/agent/engine/singbox"
	"eggbox/internal/agent/policy"
	"eggbox/internal/agentapi"
)

// upstreamNode is a Shadowsocks server on a fixed loopback port that can be
// stopped and started again.
type upstreamNode struct {
	t    *testing.T
	port int
	eng  *singbox.Engine
	dir  string
}

func newUpstream(t *testing.T) *upstreamNode {
	t.Helper()
	u := &upstreamNode{t: t, port: freePort(t), dir: t.TempDir()}
	u.start()
	t.Cleanup(func() {
		if u.eng != nil {
			stop(t, u.eng)
		}
	})
	return u
}

func (u *upstreamNode) start() {
	u.t.Helper()
	u.eng = singbox.New(singbox.RegistryOptions{ServerInbounds: true})
	cfg := fmt.Sprintf(`{
	  "log": {"level": "warn"},
	  "inbounds": [{"type": "shadowsocks", "tag": "ss-in", "listen": "127.0.0.1", "listen_port": %d,
	                "method": "aes-128-gcm", "password": "test-password"}],
	  "outbounds": [{"type": "direct", "tag": "direct"}],
	  "experimental": {"cache_file": {"enabled": true, "path": %q}}
	}`, u.port, filepath.Join(u.dir, "cache.db"))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := u.eng.Start(ctx, engine.Prepared{Revision: 1, JSON: []byte(cfg)}); err != nil {
		u.t.Fatalf("upstream on %d: %v", u.port, err)
	}
}

func (u *upstreamNode) stop() {
	u.t.Helper()
	stop(u.t, u.eng)
	u.eng = nil
}

func eventually(t *testing.T, within time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for: %s", within, what)
}

func groupOf(t *testing.T, e *singbox.Engine, tag string) *agentapi.Group {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, err := e.WatchGroups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case gs, ok := <-ch:
		if !ok {
			t.Fatal("group stream closed")
		}
		for _, g := range gs.Groups {
			if g.Tag == tag {
				return g
			}
		}
	case <-ctx.Done():
		t.Fatal("no group snapshot")
	}
	return nil
}

func TestPreferredGroupFailsOverAndRecovers(t *testing.T) {
	dir := t.TempDir()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/generate_204" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = w.Write([]byte("hello from origin"))
	}))
	defer origin.Close()
	ou, _ := url.Parse(origin.URL)

	a, b := newUpstream(t), newUpstream(t)
	clientPort := freePort(t)
	link := origin.URL + "/generate_204"
	cfg := fmt.Sprintf(`{
	  "inbounds": [{"type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": %d}],
	  "outbounds": [
	    {"type": "shadowsocks", "tag": "node-a", "server": "127.0.0.1", "server_port": %d, "method": "aes-128-gcm", "password": "test-password"},
	    {"type": "shadowsocks", "tag": "node-b", "server": "127.0.0.1", "server_port": %d, "method": "aes-128-gcm", "password": "test-password"},
	    {"type": "direct", "tag": "direct"},
	    {"type": "urltest", "tag": "region-x", "outbounds": ["node-a", "node-b"], "url": %q, "interval": "10m", "tolerance": 5000},
	    {"type": "preferred", "tag": "pick", "outbounds": ["region-x", "node-a", "node-b", "direct"], "default": "node-a",
	     "fallbacks": {"node-a": "region-x", "node-b": "region-x"}, "url": %q, "interval": "1s"}
	  ],
	  "route": {"final": "pick", "rules": [{"clash_mode": "Direct", "outbound": "direct"}]}
	}`, clientPort, a.port, b.port, link, link)

	pol := policy.New(singbox.PolicyOptions(singbox.RegistryOptions{}))
	res := pol.Normalize([]byte(cfg), policy.Env{ResourceDir: dir, Resources: map[string]bool{}, CachePath: filepath.Join(dir, "cache.db"), LogLevel: "info"})
	if res.HasErrors() {
		t.Fatalf("the policy rejected a preferred group: %v", res.Errors())
	}
	client := singbox.New(singbox.RegistryOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := client.Start(ctx, engine.Prepared{Revision: 1, JSON: res.JSON}); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer stop(t, client)
	_ = ou

	proxy, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", clientPort))
	get := func() error {
		c := &http.Client{Timeout: 4 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}}
		resp, err := c.Get(origin.URL + "/x")
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			return fmt.Errorf("status %d", resp.StatusCode)
		}
		return nil
	}
	selected := func() string { return groupOf(t, client, "pick").Selected }

	// A preferred group is something the interface can choose from.
	if g := groupOf(t, client, "pick"); !g.Selectable || g.Type != "preferred" || len(g.Items) != 4 {
		t.Fatalf("group = %+v", g)
	}
	eventually(t, 10*time.Second, "traffic through the pinned node", func() bool { return get() == nil && selected() == "node-a" })

	// The pinned node stops answering: after at most one failed connection
	// the traffic goes to the region group's pick, which is the other node.
	a.stop()
	failed := 0
	eventually(t, 15*time.Second, "traffic through the stand-in", func() bool {
		if err := get(); err != nil {
			failed++
			return false
		}
		return selected() == "region-x"
	})
	if failed > 3 {
		t.Errorf("%d connections failed before the stand-in took over; the first failure should be enough", failed)
	}
	if got := groupOf(t, client, "region-x").Selected; got != "node-b" {
		t.Errorf("the stand-in group picked %q, want node-b", got)
	}

	// The node comes back: the group notices on its own while traffic flows.
	a.start()
	eventually(t, 20*time.Second, "the pinned node to be used again", func() bool {
		_ = get()
		return selected() == "node-a"
	})
	if err := get(); err != nil {
		t.Errorf("after recovery: %v", err)
	}

	// Choosing another member works like in a selector, also for a group the
	// daemon would not call selectable.
	if err := client.SelectOutbound("pick", "node-b"); err != nil {
		t.Fatalf("SelectOutbound: %v", err)
	}
	eventually(t, 5*time.Second, "the new choice", func() bool { return selected() == "node-b" })
	if err := client.SelectOutbound("pick", "nope"); err == nil {
		t.Error("an unknown member must be refused")
	}
	if err := client.SelectOutbound("pick", "direct"); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "direct", func() bool { return selected() == "direct" && get() == nil })
}

func TestPreferredGroupWithoutStandInIsAPlainSelector(t *testing.T) {
	dir := t.TempDir()
	a := newUpstream(t)
	clientPort := freePort(t)
	cfg := fmt.Sprintf(`{
	  "inbounds": [{"type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": %d}],
	  "outbounds": [
	    {"type": "shadowsocks", "tag": "node-a", "server": "127.0.0.1", "server_port": %d, "method": "aes-128-gcm", "password": "test-password"},
	    {"type": "direct", "tag": "direct"},
	    {"type": "preferred", "tag": "pick", "outbounds": ["node-a", "direct"], "default": "node-a"}
	  ],
	  "route": {"final": "pick"}
	}`, clientPort, a.port)
	pol := policy.New(singbox.PolicyOptions(singbox.RegistryOptions{}))
	res := pol.Normalize([]byte(cfg), policy.Env{ResourceDir: dir, Resources: map[string]bool{}, CachePath: filepath.Join(dir, "cache.db"), LogLevel: "info"})
	if res.HasErrors() {
		t.Fatal(res.Errors())
	}
	client := singbox.New(singbox.RegistryOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := client.Start(ctx, engine.Prepared{Revision: 1, JSON: res.JSON}); err != nil {
		t.Fatal(err)
	}
	defer stop(t, client)
	if got := groupOf(t, client, "pick").Selected; got != "node-a" {
		t.Errorf("selected %q", got)
	}
	// With nothing to fall back on, a dead node stays selected: the group
	// never makes a choice the person did not.
	a.stop()
	time.Sleep(2 * time.Second)
	if got := groupOf(t, client, "pick").Selected; got != "node-a" {
		t.Errorf("selected %q after the node went away", got)
	}
}

func TestPolicyRejectsBrokenPreferredGroups(t *testing.T) {
	pol := policy.New(singbox.PolicyOptions(singbox.RegistryOptions{}))
	env := policy.Env{ResourceDir: t.TempDir(), Resources: map[string]bool{}, CachePath: filepath.Join(t.TempDir(), "c.db"), LogLevel: "info"}
	base := func(group string) string {
		return `{"outbounds":[{"type":"direct","tag":"a"},{"type":"direct","tag":"b"},` + group + `]}`
	}
	for name, group := range map[string]string{
		"unknown member":                `{"type":"preferred","tag":"p","outbounds":["a","zzz"]}`,
		"unknown default":               `{"type":"preferred","tag":"p","outbounds":["a","b"],"default":"zzz"}`,
		"stand-in for a non-member":     `{"type":"preferred","tag":"p","outbounds":["a","b"],"fallbacks":{"zzz":"b"}}`,
		"stand-in that is not a member": `{"type":"preferred","tag":"p","outbounds":["a","b"],"fallbacks":{"a":"zzz"}}`,
	} {
		if res := pol.Normalize([]byte(base(group)), env); !res.HasErrors() {
			t.Errorf("%s: accepted", name)
		}
	}
	ok := base(`{"type":"preferred","tag":"p","outbounds":["a","b"],"default":"a","fallbacks":{"a":"b"},"interval":"30s"}`)
	if res := pol.Normalize([]byte(ok), env); res.HasErrors() {
		t.Errorf("a valid preferred group was refused: %v", res.Errors())
	}
}

func TestPreferredGroupRemembersTheChoice(t *testing.T) {
	dir := t.TempDir()
	cfg := func(members string) string {
		return fmt.Sprintf(`{
		  "inbounds": [{"type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": %d}],
		  "outbounds": [
		    {"type": "direct", "tag": "direct"},
		    {"type": "block", "tag": "block"},
		    {"type": "direct", "tag": "other"},
		    {"type": "preferred", "tag": "pick", "outbounds": [%s], "default": "direct"}
		  ],
		  "route": {"final": "pick"}
		}`, freePort(t), members)
	}
	pol := policy.New(singbox.PolicyOptions(singbox.RegistryOptions{}))
	env := policy.Env{ResourceDir: dir, Resources: map[string]bool{}, CachePath: filepath.Join(dir, "cache.db"), LogLevel: "info"}
	run := func(members string, steps func(c *singbox.Engine)) {
		t.Helper()
		res := pol.Normalize([]byte(cfg(members)), env)
		if res.HasErrors() {
			t.Fatal(res.Errors())
		}
		c := singbox.New(singbox.RegistryOptions{})
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := c.Start(ctx, engine.Prepared{Revision: 1, JSON: res.JSON}); err != nil {
			t.Fatal(err)
		}
		steps(c)
		stop(t, c)
	}
	run(`"direct", "other", "block"`, func(c *singbox.Engine) {
		if got := groupOf(t, c, "pick").Selected; got != "direct" {
			t.Fatalf("the default was not used: %q", got)
		}
		if err := c.SelectOutbound("pick", "other"); err != nil {
			t.Fatal(err)
		}
	})
	// A new run, the same members: the person's choice is still there.
	run(`"direct", "other", "block"`, func(c *singbox.Engine) {
		if got := groupOf(t, c, "pick").Selected; got != "other" {
			t.Errorf("the choice was forgotten: %q", got)
		}
	})
	// The chosen member is gone from the configuration: back to the default,
	// not to a dangling name.
	run(`"direct", "block"`, func(c *singbox.Engine) {
		if got := groupOf(t, c, "pick").Selected; got != "direct" {
			t.Errorf("a remembered member that no longer exists was kept: %q", got)
		}
	})
}
