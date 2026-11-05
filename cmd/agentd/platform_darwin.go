//go:build darwin

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/jtsang4/sing-box-ui/internal/agent/ledger"
	"github.com/jtsang4/sing-box-ui/internal/agent/platform/macplat"
	"github.com/jtsang4/sing-box-ui/internal/darwin/bridge"
	"github.com/jtsang4/sing-box-ui/internal/macsys"
)

const platformUsage = `  daemon       run as the launchd service; the system starts it this way (root)
  reset-owner  forget the authorized user, so the next user of the application becomes it (root)
  cleanup      remove what a service that died left behind (root)
  print-plist  print the launchd property list the installer puts in the application
`

// Exit codes. The service reports them to launchd; the commands to whoever ran them.
const (
	exitOK      = 0
	exitFailed  = 1
	exitUsage   = 2
	exitNotRoot = 3
)

// runPlatform runs a macOS-specific command.
func runPlatform(args []string) (int, bool) {
	switch args[0] {
	case "daemon":
		return runDaemon(), true
	case "reset-owner", "cleanup":
		return runAdmin(args[0]), true
	case "print-plist":
		if _, err := os.Stdout.Write(macsys.DaemonPlistXML()); err != nil {
			return exitFailed, true
		}
		return exitOK, true
	}
	return 0, false
}

// runAdmin runs a command that changes what the service keeps on the machine.
func runAdmin(name string) int {
	if os.Geteuid() != 0 {
		fmt.Fprintf(os.Stderr, "agentd %s: this needs root; run it with sudo\n", name)
		return exitNotRoot
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	dir, err := dataDir()
	if err == nil {
		switch name {
		case "reset-owner":
			err = macsys.RemoveOwner(dir)
			if err == nil {
				fmt.Println("done: the next user of the application becomes the authorized user.")
				fmt.Println("A connection that is open keeps working until the service restarts.")
			}
		case "cleanup":
			err = cleanupLeftovers(ctx, dir)
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "agentd %s: %v\n", name, err)
		return exitFailed
	}
	return exitOK
}

// dataDir returns the service's data folder, made safe: neither it nor the
// folder holding it may be changed by anybody but root, who could otherwise
// plant files for a program that runs as root.
func dataDir() (string, error) {
	if err := macsys.EnsureProtectedDir(macsys.DataDir, 0); err != nil {
		return "", err
	}
	return macsys.DataDir, nil
}

// loadBridge loads the Swift library from beside the program.
func loadBridge() (*bridge.Bridge, error) {
	path, err := bridge.DefaultPath()
	if err != nil {
		return nil, err
	}
	return bridge.Load(path)
}

// cleanupLeftovers removes what a service that died left on the machine, using
// the ledger the service kept. If the ledger cannot be read nothing is known
// about who made what, so only what carries the service's own mark is removed.
func cleanupLeftovers(ctx context.Context, dir string) error {
	b, err := loadBridge()
	if err != nil {
		return err
	}
	drv, err := macplat.New(macplat.Options{System: b, Host: macplat.SystemHost()})
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

// shutdownBudget is how long stopping may take; launchd is told to wait longer
// (see macsys.ExitTimeOut).
const shutdownBudget = macsys.ShutdownBudget
