//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows/svc"

	"eggbox/internal/agent/daemon"
	"eggbox/internal/agent/engine/singbox"
	"eggbox/internal/agent/platform"
	"eggbox/internal/agent/platform/winplat"
	"eggbox/internal/agent/policy"
	"eggbox/internal/agent/server"
	"eggbox/internal/agent/transport"
	"eggbox/internal/brand"
	"eggbox/internal/version"
	"eggbox/internal/winsys"
)

// Exit codes the service reports to the service manager.
const (
	serviceNoOwner   uint32 = 10
	serviceBadSetup  uint32 = 11
	serviceBuildFail uint32 = 12
)

// shutdownBudget is how long stopping may take: the system gives a service
// that is stopping about this long before it ends it.
const shutdownBudget = 25 * time.Second

// runService hands the process to the service manager.
func runService() int {
	isService, err := svc.IsWindowsService()
	if err != nil {
		fmt.Fprintln(os.Stderr, "agentd service:", err)
		return exitFailed
	}
	if !isService {
		fmt.Fprintln(os.Stderr, "agentd service: the system starts this; use `agentd install` to set it up")
		return exitUsage
	}
	if err := svc.Run(brand.WindowsServiceName, &handler{}); err != nil {
		return exitFailed
	}
	return exitOK
}

// handler answers the service manager.
type handler struct{}

func (h *handler) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown | svc.AcceptPowerEvent | svc.AcceptSessionChange
	status <- svc.Status{State: svc.StartPending, WaitHint: 30000}

	inst, code := startAgent()
	if inst == nil {
		return true, code
	}
	status <- svc.Status{State: svc.Running, Accepts: accepted}

	for c := range requests {
		switch c.Cmd {
		case svc.Interrogate:
			status <- c.CurrentStatus
		case svc.Stop, svc.Shutdown:
			status <- svc.Status{State: svc.StopPending, WaitHint: uint32(shutdownBudget / time.Millisecond)}
			inst.stop()
			return false, 0
		case svc.PowerEvent:
			inst.power(c.EventType)
		case svc.SessionChange:
			inst.session(c.EventType, c.EventData)
		}
	}
	inst.stop()
	return false, 0
}

// instance is the running agent.
type instance struct {
	d      *daemon.Daemon
	driver *winplat.Driver
	cancel context.CancelFunc
	served chan error

	mu      sync.Mutex
	tracker *winsys.SessionTracker
}

// startAgent builds and starts the agent. On failure it says why in a file the
// administrator can read, since a service has no console, and returns the exit
// code for the service manager.
func startAgent() (*instance, uint32) {
	dir, err := dataDir()
	if err != nil {
		return nil, serviceBadSetup
	}
	fail := func(code uint32, format string, args ...any) (*instance, uint32) {
		msg := fmt.Sprintf(format, args...)
		_ = os.MkdirAll(filepath.Join(dir, "logs"), 0o700)
		_ = os.WriteFile(filepath.Join(dir, "logs", "start-failure.txt"),
			[]byte(time.Now().Format(time.RFC3339)+" "+msg+"\n"), 0o600)
		return nil, code
	}

	owner, err := winsys.LoadOwner(dir)
	if errors.Is(err, winsys.ErrNoOwner) {
		return fail(serviceNoOwner, "no user is authorized yet; run the application and allow the service")
	}
	if err != nil {
		return fail(serviceBadSetup, "cannot read the authorized user: %v", err)
	}
	self, err := winsys.Executable()
	if err != nil {
		return fail(serviceBadSetup, "cannot find the service's own file: %v", err)
	}

	drv, err := winplat.New(winplat.Options{})
	if err != nil {
		return fail(serviceBuildFail, "cannot watch the network: %v", err)
	}
	reg := singbox.RegistryOptions{}
	d, err := daemon.Build(daemon.Config{
		DataDir: dir,
		Driver:  drv,
		Engine:  singbox.New(reg),
		Policy:  policy.New(singbox.PolicyOptions(reg)),
		Version: version.String(), AuthorizedUser: owner.Name,
	})
	if err != nil {
		_ = drv.Close()
		return fail(serviceBuildFail, "cannot build the agent: %v", err)
	}
	l, verify, err := transport.ListenPipe(transport.PipeConfig{
		OwnerSID:      owner.SID,
		Images:        []string{winsys.GUIExecutable(self)},
		RequireSigned: transport.FileIsSigned(self),
	})
	if err != nil {
		_ = d.Shutdown(context.Background())
		return fail(serviceBuildFail, "cannot open the pipe: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	if err := d.Start(ctx); err != nil {
		cancel()
		_ = l.Close()
		_ = d.Shutdown(context.Background())
		return fail(serviceBuildFail, "cannot start the agent: %v", err)
	}
	_ = os.Remove(filepath.Join(dir, "logs", "start-failure.txt"))

	inst := &instance{d: d, driver: drv, cancel: cancel, served: make(chan error, 1)}
	go func() { inst.served <- d.Serve(l, server.NewCredentials(verify)) }()

	// Is the user already at the computer? (The service was started by hand,
	// or restarted, rather than by the computer starting up.)
	present := false
	var session uint32
	if who, s, err := winsys.ConsoleUser(); err == nil && who.SID == owner.SID {
		present, session = true, s
	}
	inst.tracker = winsys.NewSessionTracker(owner.SID, present, session)
	if present {
		drv.Inject(platform.UserLoggedIn, "the user was already signed in")
	}
	return inst, 0
}

// power passes a sleep or resume on to the agent.
func (i *instance) power(eventType uint32) {
	if kind, ok := winplat.PowerEvent(eventType); ok {
		i.driver.Inject(kind, "")
	}
}

// session follows who is signed in and tells the agent when its user arrives
// or leaves. data is the pointer the service manager gave with the event.
func (i *instance) session(eventType uint32, data uintptr) {
	if data == 0 {
		return
	}
	// WTSSESSION_NOTIFICATION { DWORD cbSize; DWORD dwSessionId; }. The system
	// owns the memory; it is read at once, before the handler returns.
	p := *(*unsafe.Pointer)(unsafe.Pointer(&data))
	id := (*[2]uint32)(p)[1]
	i.mu.Lock()
	changed, present := i.tracker.Apply(eventType, id, func(session uint32) (string, error) {
		o, err := winsys.UserOfSession(session)
		return o.SID, err
	})
	i.mu.Unlock()
	if !changed {
		return
	}
	if present {
		i.driver.Inject(platform.UserLoggedIn, "")
	} else {
		i.driver.Inject(platform.UserLoggedOut, "")
	}
}

// stop disconnects, cleans up and ends the agent.
func (i *instance) stop() {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownBudget)
	defer cancel()
	_ = i.d.Shutdown(ctx)
	i.cancel()
}
