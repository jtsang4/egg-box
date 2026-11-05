package agentclient_test

import (
	"context"
	"errors"
	"net"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"eggbox/internal/agent/testkit"
	"eggbox/internal/agentapi"
	"eggbox/internal/agentclient"
)

const minimalConfig = `{
  "inbounds": [{"type": "mixed", "tag": "mixed-in", "listen_port": 7890}],
  "outbounds": [{"type": "direct", "tag": "direct"}]
}`

func bundle(cfg string) *agentapi.ConfigBundle {
	return &agentapi.ConfigBundle{ConfigJson: []byte(cfg), ProfileId: "p1", ProfileName: "Test profile"}
}

func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return c
}

// waitStatus waits until the agent reports a status matching pred.
func waitStatus(t *testing.T, c *agentclient.Client, what string, pred func(*agentapi.Status) bool) *agentapi.Status {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st, err := c.Status(ctx(t))
		if err == nil && pred(st) {
			return st
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
	return nil
}

func TestHandshakeDescribesTheAgentAndTheCaller(t *testing.T) {
	a := testkit.Start(t, testkit.Options{})
	a.SetPeer("uid=501 exe=/Applications/EggBox.app")
	c := a.Client(t)
	link := c.Link()
	if link.State != agentclient.LinkReady || link.Hello == nil {
		t.Fatalf("link = %+v", link)
	}
	h := link.Hello
	if h.ProtocolVersion != 1 || h.AgentVersion != "test" || h.CoreVersion != "fake-1.0" || h.Driver != "sim" || !h.DevMode {
		t.Fatalf("hello = %v", h)
	}
	if h.Platform != runtime.GOOS || h.Arch != runtime.GOARCH || h.InstanceId == "" || h.AgentPid == 0 || h.AgentStartedUnix == 0 {
		t.Fatalf("hello = %v", h)
	}
	if h.Peer != "uid=501 exe=/Applications/EggBox.app" {
		t.Fatalf("the agent must report the identity it verified, got %q", h.Peer)
	}
}

func TestConnectAndDisconnectEndToEnd(t *testing.T) {
	a := testkit.Start(t, testkit.Options{})
	c := a.Client(t)

	res, err := c.ApplyConfig(ctx(t), 0, bundle(minimalConfig), agentapi.ApplyMode_APPLY_MODE_STORE_ONLY)
	if err != nil || res.Revision != 1 {
		t.Fatalf("apply: %v %v", res, err)
	}
	if _, err := c.Connect(ctx(t), agentclient.ConnectOptions{}); err != nil {
		t.Fatal(err)
	}
	st := waitStatus(t, c, "connected", func(s *agentapi.Status) bool {
		return s.Connection == agentapi.ConnectionState_CONNECTION_STATE_CONNECTED
	})
	if st.ActiveRevision != 1 || !st.IntentConnected || !st.HasConfig {
		t.Fatalf("status = %v", st)
	}
	if !a.Engine.Running() {
		t.Fatal("the core must be running")
	}
	if _, err := c.Disconnect(ctx(t)); err != nil {
		t.Fatal(err)
	}
	st = waitStatus(t, c, "disconnected", func(s *agentapi.Status) bool {
		return s.Connection == agentapi.ConnectionState_CONNECTION_STATE_DISCONNECTED
	})
	if st.IntentConnected || a.Engine.Running() {
		t.Fatalf("still running after a disconnect: %v", st)
	}
}

func TestOperationFailuresCarryTheirCodeAndIssues(t *testing.T) {
	a := testkit.Start(t, testkit.Options{})
	c := a.Client(t)

	_, err := c.Connect(ctx(t), agentclient.ConnectOptions{})
	var op *agentclient.OpError
	if !errors.As(err, &op) || op.Code != agentapi.ErrorCode_ERROR_CODE_NO_CONFIG {
		t.Fatalf("connect without a configuration: %v", err)
	}
	if !errors.Is(err, &agentclient.OpError{Code: agentapi.ErrorCode_ERROR_CODE_NO_CONFIG}) || agentclient.CodeOf(err) != agentapi.ErrorCode_ERROR_CODE_NO_CONFIG {
		t.Fatal("errors.Is / CodeOf must recognise the code")
	}

	bad := `{"inbounds":[{"type":"mixed","tag":"m","listen_port":7890,"set_system_proxy":true}],"outbounds":[{"type":"direct","tag":"direct"}]}`
	_, err = c.ApplyConfig(ctx(t), 0, bundle(bad), agentapi.ApplyMode_APPLY_MODE_STORE_ONLY)
	if !errors.As(err, &op) || len(op.Issues) == 0 {
		t.Fatalf("a configuration that touches the system proxy must be refused with issues: %v", err)
	}
	found := false
	for _, is := range op.Issues {
		found = found || is.Code == "system_proxy_not_allowed"
	}
	if !found {
		t.Fatalf("issues = %v", op.Issues)
	}

	// A revision conflict is reported as such.
	if _, err := c.ApplyConfig(ctx(t), 0, bundle(minimalConfig), agentapi.ApplyMode_APPLY_MODE_STORE_ONLY); err != nil {
		t.Fatal(err)
	}
	_, err = c.ApplyConfig(ctx(t), 0, bundle(minimalConfig), agentapi.ApplyMode_APPLY_MODE_STORE_ONLY)
	if agentclient.CodeOf(err) != agentapi.ErrorCode_ERROR_CODE_REVISION_CONFLICT {
		t.Fatalf("stale revision: %v", err)
	}
}

func TestValidateStoresNothing(t *testing.T) {
	a := testkit.Start(t, testkit.Options{})
	c := a.Client(t)
	good, err := c.Validate(ctx(t), bundle(minimalConfig))
	if err != nil || !good.Valid {
		t.Fatalf("validate: %v %v", good, err)
	}
	bad, err := c.Validate(ctx(t), bundle(`{"inbounds":[{"type":"mixed","tag":"m","listen":"0.0.0.0","listen_port":1}]}`))
	if err != nil || bad.Valid || len(bad.Issues) == 0 {
		t.Fatalf("validate: %v %v", bad, err)
	}
	st, _ := c.Status(ctx(t))
	if st.HasConfig {
		t.Fatal("validation must not store a configuration")
	}
}

func TestRequestIDsAreIdempotentOverTheWire(t *testing.T) {
	a := testkit.Start(t, testkit.Options{})
	c := a.Client(t)
	if _, err := c.ApplyConfig(ctx(t), 0, bundle(minimalConfig), agentapi.ApplyMode_APPLY_MODE_STORE_ONLY); err != nil {
		t.Fatal(err)
	}
	api, err := c.API()
	if err != nil {
		t.Fatal(err)
	}
	req := &agentapi.ConnectRequest{RequestId: "same-id-twice"}
	first, err := api.Connect(ctx(t), req)
	if err != nil || !first.Ok || first.Replayed {
		t.Fatalf("first: %v %v", first, err)
	}
	second, err := api.Connect(ctx(t), req)
	if err != nil || !second.Ok || !second.Replayed {
		t.Fatalf("a repeated request id must be answered from the cache: %v %v", second, err)
	}
	if n := countCalls(a.Engine.Calls(), "start"); n != 1 {
		t.Fatalf("the core was started %d times", n)
	}
}

func countCalls(calls []string, prefix string) int {
	n := 0
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func TestSettingsRoundTrip(t *testing.T) {
	a := testkit.Start(t, testkit.Options{})
	c := a.Client(t)
	s, err := c.Settings(ctx(t))
	if err != nil || s.LogLevel != "info" || s.AutoConnect {
		t.Fatalf("defaults: %v %v", s, err)
	}
	on := true
	level := "debug"
	s, err = c.UpdateSettings(ctx(t), &agentapi.UpdateSettingsRequest{AutoConnect: &on, LogLevel: &level})
	if err != nil || !s.AutoConnect || s.LogLevel != "debug" {
		t.Fatalf("update: %v %v", s, err)
	}
	bad := "chatty"
	_, err = c.UpdateSettings(ctx(t), &agentapi.UpdateSettingsRequest{LogLevel: &bad})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("an unknown level must be InvalidArgument, got %v", err)
	}
}

func TestDiagnosticsAreRedactedAndNamed(t *testing.T) {
	a := testkit.Start(t, testkit.Options{})
	c := a.Client(t)
	secret := `{"inbounds":[{"type":"mixed","tag":"m","listen_port":7890}],"outbounds":[{"type":"shadowsocks","tag":"n","server":"203.0.113.9","server_port":8388,"method":"aes-128-gcm","password":"hunter2-very-secret"}]}`
	if _, err := c.ApplyConfig(ctx(t), 0, bundle(secret), agentapi.ApplyMode_APPLY_MODE_STORE_ONLY); err != nil {
		t.Fatal(err)
	}
	rep, err := c.Diagnostics(ctx(t))
	if err != nil || rep.SuggestedFilename == "" || len(rep.Json) == 0 {
		t.Fatalf("diagnostics: %v %v", rep, err)
	}
	if strings.Contains(string(rep.Json), "hunter2") || strings.Contains(string(rep.Json), "203.0.113.9") {
		t.Fatalf("the report leaks configuration content: %s", rep.Json)
	}
}

// ------------------------------------------------------------- protocol guard

func rawClient(t *testing.T, a *testkit.Agent) (agentapi.AgentClient, func()) {
	t.Helper()
	conn, err := grpc.NewClient("passthrough:///agent",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(c context.Context, _ string) (net.Conn, error) { return a.Dial(c) }))
	if err != nil {
		t.Fatal(err)
	}
	return agentapi.NewAgentClient(conn), func() { conn.Close() }
}

