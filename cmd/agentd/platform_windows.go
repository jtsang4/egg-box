//go:build windows

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"

	"github.com/jtsang4/sing-box-ui/internal/agent/ledger"
	"github.com/jtsang4/sing-box-ui/internal/agent/platform/winplat"
	"github.com/jtsang4/sing-box-ui/internal/winsys"
)

const platformUsage = `  service      run as the Windows service (the system starts it this way)
  install      register the service and its protected data folder (administrator)
  uninstall    disconnect, clean up and remove the service; --purge also removes its data (administrator)
  authorize    allow a user to control the service, then restart it (administrator)
               --sid S-1-5-... --name DOMAIN\user; without them, the user signed in here
               --if-unowned: only when nobody is allowed yet (an upgrade changes nothing)
  set-boot     on | off: whether the service starts with the computer (administrator)
  start        start the service (administrator)
  stop         stop the service: disconnect and clean up first (administrator)
  status       say whether the service is installed and running
  cleanup      remove what a service that died left behind (administrator)
`

// Exit codes an elevated run reports; the application only sees these.
const (
	exitOK          = 0
	exitFailed      = 1
	exitUsage       = 2
	exitNotElevated = 3
)

// runPlatform runs a Windows-specific command.
func runPlatform(args []string) (int, bool) {
	switch args[0] {
	case "service":
		return runService(), true
	case "status":
		return cmdStatus(), true
	case "install", "uninstall", "authorize", "set-boot", "start", "stop", "cleanup":
		return runAdmin(args[0], args[1:]), true
	}
	return 0, false
}

