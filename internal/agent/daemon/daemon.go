// Package daemon assembles the agent: it opens the state directory, builds the
// controller from an engine and a platform driver, and serves the API on
// whatever listener the platform's transport provides.
//
// It is the one place that knows how the pieces fit together, shared by the
// installed service and by the development agent and the tests, which differ
// only in the driver, the engine and the transport they hand it.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jtsang4/sing-box-ui/internal/agent/controller"
	"github.com/jtsang4/sing-box-ui/internal/agent/engine"
	"github.com/jtsang4/sing-box-ui/internal/agent/ledger"
	"github.com/jtsang4/sing-box-ui/internal/agent/loghub"
	"github.com/jtsang4/sing-box-ui/internal/agent/platform"
	"github.com/jtsang4/sing-box-ui/internal/agent/policy"
	"github.com/jtsang4/sing-box-ui/internal/agent/server"
	"github.com/jtsang4/sing-box-ui/internal/agent/store"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// Config is everything the agent needs from its host process.
type Config struct {
	// DataDir is the agent's private state directory (configuration revisions,
	// ledger, settings, cache database, log files). It must not be writable by
	// unprivileged users.
	DataDir string

	Driver platform.Driver
	Engine engine.Engine
	Policy *policy.Policy

	// Version is the agent build version.
	Version string
	// AuthorizedUser is the identity the agent serves, for display.
	AuthorizedUser string
	// DevMode marks the development agent.
	DevMode bool

	// Logger receives the agent's own log lines in addition to the log hub and
	// the log file. Nil means a file-and-hub logger.
	Logger *slog.Logger

	// Tune adjusts the controller configuration (tests shorten the timings).
	Tune func(*controller.Config)
}

// Daemon is a built, not yet running agent.
type Daemon struct {
	cfg        Config
	Controller *controller.Controller
	Server     *server.Server
	Hub        *loghub.Hub

	log     *slog.Logger
	logFile *rotatingFile

	mu      sync.Mutex
	grpcSrv *grpc.Server
	started bool
}

// Build opens the state under cfg.DataDir and wires the controller and the API.
// Nothing runs until Start.
func Build(cfg Config) (*Daemon, error) {
	if cfg.DataDir == "" || !filepath.IsAbs(cfg.DataDir) {
		return nil, errors.New("daemon: the data directory must be an absolute path")
	}
	if cfg.Driver == nil || cfg.Engine == nil || cfg.Policy == nil {
		return nil, errors.New("daemon: a driver, an engine and a policy are required")
	}
	for _, dir := range []string{cfg.DataDir, filepath.Join(cfg.DataDir, "core"), filepath.Join(cfg.DataDir, "logs")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create %s: %w", dir, err)
		}
	}

	d := &Daemon{cfg: cfg, Hub: loghub.New(loghub.DefaultCapacity)}
	logger := cfg.Logger
	if logger == nil {
		lf, err := openRotating(filepath.Join(cfg.DataDir, "logs", "agent.log"), 5<<20, 3)
		if err != nil {
			return nil, err
		}
		d.logFile = lf
		logger = slog.New(d.Hub.SlogHandler(loghub.SourceAgent, slog.NewTextHandler(lf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	}

	st, err := store.Open(cfg.DataDir)
	if err != nil {
		return nil, d.fail(fmt.Errorf("open configuration store: %w", err))
	}
	led, err := ledger.Open(filepath.Join(cfg.DataDir, "ledger.json"))
	if err != nil {
		return nil, d.fail(fmt.Errorf("open ledger: %w", err))
	}
	settings, err := store.OpenFile(cfg.DataDir, "settings.json", store.DefaultSettings())
	if err != nil {
		return nil, d.fail(err)
	}
	intent, err := store.OpenFile(cfg.DataDir, "intent.json", store.Intent{})
	if err != nil {
		return nil, d.fail(err)
	}

	d.log = logger
	instance := fmt.Sprintf("agent-%d-%d", os.Getpid(), time.Now().UnixNano())
	cc := controller.Config{
		Store: st, Ledger: led, Settings: settings, Intent: intent,
		Engine: cfg.Engine, Driver: cfg.Driver, Policy: cfg.Policy,
		Hub: d.Hub, Logger: logger,
		Version: cfg.Version, AuthorizedUser: cfg.AuthorizedUser, InstanceID: instance,
		CachePath: filepath.Join(cfg.DataDir, "core", "cache.db"),
	}
	if cfg.Tune != nil {
		cfg.Tune(&cc)
	}
	if d.Controller, err = controller.New(cc); err != nil {
		return nil, d.fail(err)
	}
	if d.Server, err = server.New(server.Config{
		Controller: d.Controller, Version: cfg.Version, Driver: cfg.Driver.Name(),
		DevMode: cfg.DevMode, InstanceID: instance, Logger: logger,
	}); err != nil {
		return nil, d.fail(err)
	}
	return d, nil
}

func (d *Daemon) fail(err error) error {
	if d.logFile != nil {
		_ = d.logFile.Close()
	}
	return err
}

// Start launches the controller; startup recovery (removing what a crashed
// predecessor left on the machine) is its first job.
func (d *Daemon) Start(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.started {
		return errors.New("daemon: already started")
	}
	d.log.Info("agent starting", "version", d.cfg.Version, "driver", d.cfg.Driver.Name(),
		"core", d.cfg.Engine.Version(), "dev", d.cfg.DevMode, "pid", os.Getpid())
	if err := d.Controller.Start(ctx); err != nil {
		return err
	}
	d.started = true
	return nil
}

// Serve answers API calls on l until Shutdown. It returns nil after a Shutdown
// and the listener's error otherwise.
func (d *Daemon) Serve(l net.Listener, creds credentials.TransportCredentials) error {
	g := d.Server.GRPC(creds)
	d.mu.Lock()
	d.grpcSrv = g
	d.mu.Unlock()
	if err := g.Serve(l); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
		return err
	}
	return nil
}

// Shutdown disconnects cleanly (so the next start finds a clean machine), stops
// serving and releases the platform driver. The API stays up while the
// connection is torn down so the GUI can watch it happen.
func (d *Daemon) Shutdown(ctx context.Context) error {
	d.mu.Lock()
	was := d.started
	d.mu.Unlock()
	if was {
		d.log.Info("agent stopping")
	}
	err := d.Controller.Shutdown(ctx)

	d.mu.Lock()
	g := d.grpcSrv
	d.mu.Unlock()
	if g != nil {
		stopped := make(chan struct{})
		go func() { g.GracefulStop(); close(stopped) }()
		select {
		case <-stopped:
		case <-time.After(2 * time.Second):
			g.Stop() // streams never end on their own
		}
	}
	if cerr := d.cfg.Driver.Close(); cerr != nil && err == nil {
		err = cerr
	}
	if was {
		d.log.Info("agent stopped", "clean", err == nil)
	}
	if d.logFile != nil {
		_ = d.logFile.Close()
	}
	return err
}
