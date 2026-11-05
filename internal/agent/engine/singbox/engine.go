package singbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	box "github.com/sagernet/sing-box"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/daemon"
	"github.com/sagernet/sing-box/option"
	sjson "github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/common/memory"
	"github.com/sagernet/sing/service"
	"google.golang.org/protobuf/types/known/emptypb"

	"eggbox/internal/agent/engine"
	"eggbox/internal/agent/engine/singbox/urltestgroup"
	"eggbox/internal/agentapi"
)

const (
	defaultCloseTimeout = 20 * time.Second
	minInterval         = 250 * time.Millisecond
	maxInterval         = 10 * time.Second
)

const (
	stateStarting int32 = iota
	stateRunning
	stateClosing
)

// Engine runs the embedded sing-box core. It implements engine.Engine on top
// of sing-box's own daemon service, so group, connection and traffic
// bookkeeping is the core's, not a reimplementation.
//
// Each Start creates a fresh daemon service: a failed or stopped session can
// never leak state into the next one.
type Engine struct {
	reg          RegistryOptions
	closeTimeout time.Duration

	// tunExists and tunCheckEvery drive the TUN watchdog; tests replace them.
	tunExists     func(name string) bool
	tunCheckEvery time.Duration

	// life serializes Start and Stop.
	life sync.Mutex

	mu     sync.Mutex
	sess   *session
	sink   engine.LogSink
	exited chan engine.ExitEvent
}

// New returns an idle engine. reg selects the protocols the core may use.
func New(reg RegistryOptions) *Engine {
	return &Engine{
		reg: reg, closeTimeout: defaultCloseTimeout,
		tunExists: interfaceExists, tunCheckEvery: 2 * time.Second,
		exited: make(chan engine.ExitEvent, 1),
	}
}

var _ engine.Engine = (*Engine)(nil)

type session struct {
	svc    *daemon.StartedService
	ctx    context.Context
	cancel context.CancelFunc

	state     atomic.Int32
	closing   atomic.Bool // a stop was requested (by us), so the core going away is expected
	crashed   atomic.Bool
	closeOnce sync.Once
	closed    chan struct{}
	closeErr  error

	tun string
}

// Version reports the pinned core version.
func (e *Engine) Version() string { return coreVersion() }

func coreVersion() string {
	if C.Version != "" && C.Version != "unknown" {
		return C.Version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range info.Deps {
			if dep.Path == "github.com/sagernet/sing-box" {
				return strings.TrimPrefix(dep.Version, "v")
			}
		}
	}
	return "unknown"
}

func init() {
	// The core puts its version into user agents and the daemon API; without
	// linker flags it would say "unknown".
	if C.Version == "unknown" {
		if v := coreVersion(); v != "unknown" {
			C.Version = v
		}
	}
}

// SetLogSink installs the receiver of the core's log lines.
func (e *Engine) SetLogSink(s engine.LogSink) {
	e.mu.Lock()
	e.sink = s
	e.mu.Unlock()
}

func (e *Engine) logSink() engine.LogSink {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.sink
}

// Exited delivers an event when a running core dies without being asked to.
func (e *Engine) Exited() <-chan engine.ExitEvent { return e.exited }

// ---------------------------------------------------------------- check

// Check builds the whole configuration (every inbound, outbound, DNS server,
// rule and rule-set file), resolves the references between them (selector
// members, detours, rule targets, rule-sets) and throws it away. It runs the
// core's pre-start phase, which opens no listener and creates no TUN device,
// route or DNS setting: the inbounds are never started. It is independent of a
// core that is currently running, with one exception the caller owns: the
// cache database is locked by whoever opens it, so a check must be given a
// cache path of its own (the controller's dry runs do).
func (e *Engine) Check(ctx context.Context, p engine.Prepared) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("the core crashed while checking the configuration: %v", r)
		}
	}()
	cctx := service.ExtendContext(Context(ctx, e.reg))
	options, err := sjson.UnmarshalExtendedContext[option.Options](cctx, p.JSON)
	if err != nil {
		return fmt.Errorf("decode config: %w", err)
	}
	if err := checkCachePath(p.JSON, false); err != nil {
		return err
	}
	// A check is not a session: nothing it logs belongs in the log.
	options.Log = &option.LogOptions{Disabled: true}
	cctx, cancel := context.WithCancel(cctx)
	defer cancel()
	instance, err := box.New(box.Options{Context: cctx, Options: options})
	if err != nil {
		return err
	}
	// PreStart closes the instance itself when it fails; closing again is harmless.
	defer func() { _ = instance.Close() }()
	return instance.PreStart()
}

