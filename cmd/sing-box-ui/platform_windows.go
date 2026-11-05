//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/Microsoft/go-winio"

	"github.com/jtsang4/sing-box-ui/internal/app"
	"github.com/jtsang4/sing-box-ui/internal/boot"
	"github.com/jtsang4/sing-box-ui/internal/brand"
	"github.com/jtsang4/sing-box-ui/internal/winsys"
)

// installedServices wires the application to the Windows service: it reaches
// it through its named pipe, and asks the system, through the one prompt for
// administrator rights, to set it up or change how it starts.
func installedServices() platformServices {
	agent := agentProgram()
	return platformServices{
		Dial: func(ctx context.Context) (net.Conn, error) {
			return winio.DialPipeContext(ctx, brand.WindowsPipePath)
		},
		Authorize: func(ctx context.Context) error {
			who, err := winsys.CurrentUser()
			if err != nil {
				return fmt.Errorf("%w: %v", app.ErrServiceSetup, err)
			}
			args := []string{"authorize", "--sid", who.SID}
			if who.Name != "" {
				args = append(args, "--name", who.Name)
			}
			return runAgentElevated(ctx, agent, args...)
		},
		ServiceStartup: &boot.Toggle{
			Get: func() (bool, error) {
				info, err := winsys.Query()
				return info.AtBoot, err
			},
			Set: func(on bool) error {
				arg := "off"
				if on {
					arg = "on"
				}
				return runAgentElevated(context.Background(), agent, "set-boot", arg)
			},
		},
	}
}

// agentProgram is the service's program, installed beside the application.
func agentProgram() string {
	exe, err := os.Executable()
	if err != nil {
		return brand.AgentBinary + ".exe"
	}
	return filepath.Join(filepath.Dir(exe), brand.AgentBinary+".exe")
}

// runAgentElevated runs one administrative command of the service's program
// and turns how it ended into an error the interface can explain.
func runAgentElevated(ctx context.Context, agent string, args ...string) error {
	if _, err := os.Stat(agent); err != nil {
		return fmt.Errorf("%w: %s is missing", app.ErrServiceSetup, filepath.Base(agent))
	}
	code, err := winsys.RunElevated(ctx, agent, args...)
	switch {
	case errors.Is(err, winsys.ErrDeclined):
		return app.ErrAuthorizationDeclined
	case err != nil:
		return fmt.Errorf("%w: %v", app.ErrServiceSetup, err)
	case code != 0:
		return fmt.Errorf("%w (code %d)", app.ErrServiceSetup, code)
	}
	return nil
}
