package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"

	"eggbox/internal/agent/engine"
	"eggbox/internal/agent/engine/remote"
	"eggbox/internal/agent/engine/singbox"
	"eggbox/internal/agent/platform"
	"eggbox/internal/agent/platform/phoneplat"
	"eggbox/internal/gui"
	"eggbox/internal/phone/iosengine"
	_ "eggbox/internal/phone/iosplugin" // the entitlements and the App Group
	"eggbox/internal/phone/iostunnel"
	"eggbox/internal/phone/iosvpn"
	"eggbox/internal/phone/tunnel"
)

// phoneCore is the core on iOS: the agent runs here and the core in the
// application's packet tunnel extension, which the system starts with the
// VPN. They share an App Group container: the agent keeps its data there, so
// that the configuration it accepted and the rule files it names are the
// extension's to read, and the extension serves the core on a socket there.
func phoneCore(reg singbox.RegistryOptions) (engine.Engine, platform.Driver, error) {
	sys, dir, err := iosTunnel()
	if err != nil {
		return nil, nil, err
	}
	core := &remote.Client{Dial: func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", iostunnel.SocketName)
	}}
	// Configurations are checked here, by the same core with the same view
	// of the device: a check opens no tunnel.
	checker := singbox.NewWithPlatform(reg, tunnel.New(&iostunnel.Host{}))
	eng := iosengine.New(iosengine.Options{Dir: dir, System: sys, Core: core, Checker: checker})
	return eng, phoneplat.New(phoneplat.Options{Name: "ios"}), nil
}

var ios struct {
	once sync.Once
	sys  iosvpn.Tunnel
	dir  string
	err  error
}

// iosTunnel is the system's VPN of the application and the container it
// shares with the extension, which becomes the working directory: the
// socket's full path there is too long for a Unix socket's address on the
// simulator, and everything else is named by absolute paths.
func iosTunnel() (iosvpn.Tunnel, string, error) {
	ios.once.Do(func() {
		ios.sys, ios.err = iosvpn.NewTunnel()
		if ios.err != nil {
			return
		}
		ios.dir, ios.err = ios.sys.GroupDirectory()
		if ios.err == nil {
			ios.err = os.Chdir(ios.dir)
		}
	})
	return ios.sys, ios.dir, ios.err
}

// agentDataDir is in the container the application shares with its
// extension.
func agentDataDir() (string, error) {
	_, dir, err := iosTunnel()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "agent"), nil
}

// allowVPN adds EggBox's VPN configuration before connecting: iOS asks the
// person to allow it the first time.
func allowVPN(ctx context.Context) error {
	sys, _, err := iosTunnel()
	if err != nil {
		return err
	}
	err = sys.Install(ctx)
	if errors.Is(err, iosvpn.ErrNotAllowed) {
		return fmt.Errorf("%w: %w", gui.ErrVPNDeclined, err)
	}
	return err
}
