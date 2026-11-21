// Package controller is the brain of the privileged agent. It owns the
// connection lifecycle and everything that must never happen concurrently:
//
//   - every state-changing request runs through one serialized queue;
//   - requests carry an id, and replaying an id returns the first result;
//   - a new configuration is validated, stored as a revision, hot-applied and
//     rolled back to the last good revision when it does not start;
//   - what the agent creates on the machine is written to the ledger first, so
//     a crash can always be cleaned up before anything else happens;
//   - network, power and session events are coalesced into at most one
//     reconnect decision per burst.
//
// The controller talks to the proxy core through engine.Engine and to the
// operating system through platform.Driver, so all of it is testable with a
// fake core and a simulated OS.
package controller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"eggbox/internal/agent/engine"
	"eggbox/internal/agent/ledger"
	"eggbox/internal/agent/loghub"
	"eggbox/internal/agent/netwatch"
	"eggbox/internal/agent/platform"
	"eggbox/internal/agent/policy"
	"eggbox/internal/agent/routecheck"
	"eggbox/internal/agent/store"
	"eggbox/internal/agentapi"
)

// Config wires the controller to its collaborators.
type Config struct {
	Store    *store.Store
	Ledger   *ledger.Ledger
	Settings *store.File[store.Settings]
	Intent   *store.File[store.Intent]
	Engine   engine.Engine
	Driver   platform.Driver
	Policy   *policy.Policy
	Hub      *loghub.Hub
	Logger   *slog.Logger

	// Version is the agent build version shown in the status.
	Version string
	// AuthorizedUser is the identity the agent serves (display only).
	AuthorizedUser string
	// InstanceID changes on every agent start.
	InstanceID string
	// CachePath is where the core keeps its cache database (protected directory).
	CachePath string

	// Candidates are the private TUN subnets to try; defaults to routecheck.DefaultCandidates.
	Candidates []routecheck.Plan
	// Netwatch tunes event coalescing.
	Netwatch netwatch.Config
	// Backoff is the delay before each automatic reconnect attempt.
	Backoff []time.Duration
	// MaxReconnects bounds consecutive automatic attempts.
	MaxReconnects int
	// StartTimeout bounds one core start; StopTimeout bounds a teardown.
	StartTimeout time.Duration
	StopTimeout  time.Duration
	// SandboxPort fixes the loopback port used in sandbox mode (0 picks a free one).
	SandboxPort uint16
	// QueueSize bounds the pending state-changing requests (default 32).
	QueueSize int
}

func (cfg *Config) defaults() {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Hub == nil {
		cfg.Hub = loghub.New(loghub.DefaultCapacity)
	}
	if len(cfg.Candidates) == 0 {
		cfg.Candidates = routecheck.DefaultCandidates()
	}
	if len(cfg.Backoff) == 0 {
		cfg.Backoff = []time.Duration{time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second}
	}
	if cfg.MaxReconnects <= 0 {
		cfg.MaxReconnects = 8
	}
	if cfg.StartTimeout <= 0 {
		cfg.StartTimeout = 45 * time.Second
	}
	if cfg.StopTimeout <= 0 {
		cfg.StopTimeout = 20 * time.Second
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 32
	}
	if cfg.InstanceID == "" {
		cfg.InstanceID = fmt.Sprintf("agent-%d-%d", os.Getpid(), time.Now().UnixNano())
	}
}

// Controller is safe for concurrent use.
type Controller struct {
	cfg Config
	log *slog.Logger

	root   context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	queue   chan *job
	closing atomic.Bool
	ready   chan struct{}

	curMu            sync.Mutex
	curKind          string
	curCancel        context.CancelFunc
	curInterruptible bool

	validateSem chan struct{}

	idem *idemCache

	mu   sync.RWMutex
	st   state
	seq  uint64
	subs map[*statusSub]struct{}

	// generation counts core starts and stops so observers can re-subscribe.
	genMu     sync.Mutex
	gen       uint64
	genNotify chan struct{}

	retryMu    sync.Mutex
	retryTimer *time.Timer
	retrySeq   uint64

	// coreLogRank is the minimum loghub.Rank a core log line must meet to enter
	// the hub, kept in sync with settings.LogLevel so changes take effect at once.
	coreLogRank atomic.Int32
}

// New builds a controller. Nothing runs until Start.
func New(cfg Config) (*Controller, error) {
	cfg.defaults()
	switch {
	case cfg.Store == nil, cfg.Ledger == nil, cfg.Settings == nil, cfg.Intent == nil:
		return nil, errors.New("controller: store, ledger, settings and intent are required")
	case cfg.Engine == nil, cfg.Driver == nil, cfg.Policy == nil:
		return nil, errors.New("controller: engine, driver and policy are required")
	}
	c := &Controller{
		cfg:         cfg,
		log:         cfg.Logger,
		queue:       make(chan *job, cfg.QueueSize),
		ready:       make(chan struct{}),
		idem:        newIdemCache(),
		validateSem: make(chan struct{}, 2),
		subs:        map[*statusSub]struct{}{},
		genNotify:   make(chan struct{}),
	}
	c.st = c.initialState()
	c.coreLogRank.Store(int32(loghub.Rank(cfg.Settings.Load().LogLevel)))
	cfg.Engine.SetLogSink(func(level, message string) {
		if loghub.Rank(level) < int(c.coreLogRank.Load()) {
			return
		}
		cfg.Hub.Write(level, loghub.SourceCore, message)
	})
	return c, nil
}

// Logs returns the log hub (the gRPC layer streams from it).
func (c *Controller) Logs() *loghub.Hub { return c.cfg.Hub }

