package main

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/egoist/mygo"

	"eggbox/internal/agent/engine"
	"eggbox/internal/agent/engine/singbox"
	"eggbox/internal/agent/platform"
	"eggbox/internal/agent/platform/phoneplat"
)

// phoneCore is the core on iOS. The VPN runs in the application's packet
// tunnel extension; until that is wired in, connecting says so.
func phoneCore(reg singbox.RegistryOptions) (engine.Engine, platform.Driver, error) {
	return noTunnel{singbox.New(reg)}, phoneplat.New(phoneplat.Options{Name: "ios"}), nil
}

// noTunnel checks configurations like the real core and refuses to start one.
type noTunnel struct{ *singbox.Engine }

func (noTunnel) Start(context.Context, engine.Prepared) error {
	return errors.New("the iOS VPN extension is not part of this build")
}

func agentDataDir() (string, error) {
	dir, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "agent"), nil
}

// allowVPN: iOS asks to add the VPN configuration when the tunnel is first
// saved, which the extension's manager does as it connects.
func allowVPN(context.Context) error { return nil }
