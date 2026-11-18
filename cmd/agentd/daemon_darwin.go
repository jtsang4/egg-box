//go:build darwin && !ios

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"eggbox/internal/agent/daemon"
	"eggbox/internal/agent/engine/singbox"
	"eggbox/internal/agent/platform/macplat"
	"eggbox/internal/agent/policy"
	"eggbox/internal/agent/server"
	"eggbox/internal/agent/transport"
	"eggbox/internal/brand"
	"eggbox/internal/macsys"
	"eggbox/internal/version"
)

// runDaemon is the service: launchd starts it, as root, when the application
// first speaks to its Mach service, or at start-up once the person has allowed
// it in System Settings. It runs until launchd asks it to stop, and puts the
// network back as it found it before it does.
func runDaemon() int {
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "agentd daemon: the system starts this as root; the application sets it up")
		return exitNotRoot
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	dir, err := dataDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "agentd daemon: the data folder is not safe:", err)
		return exitFailed
	}
	fail := func(format string, args ...any) int {
		msg := fmt.Sprintf(format, args...)
		log.Error(msg)
		_ = os.MkdirAll(filepath.Join(dir, "logs"), 0o700)
		_ = os.WriteFile(filepath.Join(dir, "logs", "start-failure.txt"),
			[]byte(time.Now().Format(time.RFC3339)+" "+msg+"\n"), 0o600)
		return exitFailed
	}

	// A program that runs as root must not be one that somebody else could
	// have changed: the application the installer put in place, in folders
	// only root can write in, or nothing.
	self, err := os.Executable()
	if err != nil {
		return fail("cannot find the service's own file: %v", err)
	}
	if err := macsys.CheckInstallLocation(self, 0); err != nil {
		return fail("the service is not installed where only administrators can change it (%v); install the application with its installer", err)
	}

	b, err := loadBridge()
	if err != nil {
		return fail("cannot load the service bridge: %v", err)
	}

	// The application must be signed by the same team as this program. A
	// build with no team (unsigned, for development) can be told apart by name
	// alone, which anybody can fake; say so rather than pretend.
	team, signed := b.SelfTeam()
	requirement, err := macsys.AppRequirement(team)
	if err != nil {
		return fail("cannot state who may connect: %v", err)
	}
	if !signed {
		log.Warn("this program carries no team identifier: callers are told apart by name only")
	}

	owners := macsys.NewOwnerStore(dir)
	drv, err := macplat.New(macplat.Options{System: b, Host: macplat.SystemHost(), Logger: log})
	if err != nil {
		return fail("cannot watch the network: %v", err)
	}
	if err := drv.FollowSystem(owners.UID); err != nil {
		_ = drv.Close()
		return fail("%v", err)
	}

	who := "(nobody yet)"
	if o, ok := owners.Owner(); ok {
		who = o.Name
	}
	reg := singbox.RegistryOptions{}
	d, err := daemon.Build(daemon.Config{
		DataDir: dir,
		Driver:  drv,
		Engine:  singbox.New(reg),
		Policy:  policy.New(singbox.PolicyOptions(reg)),
		Version: version.String(), AuthorizedUser: who,
		Logger: log,
	})
	if err != nil {
		_ = drv.Close()
		return fail("cannot build the agent: %v", err)
	}

	l, verify, err := transport.ListenXPC(b, brand.AgentLabel, requirement, transport.UIDPolicy{
		Owner: owners.UID,
		SetOwner: func(uid uint32) error {
			name, _ := b.UserName(uid)
			if err := owners.Claim(uid, name); err != nil {
				return err
			}
			log.Info("the first user to connect is now the authorized user", "uid", uid)
			drv.OwnerChanged()
			return nil
		},
		Console: b.ConsoleUser,
	})
	if err != nil {
		_ = d.Shutdown(context.Background())
		return fail("cannot listen: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := d.Start(ctx); err != nil {
		_ = l.Close()
		_ = d.Shutdown(context.Background())
		return fail("cannot start the agent: %v", err)
	}
	_ = os.Remove(filepath.Join(dir, "logs", "start-failure.txt"))

	served := make(chan error, 1)
	go func() { served <- d.Serve(l, server.NewCredentials(verify)) }()
	log.Info("the service is running", "version", version.String(), "signed", signed)

	code := exitOK
	select {
	case <-ctx.Done():
		log.Info("asked to stop")
	case err := <-served:
		log.Error("the server stopped", "error", err)
		code = exitFailed
	}
	sctx, cancel := context.WithTimeout(context.Background(), shutdownBudget)
	defer cancel()
	if err := d.Shutdown(sctx); err != nil {
		log.Error("shutdown", "error", err)
		code = exitFailed
	}
	_ = drv.Close()
	return code
}
