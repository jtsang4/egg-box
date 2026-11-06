// Package boot assembles what the interface runs on: the library of
// subscriptions and rules, the rule data, the connection to the privileged
// agent and the application service on top of them.
//
// It has no windows in it. The same assembly serves three ways of running:
//
//   - Installed: the application talks to the privileged service the platform
//     installed, over the platform's transport.
//   - Dev: the application talks to a development agent (agentd dev) on a Unix
//     socket. The agent runs the real core in a sandbox that replaces the TUN
//     with a loopback proxy, so the host's network is never touched.
//   - Demo: the whole thing, agent included, runs inside this process on a
//     simulated core and an invented world. Nothing is read from or sent to
//     the network, and nothing is kept on disk.
package boot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"eggbox/internal/agent/engine/demo"
	"eggbox/internal/agent/inproc"
	"eggbox/internal/agentclient"
	"eggbox/internal/app"
	"eggbox/internal/demodata"
	"eggbox/internal/library"
	"eggbox/internal/ruledata"
	"eggbox/internal/sysproxy"
)

// Mode is how the application runs.
type Mode int

const (
	// Installed uses the privileged service of the platform.
	Installed Mode = iota
	// Dev uses a development agent over a Unix socket.
	Dev
	// Demo simulates everything in this process.
	Demo
)

func (m Mode) String() string { return [...]string{"installed", "dev", "demo"}[m] }

// Toggle is a setting kept outside the application, such as whether the
// privileged service starts together with the computer.
type Toggle struct {
	Get func() (bool, error)
	Set func(bool) error
}

// Config says how to assemble.
type Config struct {
	Mode Mode
	// DataDir holds the library and the rule data (absolute). Demo mode
	// ignores it and uses a directory of its own that is removed at the end.
	DataDir string
	// Secrets keeps the subscription links. Demo mode keeps them in memory.
	Secrets library.SecretStore
	// Dial reaches the agent (Installed and Dev).
	Dial agentclient.Dialer
	// HTTPClient downloads subscriptions and rule data; nil uses the default
	// clients. Demo mode brings its own, which only knows the invented world.
	HTTPClient *http.Client
	// Resolve looks a name up for the dialog that says where an address
	// would go; nil asks the system. Tests supply their own.
	Resolve func(ctx context.Context, host string) ([]netip.Addr, error)
	// Authorize asks the system to allow the privileged service (Installed).
	Authorize func(ctx context.Context) error
	// ServiceStartup reads and changes the service's start with the computer.
	ServiceStartup *Toggle
	// SystemProxies reads the system's manual proxy settings (Installed, on
	// platforms that have a reader); see app.Config.
	SystemProxies func() ([]sysproxy.Setting, error)
	// Version is the application's version.
	Version string
	// Fresh starts the demonstration as a first run: an empty library and a
	// service that is not allowed yet.
	Fresh bool
	// Seed is the seed of the demonstration's invented traffic; 0 is random.
	Seed uint64
	// Now is the clock.
	Now func() time.Time
	// Log receives what the assembly has to say.
	Log *slog.Logger
	// OnChange is called, from any goroutine, when something the interface
	// shows changed.
	OnChange func()
}

// Runtime is the assembled application.
type Runtime struct {
	Mode    Mode
	Service *app.Service
	Library *library.Library
	Agent   *agentclient.Client
	Rules   *ruledata.Manager
	DataDir string
	Version string
	// ServiceStartup is the service's start with the computer, when the
	// platform has such a thing.
	ServiceStartup *Toggle

	authorize func(ctx context.Context) error
	cancel    context.CancelFunc
	closers   []func() // run last to first
	once      sync.Once
}

// Authorize asks the system to allow the privileged service. It returns nil
// at once when there is nothing to ask.
func (r *Runtime) Authorize(ctx context.Context) error {
	if r.authorize == nil {
		return nil
	}
	r.Service.RecordServiceLog("info", "Requesting authorization for the background service")
	err := r.authorize(ctx)
	if err == nil {
		readyCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		if waitErr := r.Agent.WaitReady(readyCtx); waitErr != nil {
			cause := r.Agent.Link().Err
			if cause == nil {
				cause = waitErr
			}
			err = fmt.Errorf("%w: the background service was approved but could not be reached: %v", app.ErrServiceSetup, cause)
		}
	}
	if err != nil {
		r.Service.RecordServiceLog("error", "Background service authorization failed: "+err.Error())
		return err
	}
	r.Service.RecordServiceLog("info", "The background service is authorized and reachable")
	return nil
}

