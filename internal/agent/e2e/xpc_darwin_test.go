//go:build darwin

package e2e

import (
	"context"
	"log/slog"
	"net"
	"os"
	"testing"
	"time"

	"eggbox/internal/agent/daemon"
	"eggbox/internal/agent/engine/fake"
	"eggbox/internal/agent/platform/sim"
	"eggbox/internal/agent/policy"
	"eggbox/internal/agent/server"
	"eggbox/internal/agent/transport"
	"eggbox/internal/agentclient"
	"eggbox/internal/darwin/bridge"
	"eggbox/internal/darwin/bridge/bridgetest"
)

// The agent as the installed service runs it on macOS, with the two things
// that cannot be shared left out: it listens on an anonymous XPC channel
// private to this process rather than on a Mach service of the system, and it
// is a simulated operating system underneath. The XPC introduction, the
// descriptor passing, the user policy and gRPC over it are the real ones.

func startOverXPC(t *testing.T, b *bridge.Bridge, policy transport.UIDPolicy) *agentclient.Client {
	t.Helper()
	l, verify, err := transport.ListenXPC(b, "", "", policy)
	if err != nil {
		t.Fatal(err)
	}
	ep, err := l.(*bridge.Listener).Endpoint()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Release(ep) })

	d, err := daemon.Build(daemon.Config{
		DataDir: t.TempDir(),
		Driver:  sim.New(sim.Options{Sandbox: true}),
		Engine:  fake.New(),
		Policy:  policyForTests(),
		Version: "xpc-test", AuthorizedUser: "tester", DevMode: true,
		Logger: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- d.Serve(l, server.NewCredentials(verify)) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = d.Shutdown(ctx)
		select {
		case <-served:
		case <-time.After(5 * time.Second):
			t.Error("the server did not stop")
		}
	})

	client := agentclient.New(func(ctx context.Context) (net.Conn, error) {
		return b.ConnectEndpoint(ctx, ep, "")
	}, agentclient.Options{Version: "xpc-test"})
	bg, cancel := context.WithCancel(context.Background())
	client.Start(bg)
	t.Cleanup(func() { cancel(); client.Close() })
	return client
}

func policyForTests() *policy.Policy {
	return policy.New(policy.Options{WireGuard: true, QUIC: true, DHCP: true})
}

func ownUser() transport.UIDPolicy {
	uid := uint32(os.Getuid())
	return transport.UIDPolicy{
		Owner:    func() (uint32, bool) { return uid, true },
		SetOwner: func(uint32) error { return nil },
		Console:  func() (uint32, bool) { return uid, true },
	}
}

func TestAgentOverXPCServesItsOwner(t *testing.T) {
	b := bridgetest.Load(t)
	client := startOverXPC(t, b, ownUser())

	wctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := client.WaitReady(wctx); err != nil {
		t.Fatalf("client: %v (%s)", err, client.Link().Describe())
	}
	st, err := client.Status(wctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.GetAgentVersion() != "xpc-test" {
		t.Errorf("agent version %q: the reply did not come from our agent", st.GetAgentVersion())
	}
	if _, err := client.Settings(wctx); err != nil {
		t.Fatalf("settings: %v", err)
	}
}

func TestAgentOverXPCRefusesAnotherUser(t *testing.T) {
	b := bridgetest.Load(t)
	other := uint32(os.Getuid()) + 1
	client := startOverXPC(t, b, transport.UIDPolicy{
		Owner:    func() (uint32, bool) { return other, true },
		SetOwner: func(uint32) error { return nil },
		Console:  func() (uint32, bool) { return other, true },
	})

	wctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.WaitReady(wctx); err == nil {
		t.Fatal("a caller that is not the authorized user got through")
	}
	if client.Link().State == agentclient.LinkReady {
		t.Error("the link claims to be ready")
	}
}

func TestAgentOverXPCLetsTheFirstUserAtTheScreenClaimIt(t *testing.T) {
	b := bridgetest.Load(t)
	uid := uint32(os.Getuid())
	var recorded []uint32
	var owner uint32
	var known bool
	client := startOverXPC(t, b, transport.UIDPolicy{
		Owner: func() (uint32, bool) { return owner, known },
		SetOwner: func(u uint32) error {
			recorded = append(recorded, u)
			owner, known = u, true
			return nil
		},
		Console: func() (uint32, bool) { return uid, true },
	})
	wctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := client.WaitReady(wctx); err != nil {
		t.Fatalf("client: %v (%s)", err, client.Link().Describe())
	}
	if len(recorded) == 0 || recorded[0] != uid {
		t.Errorf("owner recorded as %v, want [%d]", recorded, uid)
	}
}
