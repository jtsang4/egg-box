package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"

	"eggbox/internal/agent/engine"
	"eggbox/internal/agent/engine/singbox"
	"eggbox/internal/agent/platform"
	"eggbox/internal/agent/platform/phoneplat"
	"eggbox/internal/app"
	"eggbox/internal/brand"
	"eggbox/internal/gui"
	"eggbox/internal/phone/androidhost"
	"eggbox/internal/phone/droidvpn"
	"eggbox/internal/phone/tunnel"
)

// phoneCore is the core on Android: in this process, its VPN interface made by
// the application's VpnService.
func phoneCore(reg singbox.RegistryOptions) (engine.Engine, platform.Driver, error) {
	svc, err := vpnService()
	if err != nil {
		return nil, nil, err
	}
	host := androidhost.New(svc, brand.DisplayName)
	plat := tunnel.New(host)
	host.Bind(plat)
	drv := phoneplat.New(phoneplat.Options{Name: "android", Networks: host.Networks})
	return singbox.NewWithPlatform(reg, plat), drv, nil
}

// agentDataDir is in the application's private storage, which no other
// application can read.
func agentDataDir() (string, error) {
	dir, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "agent"), nil
}

var vpn struct {
	once sync.Once
	svc  droidvpn.Service
	err  error
}

// vpnService is the application's one VpnService module.
func vpnService() (droidvpn.Service, error) {
	vpn.once.Do(func() { vpn.svc, vpn.err = droidvpn.NewService() })
	return vpn.svc, vpn.err
}

// allowVPN asks the person, the first time, to let EggBox be the device's VPN.
func allowVPN(ctx context.Context) error {
	svc, err := vpnService()
	if err != nil {
		return err
	}
	ok, err := svc.Prepare(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %w", gui.ErrVPNDeclined, androidhost.ErrNotAllowed)
	}
	return nil
}

// systemStarts and tileToggles keep Android's system-initiated callbacks alive
// for the life of the process.
var (
	systemStarts io.Closer
	tileToggles  io.Closer
	tileBusy     atomic.Bool
	tileLast     tileSync
)

// followSystemStarts connects whenever the system starts the VPN by itself:
// Android's always-on VPN, at boot or after the VPN went away, which may be
// what started this process, without a window.
func (d *desktop) followSystemStarts() {
	svc, err := vpnService()
	if err != nil {
		return
	}
	systemStarts = svc.OnSystemStart(func() error {
		go d.connectForSystem()
		return nil
	})
}

// followTile listens for taps on EggBox's Quick Settings tile and keeps the
// tile's state in sync with the connection.
func (d *desktop) followTile() {
	svc, err := vpnService()
	if err != nil {
		return
	}
	tileToggles = svc.OnTileToggle(func() error {
		if tileBusy.CompareAndSwap(false, true) {
			go d.toggleForTile()
		}
		return nil
	})
	if !tileBusy.Load() {
		d.syncPhoneStatus()
	}
}

func (d *desktop) hasPhoneStatus() bool { return true }

func (d *desktop) syncPhoneStatus() { d.syncTile(false) }

func (d *desktop) syncTile(force bool) {
	if d.app == nil {
		return
	}
	d.pushTileState(tileStateFor(d.app.Status()), force)
}

func (d *desktop) pushTileState(next droidvpn.TileState, force bool) {
	svc, err := vpnService()
	if err != nil {
		return
	}
	if err := tileLast.push(next, force, svc.SetTileState); err != nil {
		log.Printf("quick settings tile: %v", err)
	}
}

// toggleForTile connects or disconnects in response to Android's Quick
// Settings tile, waiting for the in-process agent on a cold headless start and
// always pushing the settled state back to the tile when done.
func (d *desktop) toggleForTile() {
	defer func() {
		tileBusy.Store(false)
		d.syncTile(true)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := d.rt.Agent.WaitReady(ctx); err != nil {
		log.Printf("quick settings tile: the agent did not answer: %v", err)
		return
	}
	snap := d.rt.Service.Snapshot()
	if snap != nil && snap.Phase.Transitional() {
		return
	}
	if snap != nil && snap.Phase.Online() {
		d.pushTileState(droidvpn.TileState{State: droidvpn.TileDisconnecting, Subtitle: d.app.Say("phase.disconnecting")}, true)
		if err := d.rt.Service.Disconnect(ctx); err != nil {
			log.Printf("quick settings tile disconnect: %v", err)
		}
		return
	}
	d.pushTileState(droidvpn.TileState{State: droidvpn.TileConnecting, Subtitle: d.app.Say("phase.connecting")}, true)
	if err := d.rt.Service.Connect(ctx, app.ConnectOptions{}); err != nil {
		log.Printf("quick settings tile connect: %v", err)
	}
}

// connectForSystem connects as the system asked, with what the person chose
// last, once the agent answers.
func (d *desktop) connectForSystem() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := d.rt.Agent.WaitReady(ctx); err != nil {
		log.Printf("always-on VPN: the agent did not answer: %v", err)
		return
	}
	if snap := d.rt.Service.Snapshot(); snap != nil && (snap.Phase.Online() || snap.Phase.Transitional()) {
		return // already up, or on its way
	}
	if err := d.rt.Service.Connect(ctx, app.ConnectOptions{}); err != nil {
		log.Printf("always-on VPN: %v", err)
	}
}
