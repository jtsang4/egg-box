// Command agentd is the privileged agent: a Windows Service or macOS
// LaunchDaemon that embeds the sing-box core and owns the TUN adapter, routes,
// DNS and the connection lifecycle, so closing the GUI keeps the connection.
//
// In release builds it only runs as the installed service. Development builds
// (tag sbui_dev) add `agentd dev`, which runs the same agent against a
// simulated operating system on a Unix socket and cannot change the host's
// network.
package main

import (
	"fmt"
	"os"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func usage(w *os.File, extra string) {
	fmt.Fprintln(w, "usage: agentd <command>")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "  version      print the version")
	if extra != "" {
		fmt.Fprint(w, extra)
	}
}
