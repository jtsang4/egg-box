// Package testkit runs a complete agent in memory for tests: the real
// controller, policy, store and gRPC service, with a fake core and a simulated
// operating system, reachable over an in-memory connection. It cannot touch
// the host: there is no socket, no TUN, no route and no resolver involved.
package testkit

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/test/bufconn"

	"github.com/jtsang4/sing-box-ui/internal/agent/controller"
	"github.com/jtsang4/sing-box-ui/internal/agent/daemon"
	"github.com/jtsang4/sing-box-ui/internal/agent/engine"
	"github.com/jtsang4/sing-box-ui/internal/agent/engine/fake"
	"github.com/jtsang4/sing-box-ui/internal/agent/netwatch"
	"github.com/jtsang4/sing-box-ui/internal/agent/platform/sim"
	"github.com/jtsang4/sing-box-ui/internal/agent/policy"
	"github.com/jtsang4/sing-box-ui/internal/agent/server"
	"github.com/jtsang4/sing-box-ui/internal/agentclient"
)

// Agent is a running in-memory agent.
type Agent struct {
	Daemon *daemon.Daemon
	Engine *fake.Engine
	Driver *sim.Driver
	Dir    string

	lis     *bufconn.Listener
	allowed atomic.Bool
	peer    atomic.Value
	done    chan error
}

// Options tweaks the agent.
type Options struct {
	// Sandbox makes the simulated platform request sandbox mode.
	Sandbox bool
	// Tune adjusts the controller configuration.
	Tune func(*controller.Config)
	// Engine replaces the fake core.
	Engine engine.Engine
}

// Start boots an agent in a temporary directory and stops it when the test ends.
func Start(t testing.TB, opts Options) *Agent {
	t.Helper()
	a := &Agent{Dir: t.TempDir(), lis: bufconn.Listen(1 << 20), done: make(chan error, 1)}
	a.allowed.Store(true)
	a.peer.Store("testkit client")
	a.Engine = fake.New()
	a.Driver = sim.New(sim.Options{Sandbox: opts.Sandbox})
	var eng engine.Engine = a.Engine
	if opts.Engine != nil {
		eng = opts.Engine
	}

	d, err := daemon.Build(daemon.Config{
		DataDir: a.Dir, Driver: a.Driver, Engine: eng,
		Policy:  policy.New(policy.Options{WireGuard: true, QUIC: true, DHCP: true}),
		Version: "test", AuthorizedUser: "tester", DevMode: true,
		Logger: slog.New(slog.DiscardHandler),
		Tune: func(c *controller.Config) {
			c.Netwatch = netwatch.Config{Quiet: 20 * time.Millisecond, WakeQuiet: 40 * time.Millisecond, Max: 200 * time.Millisecond}
			c.Backoff = []time.Duration{5 * time.Millisecond, 5 * time.Millisecond}
			c.StartTimeout = 3 * time.Second
			c.StopTimeout = 3 * time.Second
			if opts.Tune != nil {
				opts.Tune(c)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	a.Daemon = d
	if err := d.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-d.Controller.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("the agent never became ready")
	}

	verify := func(net.Conn) (server.Peer, error) {
		if !a.allowed.Load() {
			return server.Peer{}, errors.New("not the authorized user")
		}
		return server.Peer{Description: a.peer.Load().(string)}, nil
	}
	go func() { a.done <- d.Serve(a.lis, server.NewCredentials(verify)) }()

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = d.Shutdown(ctx)
		select {
		case <-a.done:
		case <-time.After(5 * time.Second):
			t.Error("the API server did not stop")
		}
	})
	return a
}

// Dial connects to the agent over the in-memory transport.
func (a *Agent) Dial(ctx context.Context) (net.Conn, error) { return a.lis.DialContext(ctx) }

// Dialer is Dial as an agentclient.Dialer.
func (a *Agent) Dialer() agentclient.Dialer { return a.Dial }

// SetAllowed makes the agent accept or turn away new connections.
func (a *Agent) SetAllowed(ok bool) { a.allowed.Store(ok) }

// SetPeer changes the identity the agent reports for new connections.
func (a *Agent) SetPeer(desc string) { a.peer.Store(desc) }

// Client returns a started client of the agent, closed with the test.
func (a *Agent) Client(t testing.TB) *agentclient.Client {
	t.Helper()
	c := agentclient.New(a.Dialer(), agentclient.Options{
		Version: "test-client",
		Backoff: func(int) time.Duration { return 20 * time.Millisecond },
	})
	ctx, cancel := context.WithCancel(context.Background())
	c.Start(ctx)
	t.Cleanup(func() { cancel(); c.Close() })
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	if err := c.WaitReady(ctx2); err != nil {
		t.Fatalf("the client never became ready: %v (%s)", err, c.Link().Describe())
	}
	return c
}
