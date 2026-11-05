//go:build darwin

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"

	"github.com/jtsang4/sing-box-ui/internal/app"
	"github.com/jtsang4/sing-box-ui/internal/brand"
	"github.com/jtsang4/sing-box-ui/internal/darwin/bridge"
	"github.com/jtsang4/sing-box-ui/internal/darwin/service"
	"github.com/jtsang4/sing-box-ui/internal/macsys"
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
	}
}
