package main

import (
	"context"

	"eggbox/internal/agentclient"
	"eggbox/internal/boot"
	"eggbox/internal/sysproxy"
)

// platformServices is what differs between operating systems when the
// application runs installed: how it reaches the privileged service, how the
// system is asked to allow it, and whether it starts with the computer.
type platformServices struct {
	// Dial opens a connection to the privileged service.
	Dial agentclient.Dialer
	// Authorize asks the system to allow the service; nil when the installer
	// already did.
	Authorize func(ctx context.Context) error
	// ServiceStartup is the service's start with the computer; nil when the
	// platform has no such choice.
	ServiceStartup *boot.Toggle
	// OpenServiceSettings opens the system page where the service is switched
	// on or off, on platforms where that is the only place; nil otherwise.
	OpenServiceSettings func()
	// RemoveService takes the service out of the system, on platforms where
	// the application can do that itself; nil otherwise.
	RemoveService func(ctx context.Context) error
	// SystemProxies reads the system's manual proxy settings, so that one
	// another program left behind can be pointed out; nil where there is no
	// reader.
	SystemProxies func() ([]sysproxy.Setting, error)
	// OpenProxySettings opens the system page where those settings are
	// changed; nil where there is none to open.
	OpenProxySettings func()
}