// runAdmin runs a command that changes the machine. It refuses to run without
// administrator rights and keeps a record of what it did next to the service's
// own logs.
func runAdmin(name string, args []string) int {
	if !windows.GetCurrentProcessToken().IsElevated() {
		fmt.Fprintf(os.Stderr, "agentd %s: this needs administrator rights\n", name)
		return exitNotElevated
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	note := adminLog()
	note("agentd " + name + " " + strings.Join(args, " "))
	var err error
	switch name {
	case "install":
		err = cmdInstall()
	case "uninstall":
		err = cmdUninstall(ctx, args)
	case "authorize":
		err = cmdAuthorize(args)
	case "set-boot":
		err = cmdSetBoot(args)
	case "start":
		err = winsys.Start()
	case "stop":
		err = winsys.Stop()
	case "cleanup":
		err = cmdCleanup(ctx)
	}
	if err != nil {
		note("failed: " + err.Error())
		fmt.Fprintf(os.Stderr, "agentd %s: %v\n", name, err)
		if errors.Is(err, errUsage) {
			return exitUsage
		}
		return exitFailed
	}
	note("done")
	return exitOK
}

var errUsage = errors.New("usage")

// adminLog appends to the record of administrative actions. It never fails
// loudly: the record is a convenience, not a part of the action.
func adminLog() func(string) {
	dir, err := dataDir()
	if err != nil {
		return func(string) {}
	}
	path := filepath.Join(dir, "logs", "admin.log")
	return func(msg string) {
		_ = os.MkdirAll(filepath.Dir(path), 0o700)
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		defer f.Close()
		fmt.Fprintf(f, "%s %s\n", time.Now().Format(time.RFC3339), msg)
	}
}

// dataDir returns the service's data directory, made safe: neither it nor the
// folder holding it may be changed by an ordinary user, who could otherwise
// plant files for a service that runs as SYSTEM.
func dataDir() (string, error) {
	dir, err := winsys.DataDir()
	if err != nil {
		return "", err
	}
	for _, d := range []string{filepath.Dir(dir), dir} {
		if err := winsys.EnsureProtectedDir(d); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func cmdInstall() error {
	exe, err := winsys.Executable()
	if err != nil {
		return err
	}
	dir, err := dataDir()
	if err != nil {
		return err
	}
	if err := winsys.Install(exe); err != nil {
		return err
	}
	// Nobody may use the service until somebody is authorized; it would not
	// know whose pipe to open.
	if _, err := winsys.LoadOwner(dir); err != nil {
		return nil
	}
	return winsys.Start()
}

func cmdUninstall(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	purge := fs.Bool("purge", false, "also remove the service's data")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	// Stopping disconnects and cleans up. A failure to stop is not a reason to
	// leave the service installed, but it is a reason to look for leftovers.
	stopErr := winsys.Stop()
	if dir, err := dataDir(); err == nil {
		if err := cleanupLeftovers(ctx, dir); err != nil && stopErr == nil {
			stopErr = err
		}
	}
	if err := winsys.Uninstall(); err != nil {
		return err
	}
	if *purge {
		if dir, err := winsys.DataDir(); err == nil {
			if err := os.RemoveAll(filepath.Dir(dir)); err != nil {
				return err
			}
		}
	}
	return stopErr
}

func cmdAuthorize(args []string) error {
	fs := flag.NewFlagSet("authorize", flag.ContinueOnError)
	sid := fs.String("sid", "", "the user's security identifier")
	name := fs.String("name", "", "the user's name, for display")
	ifUnowned := fs.Bool("if-unowned", false, "do nothing when a user is already allowed")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	if *ifUnowned {
		// An upgrade runs this too. Whoever is signed in while an
		// administrator updates the computer does not become the one who
		// controls the service, and the service is not restarted a second time:
		// that would lose the connection the upgrade has just brought back.
		if dir, err := dataDir(); err == nil {
			if _, err := winsys.LoadOwner(dir); err == nil {
				return nil
			}
		}
	}
	owner := winsys.Owner{SID: *sid, Name: *name}
	if *sid == "" {
		who, err := winsys.SessionUser()
		if err != nil {
			return fmt.Errorf("find who is signed in: %w", err)
		}
		owner = who
	}
	if !owner.Valid() {
		return fmt.Errorf("%w: %q is not a security identifier", errUsage, owner.SID)
	}

	exe, err := winsys.Executable()
	if err != nil {
		return err
	}
	dir, err := dataDir()
	if err != nil {
		return err
	}
	// Stop first: the service must not keep serving the former user.
	if err := winsys.Stop(); err != nil {
		return err
	}
	if err := winsys.SaveOwner(dir, owner); err != nil {
		return err
	}
	if err := winsys.Install(exe); err != nil {
		return err
	}
	return winsys.Start()
}

func cmdSetBoot(args []string) error {
	if len(args) != 1 || (args[0] != "on" && args[0] != "off") {
		return fmt.Errorf("%w: set-boot on|off", errUsage)
	}
	return winsys.SetAtBoot(args[0] == "on")
}

func cmdCleanup(ctx context.Context) error {
	dir, err := dataDir()
	if err != nil {
		return err
	}
	if err := winsys.Stop(); err != nil {
		return err
	}
	return cleanupLeftovers(ctx, dir)
}

// cleanupLeftovers removes what a service that died left on the machine, using
// the ledger the service kept. If the ledger cannot be read nothing is known
// about who made what, so only a leftover of the well-known name is removed.
func cleanupLeftovers(ctx context.Context, dir string) error {
	drv, err := winplat.New(winplat.Options{})
	if err != nil {
		return err
	}
	defer drv.Close()
	led, err := ledger.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		return drv.Sweep(ctx)
	}
	if rep := led.Cleanup(ctx, drv); !rep.OK() {
		return errors.New(rep.Summary())
	}
	return nil
}

// cmdStatus tells anyone, administrator or not, what the system says about
// the service.
func cmdStatus() int {
	info, err := winsys.Query()
	if err != nil {
		fmt.Fprintln(os.Stderr, "agentd status:", err)
		return exitFailed
	}
	fmt.Printf("installed %t\nrunning %t\nat-boot %t\n", info.Installed, info.Running, info.AtBoot)
	return exitOK
}
