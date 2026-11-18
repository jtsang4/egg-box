package main

import (
	"path/filepath"

	"github.com/egoist/mygo"

	"eggbox/internal/agent/engine"
	"eggbox/internal/agent/engine/singbox"
	"eggbox/internal/agent/platform"
	"eggbox/internal/agent/platform/phoneplat"
	"eggbox/internal/brand"
	"eggbox/internal/phone/androidhost"
	"eggbox/internal/phone/droidvpn"
	"eggbox/internal/phone/tunnel"
)

// phoneCore is the core on Android: in this process, its VPN interface made by
// the application's VpnService.
func phoneCore(reg singbox.RegistryOptions) (engine.Engine, platform.Driver, error) {
	svc, err := droidvpn.NewService()
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
