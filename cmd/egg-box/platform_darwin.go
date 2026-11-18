//go:build darwin && !ios

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"sync"

	"eggbox/internal/app"
	"eggbox/internal/brand"
	"eggbox/internal/darwin/bridge"
	"eggbox/internal/darwin/service"
	"eggbox/internal/macsys"
	"eggbox/internal/sysproxy"

	"github.com/egoist/mygo"
)

// installedServices wires the application to the macOS service. The bridge
// library sits in the application bundle, next to this program; the service
// is a launch daemon of the same bundle, which the person allows once in
// System Settings. The application reaches it over XPC, and the system checks
// that what answers is signed by the same team as this program.
func installedServices() platformServices {
	var (
		once    sync.Once
		b       *bridge.Bridge
		loadErr error
	)
	load := func() (*bridge.Bridge, error) {
		once.Do(func() {
			path, err := bridge.DefaultPath()
			if err == nil {
				b, err = bridge.Load(path)
			}
			loadErr = err
		})
		return b, loadErr
	}

	return platformServices{
		Dial: func(ctx context.Context) (net.Conn, error) {
			b, err := load()
			if err != nil {
				// Not running from an installed bundle: there is no service.
				return nil, fmt.Errorf("%w: %v", os.ErrNotExist, err)
			}
			team, _ := b.SelfTeam()
			requirement, err := macsys.AgentRequirement(team)
			if err != nil {
				return nil, err
			}
			conn, err := b.Connect(ctx, brand.AgentLabel, requirement)
			if errors.Is(err, bridge.ErrUnavailable) {
				return nil, fmt.Errorf("%w: %v", os.ErrNotExist, err)
			}
			return conn, err
		},
		Authorize: func(ctx context.Context) error {
			b, err := load()
			if err != nil {
				return fmt.Errorf("%w: run the application installed from its installer (%v)", app.ErrServiceSetup, err)
			}
			return service.Authorize(ctx, b, macsys.DaemonPlist, service.DefaultTiming)
		},
		OpenServiceSettings: func() {
			if b, err := load(); err == nil {
				b.OpenLoginItems()
			}
		},
		// Unregistering stops the service (launchd ends it with a termination
		// signal, which it answers by putting the network back) and removes
		// its entry from Login Items & Extensions.
		RemoveService: func(context.Context) error {
			b, err := load()
			if err != nil {
				return err
			}
			return b.Unregister(macsys.DaemonPlist)
		},
		SystemProxies: func() ([]sysproxy.Setting, error) {
			b, err := load()
			if err != nil {
				return nil, err
			}
			data, err := b.SystemProxies()
			if err != nil {
				return nil, err
			}
			return sysproxy.ParseDarwin(data)
		},
		// The proxies are in the Details of each network service there; the
		// page has no address of its own.
		OpenProxySettings: func() {
			if err := mygo.Shell.OpenExternal("x-apple.systempreferences:com.apple.Network-Settings.extension"); err != nil {
				log.Printf("open the network settings: %v", err)
			}
		},
	}
}
