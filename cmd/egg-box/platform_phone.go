//go:build android || ios

package main

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"eggbox/internal/agent/engine/singbox"
	"eggbox/internal/agent/inproc"
	"eggbox/internal/agent/policy"
	"eggbox/internal/version"
)

// installedServices on a phone: there is no privileged service. The agent runs
// in this process, the only one the system lets run the VPN, and the system
// makes the VPN interface for its core (phone_android.go, phone_ios.go). The
// person allows the VPN the first time it connects, in the system's own
// dialog, so there is nothing to authorize beforehand.
func installedServices() platformServices {
	return platformServices{Dial: phone.dial}
}

var phone phoneAgent

// phoneAgent starts the agent the first time the interface reaches for it.
type phoneAgent struct {
	once  sync.Once
	agent *inproc.Agent
	err   error
}

func (p *phoneAgent) dial(ctx context.Context) (net.Conn, error) {
	p.once.Do(func() { p.agent, p.err = startPhoneAgent() })
	if p.err != nil {
		return nil, p.err
	}
	return p.agent.Dial(ctx)
}

func startPhoneAgent() (*inproc.Agent, error) {
	dir, err := agentDataDir()
	if err != nil {
		return nil, fmt.Errorf("find the agent's data directory: %w", err)
	}
	reg := singbox.RegistryOptions{}
	eng, drv, err := phoneCore(reg)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return inproc.Start(ctx, inproc.Options{
		DataDir: dir, Engine: eng, Driver: drv,
		Policy:  policy.New(singbox.PolicyOptions(reg)),
		Version: version.String(), AuthorizedUser: "this device",
		Production: true,
	})
}
