// Package fake is a programmable engine for tests. It records every call,
// can fail, hang or crash on demand, and serves canned observation data.
package fake

import (
	"context"
	"fmt"
	"sync"
	"time"

	"eggbox/internal/agent/engine"
	"eggbox/internal/agentapi"
)

// Engine implements engine.Engine.
type Engine struct {
	mu       sync.Mutex
	running  bool
	runDone  chan struct{} // closed when the current run ends (stop or crash)
	prepared engine.Prepared
	sink     engine.LogSink
	exited   chan engine.ExitEvent
	calls    []string
	started  []engine.Prepared

	checkErr   error
	startErrs  []error
	startHook  func(engine.Prepared) error
	startBlock chan struct{}
	stopErr    error
	tunName    string

	mode      string
	modes     []string
	groups    *agentapi.Groups
	groupSubs map[chan *agentapi.Groups]struct{}
	conns     map[string]*agentapi.Connection
	connSubs  map[chan *agentapi.ConnectionEvents]struct{}
	uplink    int64
	downlink  int64
}

// New returns an engine that starts successfully and reports a TUN named "utun-fake".
func New() *Engine {
	return &Engine{
		exited:  make(chan engine.ExitEvent, 1),
		tunName: "utun-fake",
		mode:    "Rule",
		modes:   []string{"Rule", "Global", "Direct"},
		groups: &agentapi.Groups{Groups: []*agentapi.Group{{
			Tag: "proxy", Type: "selector", Selectable: true, Selected: "node-a",
			Items: []*agentapi.GroupItem{{Tag: "node-a", Type: "shadowsocks", DelayMs: 80}, {Tag: "node-b", Type: "vmess"}},
		}}},
		conns:     map[string]*agentapi.Connection{},
		connSubs:  map[chan *agentapi.ConnectionEvents]struct{}{},
		groupSubs: map[chan *agentapi.Groups]struct{}{},
	}
}

// endRunLocked ends the current run: observation streams see it and finish.
func (e *Engine) endRunLocked() {
	if e.runDone != nil {
		close(e.runDone)
		e.runDone = nil
	}
}

func (e *Engine) record(format string, args ...any) {
	e.calls = append(e.calls, fmt.Sprintf(format, args...))
}

// ---------------------------------------------------------------- programming

// FailNextStart makes the next Start call return err.
func (e *Engine) FailNextStart(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.startErrs = append(e.startErrs, err)
}

// FailStartWhen installs a hook consulted on every Start; a non-nil result fails it.
func (e *Engine) FailStartWhen(fn func(engine.Prepared) error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.startHook = fn
}

// FailCheck makes Check return err until cleared with nil.
func (e *Engine) FailCheck(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.checkErr = err
}

// FailStop makes Stop return err until cleared with nil. The core still stops.
func (e *Engine) FailStop(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.stopErr = err
}

// BlockStart makes Start wait until the returned release func is called or the
// start context is cancelled.
func (e *Engine) BlockStart() (release func()) {
	e.mu.Lock()
	defer e.mu.Unlock()
	ch := make(chan struct{})
	e.startBlock = ch
	var once sync.Once
	return func() { once.Do(func() { close(ch) }) }
}

// SetTunName changes the interface name reported for configurations with a TUN.
func (e *Engine) SetTunName(name string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.tunName = name
}

// Crash simulates the core dying on its own.
func (e *Engine) Crash(err error) {
	e.mu.Lock()
	wasRunning := e.running
	e.running = false
	e.endRunLocked()
	e.mu.Unlock()
	if wasRunning {
		select {
		case e.exited <- engine.ExitEvent{Err: err}:
		default:
		}
	}
}

// Revoke simulates a phone's system ending the VPN (the person turned it off
// in the settings).
func (e *Engine) Revoke(err error) {
	e.mu.Lock()
	wasRunning := e.running
	e.running = false
	e.endRunLocked()
	e.mu.Unlock()
	if wasRunning {
		select {
		case e.exited <- engine.ExitEvent{Err: err, Revoked: true}:
		default:
		}
	}
}

