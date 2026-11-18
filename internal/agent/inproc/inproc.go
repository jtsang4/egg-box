// Package inproc runs a complete agent inside the calling process, reachable
// over an in-memory connection: the real controller, configuration policy,
// store and gRPC service, on top of a core and an operating system of the
// caller's choosing.
//
// The demonstration mode of the application uses it with the simulated core
// and the simulated platform, so the interface can be run and looked at
// without a privileged service and without touching the network. Phones use it
// for real (Production): there the application is the only process allowed to
// run the VPN, and the system, not a privileged service, owns the interface.
// Nothing in here opens a socket or changes the host by itself.
package inproc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	"google.golang.org/grpc/test/bufconn"

	"eggbox/internal/agent/controller"
	"eggbox/internal/agent/daemon"
	"eggbox/internal/agent/engine"
	"eggbox/internal/agent/netwatch"
	"eggbox/internal/agent/platform"
	"eggbox/internal/agent/platform/sim"
	"eggbox/internal/agent/policy"
	"eggbox/internal/agent/server"
)

// Options configure the agent.
type Options struct {
	// DataDir is the agent's state directory (absolute, created when missing).
	DataDir string
	// Engine is the core; required.
	Engine engine.Engine
	// Driver is the operating system; nil uses the simulated platform.
	Driver platform.Driver
	// Version is reported to the interface (default "demo").
	Version string
	// AuthorizedUser is the name shown as the one the agent serves.
	AuthorizedUser string
	// Logger receives the agent's own log lines; nil writes them to the log
	// file under DataDir and into the log buffer the interface shows.
	Logger *slog.Logger
	// Tune adjusts the controller configuration.
	Tune func(*controller.Config)
	// Policy is the configuration gate; nil allows what the demonstration's
	// simulated core does.
	Policy *policy.Policy
	// Production runs the agent as a release build does: not in development
	// mode, and with the controller's own timings.
	Production bool
}

// Agent is a running in-process agent.
type Agent struct {
	Daemon *daemon.Daemon
	Driver platform.Driver

	lis  *bufconn.Listener
	done chan error

	mu     sync.Mutex
	closed bool
}

// Start builds and starts the agent and begins serving it.
func Start(ctx context.Context, o Options) (*Agent, error) {
	if o.Engine == nil {
		return nil, errors.New("inproc: no engine")
	}
	if o.Driver == nil {
		o.Driver = sim.New(sim.Options{})
	}
	if o.Version == "" {
		o.Version = "demo"
	}
	if o.AuthorizedUser == "" {
		o.AuthorizedUser = "demo"
	}
	if o.Policy == nil {
		o.Policy = policy.New(policy.Options{WireGuard: true, QUIC: true, DHCP: true})
	}
	d, err := daemon.Build(daemon.Config{
		DataDir: o.DataDir, Driver: o.Driver, Engine: o.Engine, Policy: o.Policy,
		Version: o.Version, AuthorizedUser: o.AuthorizedUser, DevMode: !o.Production,
		Logger: o.Logger,
		Tune: func(c *controller.Config) {
			if !o.Production {
				c.Netwatch = netwatch.Config{Quiet: 300 * time.Millisecond, WakeQuiet: time.Second, Max: 3 * time.Second}
			}
			if o.Tune != nil {
				o.Tune(c)
			}
		},
	})
	if err != nil {
		return nil, err
	}
	if err := d.Start(ctx); err != nil {
		return nil, err
	}
	select {
	case <-d.Controller.Ready():
	case <-time.After(15 * time.Second):
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = d.Shutdown(sctx)
		return nil, errors.New("inproc: the agent never became ready")
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	a := &Agent{Daemon: d, Driver: o.Driver, lis: bufconn.Listen(1 << 20), done: make(chan error, 1)}
	verify := func(net.Conn) (server.Peer, error) {
		return server.Peer{Description: fmt.Sprintf("in-process (pid %d)", os.Getpid())}, nil
	}
	go func() { a.done <- d.Serve(a.lis, server.NewCredentials(verify)) }()
	return a, nil
}

// Dial connects to the agent. It is an agentclient.Dialer.
func (a *Agent) Dial(ctx context.Context) (net.Conn, error) { return a.lis.DialContext(ctx) }

// Close disconnects, stops serving and releases everything. It is safe to
// call more than once.
func (a *Agent) Close(ctx context.Context) error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	a.mu.Unlock()
	err := a.Daemon.Shutdown(ctx)
	select {
	case <-a.done:
	case <-time.After(5 * time.Second):
	case <-ctx.Done():
	}
	_ = a.lis.Close()
	return err
}
