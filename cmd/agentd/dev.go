//go:build sbui_dev && (darwin || linux)

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"os/user"
	"syscall"
	"time"

	"github.com/jtsang4/sing-box-ui/internal/agent/controller"
	"github.com/jtsang4/sing-box-ui/internal/agent/daemon"
	"github.com/jtsang4/sing-box-ui/internal/agent/engine/singbox"
	"github.com/jtsang4/sing-box-ui/internal/agent/platform/sim"
	"github.com/jtsang4/sing-box-ui/internal/agent/policy"
	"github.com/jtsang4/sing-box-ui/internal/agent/server"
	"github.com/jtsang4/sing-box-ui/internal/agent/transport"
	"github.com/jtsang4/sing-box-ui/internal/version"
)

const devUsage = "  dev          run the development agent: simulated OS, loopback proxy instead of a TUN\n"

// run dispatches the command line of a development build.
func run(args []string) int {
	if len(args) == 0 {
		usage(os.Stderr, devUsage)
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Println(version.String(), "(development build)")
		return 0
	case "dev":
		return runDev(args[1:])
	default:
		usage(os.Stderr, devUsage)
		return 2
	}
}

// runDev runs the agent against the simulated platform. Nothing here can touch
// the host's interfaces, routes, DNS or proxy settings: the driver is an
// in-memory simulation, TUN configurations run as a loopback "mixed" proxy, and
// the only listening sockets are loopback ports and the Unix socket.
func runDev(args []string) int {
	fs := flag.NewFlagSet("agentd dev", flag.ContinueOnError)
	socket := fs.String("socket", "", "absolute path of the Unix socket to serve (required)")
	dataDir := fs.String("data-dir", "", "absolute path of the state directory (required)")
	sandboxPort := fs.Uint("sandbox-port", 0, "loopback port of the proxy that replaces the TUN (0 picks a free one)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *socket == "" || *dataDir == "" {
		fmt.Fprintln(os.Stderr, "agentd dev: --socket and --data-dir are required")
		return 2
	}
	if os.Geteuid() == 0 {
		fmt.Fprintln(os.Stderr, "agentd dev: refusing to run as root; the development agent is for an ordinary user")
		return 2
	}

	reg := singbox.RegistryOptions{}
	who := "developer"
	if u, err := user.Current(); err == nil {
		who = u.Username
	}
	d, err := daemon.Build(daemon.Config{
		DataDir: *dataDir,
		Driver:  sim.New(sim.Options{Sandbox: true}),
		Engine:  singbox.New(reg),
		Policy:  policy.New(singbox.PolicyOptions(reg)),
		Version: version.String(), AuthorizedUser: who, DevMode: true,
		Tune: func(c *controller.Config) { c.SandboxPort = uint16(*sandboxPort) },
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "agentd dev:", err)
		return 1
	}
	l, verify, err := transport.ListenDev(*socket)
	if err != nil {
		fmt.Fprintln(os.Stderr, "agentd dev:", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := d.Start(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "agentd dev:", err)
		return 1
	}
	served := make(chan error, 1)
	go func() { served <- d.Serve(l, server.NewCredentials(verify)) }()
	fmt.Printf("agentd dev %s listening on %s (data in %s)\n", version.String(), *socket, *dataDir)

	code := 0
	select {
	case <-ctx.Done():
	case err := <-served:
		fmt.Fprintln(os.Stderr, "agentd dev: server stopped:", err)
		code = 1
	}
	sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := d.Shutdown(sctx); err != nil {
		fmt.Fprintln(os.Stderr, "agentd dev: shutdown:", err)
		code = 1
	}
	_ = os.Remove(*socket)
	return code
}