// Calls returns the recorded call log.
func (e *Engine) Calls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.calls...)
}

// Started returns every configuration Start accepted, in order.
func (e *Engine) Started() []engine.Prepared {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]engine.Prepared(nil), e.started...)
}

// Current returns the configuration the running core uses.
func (e *Engine) Current() engine.Prepared {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.prepared
}

// Log pushes a line through the installed sink.
func (e *Engine) Log(level, msg string) {
	e.mu.Lock()
	sink := e.sink
	e.mu.Unlock()
	if sink != nil {
		sink(level, msg)
	}
}

// SetGroups replaces the group data and notifies watchers.
func (e *Engine) SetGroups(g *agentapi.Groups) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.groups = g
	for ch := range e.groupSubs {
		select {
		case ch <- g:
		default:
		}
	}
}

// AddConnection registers a live connection: new watchers see it in their
// first (reset) message, current ones get a NEW event.
func (e *Engine) AddConnection(c *agentapi.Connection) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.conns[c.Id] = c
	e.emitLocked(&agentapi.ConnectionEvent{Type: agentapi.ConnectionEvent_TYPE_NEW, Id: c.Id, Connection: c})
}

// emitLocked tells the current connection watchers about an event.
func (e *Engine) emitLocked(ev *agentapi.ConnectionEvent) {
	for ch := range e.connSubs {
		select {
		case ch <- &agentapi.ConnectionEvents{Events: []*agentapi.ConnectionEvent{ev}}:
		default:
		}
	}
}

// ----------------------------------------------------------------- engine API

func (e *Engine) Version() string { return "fake-1.0" }

func (e *Engine) SetLogSink(s engine.LogSink) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.sink = s
}

func (e *Engine) Check(_ context.Context, p engine.Prepared) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.record("check rev=%d", p.Revision)
	return e.checkErr
}

func (e *Engine) Start(ctx context.Context, p engine.Prepared) error {
	e.mu.Lock()
	e.record("start rev=%d tun=%v", p.Revision, p.HasTun)
	if e.running {
		e.mu.Unlock()
		return fmt.Errorf("fake engine: start while running")
	}
	block := e.startBlock
	e.startBlock = nil
	var err error
	if len(e.startErrs) > 0 {
		err = e.startErrs[0]
		e.startErrs = e.startErrs[1:]
	}
	hook := e.startHook
	e.mu.Unlock()

	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			e.mu.Lock()
			e.record("start-aborted rev=%d", p.Revision)
			e.mu.Unlock()
			return ctx.Err()
		}
	}
	if err == nil && hook != nil {
		err = hook(p)
	}
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	e.running = true
	e.runDone = make(chan struct{})
	e.prepared = p
	e.started = append(e.started, p)
	return nil
}

func (e *Engine) Stop(context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.record("stop")
	e.running = false
	e.endRunLocked()
	e.prepared = engine.Prepared{}
	return e.stopErr
}

func (e *Engine) Running() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.running
}

func (e *Engine) Exited() <-chan engine.ExitEvent { return e.exited }

func (e *Engine) TunInterface() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.running && e.prepared.HasTun {
		return e.tunName
	}
	return ""
}

func (e *Engine) requireRunning() error {
	if !e.running {
		return engine.ErrNotRunning
	}
	return nil
}

func (e *Engine) WatchGroups(ctx context.Context) (<-chan *agentapi.Groups, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.requireRunning(); err != nil {
		return nil, err
	}
	ch := make(chan *agentapi.Groups, 4)
	ch <- e.groups
	e.groupSubs[ch] = struct{}{}
	done := e.runDone
	go func() {
		select {
		case <-ctx.Done():
		case <-done:
		}
		e.mu.Lock()
		delete(e.groupSubs, ch)
		close(ch)
		e.mu.Unlock()
	}()
	return ch, nil
}

