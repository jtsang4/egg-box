package app_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"eggbox/internal/app"
	"eggbox/internal/configgen"
	"eggbox/internal/sysproxy"
)

// fakeProxies are invented system proxy settings and listeners.
type fakeProxies struct {
	mu        sync.Mutex
	settings  []sysproxy.Setting
	listening map[int]bool
	reads     int
}

func (f *fakeProxies) read() ([]sysproxy.Setting, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	return append([]sysproxy.Setting(nil), f.settings...), nil
}

func (f *fakeProxies) probe(_ context.Context, _ string, port int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listening[port]
}

func (f *fakeProxies) set(s ...sysproxy.Setting) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settings = s
}

func (f *fakeProxies) readCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads
}

func noticesWith(s *app.Snapshot, code string) int {
	n := 0
	for _, x := range s.Notices {
		if x.Code == code {
			n++
		}
	}
	return n
}

func TestSystemProxyLeftBehindIsFoundWhileConnected(t *testing.T) {
	fp := &fakeProxies{listening: map[int]bool{7891: true}}
	fp.set(
		sysproxy.Setting{Service: "Wi-Fi", Active: true, Kind: sysproxy.KindHTTP, Host: "127.0.0.1", Port: 3067},
		sysproxy.Setting{Service: "Wi-Fi", Active: true, Kind: sysproxy.KindSOCKS, Host: "127.0.0.1", Port: 3067},
		sysproxy.Setting{Service: "USB LAN", Kind: sysproxy.KindHTTP, Host: "127.0.0.1", Port: 7891},
	)
	h := newHarness(t, func(c *app.Config) {
		c.SystemProxies, c.ProbeProxy, c.ProxyCheckEvery = fp.read, fp.probe, 40*time.Millisecond
	})

	// Not connected: nothing is read, there is nothing to say.
	time.Sleep(100 * time.Millisecond)
	if fp.readCount() != 0 || len(h.svc.Snapshot().ProxyIssues) != 0 {
		t.Fatalf("the settings are looked at only while connected (%d reads)", fp.readCount())
	}

	h.addSubscription()
	if err := h.svc.Connect(h.ctx(), app.ConnectOptions{}); err != nil {
		t.Fatal(err)
	}
	snap := h.waitFor("the issues", func(s *app.Snapshot) bool { return len(s.ProxyIssues) == 2 })
	dead, other := snap.ProxyIssues[0], snap.ProxyIssues[1]
	if dead.Endpoint() != "127.0.0.1:3067" || dead.State != sysproxy.Dead || !dead.Active {
		t.Fatalf("the active dead address comes first: %+v", dead)
	}
	if other.Endpoint() != "127.0.0.1:7891" || other.State != sysproxy.Foreign {
		t.Fatalf("a port another program listens on: %+v", other)
	}
	h.waitFor("the warning", func(s *app.Snapshot) bool { return noticesWith(s, "system_proxy_dead") == 1 })

	// Looked at again and again, it is told once.
	reads := fp.readCount()
	h.waitFor("more looks", func(*app.Snapshot) bool { return fp.readCount() >= reads+3 })
	if n := noticesWith(h.svc.Snapshot(), "system_proxy_dead"); n != 1 {
		t.Fatalf("told %d times", n)
	}

	// The person switches it off: the next look finds nothing more to say.
	fp.set()
	h.waitFor("the issues to clear", func(s *app.Snapshot) bool { return len(s.ProxyIssues) == 0 })

	fp.set(sysproxy.Setting{Service: "Wi-Fi", Active: true, Kind: sysproxy.KindHTTP, Host: "127.0.0.1", Port: 3067})
	h.waitFor("the issue again", func(s *app.Snapshot) bool { return len(s.ProxyIssues) == 1 })
	if err := h.svc.Disconnect(h.ctx()); err != nil {
		t.Fatal(err)
	}
	h.waitFor("the issues to clear when disconnected", func(s *app.Snapshot) bool { return len(s.ProxyIssues) == 0 })
}

func TestSystemProxyPointingAtOurOwnPortIsNotAnIssue(t *testing.T) {
	fp := &fakeProxies{}
	fp.set(sysproxy.Setting{Service: "Wi-Fi", Active: true, Kind: sysproxy.KindHTTP, Host: "127.0.0.1", Port: 7890})
	h := newHarness(t, func(c *app.Config) {
		c.SystemProxies, c.ProbeProxy, c.ProxyCheckEvery = fp.read, fp.probe, 40*time.Millisecond
	})
	p := h.lib.Prefs()
	p.TunDisabled, p.MixedPort = true, 0
	if _, err := h.svc.SavePrefs(h.ctx(), p); err != nil {
		t.Fatal(err)
	}
	h.addSubscription()
	if err := h.svc.Connect(h.ctx(), app.ConnectOptions{}); err != nil {
		t.Fatal(err)
	}
	h.waitFor("connected", func(s *app.Snapshot) bool { return s.Phase == app.PhaseConnected })
	reads := fp.readCount()
	h.waitFor("a few looks", func(*app.Snapshot) bool { return fp.readCount() >= reads+2 })
	if got := h.svc.Snapshot().ProxyIssues; len(got) != 0 {
		t.Fatalf("our own proxy (port %d) is the person's choice: %+v", configgen.DefaultMixedPort, got)
	}
}