// ---------------------------------------------------------------- lifecycle

// Start boots the core and returns once it is serving. A cancelled ctx aborts
// the start and leaves nothing running.
func (e *Engine) Start(ctx context.Context, p engine.Prepared) error {
	e.life.Lock()
	defer e.life.Unlock()

	e.mu.Lock()
	busy := e.sess != nil
	e.mu.Unlock()
	if busy {
		return errors.New("the previous core is still shutting down")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkCachePath(p.JSON, true); err != nil {
		return err
	}

	cfg, err := quietConsole(p.JSON)
	if err != nil {
		return fmt.Errorf("prepare config: %w", err)
	}

	sctx, cancel := context.WithCancel(Context(context.Background(), e.reg))
	svc := daemon.NewStartedService(daemon.ServiceOptions{Context: sctx, Handler: platformHandler{}, LogMaxLines: 2000})
	s := &session{svc: svc, ctx: sctx, cancel: cancel, closed: make(chan struct{})}
	s.state.Store(stateStarting)
	e.mu.Lock()
	e.sess = s
	e.mu.Unlock()
	e.forwardLogs(s)

	startErr := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				startErr <- fmt.Errorf("the core crashed while starting: %v", r)
			}
		}()
		startErr <- svc.StartOrReloadService(context.Background(), string(cfg), nil)
	}()

	select {
	case err = <-startErr:
	case <-ctx.Done():
		// CloseService interrupts a start in progress and waits for it to unwind.
		closed := make(chan struct{})
		go func() { _ = svc.CloseService(); close(closed) }()
		select {
		case <-closed:
			<-startErr
		case <-time.After(e.closeTimeout):
		}
		err = ctx.Err()
	}
	if err == nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	if err == nil && svc.Instance() == nil {
		err = errors.New("the core did not start")
	}
	if err != nil {
		e.beginClose(s)
		wctx, wcancel := context.WithTimeout(context.Background(), e.closeTimeout)
		cerr := e.waitClosed(wctx, s)
		wcancel()
		if cerr != nil {
			err = fmt.Errorf("%w (and releasing it failed: %v)", err, cerr)
		}
		return err
	}

	tun := discoverTun(ctx, p)
	e.mu.Lock()
	s.tun = tun
	e.mu.Unlock()
	s.state.Store(stateRunning)
	if tun != "" {
		go e.watchTun(s, tun)
	}
	return nil
}

// Stop shuts the core down and returns after everything it held is released.
func (e *Engine) Stop(ctx context.Context) error {
	e.life.Lock()
	defer e.life.Unlock()
	e.mu.Lock()
	s := e.sess
	e.mu.Unlock()
	if s == nil {
		return nil
	}
	e.beginClose(s)
	return e.waitClosed(ctx, s)
}

func (e *Engine) waitClosed(ctx context.Context, s *session) error {
	select {
	case <-s.closed:
		return s.closeErr
	case <-ctx.Done():
		return fmt.Errorf("the core did not stop in time: %w", ctx.Err())
	}
}

// beginClose starts releasing the session exactly once. The release continues
// in the background even when a caller stops waiting for it, and the engine
// reports Running until it is really finished.
func (e *Engine) beginClose(s *session) {
	s.closeOnce.Do(func() {
		s.closing.Store(true)
		s.state.Store(stateClosing)
		go func() {
			var err error
			func() {
				defer func() {
					if r := recover(); r != nil {
						err = fmt.Errorf("the core crashed while stopping: %v", r)
					}
				}()
				err = s.svc.CloseService()
				s.svc.Close()
			}()
			s.cancel()
			s.closeErr = err
			e.mu.Lock()
			if e.sess == s {
				e.sess = nil
			}
			e.mu.Unlock()
			close(s.closed)
		}()
	})
}