func (e *Engine) WatchTraffic(ctx context.Context, interval time.Duration) (<-chan *agentapi.Traffic, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.requireRunning(); err != nil {
		return nil, err
	}
	out := make(chan *agentapi.Traffic, 1)
	done := e.runDone
	go func() {
		defer close(out)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			e.mu.Lock()
			e.uplink += 100
			e.downlink += 1000
			msg := &agentapi.Traffic{Uplink: 100, Downlink: 1000, UplinkTotal: e.uplink, DownlinkTotal: e.downlink, Available: true, TimeUnixMs: time.Now().UnixMilli()}
			e.mu.Unlock()
			select {
			case out <- msg:
			case <-ctx.Done():
				return
			case <-done:
				return
			}
			select {
			case <-t.C:
			case <-ctx.Done():
				return
			case <-done:
				return
			}
		}
	}()
	return out, nil
}

func (e *Engine) WatchConnections(ctx context.Context, _ time.Duration) (<-chan *agentapi.ConnectionEvents, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.requireRunning(); err != nil {
		return nil, err
	}
	out := make(chan *agentapi.ConnectionEvents, 1)
	reset := &agentapi.ConnectionEvents{Reset_: true}
	for _, c := range e.conns {
		reset.Events = append(reset.Events, &agentapi.ConnectionEvent{Type: agentapi.ConnectionEvent_TYPE_NEW, Id: c.Id, Connection: c})
	}
	out <- reset
	live := make(chan *agentapi.ConnectionEvents, 32)
	e.connSubs[live] = struct{}{}
	done := e.runDone
	go func() {
		defer close(out)
		defer func() {
			e.mu.Lock()
			delete(e.connSubs, live)
			e.mu.Unlock()
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case ev := <-live:
				select {
				case out <- ev:
				case <-ctx.Done():
					return
				case <-done:
					return
				}
			}
		}
	}()
	return out, nil
}

func (e *Engine) SelectOutbound(group, outbound string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.requireRunning(); err != nil {
		return err
	}
	e.record("select %s=%s", group, outbound)
	for _, g := range e.groups.Groups {
		if g.Tag != group {
			continue
		}
		if !g.Selectable {
			return fmt.Errorf("%w: %s is not a selector", engine.ErrInvalid, group)
		}
		for _, it := range g.Items {
			if it.Tag == outbound {
				g.Selected = outbound
				return nil
			}
		}
		return fmt.Errorf("%w: %s not in %s", engine.ErrNotFound, outbound, group)
	}
	return fmt.Errorf("%w: group %s", engine.ErrNotFound, group)
}

func (e *Engine) URLTest(_ context.Context, tag string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.requireRunning(); err != nil {
		return err
	}
	e.record("urltest %s", tag)
	return nil
}

func (e *Engine) Mode() (string, []string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.requireRunning(); err != nil {
		return "", nil, err
	}
	return e.mode, append([]string(nil), e.modes...), nil
}

func (e *Engine) SetMode(mode string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.requireRunning(); err != nil {
		return err
	}
	for _, m := range e.modes {
		if m == mode {
			e.mode = mode
			e.record("mode %s", mode)
			return nil
		}
	}
	return fmt.Errorf("%w: mode %q", engine.ErrInvalid, mode)
}

func (e *Engine) CloseConnection(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.requireRunning(); err != nil {
		return err
	}
	c, ok := e.conns[id]
	if !ok {
		return fmt.Errorf("%w: connection %s", engine.ErrNotFound, id)
	}
	delete(e.conns, id)
	e.record("close-conn %s", id)
	e.emitLocked(&agentapi.ConnectionEvent{Type: agentapi.ConnectionEvent_TYPE_CLOSED, Id: id, Connection: c})
	return nil
}

func (e *Engine) CloseAllConnections() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.requireRunning(); err != nil {
		return err
	}
	for id, c := range e.conns {
		e.emitLocked(&agentapi.ConnectionEvent{Type: agentapi.ConnectionEvent_TYPE_CLOSED, Id: id, Connection: c})
	}
	e.conns = map[string]*agentapi.Connection{}
	e.record("close-all-conns")
	return nil
}

var _ engine.Engine = (*Engine)(nil)
