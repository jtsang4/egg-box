// Package iosengine is the engine of EggBox's agent on iOS, where the core runs
// in the application's packet tunnel extension (internal/phone/iostunnel)
// and the agent in the application. Starting hands the configuration the
// policy accepted to the extension, through the App Group container, and
// asks the system for the tunnel; what the agent asks of the running core
// goes to the extension over its socket (internal/agent/engine/remote).
//
// A configuration is checked here, in the application, by the same core
// without a tunnel: the check opens nothing.
package iosengine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"eggbox/internal/agent/engine"
	"eggbox/internal/agent/engine/remote"
	"eggbox/internal/agentapi"
	"eggbox/internal/phone/iostunnel"
)

// System is the system's VPN of the application (the iosvpn native module).
type System interface {
	// Start starts the tunnel and returns once it is connected.
	Start(ctx context.Context) error
	// Stop stops the tunnel and returns once it is down.
	Stop(ctx context.Context) error
}

// Checker checks configurations: the core, without a tunnel.
type Checker interface {
	Version() string
	Check(ctx context.Context, p engine.Prepared) error
}

// Options configure an Engine.
type Options struct {
	// Dir is the App Group container.
	Dir string
	// System starts and stops the tunnel.
	System System
	// Core reaches the extension's core.
	Core *remote.Client
	// Checker checks configurations.
	Checker Checker
	// Ready bounds the wait for the extension's core after the tunnel is up.
	Ready time.Duration
}

// Engine implements engine.Engine with the extension's core.
type Engine struct {
	o Options

	life sync.Mutex // serializes Start and Stop

	mu       sync.Mutex
	sink     engine.LogSink
	running  bool
	tun      string
	stopping bool
	cancel   context.CancelFunc
	exited   chan engine.ExitEvent
}

var _ engine.Engine = (*Engine)(nil)

// New returns an idle engine.
func New(o Options) *Engine {
	if o.Ready == 0 {
		o.Ready = 15 * time.Second
	}
	return &Engine{o: o, exited: make(chan engine.ExitEvent, 1)}
}

func (e *Engine) Version() string { return e.o.Checker.Version() }

func (e *Engine) SetLogSink(s engine.LogSink) {
	e.mu.Lock()
	e.sink = s
	e.mu.Unlock()
}

func (e *Engine) Check(ctx context.Context, p engine.Prepared) error {
	return e.o.Checker.Check(ctx, p)
}

func (e *Engine) Exited() <-chan engine.ExitEvent { return e.exited }

func (e *Engine) Running() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.running
}

func (e *Engine) TunInterface() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.tun
}

// Start hands the configuration to the extension and starts the tunnel.
func (e *Engine) Start(ctx context.Context, p engine.Prepared) error {
	e.life.Lock()
	defer e.life.Unlock()
	if e.Running() {
		return errors.New("the previous core is still running")
	}
	if err := iostunnel.WritePrepared(e.o.Dir, p); err != nil {
		return fmt.Errorf("hand the configuration to the tunnel: %w", err)
	}
	if err := e.o.System.Start(ctx); err != nil {
		return fmt.Errorf("start the tunnel: %w", err)
	}
	st, err := e.waitCore(ctx)
	if err != nil {
		e.stopSystem()
		return err
	}
	wctx, cancel := context.WithCancel(context.Background())
	e.mu.Lock()
	e.running, e.tun, e.stopping, e.cancel = true, st.Tun, false, cancel
	e.mu.Unlock()
	go e.forwardLogs(wctx)
	go e.watchExit(wctx)
	return nil
}

// waitCore waits until the extension's core says it runs.
func (e *Engine) waitCore(ctx context.Context) (remote.Status, error) {
	deadline := time.Now().Add(e.o.Ready)
	var last error
	for {
		st, err := e.o.Core.Status(ctx)
		switch {
		case err == nil && st.Running:
			return st, nil
		case err == nil:
			last = errors.New("the tunnel's core is not running")
		default:
			last = err
		}
		if time.Now().After(deadline) {
			return remote.Status{}, fmt.Errorf("the tunnel's core did not answer: %w", last)
		}
		select {
		case <-ctx.Done():
			return remote.Status{}, ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
}

// forwardLogs feeds the core's lines to the agent's log.
func (e *Engine) forwardLogs(ctx context.Context) {
	lines, err := e.o.Core.Logs(ctx)
	if err != nil {
		return
	}
	for l := range lines {
		e.mu.Lock()
		sink := e.sink
		e.mu.Unlock()
		if sink != nil {
			sink(l.Level, l.Message)
		}
	}
}

// watchExit reports the core stopping without being asked to. The extension
// going away with the core still running is the system ending the VPN: the
// person turned it off in the settings, or another VPN took its place.
func (e *Engine) watchExit(ctx context.Context) {
	ev, err := e.o.Core.WaitExit(ctx)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		ev = engine.ExitEvent{Err: errors.New("the system ended the VPN"), Revoked: true}
	}
	e.mu.Lock()
	stopping := e.stopping
	e.running, e.tun = false, ""
	e.mu.Unlock()
	if stopping {
		return
	}
	if !ev.Revoked {
		// The extension stays up after its core stopped: end it.
		e.stopSystem()
	}
	select {
	case e.exited <- ev:
	default:
	}
}

func (e *Engine) stopSystem() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = e.o.System.Stop(ctx)
}

// Stop stops the tunnel.
func (e *Engine) Stop(ctx context.Context) error {
	e.life.Lock()
	defer e.life.Unlock()
	e.mu.Lock()
	e.stopping = true
	cancel := e.cancel
	e.cancel = nil
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	err := e.o.System.Stop(ctx)
	e.mu.Lock()
	e.running, e.tun = false, ""
	e.mu.Unlock()
	return err
}

// ---------------------------------------------------------------- observer

func (e *Engine) live() error {
	if !e.Running() {
		return engine.ErrNotRunning
	}
	return nil
}

func (e *Engine) WatchGroups(ctx context.Context) (<-chan *agentapi.Groups, error) {
	if err := e.live(); err != nil {
		return nil, err
	}
	return e.o.Core.WatchGroups(ctx)
}

func (e *Engine) WatchTraffic(ctx context.Context, interval time.Duration) (<-chan *agentapi.Traffic, error) {
	if err := e.live(); err != nil {
		return nil, err
	}
	return e.o.Core.WatchTraffic(ctx, interval)
}

func (e *Engine) WatchConnections(ctx context.Context, interval time.Duration) (<-chan *agentapi.ConnectionEvents, error) {
	if err := e.live(); err != nil {
		return nil, err
	}
	return e.o.Core.WatchConnections(ctx, interval)
}

func (e *Engine) SelectOutbound(group, outbound string) error {
	if err := e.live(); err != nil {
		return err
	}
	return e.o.Core.SelectOutbound(group, outbound)
}

func (e *Engine) URLTest(ctx context.Context, tag string) error {
	if err := e.live(); err != nil {
		return err
	}
	return e.o.Core.URLTest(ctx, tag)
}

func (e *Engine) Mode() (string, []string, error) {
	if err := e.live(); err != nil {
		return "", nil, err
	}
	return e.o.Core.Mode()
}

func (e *Engine) SetMode(mode string) error {
	if err := e.live(); err != nil {
		return err
	}
	return e.o.Core.SetMode(mode)
}

func (e *Engine) CloseConnection(id string) error {
	if err := e.live(); err != nil {
		return err
	}
	return e.o.Core.CloseConnection(id)
}

func (e *Engine) CloseAllConnections() error {
	if err := e.live(); err != nil {
		return err
	}
	return e.o.Core.CloseAllConnections()
}