// Running reports whether a core is serving (or still releasing what it held).
func (e *Engine) Running() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.sess != nil && e.sess.state.Load() != stateStarting
}

// TunInterface is the TUN device of the running core ("" when none).
func (e *Engine) TunInterface() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.sess == nil {
		return ""
	}
	return e.sess.tun
}

// watchTun reports the core as dead when its TUN device disappears: somebody
// removed the interface, or the driver went away. The core keeps running in
// that state but can no longer carry traffic, and the controller's answer
// (clean up, then reconnect if the user wants that) is the same as for a crash.
//
// An embedded core has no other way to die on its own: a panic takes the whole
// agent with it, and the service manager's restart plus the ledger deal with that.
func (e *Engine) watchTun(s *session, name string) {
	t := time.NewTicker(e.tunCheckEvery)
	defer t.Stop()
	misses := 0
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
		}
		if s.closing.Load() {
			return
		}
		if e.tunExists(name) {
			misses = 0
			continue
		}
		// Two checks in a row: an interface can flicker while the OS reconfigures it.
		if misses++; misses < 2 {
			continue
		}
		if s.crashed.CompareAndSwap(false, true) {
			select {
			case e.exited <- engine.ExitEvent{Err: fmt.Errorf("the TUN interface %s disappeared", name)}:
			default:
			}
		}
		return
	}
}

func interfaceExists(name string) bool {
	_, err := net.InterfaceByName(name)
	return err == nil
}

// forwardLogs feeds the core's log lines to the installed sink.
func (e *Engine) forwardLogs(s *session) {
	go func() {
		_ = s.svc.SubscribeLog(&emptypb.Empty{}, newSink(s.ctx, func(m *daemon.Log) error {
			if m.Reset_ {
				return nil
			}
			sink := e.logSink()
			if sink == nil {
				return nil
			}
			for _, line := range m.Messages {
				if text := cleanLine(line.Message); text != "" {
					sink(coreLevel(line.Level), text)
				}
			}
			return nil
		}))
	}()
}

// checkCachePath makes sure the core's cache database lands where the agent
// chose. A configuration without an absolute path would make the core create
// "cache.db" in whatever directory the process happens to run in, which is the
// system directory for a Windows service and the read-only root for a macOS
// daemon. The policy always sets the path; this is the engine refusing to
// depend on that. A running core always has a cache database, so Start
// requires the path; a check only needs it when the cache is enabled.
func checkCachePath(raw []byte, required bool) error {
	var doc struct {
		Experimental struct {
			CacheFile *struct {
				Enabled bool   `json:"enabled"`
				Path    string `json:"path"`
			} `json:"cache_file"`
		} `json:"experimental"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("decode config: %w", err)
	}
	cf := doc.Experimental.CacheFile
	if cf == nil || (!required && !cf.Enabled) {
		if required {
			return errors.New("experimental.cache_file.path must be set to an absolute path")
		}
		return nil
	}
	if !filepath.IsAbs(cf.Path) {
		return fmt.Errorf("experimental.cache_file.path must be an absolute path, got %q", cf.Path)
	}
	return nil
}

// quietConsole makes the core write its console output to the null device. The
// lines reach the agent's log hub through the platform writer instead; letting
// the core also print them would only fill whatever the service manager
// captures stderr into.
func quietConsole(raw []byte) ([]byte, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, err
	}
	logObj := map[string]any{}
	if r, ok := root["log"]; ok {
		if err := json.Unmarshal(r, &logObj); err != nil {
			return nil, fmt.Errorf("log section: %w", err)
		}
	}
	logObj["output"] = os.DevNull
	enc, err := json.Marshal(logObj)
	if err != nil {
		return nil, err
	}
	root["log"] = enc
	return json.Marshal(root)
}

// ---------------------------------------------------------------- TUN discovery

// discoverTun finds the interface the core created by the addresses the
// configuration gave it, which works the same way on every platform (macOS
// picks utunN itself).
func discoverTun(ctx context.Context, p engine.Prepared) string {
	if !p.HasTun {
		return ""
	}
	addrs, fixedName := tunTargets(p.JSON, p.TunTag)
	deadline := time.Now().Add(3 * time.Second)
	for {
		if name := interfaceWith(addrs); name != "" {
			return name
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return fixedName
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func tunTargets(raw []byte, tag string) (addrs []netip.Addr, name string) {
	var doc struct {
		Inbounds []struct {
			Type          string   `json:"type"`
			Tag           string   `json:"tag"`
			Address       []string `json:"address"`
			InterfaceName string   `json:"interface_name"`
		} `json:"inbounds"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return nil, ""
	}
	for _, in := range doc.Inbounds {
		if in.Type != "tun" || (tag != "" && in.Tag != tag) {
			continue
		}
		for _, a := range in.Address {
			if prefix, err := netip.ParsePrefix(a); err == nil {
				addrs = append(addrs, prefix.Addr())
			}
		}
		return addrs, in.InterfaceName
	}
	return nil, ""
}