func TestCallsWithoutTheRightProtocolVersionAreRefused(t *testing.T) {
	a := testkit.Start(t, testkit.Options{})
	api, closeFn := rawClient(t, a)
	defer closeFn()

	// No version at all.
	if _, err := api.GetStatus(ctx(t), &emptypb.Empty{}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("a call without a version: %v", err)
	}
	// A different version.
	wrong := metadata.AppendToOutgoingContext(ctx(t), agentapi.ProtocolMetadataKey, "99")
	_, err := api.Connect(wrong, &agentapi.ConnectRequest{RequestId: "x"})
	if status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "99") {
		t.Fatalf("a call with another version: %v", err)
	}
	// A garbled one.
	garbled := metadata.AppendToOutgoingContext(ctx(t), agentapi.ProtocolMetadataKey, "one")
	if _, err := api.GetStatus(garbled, &emptypb.Empty{}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("a garbled version: %v", err)
	}
	// Streams are guarded too.
	stream, err := api.WatchStatus(wrong, &emptypb.Empty{})
	if err == nil {
		_, err = stream.Recv()
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("a stream with another version: %v", err)
	}
	// The handshake always works: it is how a client learns the agent's version.
	hello, err := api.Handshake(ctx(t), &agentapi.HandshakeRequest{ProtocolVersion: 99})
	if err != nil || hello.ProtocolVersion != 1 {
		t.Fatalf("handshake: %v %v", hello, err)
	}
	// And nothing was changed by the refused calls.
	if n := countCalls(a.Engine.Calls(), "start"); n != 0 {
		t.Fatal("a refused call must not run")
	}
}

