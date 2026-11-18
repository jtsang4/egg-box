package iosengine

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"eggbox/internal/agent/engine"
	"eggbox/internal/agent/engine/fake"
	"eggbox/internal/agent/engine/remote"
	"eggbox/internal/phone/iostunnel"
)

// extension plays the packet tunnel extension and the system that starts it:
// Start reads the configuration from the shared container and serves a core
// on the socket, Stop ends it, and kill makes the process vanish.
type extension struct {
	t    *testing.T
	dir  string
	sock string

	mu      sync.Mutex
	core    *fake.Engine
	lis     net.Listener
	conns   []net.Conn
	started []string
	stops   int
	serve   bool // false: the tunnel comes up but its core never answers
}

func (x *extension) Start(ctx context.Context) error {
	p, err := iostunnel.ReadPrepared(x.dir)
	if err != nil {
		return err
	}
	core := fake.New()
	if err := core.Start(ctx, p); err != nil {
		return err
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	x.started = append(x.started, string(p.JSON))
	x.core = core
	if !x.serve {
		return nil
	}
	srv := remote.NewServer(core)
	lis, err := net.Listen("unix", x.sock)
	if err != nil {
		return err
	}
	x.lis = lis
	go func() {
		for {
			c, err := lis.Accept()
			if err != nil {
				return
			}
			x.mu.Lock()
			if x.lis == nil { // killed while this one was being accepted
				x.mu.Unlock()
				c.Close()
				return
			}
			x.conns = append(x.conns, c)
			x.mu.Unlock()
			go srv.Serve(&oneConn{c: c})
		}
	}()
	return nil
}

func (x *extension) Stop(context.Context) error {
	x.mu.Lock()
	x.stops++
	x.mu.Unlock()
	x.kill()
	return nil
}

// kill ends the extension's process: its socket and every connection go.
func (x *extension) kill() {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.lis != nil {
		x.lis.Close()
		x.lis = nil
	}
	for _, c := range x.conns {
		c.Close()
	}
	x.conns = nil
	_ = os.Remove(x.sock)
}

// oneConn is a listener of one connection, so that each accepted connection
// is served by the Server and can be closed by kill.
type oneConn struct {
	c    net.Conn
	once sync.Once
}

func (l *oneConn) Accept() (net.Conn, error) {
	var c net.Conn
	l.once.Do(func() { c = l.c })
	if c == nil {
		return nil, net.ErrClosed
	}
	return c, nil
}
func (l *oneConn) Close() error   { return nil }
func (l *oneConn) Addr() net.Addr { return l.c.LocalAddr() }

type checker struct{ checked int }

func (c *checker) Version() string                              { return "1.14.4" }
func (c *checker) Check(context.Context, engine.Prepared) error { c.checked++; return nil }

func setup(t *testing.T) (*Engine, *extension) {
	t.Helper()
	dir, err := os.MkdirTemp("", "ios")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	x := &extension{t: t, dir: dir, sock: filepath.Join(dir, "e.sock"), serve: true}
	core := &remote.Client{Dial: func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", x.sock)
	}}
	e := New(Options{Dir: dir, System: x, Core: core, Checker: &checker{}, Ready: 2 * time.Second})
	t.Cleanup(func() { _ = e.Stop(context.Background()) })
	return e, x
}

var config = engine.Prepared{Revision: 3, JSON: []byte(`{"inbounds":[{"type":"tun","tag":"tun-in"}]}`), HasTun: true, TunTag: "tun-in"}

func TestStartHandsTheConfigurationToTheExtension(t *testing.T) {
	e, x := setup(t)
	var mu sync.Mutex
	var lines []string
	e.SetLogSink(func(level, msg string) {
		mu.Lock()
		lines = append(lines, level+" "+msg)
		mu.Unlock()
	})
	if err := e.Start(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	if !e.Running() || e.TunInterface() == "" {
		t.Fatalf("running %v, tun %q", e.Running(), e.TunInterface())
	}
	if len(x.started) != 1 || x.started[0] != string(config.JSON) {
		t.Fatalf("the extension ran %q", x.started)
	}
	// What the agent asks reaches the extension's core.
	if err := e.SetMode("Global"); err != nil {
		t.Fatal(err)
	}
	if cur, _, err := e.Mode(); err != nil || cur != "Global" {
		t.Fatalf("mode %q, %v", cur, err)
	}
	// Its log reaches the agent's.
	x.core.Log("info", "from the extension")
	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		got := strings.Join(lines, "\n")
		mu.Unlock()
		if strings.Contains(got, "from the extension") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the extension's log did not arrive: %q", got)
		}
		time.Sleep(20 * time.Millisecond)
	}

	if err := e.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.Running() {
		t.Fatal("still running after Stop")
	}
	if err := e.SetMode("Rule"); !errors.Is(err, engine.ErrNotRunning) {
		t.Fatalf("a call after Stop: %v, want ErrNotRunning", err)
	}
	select {
	case ev := <-e.Exited():
		t.Fatalf("a stop that was asked for reported an exit: %+v", ev)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestACrashOfTheCoreIsReportedAndEndsTheTunnel(t *testing.T) {
	e, x := setup(t)
	if err := e.Start(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	x.core.Crash(errors.New("boom"))
	select {
	case ev := <-e.Exited():
		if ev.Revoked || ev.Err == nil || !strings.Contains(ev.Err.Error(), "boom") {
			t.Fatalf("exit %+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the crash was not reported")
	}
	x.mu.Lock()
	stops := x.stops
	x.mu.Unlock()
	if stops == 0 || e.Running() {
		t.Fatalf("the tunnel stays up after its core crashed: stops %d, running %v", stops, e.Running())
	}
}

func TestTheExtensionGoingAwayIsTheSystemEndingTheVPN(t *testing.T) {
	e, x := setup(t)
	if err := e.Start(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	x.kill() // turned off in the settings: the system ends the extension
	select {
	case ev := <-e.Exited():
		if !ev.Revoked {
			t.Fatalf("exit %+v: the system ending the VPN is not a crash", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the end of the extension was not reported")
	}
}

func TestACoreThatNeverAnswersFailsTheStart(t *testing.T) {
	e, x := setup(t)
	x.serve = false
	err := e.Start(context.Background(), config)
	if err == nil || !strings.Contains(err.Error(), "did not answer") {
		t.Fatalf("start: %v", err)
	}
	if x.stops == 0 || e.Running() {
		t.Fatal("a tunnel whose core never answered is left up")
	}
}

func TestCheckStaysInTheApplication(t *testing.T) {
	e, x := setup(t)
	if err := e.Check(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	if len(x.started) != 0 || e.o.Checker.(*checker).checked != 1 {
		t.Fatal("a check must not start the tunnel")
	}
}