// Start assembles and starts everything. The context bounds the startup only;
// Close stops the running parts.
func Start(ctx context.Context, cfg Config) (*Runtime, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.Version == "" {
		cfg.Version = "dev"
	}
	rt := &Runtime{Mode: cfg.Mode, Version: cfg.Version, authorize: cfg.Authorize, ServiceStartup: cfg.ServiceStartup}
	ok := false
	defer func() {
		if !ok {
			rt.Close()
		}
	}()

	dial := cfg.Dial
	secrets := cfg.Secrets
	httpc := cfg.HTTPClient
	var mirrors func(setting, prefix string) []ruledata.Mirror
	var demoAgent atomic.Pointer[inproc.Agent]
	var startDemoAgent func(context.Context) error

	if cfg.Mode == Demo {
		dir, err := os.MkdirTemp("", "egg-box-demo-*")
		if err != nil {
			return nil, err
		}
		rt.closers = append(rt.closers, func() { _ = os.RemoveAll(dir) })
		cfg.DataDir = dir
		secrets = library.NewMemorySecrets()
		httpc = demodata.Client(cfg.Now)
		mirrors = demodata.Mirrors

		var startMu sync.Mutex
		startDemoAgent = func(ctx context.Context) error {
			startMu.Lock()
			defer startMu.Unlock()
			if demoAgent.Load() != nil {
				return nil
			}
			a, err := inproc.Start(ctx, inproc.Options{
				DataDir: filepath.Join(dir, "agent"),
				Engine:  demo.New(demo.Options{Seed: cfg.Seed, Now: cfg.Now}),
				Version: cfg.Version,
			})
			if err != nil {
				return err
			}
			demoAgent.Store(a)
			return nil
		}
		dial = func(ctx context.Context) (net.Conn, error) {
			a := demoAgent.Load()
			if a == nil {
				return nil, fmt.Errorf("the demonstration service has not been allowed yet: %w", os.ErrNotExist)
			}
			return a.Dial(ctx)
		}
		rt.closers = append(rt.closers, func() {
			if a := demoAgent.Load(); a != nil {
				cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_ = a.Close(cctx)
			}
		})
		rt.authorize = func(ctx context.Context) error {
			// The system's approval takes a moment of the user's attention.
			select {
			case <-time.After(1200 * time.Millisecond):
			case <-ctx.Done():
				return ctx.Err()
			}
			return startDemoAgent(ctx)
		}
		var atBoot atomic.Bool
		atBoot.Store(true)
		rt.ServiceStartup = &Toggle{
			Get: func() (bool, error) { return atBoot.Load(), nil },
			Set: func(on bool) error { atBoot.Store(on); return nil },
		}
	}

	if cfg.DataDir == "" || !filepath.IsAbs(cfg.DataDir) {
		return nil, errors.New("boot: the data directory must be an absolute path")
	}
	if dial == nil {
		return nil, errors.New("boot: there is no way to reach the service")
	}
	if secrets == nil {
		return nil, errors.New("boot: there is no secret store")
	}
	rt.DataDir = cfg.DataDir

	lib, err := library.Open(filepath.Join(cfg.DataDir, "library"), library.Options{Secrets: secrets, Now: cfg.Now})
	if err != nil {
		return nil, err
	}
	rt.Library = lib
	rd, err := ruledata.New(ruledata.Options{Dir: filepath.Join(cfg.DataDir, "ruledata"), Client: httpc, Now: cfg.Now})
	if err != nil {
		return nil, err
	}
	rt.Rules = rd

	backoff := (func(int) time.Duration)(nil)
	if cfg.Mode == Demo {
		backoff = func(int) time.Duration { return 250 * time.Millisecond }
	}
	client := agentclient.New(dial, agentclient.Options{Version: cfg.Version, Backoff: backoff})
	rt.Agent = client

	svc := app.New(app.Config{
		Library: lib, Rules: rd, Agent: client, Version: cfg.Version,
		Clock: cfg.Now, HTTPClient: httpc, Notify: cfg.OnChange, Mirrors: mirrors, Resolve: cfg.Resolve,
		SystemProxies: cfg.SystemProxies,
	})
	rt.Service = svc

	rctx, cancel := context.WithCancel(context.Background())
	rt.cancel = cancel
	client.Start(rctx)
	svc.Run(rctx)
	rt.closers = append(rt.closers, func() { svc.Close(); client.Close() })

	if cfg.Mode == Demo {
		if !cfg.Fresh {
			if err := startDemoAgent(ctx); err != nil {
				return nil, err
			}
			wctx, wcancel := context.WithTimeout(ctx, 20*time.Second)
			err := client.WaitReady(wctx)
			wcancel()
			if err != nil {
				return nil, fmt.Errorf("boot: the demonstration service did not answer: %w", err)
			}
			if err := demodata.Seed(ctx, svc); err != nil {
				return nil, err
			}
			if err := lib.UpdateSettings(func(s *library.Settings) { s.OnboardingDone = true }); err != nil {
				return nil, err
			}
		}
	}
	ok = true
	return rt, nil
}

// Close stops everything. It is safe to call more than once.
func (r *Runtime) Close() {
	r.once.Do(func() {
		if r.cancel != nil {
			r.cancel()
		}
		for i := len(r.closers) - 1; i >= 0; i-- {
			r.closers[i]()
		}
	})
}
