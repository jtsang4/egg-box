package singbox

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/daemon"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jtsang4/sing-box-ui/internal/agent/engine"
	"github.com/jtsang4/sing-box-ui/internal/agentapi"
)

// watchRig runs the TUN watchdog against a fake interface table.
type watchRig struct {
	e    *Engine
	s    *session
	done chan struct{}
	stop context.CancelFunc
}

func newWatchRig(t *testing.T, exists func(string) bool) *watchRig {
	t.Helper()
	e := New(RegistryOptions{})
	e.tunCheckEvery = 5 * time.Millisecond
	e.tunExists = exists
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r := &watchRig{e: e, s: &session{ctx: ctx, cancel: cancel, closed: make(chan struct{})}, done: make(chan struct{}), stop: cancel}
	go func() {
		e.watchTun(r.s, "utun9")
		close(r.done)
	}()
	return r
}

func (r *watchRig) exit(d time.Duration) (engine.ExitEvent, bool) {
	select {
	case ev := <-r.e.Exited():
		return ev, true
	case <-time.After(d):
		return engine.ExitEvent{}, false
	}
}

func TestWatchTunReportsADisappearedInterface(t *testing.T) {
	var present atomic.Bool
	present.Store(true)
	r := newWatchRig(t, func(string) bool { return present.Load() })
	if _, ok := r.exit(80 * time.Millisecond); ok {
		t.Fatal("an interface that is there must not be reported")
	}
	present.Store(false)
	ev, ok := r.exit(2 * time.Second)
	if !ok {
		t.Fatal("the missing interface was not reported")
	}
	if ev.Err == nil || !strings.Contains(ev.Err.Error(), "utun9") {
		t.Fatalf("event = %+v", ev)
	}
	select {
	case <-r.done:
	case <-time.After(time.Second):
		t.Fatal("the watchdog must stop after reporting")
	}
	if !r.s.crashed.Load() {
		t.Fatal("the session is marked as crashed")
	}
}

func TestWatchTunIgnoresASingleFlicker(t *testing.T) {
	var calls atomic.Int32
	// Missing exactly once, on the third look.
	r := newWatchRig(t, func(string) bool { return calls.Add(1) != 3 })
	if ev, ok := r.exit(250 * time.Millisecond); ok {
		t.Fatalf("one missed look is a flicker, not a death: %+v", ev)
	}
	if calls.Load() < 10 {
		t.Fatalf("the watchdog stopped looking after %d checks", calls.Load())
	}
}

func TestWatchTunStaysQuietWhileStopping(t *testing.T) {
	r := newWatchRig(t, func(string) bool { return false })
	r.s.closing.Store(true) // we asked for the stop, so the interface going away is expected
	if ev, ok := r.exit(200 * time.Millisecond); ok {
		t.Fatalf("an expected shutdown was reported as a crash: %+v", ev)
	}
	select {
	case <-r.done:
	case <-time.After(time.Second):
		t.Fatal("the watchdog must stop once the session is closing")
	}
}

func TestWatchTunStopsWithItsSession(t *testing.T) {
	r := newWatchRig(t, func(string) bool { return true })
	r.stop()
	select {
	case <-r.done:
	case <-time.After(time.Second):
		t.Fatal("the watchdog outlived its session")
	}
}

