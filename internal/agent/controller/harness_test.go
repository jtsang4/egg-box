package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jtsang4/sing-box-ui/internal/agent/engine/fake"
	"github.com/jtsang4/sing-box-ui/internal/agent/ledger"
	"github.com/jtsang4/sing-box-ui/internal/agent/netwatch"
	"github.com/jtsang4/sing-box-ui/internal/agent/platform/sim"
	"github.com/jtsang4/sing-box-ui/internal/agent/policy"
	"github.com/jtsang4/sing-box-ui/internal/agent/store"
	"github.com/jtsang4/sing-box-ui/internal/agentapi"
)

const (
	tunConfig   = `{"inbounds":[{"type":"tun","tag":"tun-in"}],"outbounds":[{"type":"direct","tag":"direct"}],"route":{"final":"direct"}}`
	mixedConfig = `{"inbounds":[{"type":"mixed","tag":"mixed-in","listen_port":7890}],"outbounds":[{"type":"direct","tag":"direct"}]}`
)

type hopts struct {
	sandbox  bool
	queue    int
	maxRetry int
	settings func(*store.Settings)
}

func withSandbox(o *hopts) { o.sandbox = true }

// harness is a controller wired to a fake core and a simulated OS. Several
// harness generations can share one data directory and one OS, which is how
// restarts and crashes are simulated.
type harness struct {
	t    *testing.T
	dir  string
	opts hopts

	c        *Controller
	eng      *fake.Engine
	drv      *sim.Driver
	led      *ledger.Ledger
	st       *store.Store
	settings *store.File[store.Settings]
	intent   *store.File[store.Intent]

	abandoned bool
}

func newHarness(t *testing.T, mods ...func(*hopts)) *harness {
	t.Helper()
	o := hopts{maxRetry: 3}
	for _, m := range mods {
		m(&o)
	}
	h := &harness{t: t, dir: t.TempDir(), opts: o}
	h.drv = sim.New(sim.Options{Sandbox: o.sandbox})
	h.boot()
	t.Cleanup(h.close)
	return h
}

// boot builds a fresh controller (a new "process") on the existing data and OS.
func (h *harness) boot() {
	h.t.Helper()
	var err error
	h.eng = fake.New()
	if h.st, err = store.Open(h.dir); err != nil {
		h.t.Fatal(err)
	}
	if h.led, err = ledger.Open(filepath.Join(h.dir, "ledger.json")); err != nil {
		h.t.Fatal(err)
	}
	if h.settings, err = store.OpenFile(h.dir, "settings.json", store.DefaultSettings()); err != nil {
		h.t.Fatal(err)
	}
	if h.opts.settings != nil {
		if _, err := h.settings.Update(func(s *store.Settings) error { h.opts.settings(s); return nil }); err != nil {
			h.t.Fatal(err)
		}
	}
	if h.intent, err = store.OpenFile(h.dir, "intent.json", store.Intent{}); err != nil {
		h.t.Fatal(err)
	}
	cfg := Config{
		Store: h.st, Ledger: h.led, Settings: h.settings, Intent: h.intent,
		Engine: h.eng, Driver: h.drv,
		Policy:        policy.New(policy.Options{WireGuard: true, QUIC: true, DHCP: true}),
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version:       "test",
		CachePath:     filepath.Join(h.dir, "core", "cache.db"),
		Netwatch:      netwatch.Config{Quiet: 20 * time.Millisecond, WakeQuiet: 40 * time.Millisecond, Max: 200 * time.Millisecond},
		Backoff:       []time.Duration{5 * time.Millisecond, 5 * time.Millisecond},
		MaxReconnects: h.opts.maxRetry,
		StartTimeout:  2 * time.Second,
		StopTimeout:   2 * time.Second,
		QueueSize:     h.opts.queue,
	}
	if h.c, err = New(cfg); err != nil {
		h.t.Fatal(err)
	}
	if err := h.c.Start(context.Background()); err != nil {
		h.t.Fatal(err)
	}
	h.abandoned = false
	select {
	case <-h.c.Ready():
	case <-time.After(5 * time.Second):
		h.t.Fatal("controller never became ready")
	}
}

