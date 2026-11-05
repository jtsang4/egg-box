package boot_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"eggbox/internal/agentclient"
	"eggbox/internal/app"
	"eggbox/internal/boot"
	"eggbox/internal/library"
	"eggbox/internal/subscription"
)

func within(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAuthorizationFailureIsLoggedWithoutTheBackgroundService(t *testing.T) {
	want := fmt.Errorf("%w: SMAppServiceErrorDomain: registration refused (code 42)", app.ErrServiceSetup)
	rt, err := boot.Start(context.Background(), boot.Config{
		Mode: boot.Installed, DataDir: t.TempDir(), Secrets: library.NewMemorySecrets(),
		Dial:      func(context.Context) (net.Conn, error) { return nil, os.ErrNotExist },
		Authorize: func(context.Context) error { return want },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rt.Close)
	if err := rt.Authorize(context.Background()); !errors.Is(err, want) {
		t.Fatalf("got %v", err)
	}
	for _, line := range rt.Service.Snapshot().Logs {
		if line.Level == "error" && strings.Contains(line.Message, "SMAppServiceErrorDomain") && strings.Contains(line.Message, "code 42") {
			return
		}
	}
	t.Fatal("the system's registration error is missing from Logs")
}

func TestAuthorizationWaitsForAReachableService(t *testing.T) {
	rt, err := boot.Start(context.Background(), boot.Config{
		Mode: boot.Installed, DataDir: t.TempDir(), Secrets: library.NewMemorySecrets(),
		Dial:      func(context.Context) (net.Conn, error) { return nil, os.ErrNotExist },
		Authorize: func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rt.Close)
	within(t, "the unavailable service", func() bool { return rt.Agent.Link().State == agentclient.LinkUnavailable })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := rt.Authorize(ctx); !errors.Is(err, app.ErrServiceSetup) {
		t.Fatalf("approval without a working service was reported as success: %v", err)
	}
}

func TestDemoRunsEndToEnd(t *testing.T) {
	ctx := context.Background()
	rt, err := boot.Start(ctx, boot.Config{Mode: boot.Demo, Seed: 5, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	dir := rt.DataDir
	t.Cleanup(rt.Close)

	subs := rt.Library.Subscriptions()
	if len(subs) != 3 {
		t.Fatalf("%d subscriptions", len(subs))
	}
	nodes, err := rt.Library.ActiveNodes()
	if err != nil || len(nodes) != 22 {
		t.Fatalf("%d nodes, %v", len(nodes), err)
	}
	if !rt.Library.Settings().OnboardingDone {
		t.Fatal("the demonstration should skip the first-run welcome")
	}
	if len(rt.Library.Rules()) == 0 {
		t.Fatal("no rules")
	}
	within(t, "the link", func() bool { return rt.Service.Snapshot().Link.Ready() })
	if got := rt.Service.Snapshot().Phase; got != app.PhaseDisconnected {
		t.Fatalf("phase = %v", got)
	}

	if err := rt.Service.Connect(ctx, app.ConnectOptions{}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	within(t, "the connection", func() bool { return rt.Service.Snapshot().Phase == app.PhaseConnected })
	within(t, "the groups", func() bool {
		for _, g := range rt.Service.Snapshot().Groups {
			if g.Tag == "Proxy" && len(g.Items) >= 22 {
				return true
			}
		}
		return false
	})
	within(t, "the latencies", func() bool {
		n := 0
		for _, g := range rt.Service.Snapshot().Groups {
			if g.Tag == "Auto" {
				for _, it := range g.Items {
					if it.DelayMS > 0 {
						n++
					}
				}
			}
		}
		return n >= 15
	})
	within(t, "the traffic", func() bool {
		tr := rt.Service.Snapshot().Traffic
		return tr.Available && tr.DownTotal > 0 && len(tr.History) > 1
	})

	// Choosing a node reaches the core.
	snap := rt.Service.Snapshot()
	var pick string
	for _, g := range snap.Groups {
		if g.Tag == "Proxy" {
			for _, it := range g.Items {
				if it.Node != nil && it.Tag != g.Selected {
					pick = it.Tag
				}
			}
		}
	}
	if pick == "" {
		t.Fatal("no node to pick")
	}
	if err := rt.Service.Select(ctx, pick); err != nil {
		t.Fatal(err)
	}
	within(t, "the selection", func() bool {
		for _, g := range rt.Service.Snapshot().Groups {
			if g.Tag == "Proxy" {
				return g.Selected == pick
			}
		}
		return false
	})

	// The connection table fills while it is watched.
	rt.Service.WatchConnections(true)
	within(t, "connections", func() bool { return len(rt.Service.Snapshot().Conns) >= 3 })
	rt.Service.WatchConnections(false)

	within(t, "log lines", func() bool { return len(rt.Service.Snapshot().Logs) > 5 })

	if err := rt.Service.Disconnect(ctx); err != nil {
		t.Fatal(err)
	}
	within(t, "the disconnection", func() bool { return rt.Service.Snapshot().Phase == app.PhaseDisconnected })

	rt.Close()
	rt.Close()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the demonstration left %s behind (%v)", dir, err)
	}
}

func TestFreshDemoWaitsForTheServiceToBeAllowed(t *testing.T) {
	ctx := context.Background()
	rt, err := boot.Start(ctx, boot.Config{Mode: boot.Demo, Fresh: true, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rt.Close)
	if len(rt.Library.Subscriptions()) != 0 || rt.Library.Settings().OnboardingDone {
		t.Fatal("a fresh demonstration should start empty")
	}
	within(t, "the first failed attempt", func() bool {
		return rt.Service.Snapshot().Phase == app.PhaseNoAgent && rt.Service.Snapshot().Link.State != 0
	})
	time.Sleep(300 * time.Millisecond)
	if rt.Service.Snapshot().Link.Ready() {
		t.Fatal("the service answered before it was allowed")
	}
	if err := rt.Authorize(ctx); err != nil {
		t.Fatal(err)
	}
	within(t, "the service after approval", func() bool { return rt.Service.Snapshot().Phase == app.PhaseDisconnected })
}

func TestDemoNeverLeavesTheProcess(t *testing.T) {
	// The invented world refuses every other host, so even a subscription
	// the user types in cannot reach the network.
	ctx := context.Background()
	rt, err := boot.Start(ctx, boot.Config{Mode: boot.Demo, Fresh: true, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rt.Close)
	_, _, err = rt.Service.AddSubscription(ctx, "Real", "https://example.com/sub?token=x", library.RemoteOptions{})
	var fe *subscription.FetchError
	if !errors.As(err, &fe) || fe.Kind != subscription.FetchNetwork {
		t.Fatalf("the demonstration reached out or failed differently: %v", err)
	}
}

func TestInstalledNeedsAWayToReachTheService(t *testing.T) {
	_, err := boot.Start(context.Background(), boot.Config{Mode: boot.Installed, DataDir: filepath.Join(t.TempDir(), "x")})
	if err == nil {
		t.Fatal("started without a dialer")
	}
}
