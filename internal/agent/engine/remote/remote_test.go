package remote

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"eggbox/internal/agent/engine"
	"eggbox/internal/agent/engine/fake"
	"eggbox/internal/agentapi"
)

// served runs a fake core behind a Server on a Unix socket, as the iOS
// extension does, and returns a Client of it.
func served(t *testing.T) (*fake.Engine, *Client) {
	t.Helper()
	// A short directory: a Unix socket's path is limited to about 100 bytes.
	dir, err := os.MkdirTemp("", "rmt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "e.sock")
	lis, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lis.Close() })
	eng := fake.New()
	srv := NewServer(eng)
	go srv.Serve(lis)
	c := &Client{Dial: func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", path)
	}}
	return eng, c
}

func start(t *testing.T, eng *fake.Engine) {
	t.Helper()
	if err := eng.Start(context.Background(), engine.Prepared{Revision: 1, JSON: []byte(`{}`), HasTun: true}); err != nil {
		t.Fatal(err)
	}
}

func TestStatusAndCalls(t *testing.T) {
	eng, c := served(t)
	ctx := context.Background()
	st, err := c.Status(ctx)
	if err != nil || st.Running || st.Version != "fake-1.0" {
		t.Fatalf("status before start: %+v, %v", st, err)
	}
	if err := c.SetMode("Global"); !errors.Is(err, engine.ErrNotRunning) {
		t.Fatalf("a call to a stopped core: %v, want ErrNotRunning", err)
	}

	start(t, eng)
	eng.SetGroups(&agentapi.Groups{Groups: []*agentapi.Group{{
		Tag: "Proxy", Selectable: true, Selected: "a",
		Items: []*agentapi.GroupItem{{Tag: "a"}, {Tag: "b"}},
	}}})
	if st, _ := c.Status(ctx); !st.Running {
		t.Fatal("the core runs")
	}
	if err := c.SelectOutbound("Proxy", "b"); err != nil {
		t.Fatal(err)
	}
	if err := c.SelectOutbound("Proxy", "nowhere"); !errors.Is(err, engine.ErrNotFound) {
		t.Fatalf("unknown outbound: %v, want ErrNotFound", err)
	}
	if err := c.URLTest(ctx, "Proxy"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetMode("Global"); err != nil {
		t.Fatal(err)
	}
	if cur, all, err := c.Mode(); err != nil || cur != "Global" || len(all) == 0 {
		t.Fatalf("mode %q %v %v", cur, all, err)
	}
	if err := c.CloseAllConnections(); err != nil {
		t.Fatal(err)
	}
}

func TestGroupsStreamAndStop(t *testing.T) {
	eng, c := served(t)
	start(t, eng)
	eng.SetGroups(&agentapi.Groups{Groups: []*agentapi.Group{{Tag: "Proxy", Selected: "a", Items: []*agentapi.GroupItem{{Tag: "a"}}}}})
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := c.WatchGroups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case g := <-ch:
		if len(g.GetGroups()) != 1 || g.GetGroups()[0].GetTag() != "Proxy" {
			t.Fatalf("groups %v", g)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no groups")
	}
	cancel()
	for range ch { // the stream ends with the context
	}
}

func TestLogsReachTheApplication(t *testing.T) {
	eng, c := served(t)
	start(t, eng)
	eng.Log("info", "before anyone listened")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := c.Logs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	eng.Log("warn", "while listening")
	want := []string{"before anyone listened", "while listening"}
	for _, w := range want {
		select {
		case l := <-ch:
			if l.Message != w {
				t.Fatalf("line %q, want %q", l.Message, w)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no line %q", w)
		}
	}
}

func TestExitIsReported(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		eng, c := served(t)
		start(t, eng)
		got := make(chan engine.ExitEvent, 1)
		go func() {
			ev, err := c.WaitExit(context.Background())
			if err != nil {
				t.Error(err)
			}
			got <- ev
		}()
		time.Sleep(100 * time.Millisecond)
		if revoke {
			eng.Revoke(errors.New("turned off in the settings"))
		} else {
			eng.Crash(errors.New("boom"))
		}
		select {
		case ev := <-got:
			if ev.Revoked != revoke || ev.Err == nil {
				t.Fatalf("revoke=%v: exit %+v", revoke, ev)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the exit was not reported")
		}
		// A caller that comes after the exit learns it too.
		ev, err := c.WaitExit(context.Background())
		if err != nil || ev.Revoked != revoke {
			t.Fatalf("late caller: %+v, %v", ev, err)
		}
	}
}

func TestTheProcessGoingAwayIsReported(t *testing.T) {
	dir, _ := os.MkdirTemp("", "rmt")
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "e.sock")
	lis, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	// A server that hangs up without answering, as a process that ends does.
	go func() {
		conn, err := lis.Accept()
		if err == nil {
			time.Sleep(50 * time.Millisecond)
			conn.Close()
		}
		lis.Close()
	}()
	c := &Client{Dial: func(ctx context.Context) (net.Conn, error) { return net.Dial("unix", path) }}
	if _, err := c.WaitExit(context.Background()); !errors.Is(err, ErrGone) {
		t.Fatalf("WaitExit: %v, want ErrGone", err)
	}
}

func TestOversizedFramesAreRefused(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	go func() { _, _ = a.Write([]byte{0xff, 0xff, 0xff, 0xff}) }()
	if _, err := readFrame(b); err == nil {
		t.Fatal("a frame of 4 GB was accepted")
	}
}
