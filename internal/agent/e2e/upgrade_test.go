//go:build sbui_dev && (darwin || linux)

package e2e

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jtsang4/sing-box-ui/internal/agent/daemon"
	"github.com/jtsang4/sing-box-ui/internal/agent/engine/singbox"
	"github.com/jtsang4/sing-box-ui/internal/agent/platform/sim"
	"github.com/jtsang4/sing-box-ui/internal/agent/policy"
	"github.com/jtsang4/sing-box-ui/internal/agent/server"
	"github.com/jtsang4/sing-box-ui/internal/agent/transport"
	"github.com/jtsang4/sing-box-ui/internal/agentapi"
	"github.com/jtsang4/sing-box-ui/internal/agentclient"
)

// version starts an agent over the folder a previous one used, the way the
// system starts the new version of a service, and returns a client of it and
// the way to stop it (as the system stops a service: it disconnects first).
func version(t *testing.T, dir, name string) (*agentclient.Client, func()) {
	t.Helper()
	reg := singbox.RegistryOptions{}
	d, err := daemon.Build(daemon.Config{
		DataDir: filepath.Join(dir, "data"), Driver: sim.New(sim.Options{Sandbox: true}),
		Engine: singbox.New(reg), Policy: policy.New(singbox.PolicyOptions(reg)),
		Version: name, DevMode: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "a.sock")
	l, verify, err := transport.ListenDev(socket)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- d.Serve(l, server.NewCredentials(verify)) }()
	c := agentclient.New(transport.DialDev(socket), agentclient.Options{Backoff: func(int) time.Duration { return 20 * time.Millisecond }})
	bg, cancel := context.WithCancel(context.Background())
	c.Start(bg)
	wctx, wcancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer wcancel()
	if err := c.WaitReady(wctx); err != nil {
		t.Fatal(err)
	}
	return c, func() {
		sctx, scancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer scancel()
		_ = d.Shutdown(sctx)
		cancel()
		c.Close()
		select {
		case <-served:
		case <-time.After(5 * time.Second):
		}
	}
}

// connectedWithin waits for the connection and returns the status once it is
// up, or nil if it is not within the time.
func connectedWithin(c *agentclient.Client, t *testing.T, within time.Duration) *agentapi.Status {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if st, err := c.Status(ctx(t)); err == nil && st.Connection == agentapi.ConnectionState_CONNECTION_STATE_CONNECTED {
			return st
		}
		time.Sleep(20 * time.Millisecond)
	}
	return nil
}

// A person is connected, an installer stops the service to replace it, and the
// new version comes up: the connection is back without anybody touching it,
// but only when the installer said the stop was for an upgrade.
func TestAnUpgradeBringsTheConnectionBackAndAPlainRestartDoesNot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the development agent never runs as root")
	}
	for _, tc := range []struct {
		name       string
		leaveNote  bool
		wantBackUp bool
	}{
		{"the installer leaves its note", true, true},
		{"the service is simply restarted", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, err := os.MkdirTemp("/tmp", "sbui")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)

			old, stopOld := version(t, dir, "old")
			if _, err := old.ApplyConfig(ctx(t), 0, &agentapi.ConfigBundle{ConfigJson: []byte(tunConfig), ProfileName: "kept"}, agentapi.ApplyMode_APPLY_MODE_STORE_ONLY); err != nil {
				t.Fatal(err)
			}
			if _, err := old.Connect(ctx(t), agentclient.ConnectOptions{}); err != nil {
				t.Fatal(err)
			}
			if connectedWithin(old, t, 15*time.Second) == nil {
				t.Fatal("the old version never connected")
			}

			// What an installer does: say why, then stop the service.
			if tc.leaveNote {
				if err := os.WriteFile(filepath.Join(dir, "data", daemon.UpgradeMarker), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			stopOld()

			next, stopNext := version(t, dir, "new")
			defer stopNext()
			// A restart that is not an upgrade is given a moment to prove it
			// stays disconnected; an upgrade is given time to reconnect.
			within := 1500 * time.Millisecond
			if tc.wantBackUp {
				within = 15 * time.Second
			}
			st := connectedWithin(next, t, within)
			switch {
			case tc.wantBackUp && st == nil:
				t.Fatal("the connection the person had must come back with the new version")
			case tc.wantBackUp && st.ActiveRevision != 1:
				t.Fatalf("it must come back with the configuration that was running: %v", st)
			case !tc.wantBackUp && st != nil:
				t.Fatal("a service that is merely restarted must not reconnect by itself")
			}
			if _, err := os.Lstat(filepath.Join(dir, "data", daemon.UpgradeMarker)); !os.IsNotExist(err) {
				t.Fatalf("the note must be used up: %v", err)
			}
		})
	}
}