func TestWatchTunReportsOnlyOnce(t *testing.T) {
	// The exit channel holds one event; a second report must neither block nor queue up.
	e := New(RegistryOptions{})
	e.tunCheckEvery = time.Millisecond
	e.tunExists = func(string) bool { return false }
	e.exited <- engine.ExitEvent{Err: errors.New("earlier")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &session{ctx: ctx}
	done := make(chan struct{})
	go func() { e.watchTun(s, "utun9"); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the watchdog blocked on a full channel")
	}
	if len(e.exited) != 1 {
		t.Fatalf("queued events = %d", len(e.exited))
	}
}

func TestTunTargets(t *testing.T) {
	raw := []byte(`{"inbounds": [
	  {"type": "mixed", "tag": "m", "address": ["10.0.0.1/24"]},
	  {"type": "tun", "tag": "tun-a", "interface_name": "SingBoxUI", "address": ["172.19.0.1/30", "fdfe:dcba:9876::1/126", "bogus"]},
	  {"type": "tun", "tag": "tun-b", "address": ["172.20.0.1/30"]}
	]}`)
	addrs, name := tunTargets(raw, "")
	if name != "SingBoxUI" || len(addrs) != 2 || addrs[0] != netip.MustParseAddr("172.19.0.1") || addrs[1] != netip.MustParseAddr("fdfe:dcba:9876::1") {
		t.Fatalf("first tun = %v %q", addrs, name)
	}
	addrs, name = tunTargets(raw, "tun-b")
	if name != "" || len(addrs) != 1 || addrs[0] != netip.MustParseAddr("172.20.0.1") {
		t.Fatalf("tun-b = %v %q", addrs, name)
	}
	if addrs, name = tunTargets(raw, "missing"); addrs != nil || name != "" {
		t.Fatalf("unknown tag = %v %q", addrs, name)
	}
	if addrs, name = tunTargets([]byte(`not json`), ""); addrs != nil || name != "" {
		t.Fatalf("invalid json = %v %q", addrs, name)
	}
}

func TestInterfaceWith(t *testing.T) {
	if got := interfaceWith(nil); got != "" {
		t.Fatalf("no addresses: %q", got)
	}
	if got := interfaceWith([]netip.Addr{netip.MustParseAddr("127.0.0.1")}); got == "" {
		t.Fatal("the loopback interface owns 127.0.0.1")
	} else if _, err := net.InterfaceByName(got); err != nil {
		t.Fatalf("returned an interface that does not exist: %q", got)
	}
	// 203.0.113.0/24 is TEST-NET-3: never assigned to a real interface.
	if got := interfaceWith([]netip.Addr{netip.MustParseAddr("203.0.113.77")}); got != "" {
		t.Fatalf("an address nobody has: %q", got)
	}
	if !interfaceExists("lo0") && !interfaceExists("lo") {
		t.Fatal("no loopback interface is visible")
	}
	if interfaceExists("sbui-no-such-interface") {
		t.Fatal("a made-up interface exists")
	}
}

func TestDiscoverTunWithoutTun(t *testing.T) {
	if got := discoverTun(context.Background(), engine.Prepared{JSON: []byte(`{}`)}); got != "" {
		t.Fatalf("no tun configured: %q", got)
	}
	// A configured TUN that never appears falls back to its fixed name once the wait is over.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	raw := []byte(`{"inbounds": [{"type": "tun", "tag": "t", "interface_name": "SingBoxUI", "address": ["203.0.113.1/30"]}]}`)
	if got := discoverTun(ctx, engine.Prepared{JSON: raw, HasTun: true, TunTag: "t"}); got != "SingBoxUI" {
		t.Fatalf("fallback = %q", got)
	}
}

func TestCheckCachePath(t *testing.T) {
	abs := filepath.Join(os.TempDir(), "cache.db")
	cases := []struct {
		name     string
		cfg      string
		required bool
		ok       bool
	}{
		{"start: absolute path", `{"experimental":{"cache_file":{"enabled":true,"path":` + quote(abs) + `}}}`, true, true},
		{"start: absolute path, flag off", `{"experimental":{"cache_file":{"enabled":false,"path":` + quote(abs) + `}}}`, true, true},
		{"start: no experimental section", `{}`, true, false},
		{"start: no cache section", `{"experimental":{}}`, true, false},
		{"start: relative path", `{"experimental":{"cache_file":{"enabled":true,"path":"cache.db"}}}`, true, false},
		{"start: default path", `{"experimental":{"cache_file":{"enabled":true}}}`, true, false},
		{"check: no cache section", `{}`, false, true},
		{"check: cache off", `{"experimental":{"cache_file":{"enabled":false}}}`, false, true},
		{"check: cache on, absolute", `{"experimental":{"cache_file":{"enabled":true,"path":` + quote(abs) + `}}}`, false, true},
		{"check: cache on, relative", `{"experimental":{"cache_file":{"enabled":true,"path":"cache.db"}}}`, false, false},
		{"check: cache on, default", `{"experimental":{"cache_file":{"enabled":true}}}`, false, false},
		{"not json", `{`, true, false},
	}
	for _, c := range cases {
		err := checkCachePath([]byte(c.cfg), c.required)
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok=%v", c.name, err, c.ok)
		}
	}
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestQuietConsole(t *testing.T) {
	read := func(raw string) map[string]any {
		t.Helper()
		out, err := quietConsole([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	m := read(`{"outbounds": [{"type": "direct", "tag": "d"}]}`)
	if log, _ := m["log"].(map[string]any); log["output"] != os.DevNull {
		t.Fatalf("log = %v", m["log"])
	}
	if _, ok := m["outbounds"]; !ok {
		t.Fatal("the rest of the config must survive")
	}
	m = read(`{"log": {"level": "debug", "timestamp": true, "output": "/var/log/x"}}`)
	log, _ := m["log"].(map[string]any)
	if log["level"] != "debug" || log["timestamp"] != true || log["output"] != os.DevNull {
		t.Fatalf("log = %v", log)
	}
	for _, bad := range []string{`nope`, `{"log": 5}`, `[]`} {
		if _, err := quietConsole([]byte(bad)); err == nil {
			t.Fatalf("%s must be rejected", bad)
		}
	}
}

func TestConversions(t *testing.T) {
	groups := convertGroups(&daemon.Groups{Group: []*daemon.Group{{
		Tag: "proxy", Type: "selector", Selectable: true, Selected: "b",
		Items: []*daemon.GroupItem{{Tag: "a", Type: "shadowsocks", UrlTestDelay: 41, UrlTestTime: 7}, {Tag: "b", Type: "direct"}},
	}}})
	if len(groups.Groups) != 1 || groups.Groups[0].Selected != "b" || len(groups.Groups[0].Items) != 2 || groups.Groups[0].Items[0].DelayMs != 41 || groups.Groups[0].Items[0].TestedUnix != 7 {
		t.Fatalf("groups = %+v", groups)
	}

	if convertConnection(nil) != nil {
		t.Fatal("nil in, nil out")
	}
	c := convertConnection(&daemon.Connection{Id: "x", ChainList: []string{"a", "b"}, UplinkTotal: 3, ProcessInfo: &daemon.ProcessInfo{}})
	if c.Process != nil || len(c.Chain) != 2 || c.UplinkTotal != 3 {
		t.Fatalf("an empty process record is not a process: %+v", c)
	}
	c = convertConnection(&daemon.Connection{Id: "x", ProcessInfo: &daemon.ProcessInfo{ProcessId: 12, ProcessPath: "/bin/curl", UserName: "me"}})
	if c.Process == nil || c.Process.Pid != 12 || c.Process.Path != "/bin/curl" || c.Process.User != "me" {
		t.Fatalf("process = %+v", c.Process)
	}

	evs := convertConnectionEvents(&daemon.ConnectionEvents{Reset_: true, Events: []*daemon.ConnectionEvent{
		{Type: daemon.ConnectionEventType_CONNECTION_EVENT_NEW, Id: "1", Connection: &daemon.Connection{Id: "1"}},
		{Type: daemon.ConnectionEventType_CONNECTION_EVENT_UPDATE, Id: "1", UplinkDelta: 5, DownlinkDelta: 6},
		{Type: daemon.ConnectionEventType_CONNECTION_EVENT_CLOSED, Id: "1", ClosedAt: 99, Connection: &daemon.Connection{Id: "1"}},
	}})
	if !evs.Reset_ || len(evs.Events) != 3 ||
		evs.Events[0].Type != agentapi.ConnectionEvent_TYPE_NEW ||
		evs.Events[1].Type != agentapi.ConnectionEvent_TYPE_UPDATE || evs.Events[1].UplinkDelta != 5 || evs.Events[1].DownlinkDelta != 6 ||
		evs.Events[2].Type != agentapi.ConnectionEvent_TYPE_CLOSED || evs.Events[2].Connection.ClosedUnixMs != 99 {
		t.Fatalf("events = %+v", evs)
	}
}

func TestCoreLevels(t *testing.T) {
	for in, want := range map[daemon.LogLevel]string{
		daemon.LogLevel_PANIC: "error", daemon.LogLevel_FATAL: "error", daemon.LogLevel_ERROR: "error",
		daemon.LogLevel_WARN: "warn", daemon.LogLevel_INFO: "info", daemon.LogLevel_DEBUG: "debug", daemon.LogLevel_TRACE: "trace",
	} {
		if got := coreLevel(in); got != want {
			t.Fatalf("%v -> %q, want %q", in, got, want)
		}
	}
}

func TestCleanLine(t *testing.T) {
	if got := cleanLine("\x1b[36mINFO\x1b[0m[0000] inbound/mixed[m]: tcp server started at 127.0.0.1:1\n"); got != "INFO[0000] inbound/mixed[m]: tcp server started at 127.0.0.1:1" {
		t.Fatalf("got %q", got)
	}
	if got := cleanLine("  \x1b[0m \n"); got != "" {
		t.Fatalf("a line of only escapes: %q", got)
	}
}

func TestMapErr(t *testing.T) {
	if mapErr(nil) != nil {
		t.Fatal("nil in, nil out")
	}
	if err := mapErr(os.ErrClosed); !errors.Is(err, engine.ErrNotRunning) {
		t.Fatalf("closed: %v", err)
	}
	if err := mapErr(os.ErrInvalid); !errors.Is(err, engine.ErrNotRunning) {
		t.Fatalf("invalid: %v", err)
	}
	if err := mapErr(status.Error(codes.NotFound, "no such group")); !errors.Is(err, engine.ErrNotFound) || !strings.Contains(err.Error(), "no such group") {
		t.Fatalf("not found: %v", err)
	}
	for _, code := range []codes.Code{codes.InvalidArgument, codes.Unimplemented, codes.FailedPrecondition} {
		if err := mapErr(status.Error(code, "x")); !errors.Is(err, engine.ErrInvalid) {
			t.Fatalf("%v: %v", code, err)
		}
	}
	other := errors.New("boom")
	if err := mapErr(other); !errors.Is(err, other) {
		t.Fatalf("an unknown error passes through: %v", err)
	}
}

func TestClampInterval(t *testing.T) {
	for in, want := range map[time.Duration]time.Duration{
		-1: time.Second, 0: time.Second, time.Millisecond: minInterval, 3 * time.Second: 3 * time.Second, time.Hour: maxInterval,
	} {
		if got := clampInterval(in); got != want {
			t.Fatalf("%v -> %v, want %v", in, got, want)
		}
	}
}

func TestCoreVersionNeverEmpty(t *testing.T) {
	if v := coreVersion(); v == "" || strings.HasPrefix(v, "v") {
		t.Fatalf("version = %q", v)
	}
}
