//go:build eggbox_dev && !(darwin || linux)

package main

import (
	"fmt"
	"os"

	"eggbox/internal/version"
)

// run of a development build on an operating system the development agent
// does not support: it serves a Unix socket, which Windows builds do not use.
func run(args []string) int {
	if len(args) > 0 && args[0] == "version" {
		fmt.Println(version.String(), "(development build)")
		return 0
	}
	if len(args) > 0 {
		if code, handled := runPlatform(args); handled {
			return code
		}
	}
	fmt.Fprintln(os.Stderr, "agentd: the development agent is not available on this operating system")
	return 2
}
