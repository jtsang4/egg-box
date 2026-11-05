//go:build !eggbox_dev

package main

import (
	"fmt"
	"os"

	"eggbox/internal/version"
)

// run dispatches the command line of a release build.
func run(args []string) int {
	if len(args) == 0 {
		usage(os.Stderr, platformUsage)
		return 2
	}
	if args[0] == "version" {
		fmt.Println(version.String())
		return 0
	}
	if code, handled := runPlatform(args); handled {
		return code
	}
	usage(os.Stderr, platformUsage)
	return 2
}