// Ready is closed when startup recovery finished and the service accepts work.
func (c *Controller) Ready() <-chan struct{} { return c.ready }

// Start launches the controller's goroutines. Startup recovery is the first
// job in the queue, so nothing else can run before leftovers are cleaned up.
func (c *Controller) Start(ctx context.Context) error {
	if c.root != nil {
		return errors.New("controller: already started")
	}
	c.root, c.cancel = context.WithCancel(context.WithoutCancel(ctx))

	c.wg.Add(3)
	go func() { defer c.wg.Done(); c.worker() }()
	go func() { defer c.wg.Done(); c.watchEngineExit() }()
	go func() { defer c.wg.Done(); c.watchPlatform() }()

	if !c.enqueue(&job{kind: "recover", internal: true, run: c.recoverJob, done: make(chan *agentapi.OperationResult, 1)}) {
		return errors.New("controller: could not queue recovery")
	}
	return nil
}

// Shutdown disconnects cleanly (so the next start finds a clean ledger), then
// stops the controller. The stored intent is left alone: whether to reconnect
// later is the settings' decision, not the shutdown's.
func (c *Controller) Shutdown(ctx context.Context) error {
	if c.root == nil {
		return nil
	}
	if !c.closing.CompareAndSwap(false, true) {
		return nil
	}
	c.cancelRetry()
	c.interrupt()
	c.update(func(s *state) { s.service = agentapi.ServiceState_SERVICE_STATE_STOPPING })

	var tornDown atomic.Bool
	finish := func() {
		c.teardown("agent shutdown")
		c.update(func(s *state) {
			s.conn = agentapi.ConnectionState_CONNECTION_STATE_DISCONNECTED
			s.activeRev = 0
		})
		tornDown.Store(true)
	}
	done := make(chan *agentapi.OperationResult, 1)
	var err error
	select {
	case c.queue <- &job{kind: "shutdown", internal: true, done: done, run: func(context.Context) *agentapi.OperationResult {
		finish()
		return &agentapi.OperationResult{Ok: true}
	}}:
		select {
		case <-done:
		case <-ctx.Done():
			err = ctx.Err()
		}
	case <-ctx.Done():
		// The queue is jammed and the caller is out of patience.
		err = ctx.Err()
	}
	c.cancel()
	c.wg.Wait()
	if !tornDown.Load() {
		// The worker is gone, so nothing else can touch the machine: undo
		// what the session created here rather than leaving it to the next start.
		finish()
	}
	c.failQueued()
	return err
}

// failQueued answers requests that were still waiting when the worker stopped.
func (c *Controller) failQueued() {
	for {
		select {
		case j := <-c.queue:
			j.finish(c, failure(agentapi.ErrorCode_ERROR_CODE_UNAVAILABLE, "the agent is shutting down"))
		default:
			return
		}
	}
}

// ---------------------------------------------------------------- generations

func (c *Controller) bumpGeneration() {
	c.genMu.Lock()
	c.gen++
	close(c.genNotify)
	c.genNotify = make(chan struct{})
	c.genMu.Unlock()
}

func (c *Controller) generation() (uint64, <-chan struct{}) {
	c.genMu.Lock()
	defer c.genMu.Unlock()
	return c.gen, c.genNotify
}

// ------------------------------------------------------------------- settings

// GetSettings returns the persisted settings.
func (c *Controller) GetSettings() *agentapi.Settings {
	return toProtoSettings(c.cfg.Settings.Load())
}

// UpdateSettings applies the supplied fields.
func (c *Controller) UpdateSettings(req *agentapi.UpdateSettingsRequest) (*agentapi.Settings, error) {
	if req.LogLevel != nil && !store.ValidLogLevel(req.GetLogLevel()) {
		return nil, fmt.Errorf("%w: unknown log level %q", engine.ErrInvalid, req.GetLogLevel())
	}
	next, err := c.cfg.Settings.Update(func(s *store.Settings) error {
		if req.AutoConnect != nil {
			s.AutoConnect = req.GetAutoConnect()
		}
		if req.ResumeAfterCrash != nil {
			s.ResumeAfterCrash = req.GetResumeAfterCrash()
		}
		if req.ReconnectOnNetworkChange != nil {
			s.ReconnectOnNetworkChange = req.GetReconnectOnNetworkChange()
		}
		if req.LogLevel != nil {
			s.LogLevel = req.GetLogLevel()
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	c.coreLogRank.Store(int32(loghub.Rank(next.LogLevel)))
	return toProtoSettings(next), nil
}

func toProtoSettings(s store.Settings) *agentapi.Settings {
	return &agentapi.Settings{
		AutoConnect:              s.AutoConnect,
		ResumeAfterCrash:         s.ResumeAfterCrash,
		ReconnectOnNetworkChange: s.ReconnectOnNetworkChange,
		LogLevel:                 s.LogLevel,
	}
}

// ----------------------------------------------------------------- shared bits

func (c *Controller) setIntent(connected bool, revision uint64) {
	_, err := c.cfg.Intent.Update(func(i *store.Intent) error {
		i.Connected = connected
		i.Revision = revision
		i.Updated = time.Now().UTC()
		return nil
	})
	if err != nil {
		c.log.Warn("could not persist connection intent", "err", err)
	}
	c.update(func(s *state) { s.intentConnected = connected })
}

func (c *Controller) intentConnected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.st.intentConnected
}

func (c *Controller) logAgent(level, format string, args ...any) {
	c.cfg.Hub.Write(level, loghub.SourceAgent, fmt.Sprintf(format, args...))
}