// ------------------------------------------------------------ link behaviour

func TestUnauthorizedCallersAreTurnedAway(t *testing.T) {
	a := testkit.Start(t, testkit.Options{})
	a.SetAllowed(false)

	c := agentclient.New(a.Dialer(), agentclient.Options{Version: "intruder", Backoff: func(int) time.Duration { return 20 * time.Millisecond }})
	bg, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.Start(bg)
	defer c.Close()

	deadline := time.Now().Add(5 * time.Second)
	for c.Link().State != agentclient.LinkRefused && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := c.Link(); got.State != agentclient.LinkRefused || got.Err == nil || !strings.Contains(got.Err.Error(), "authorized") {
		t.Fatalf("link = %+v", got)
	}
	if _, err := c.Status(ctx(t)); err == nil {
		t.Fatal("a refused client must not get answers")
	}

	// Authorized again: the same client recovers on its own.
	a.SetAllowed(true)
	wait, wcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer wcancel()
	if err := c.WaitReady(wait); err != nil {
		t.Fatalf("the client never recovered: %v (%s)", err, c.Link().Describe())
	}
}

func TestUnreachableAgentIsReportedAsUnavailable(t *testing.T) {
	var attempts atomic.Int32
	dial := func(context.Context) (net.Conn, error) {
		attempts.Add(1)
		return nil, &os.PathError{Op: "dial", Path: "/nonexistent/agent.sock", Err: os.ErrNotExist}
	}
	c := agentclient.New(dial, agentclient.Options{Backoff: func(int) time.Duration { return 10 * time.Millisecond }})
	bg, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.Start(bg)
	defer c.Close()

	deadline := time.Now().Add(5 * time.Second)
	for c.Link().State != agentclient.LinkUnavailable && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if c.Link().State != agentclient.LinkUnavailable {
		t.Fatalf("link = %+v", c.Link())
	}
	time.Sleep(100 * time.Millisecond)
	if attempts.Load() < 3 {
		t.Fatalf("the client must keep trying, made %d attempts", attempts.Load())
	}
	if _, err := c.API(); !errors.Is(err, agentclient.ErrNotReady) {
		t.Fatalf("API while down: %v", err)
	}
}