func interfaceWith(want []netip.Addr) string {
	if len(want) == 0 {
		return ""
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, ifc := range ifaces {
		list, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range list {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			got, ok := netip.AddrFromSlice(ipn.IP)
			if !ok {
				continue
			}
			got = got.Unmap()
			for _, w := range want {
				if got == w.Unmap() {
					return ifc.Name
				}
			}
		}
	}
	return ""
}

// ---------------------------------------------------------------- observation

func (e *Engine) running() (*session, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.sess == nil || e.sess.state.Load() != stateRunning {
		return nil, engine.ErrNotRunning
	}
	return e.sess, nil
}

// follow runs a daemon streaming call in the background and returns its
// converted messages. The stream ends when ctx is cancelled or the session
// does, and out is closed after the last message.
func follow[In, Out any](ctx context.Context, s *session, call func(ctx context.Context, send func(*In) error) error, convert func(*In) *Out) <-chan *Out {
	out := make(chan *Out)
	cctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	go func() {
		defer close(out)
		defer stop()
		defer cancel()
		_ = call(cctx, func(m *In) error {
			select {
			case out <- convert(m):
				return nil
			case <-cctx.Done():
				return cctx.Err()
			}
		})
	}()
	return out
}

func clampInterval(d time.Duration) time.Duration {
	switch {
	case d <= 0:
		return time.Second
	case d < minInterval:
		return minInterval
	case d > maxInterval:
		return maxInterval
	}
	return d
}

// WatchGroups streams the outbound groups whenever they or their URL test
// results change.
func (e *Engine) WatchGroups(ctx context.Context) (<-chan *agentapi.Groups, error) {
	s, err := e.running()
	if err != nil {
		return nil, err
	}
	return follow(ctx, s, func(ctx context.Context, send func(*daemon.Groups) error) error {
		return s.svc.SubscribeGroups(&emptypb.Empty{}, newSink(ctx, send))
	}, convertGroups), nil
}

// WatchTraffic streams throughput and counters at the given interval. The
// first sample carries zero throughput; after that Uplink and Downlink are the
// bytes moved since the previous sample.
//
// It reads the core's traffic manager directly instead of using the daemon's
// status stream, which also reads an unsynchronized connection counter.
func (e *Engine) WatchTraffic(ctx context.Context, interval time.Duration) (<-chan *agentapi.Traffic, error) {
	s, err := e.running()
	if err != nil {
		return nil, err
	}
	interval = clampInterval(interval)
	out := make(chan *agentapi.Traffic)
	cctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	go func() {
		defer close(out)
		defer stop()
		defer cancel()
		tick := time.NewTicker(interval)
		defer tick.Stop()
		var lastUp, lastDown int64
		for first := true; ; first = false {
			sample := s.traffic()
			if sample.Available {
				if !first {
					sample.Uplink, sample.Downlink = sample.UplinkTotal-lastUp, sample.DownlinkTotal-lastDown
				}
				lastUp, lastDown = sample.UplinkTotal, sample.DownlinkTotal
			}
			select {
			case out <- sample:
			case <-cctx.Done():
				return
			}
			select {
			case <-tick.C:
			case <-cctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// traffic samples the core's counters.
func (s *session) traffic() *agentapi.Traffic {
	t := &agentapi.Traffic{
		Memory: memory.Total(), Goroutines: int32(runtime.NumGoroutine()), TimeUnixMs: time.Now().UnixMilli(),
	}
	if inst := s.svc.Instance(); inst != nil {
		if tm := inst.TrafficManager(); tm != nil {
			t.Available = true
			t.UplinkTotal, t.DownlinkTotal = tm.Total()
			t.Connections = int32(tm.ConnectionsLen())
		}
	}
	return t
}

// WatchConnections streams connection events (the first message resets the client's view).
func (e *Engine) WatchConnections(ctx context.Context, interval time.Duration) (<-chan *agentapi.ConnectionEvents, error) {
	s, err := e.running()
	if err != nil {
		return nil, err
	}
	req := &daemon.SubscribeConnectionsRequest{Interval: int64(clampInterval(interval))}
	return follow(ctx, s, func(ctx context.Context, send func(*daemon.ConnectionEvents) error) error {
		return s.svc.SubscribeConnections(req, newSink(ctx, send))
	}, convertConnectionEvents), nil
}

// SelectOutbound switches a selector group.
func (e *Engine) SelectOutbound(group, outbound string) error {
	s, err := e.running()
	if err != nil {
		return err
	}
	_, err = s.svc.SelectOutbound(s.ctx, &daemon.SelectOutboundRequest{GroupTag: group, OutboundTag: outbound})
	return mapErr(err)
}

// URLTest starts a latency test of an outbound or a whole group. Results show
// up in the group stream. The measurement is urltestgroup's (the round trip
// on an open connection), the same one the automatic groups use; the daemon's
// own call, which times a new connection, only serves a configuration
// without such a group.
func (e *Engine) URLTest(ctx context.Context, tag string) error {
	s, err := e.running()
	if err != nil {
		return err
	}
	if inst := s.svc.Instance(); inst != nil && inst.Box() != nil {
		err := urltestgroup.Test(inst.Box().Outbound(), tag)
		switch {
		case err == nil:
			return nil
		case errors.Is(err, urltestgroup.ErrUnknownOutbound):
			return fmt.Errorf("%w: %v", engine.ErrNotFound, err)
		case !errors.Is(err, urltestgroup.ErrNoGroup):
			return err
		}
	}
	_, err = s.svc.URLTest(ctx, &daemon.URLTestRequest{OutboundTag: tag})
	return mapErr(err)
}

// Mode returns the current Clash-style mode and the available ones.
func (e *Engine) Mode() (string, []string, error) {
	s, err := e.running()
	if err != nil {
		return "", nil, err
	}
	st, err := s.svc.GetClashModeStatus(s.ctx, &emptypb.Empty{})
	if err != nil {
		return "", nil, mapErr(err)
	}
	return st.CurrentMode, st.ModeList, nil
}

// SetMode switches the mode.
func (e *Engine) SetMode(mode string) error {
	s, err := e.running()
	if err != nil {
		return err
	}
	_, all, err := e.Mode()
	if err != nil {
		return err
	}
	found := false
	for _, m := range all {
		if m == mode {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("%w: unknown mode %q", engine.ErrInvalid, mode)
	}
	_, err = s.svc.SetClashMode(s.ctx, &daemon.ClashMode{Mode: mode})
	return mapErr(err)
}

// CloseConnection closes one tracked connection.
func (e *Engine) CloseConnection(id string) error {
	s, err := e.running()
	if err != nil {
		return err
	}
	_, err = s.svc.CloseConnection(s.ctx, &daemon.CloseConnectionRequest{Id: id})
	return mapErr(err)
}

// CloseAllConnections drops every connection.
func (e *Engine) CloseAllConnections() error {
	s, err := e.running()
	if err != nil {
		return err
	}
	_, err = s.svc.CloseAllConnections(s.ctx, &emptypb.Empty{})
	return mapErr(err)
}