func (h *harness) close() {
	if h.abandoned {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = h.c.Shutdown(ctx)
}

// crash stops the controller without any cleanup, as a killed process would.
func (h *harness) crash() {
	h.t.Helper()
	h.c.cancel()
	h.c.wg.Wait()
	h.abandoned = true
}

// restart simulates the agent process starting again on the same machine.
func (h *harness) restart() {
	h.t.Helper()
	h.boot()
}

// ---------------------------------------------------------------- operations

// coreJSON is the configuration the (fake) core was last started with, with all
// insignificant whitespace removed so tests can match on `"key":value`.
func (h *harness) coreJSON() string {
	h.t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, h.eng.Current().JSON); err != nil {
		h.t.Fatalf("the core was given invalid JSON: %v", err)
	}
	return buf.String()
}

var nextID atomic.Uint64

// rid returns a request id that is unique for the whole test binary and safe to
// call from many goroutines.
func rid() string { return fmt.Sprintf("req-%d", nextID.Add(1)) }

func (h *harness) apply(cfg string, expected uint64) *agentapi.OperationResult {
	h.t.Helper()
	return h.c.ApplyConfig(context.Background(), &agentapi.ApplyConfigRequest{
		RequestId: rid(), ExpectedRevision: expected,
		Bundle: &agentapi.ConfigBundle{ConfigJson: []byte(cfg), ProfileId: "p1", ProfileName: "Profile"},
	})
}

func (h *harness) mustApply(cfg string, expected uint64) *agentapi.OperationResult {
	h.t.Helper()
	res := h.apply(cfg, expected)
	if !res.Ok {
		h.t.Fatalf("apply failed: %v %s %v", res.ErrorCode, res.Message, res.Issues)
	}
	return res
}

func (h *harness) connect() *agentapi.OperationResult {
	h.t.Helper()
	return h.c.Connect(context.Background(), &agentapi.ConnectRequest{RequestId: rid()})
}

func (h *harness) mustConnect() {
	h.t.Helper()
	if res := h.connect(); !res.Ok {
		h.t.Fatalf("connect failed: %v %s", res.ErrorCode, res.Message)
	}
}

func (h *harness) disconnect() *agentapi.OperationResult {
	h.t.Helper()
	return h.c.Disconnect(context.Background(), &agentapi.DisconnectRequest{RequestId: rid()})
}

// connected applies cfg and connects, returning once the status says so.
func (h *harness) connected(cfg string) {
	h.t.Helper()
	h.mustApply(cfg, h.st.CurrentN())
	h.mustConnect()
	h.waitConn(agentapi.ConnectionState_CONNECTION_STATE_CONNECTED)
}

// ------------------------------------------------------------------- waiting

func (h *harness) wait(what string, cond func(*agentapi.Status) bool) *agentapi.Status {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		st := h.c.Status()
		if cond(st) {
			return st
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for %s; status: conn=%v service=%v cleanup=%v(%s) rev=%d err=%q attempts=%d",
				what, st.Connection, st.Service, st.Cleanup, st.CleanupDetail, st.ActiveRevision, st.LastError, st.ReconnectAttempts)
		}
		time.Sleep(3 * time.Millisecond)
	}
}

func (h *harness) waitConn(want agentapi.ConnectionState) *agentapi.Status {
	h.t.Helper()
	return h.wait(want.String(), func(s *agentapi.Status) bool { return s.Connection == want })
}

func (h *harness) waitStarts(n int) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(h.eng.Started()) < n {
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for %d core starts, saw %d", n, len(h.eng.Started()))
		}
		time.Sleep(3 * time.Millisecond)
	}
}

// settle gives asynchronous work a moment to (not) happen.
func settle() { time.Sleep(250 * time.Millisecond) }

// -------------------------------------------------------------- assertions

func (h *harness) requireClean() {
	h.t.Helper()
	if r := h.drv.Resolvers(); len(r) != 0 {
		h.t.Fatalf("resolver entries left behind: %v", r)
	}
	if i := h.drv.Interfaces(); len(i) != 0 {
		h.t.Fatalf("interfaces left behind: %v", i)
	}
	if e := h.led.Entries(); len(e) != 0 {
		h.t.Fatalf("ledger entries left behind: %+v", e)
	}
	if h.eng.Running() {
		h.t.Fatal("the core is still running")
	}
}

func (h *harness) requireOwns() {
	h.t.Helper()
	if len(h.drv.Resolvers()) != 1 || len(h.drv.Interfaces()) != 1 || len(h.led.Entries()) != 2 {
		h.t.Fatalf("expected one resolver, one interface and two ledger entries, got %v %v %+v",
			h.drv.Resolvers(), h.drv.Interfaces(), h.led.Entries())
	}
}