// killable wraps the in-memory transport so a test can cut every connection.
type killable struct {
	a     *testkit.Agent
	mu    sync.Mutex
	conns []net.Conn
}

func (k *killable) dial(c context.Context) (net.Conn, error) {
	conn, err := k.a.Dial(c)
	if err == nil {
		k.mu.Lock()
		k.conns = append(k.conns, conn)
		k.mu.Unlock()
	}
	return conn, err
}

func (k *killable) cut() {
	k.mu.Lock()
	defer k.mu.Unlock()
	for _, c := range k.conns {
		_ = c.Close()
	}
	k.conns = nil
}

func TestStreamsResumeAfterTheConnectionIsCut(t *testing.T) {
	a := testkit.Start(t, testkit.Options{})
	k := &killable{a: a}
	c := agentclient.New(k.dial, agentclient.Options{Backoff: func(int) time.Duration { return 10 * time.Millisecond }})
	bg, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.Start(bg)
	defer c.Close()
	if err := c.WaitReady(ctx(t)); err != nil {
		t.Fatal(err)
	}
	firstGen := c.Link().Generation

	var mu sync.Mutex
	var seqs []uint64
	go c.WatchStatus(bg, func(s *agentapi.Status) {
		mu.Lock()
		seqs = append(seqs, s.Seq)
		mu.Unlock()
	})
	count := func() int { mu.Lock(); defer mu.Unlock(); return len(seqs) }
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !cond() && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if !cond() {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
	waitFor("the first status", func() bool { return count() >= 1 })
	before := count()

	k.cut()
	waitFor("a new link generation", func() bool {
		l := c.Link()
		return l.State == agentclient.LinkReady && l.Generation > firstGen
	})
	waitFor("the stream to resume with the current status", func() bool { return count() > before })

	// The resumed stream is live: a change made now arrives.
	if _, err := c.ApplyConfig(ctx(t), 0, bundle(minimalConfig), agentapi.ApplyMode_APPLY_MODE_STORE_ONLY); err != nil {
		t.Fatal(err)
	}
	after := count()
	if _, err := c.Connect(ctx(t), agentclient.ConnectOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor("a change after the resume", func() bool { return count() > after })
}

func TestCallsAfterTheAgentStopsFailCleanly(t *testing.T) {
	a := testkit.Start(t, testkit.Options{})
	c := a.Client(t)
	sd, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.Daemon.Shutdown(sd); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for c.Link().State == agentclient.LinkReady && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if c.Link().State == agentclient.LinkReady {
		t.Fatal("the link must drop when the agent stops")
	}
	short, scancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer scancel()
	if _, err := c.Connect(short, agentclient.ConnectOptions{}); err == nil {
		t.Fatal("a call to a stopped agent must fail")
	}
}

// ------------------------------------------------------------------- streams

func TestStreamsDeliverStatusGroupsTrafficConnectionsAndLogs(t *testing.T) {
	a := testkit.Start(t, testkit.Options{})
	c := a.Client(t)
	bg, cancel := context.WithCancel(context.Background())
	defer cancel()

	statuses := make(chan *agentapi.Status, 64)
	groups := make(chan *agentapi.Groups, 64)
	traffic := make(chan *agentapi.Traffic, 64)
	conns := make(chan *agentapi.ConnectionEvents, 64)
	logs := make(chan *agentapi.LogBatch, 64)
	go c.WatchStatus(bg, func(s *agentapi.Status) { statuses <- s })
	go c.WatchGroups(bg, func(g *agentapi.Groups) { groups <- g })
	go c.WatchTraffic(bg, 250*time.Millisecond, func(x *agentapi.Traffic) { traffic <- x })
	go c.WatchConnections(bg, 250*time.Millisecond, func(e *agentapi.ConnectionEvents) { conns <- e })
	go c.WatchLogs(bg, 100, "info", func(b *agentapi.LogBatch) { logs <- b })

	recv := func(what string, ch any, pred func(any) bool) {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			var v any
			select {
			case x := <-ch.(chan *agentapi.Status):
				v = x
			case <-deadline:
				t.Fatalf("timed out waiting for %s", what)
			}
			if pred(v) {
				return
			}
		}
	}
	recv("the first status", statuses, func(v any) bool { return v.(*agentapi.Status).Service == agentapi.ServiceState_SERVICE_STATE_READY })

	if first := <-logs; !first.Reset_ {
		t.Fatal("the first log batch must be marked reset")
	}

	if _, err := c.ApplyConfig(ctx(t), 0, bundle(minimalConfig), agentapi.ApplyMode_APPLY_MODE_STORE_ONLY); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Connect(ctx(t), agentclient.ConnectOptions{}); err != nil {
		t.Fatal(err)
	}
	recv("connected", statuses, func(v any) bool {
		return v.(*agentapi.Status).Connection == agentapi.ConnectionState_CONNECTION_STATE_CONNECTED
	})

	a.Engine.SetGroups(&agentapi.Groups{Groups: []*agentapi.Group{{Tag: "proxy", Type: "selector", Selectable: true, Selected: "a", Items: []*agentapi.GroupItem{{Tag: "a"}, {Tag: "b"}}}}})
	deadline := time.After(5 * time.Second)
	for got := false; !got; {
		select {
		case g := <-groups:
			got = len(g.Groups) == 1 && g.Groups[0].Tag == "proxy"
		case <-deadline:
			t.Fatal("no groups arrived")
		}
	}

	a.Engine.AddConnection(&agentapi.Connection{Id: "c1", Destination: "example.com:443", Outbound: "a"})
	deadline = time.After(5 * time.Second)
	for got := false; !got; {
		select {
		case e := <-conns:
			for _, ev := range e.Events {
				got = got || ev.Id == "c1"
			}
		case <-deadline:
			t.Fatal("no connection event arrived")
		}
	}

	deadline = time.After(5 * time.Second)
	for got := false; !got; {
		select {
		case x := <-traffic:
			got = x.Available
		case <-deadline:
			t.Fatal("no traffic sample while connected")
		}
	}

	a.Engine.Log("info", "hello from the core")
	deadline = time.After(5 * time.Second)
	for got := false; !got; {
		select {
		case b := <-logs:
			for _, e := range b.Entries {
				got = got || strings.Contains(e.Message, "hello from the core")
			}
		case <-deadline:
			t.Fatal("the core's log line never arrived")
		}
	}
}

func TestWatchLogsRefusesAnUnknownLevelAndStops(t *testing.T) {
	a := testkit.Start(t, testkit.Options{})
	c := a.Client(t)
	api, err := c.API()
	if err != nil {
		t.Fatal(err)
	}
	stream, err := api.WatchLogs(ctx(t), &agentapi.WatchLogsRequest{MinLevel: "chatty"})
	if err == nil {
		_, err = stream.Recv()
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("err = %v", err)
	}

	// The helper must give up instead of retrying a request that cannot succeed.
	done := make(chan struct{})
	go func() {
		c.WatchLogs(context.Background(), 10, "chatty", func(*agentapi.LogBatch) {})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("WatchLogs kept retrying an invalid request")
	}
}

func TestStatusUpdatesAreSharedWithEveryClient(t *testing.T) {
	a := testkit.Start(t, testkit.Options{})
	c1, c2 := a.Client(t), a.Client(t)
	if _, err := c1.ApplyConfig(ctx(t), 0, bundle(minimalConfig), agentapi.ApplyMode_APPLY_MODE_STORE_ONLY); err != nil {
		t.Fatal(err)
	}
	st := waitStatus(t, c2, "the stored revision on the other client", func(s *agentapi.Status) bool { return s.StoredRevision == 1 })
	if !st.HasConfig {
		t.Fatalf("status = %v", st)
	}
}

func TestLinkUpdatesNotifySubscribers(t *testing.T) {
	a := testkit.Start(t, testkit.Options{})
	k := &killable{a: a}
	c := agentclient.New(k.dial, agentclient.Options{Backoff: func(int) time.Duration { return 10 * time.Millisecond }})
	bg, cancel := context.WithCancel(context.Background())
	defer cancel()
	updates := c.Updates(bg)
	c.Start(bg)
	defer c.Close()

	seen := map[agentclient.LinkState]bool{}
	deadline := time.After(5 * time.Second)
	for !seen[agentclient.LinkReady] {
		select {
		case l := <-updates:
			seen[l.State] = true
		case <-deadline:
			t.Fatalf("never saw a ready link, saw %v", seen)
		}
	}
	k.cut()
	for !seen[agentclient.LinkConnecting] || !seen[agentclient.LinkReady] {
		select {
		case l := <-updates:
			seen[l.State] = true
			if l.State == agentclient.LinkReady && l.Generation < 2 && seen[agentclient.LinkConnecting] {
				t.Fatalf("a re-established link must have a new generation: %+v", l)
			}
		case <-deadline:
			t.Fatalf("no reconnect after the cut, saw %v", seen)
		}
	}
}
